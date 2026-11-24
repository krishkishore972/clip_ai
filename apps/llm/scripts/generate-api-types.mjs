import { mkdirSync, readFileSync, writeFileSync } from "node:fs";

// Match the repository's lightweight generator convention. This generator only
// consumes the inline internal endpoint; it does not change mobile contracts.
const contract = JSON.parse(
  readFileSync(
    new URL("../../api/openapi/openapi.yaml", import.meta.url),
    "utf8",
  ),
);
const operation = contract.paths["/internal/curate"].post;
const request = operation.requestBody.content["application/json"].schema;
const response = operation.responses["200"].content["application/json"].schema;

function typeFor(schema) {
  switch (schema.type) {
    case "string":
      return "string";
    case "integer":
      return "number";
    case "array":
      return `(${typeFor(schema.items)})[]`;
    case "object": {
      const required = new Set(schema.required ?? []);
      return `{\n${Object.entries(schema.properties)
        .map(
          ([name, value]) =>
            `  ${JSON.stringify(name)}${required.has(name) ? "" : "?"}: ${typeFor(value)};`,
        )
        .join("\n")}\n}`;
    }
    default:
      throw new Error(`Unsupported curation contract type: ${schema.type}`);
  }
}

const output =
  "// Generated from apps/api/openapi/openapi.yaml. Do not edit.\n" +
  "// Regenerate: pnpm --filter llm gen:api\n\n" +
  `export type CurationRequestDTO = ${typeFor(request)};\n\n` +
  `export type CurationResponseDTO = ${typeFor(response)};\n`;
const target = new URL("../src/generated/curation.ts", import.meta.url);
if (process.argv.includes("--check")) {
  if (readFileSync(target, "utf8") !== output) {
    throw new Error("LLM API types are stale; run pnpm --filter llm gen:api");
  }
} else {
  mkdirSync(new URL("../src/generated/", import.meta.url), { recursive: true });
  writeFileSync(target, output);
}
