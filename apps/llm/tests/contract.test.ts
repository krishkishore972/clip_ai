import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { z } from "zod";
import { CURATION_LIMITS } from "../src/config/curation.js";
import { createCurationSchemas } from "../src/schemas/curation.js";

const schemaNode: z.ZodType<{
  type?: string;
  properties?: Record<string, unknown>;
  items?: unknown;
  [key: string]: unknown;
}> = z
  .object({
    type: z.string().optional(),
    properties: z.record(z.string(), z.unknown()).optional(),
    items: z.unknown().optional(),
  })
  .catchall(z.unknown());

// Compare representable structure and numeric/array constraints. Cross-field,
// blank-text, UTF-8 byte and UTF-16 length rules require runtime checks.
function compare(contractValue: unknown, runtimeValue: unknown): void {
  const contract = schemaNode.parse(contractValue);
  const runtime = schemaNode.parse(runtimeValue);
  assert.equal(contract.type, runtime.type);
  // Zod emits exclusiveMinimum: 0 for positive integers, equivalent to
  // OpenAPI's inclusive minimum: 1.
  if (
    runtime.type === "integer" &&
    typeof runtime.exclusiveMinimum === "number"
  ) {
    runtime.minimum = Math.floor(runtime.exclusiveMinimum) + 1;
  }
  for (const key of [
    "minimum",
    "maximum",
    "minItems",
    "maxItems",
    "pattern",
    "additionalProperties",
  ]) {
    assert.deepEqual(contract[key], runtime[key], `schema drift: ${key}`);
  }
  if (contract.type === "object") {
    assert.deepEqual(
      [...(contract.required as string[])].sort(),
      [...(runtime.required as string[])].sort(),
    );
    assert.deepEqual(
      Object.keys(contract.properties ?? {}).sort(),
      Object.keys(runtime.properties ?? {}).sort(),
    );
    for (const [name, child] of Object.entries(contract.properties ?? {})) {
      compare(child, runtime.properties?.[name]);
    }
  }
  if (contract.type === "array") compare(contract.items, runtime.items);
  if (contract["x-maxUtf16CodeUnits"] !== undefined) {
    assert.equal(contract["x-maxUtf16CodeUnits"], runtime.maxLength);
  }
  if (contract["x-maxUtf8Bytes"] !== undefined) {
    assert.equal(contract["x-maxUtf8Bytes"], CURATION_LIMITS.maxWordBytes);
  }
}

test("internal OpenAPI security/server and representable Zod constraints agree", async () => {
  const raw: unknown = JSON.parse(
    await readFile(
      new URL("../../api/openapi/openapi.yaml", import.meta.url),
      "utf8",
    ),
  );
  const document = z
    .object({
      paths: z.object({
        "/internal/curate": z.object({
          post: z.object({
            security: z.array(z.record(z.string(), z.array(z.string()))),
            servers: z.array(z.object({ url: z.string() })),
            requestBody: z.object({
              content: z.object({
                "application/json": z.object({ schema: z.unknown() }),
              }),
            }),
            responses: z.object({
              "200": z.object({
                content: z.object({
                  "application/json": z.object({ schema: z.unknown() }),
                }),
              }),
            }),
          }),
        }),
      }),
    })
    .parse(raw);
  const operation = document.paths["/internal/curate"].post;
  assert.deepEqual(operation.security, [{ LLMServiceBearer: [] }]);
  assert.equal(operation.servers[0]?.url, "http://127.0.0.1:8090");
  const schemas = createCurationSchemas(CURATION_LIMITS);
  compare(
    operation.requestBody.content["application/json"].schema,
    z.toJSONSchema(schemas.requestSchema, { unrepresentable: "any" }),
  );
  compare(
    operation.responses["200"].content["application/json"].schema,
    z.toJSONSchema(schemas.responseSchema, { unrepresentable: "any" }),
  );
});
