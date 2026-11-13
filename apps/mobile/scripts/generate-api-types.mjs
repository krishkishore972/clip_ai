import { readFileSync, writeFileSync } from 'node:fs';

// The API contract is JSON-compatible YAML. No second hand-written set of DTOs.
const contract = JSON.parse(readFileSync(new URL('../../api/openapi/openapi.yaml', import.meta.url), 'utf8'));
const schemas = contract.components.schemas;

function typeFor(schema) {
  if (schema.$ref) {
    const name = schema.$ref.split('/').at(-1);
    if (!(name in schemas)) throw new Error(`Unknown schema ${name}`);
    return name;
  }
  if (schema.enum) return schema.enum.map((value) => JSON.stringify(value)).join(' | ');
  if (Array.isArray(schema.type)) {
    return schema.type.map((type) => typeFor({ ...schema, type })).join(' | ');
  }
  switch (schema.type) {
    case 'string': return 'string';
    case 'number':
    case 'integer': return 'number';
    case 'boolean': return 'boolean';
    case 'null': return 'null';
    case 'array': return `(${typeFor(schema.items)})[]`;
    case 'object': {
      const properties = Object.entries(schema.properties ?? {});
      if (properties.length === 0) {
        return `Record<string, ${typeof schema.additionalProperties === 'object' ? typeFor(schema.additionalProperties) : 'unknown'}>`;
      }
      const required = new Set(schema.required ?? []);
      return `{\n${properties.map(([name, property]) => `  ${JSON.stringify(name)}${required.has(name) ? '' : '?'}: ${typeFor(property)};`).join('\n')}\n}`;
    }
    case undefined: return 'unknown';
    default: throw new Error(`Unsupported schema type ${schema.type}`);
  }
}

const output = '// Generated from apps/api/openapi/openapi.yaml. Do not edit.\n' +
  '// Regenerate: pnpm --filter mobile gen:api\n\n' +
  Object.entries(schemas).map(([name, schema]) => `export type ${name} = ${typeFor(schema)};\n`).join('\n');
const target = new URL('../src/lib/api/generated.ts', import.meta.url);
if (process.argv.includes('--check')) {
  if (readFileSync(target, 'utf8') !== output) throw new Error('API types are stale; run gen:api');
} else {
  writeFileSync(target, output);
}
