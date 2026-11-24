import assert from "node:assert/strict";
import { test } from "node:test";
import { serverEnvSchema } from "../src/config/server.js";

test("curation is enabled when the flag is omitted", () => {
  assert.equal(serverEnvSchema.parse({}).CURATION_ENABLED, "true");
});

test("explicit false retains health-only operation", () => {
  assert.equal(
    serverEnvSchema.parse({ CURATION_ENABLED: "false" }).CURATION_ENABLED,
    "false",
  );
});

test("invalid curation flags are rejected", () => {
  assert.equal(
    serverEnvSchema.safeParse({ CURATION_ENABLED: "yes" }).success,
    false,
  );
});
