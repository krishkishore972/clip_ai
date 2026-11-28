import assert from "node:assert/strict";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { readFile } from "node:fs/promises";
import Groq from "groq-sdk";
import { CURATION_BUDGETS } from "../src/config/curation.js";
import { compactCurationInput } from "../src/tasks/curation-input.js";
import {
  createCurator,
  CurationError,
  type CuratorOptions,
} from "../src/tasks/curate.js";

// Test budgets only, not product defaults.
const options = {
  model: "openai/gpt-oss-120b",
  promptPath: fileURLToPath(
    new URL("../src/prompts/curate/v2.md", import.meta.url),
  ),
  limits: {
    maxCandidates: 3,
    maxClipDurationMs: 5000,
    maxTranscriptWords: 10,
    maxWordBytes: 512,
    maxMetadataCharacters: 1000,
  },
  timeoutMs: 1000,
  maxOutputTokens: 500,
  maxResponseBytes: 4096,
} satisfies CuratorOptions;

const request = {
  transcriptHash: `sha256:${"a".repeat(64)}`,
  words: [
    { index: 0, text: "मेरे", startMs: 0, endMs: 1000 },
    { index: 1, text: "मित्र", startMs: 320, endMs: 560 },
    { index: 2, text: "कैसे", startMs: 560, endMs: 960 },
  ],
  constraints: {
    candidateCount: 2,
    minDurationMs: 500,
    maxDurationMs: 1000,
  },
};

const candidate = {
  startWordIndex: 0,
  endWordIndexExclusive: 2,
  title: "मित्र",
  hook: "Opening",
  reason: "A complete thought",
};

function completion(candidates: unknown[]) {
  return {
    choices: [
      {
        finish_reason: "stop",
        message: { content: JSON.stringify({ candidates }) },
      },
    ],
  };
}

function hasCode(code: CurationError["code"]) {
  return (error: unknown) =>
    error instanceof CurationError && error.code === code;
}

test("loads v2, sends compact arrays with bounded options and permits overlap", async () => {
  const candidates = [candidate, { ...candidate, endWordIndexExclusive: 3 }];
  const curate = await createCurator(options, async (body, config) => {
    assert.equal(body.model, options.model);
    assert.equal(body.messages[0]?.role, "system");
    assert.match(String(body.messages[0]?.content), /untrusted/);
    assert.equal(
      body.messages[1]?.content,
      JSON.stringify({
        transcriptHash: request.transcriptHash,
        words: ["मेरे", "मित्र", "कैसे"],
        startsMs: [0, 320, 560],
        endsMs: [1000, 560, 960],
        constraints: request.constraints,
      }),
    );
    assert.deepEqual(body.response_format, { type: "json_object" });
    assert.equal(config.maxRetries, 1);
    assert.equal(config.timeout, options.timeoutMs);
    return completion(candidates);
  });
  assert.deepEqual(await curate(request), { candidates });
});

test("allows no candidates", async () => {
  const curate = await createCurator(options, async () => completion([]));
  assert.deepEqual(await curate(request), { candidates: [] });
});

test("uses configured defaults and forwards a total-budget signal", async () => {
  const curate = await createCurator(
    { promptPath: options.promptPath },
    async (body, config) => {
      assert.equal(config.timeout, CURATION_BUDGETS.timeoutMs);
      assert.equal(
        body.max_completion_tokens,
        CURATION_BUDGETS.maxOutputTokens,
      );
      assert.equal(config.maxRetries, 1);
      assert.equal(config.signal?.aborted, false);
      return completion([]);
    },
  );
  assert.deepEqual(await curate(request), { candidates: [] });
});

test("rejects excessive configuration budgets", async () => {
  for (const override of [
    { timeoutMs: 30_001 },
    { overallTimeoutMs: 65_001 },
    { maxPromptBytes: 100_001 },
    { maxOutputTokens: 8_193 },
    { maxResponseBytes: 32 * 1024 + 1 },
    { limits: { ...options.limits!, maxCandidates: 6 } },
  ]) {
    await assert.rejects(
      createCurator({ ...options, ...override }, async () => completion([])),
      hasCode("CONFIGURATION"),
    );
  }
});

test("checks complete UTF-8 message size before provider work", async () => {
  const curate = await createCurator(
    { ...options, maxPromptBytes: 100 },
    async () => {
      assert.fail("oversized messages must not call Groq");
    },
  );
  await assert.rejects(curate(request), hasCode("PROMPT_TOO_LARGE"));
});

test("compaction preserves positions, signed times, Unicode and input objects", () => {
  const input = structuredClone(request);
  input.words = [
    { index: 0, text: 'हाय "hello"\\\n', startMs: -7, endMs: 1000 },
    { index: 1, text: "same", startMs: 100, endMs: 300 },
    { index: 2, text: "same", startMs: 200, endMs: 400 },
  ];
  const original = structuredClone(input);
  const compact = compactCurationInput(input);
  assert.equal(compact.words.length, compact.startsMs.length);
  assert.equal(compact.words.length, compact.endsMs.length);
  for (const [index, word] of input.words.entries()) {
    assert.equal(compact.words[index], word.text);
    assert.equal(compact.startsMs[index], word.startMs);
    assert.equal(compact.endsMs[index], word.endMs);
  }
  assert.deepEqual(input, original);
  assert.deepEqual(JSON.parse(JSON.stringify(compact)), compact);
});

test("compact full messages obey the same inclusive UTF-8 byte budget", async () => {
  const prompt = await readFile(options.promptPath, "utf8");
  const bytes = Buffer.byteLength(
    JSON.stringify([
      { role: "system", content: prompt },
      { role: "user", content: JSON.stringify(compactCurationInput(request)) },
    ]),
    "utf8",
  );
  const legacyBytes = Buffer.byteLength(
    JSON.stringify([
      { role: "system", content: prompt },
      { role: "user", content: JSON.stringify(request) },
    ]),
    "utf8",
  );
  assert.ok(bytes < legacyBytes);
  let calls = 0;
  for (const [budget, succeeds] of [
    [bytes, true],
    [bytes - 1, false],
  ] as const) {
    const curate = await createCurator(
      { ...options, maxPromptBytes: budget },
      async () => {
        calls++;
        return completion([]);
      },
    );
    if (succeeds) {
      assert.deepEqual(await curate(request), { candidates: [] });
    } else {
      await assert.rejects(curate(request), hasCode("PROMPT_TOO_LARGE"));
    }
  }
  assert.equal(calls, 1);
});

test("word size is UTF-8 bytes, not JavaScript character count", async () => {
  const curate = await createCurator(options, async () => {
    assert.fail("oversized words must not call Groq");
  });
  await assert.rejects(
    curate({
      ...request,
      words: [{ index: 0, text: "म".repeat(171), startMs: 0, endMs: 1000 }],
    }),
    hasCode("INVALID_INPUT"),
  );
});

test("total deadline aborts outstanding work and returns a safe timeout", async () => {
  let aborted = false;
  const curate = await createCurator(
    { ...options, overallTimeoutMs: 20 },
    async (_body, config) =>
      new Promise((_resolve, reject) => {
        config.signal?.addEventListener(
          "abort",
          () => {
            aborted = true;
            reject(new Error("private provider error"));
          },
          { once: true },
        );
      }),
  );
  await assert.rejects(curate(request), hasCode("TIMEOUT"));
  assert.equal(aborted, true);
});

test("caller cancellation aborts in-flight provider work", async () => {
  const controller = new AbortController();
  const curate = await createCurator(options, async (_body, config) => {
    const pending = new Promise((_resolve, reject) => {
      config.signal?.addEventListener(
        "abort",
        () => reject(new Error("cancelled")),
        { once: true },
      );
    });
    controller.abort();
    return pending;
  });
  await assert.rejects(
    curate(request, controller.signal),
    hasCode("CANCELLED"),
  );
});

test("official SDK retries transient failures once, not permanent or output failures", async (t) => {
  for (const scenario of [
    { name: "transient recovery", statuses: [500, 200], succeeds: true },
    { name: "transient exhaustion", statuses: [500, 500], succeeds: false },
    { name: "rate limit recovery", statuses: [429, 200], succeeds: true },
    { name: "authentication failure", statuses: [401], succeeds: false },
    { name: "invalid output", statuses: [200], succeeds: false },
  ]) {
    await t.test(scenario.name, async () => {
      let calls = 0;
      const sdk = new Groq({
        apiKey: "offline-test-key",
        fetch: async () => {
          const status = scenario.statuses[calls++];
          assert.notEqual(
            status,
            undefined,
            "unexpected extra provider attempt",
          );
          const body =
            status === 200
              ? scenario.name === "invalid output"
                ? completion([{ ...candidate, endWordIndexExclusive: 99 }])
                : completion([candidate])
              : { error: { message: "offline failure" } };
          return new Response(JSON.stringify(body), {
            status,
            headers: {
              "content-type": "application/json",
              "retry-after-ms": "1",
            },
          });
        },
      });
      const curate = await createCurator(options, (body, config) =>
        sdk.chat.completions.create(body, config),
      );
      if (scenario.succeeds) {
        assert.deepEqual(await curate(request), { candidates: [candidate] });
      } else {
        await assert.rejects(
          curate(request),
          hasCode(
            scenario.name === "invalid output"
              ? "INVALID_RESPONSE"
              : "PROVIDER_FAILED",
          ),
        );
      }
      assert.equal(calls, scenario.statuses.length);
    });
  }
});

test("defaults to the selected model and rejects other models", async () => {
  const { model: _model, ...withoutModel } = options;
  const curate = await createCurator(withoutModel, async (body) => {
    assert.equal(body.model, "openai/gpt-oss-120b");
    return completion([]);
  });
  await curate(request);
  await assert.rejects(
    createCurator(
      { ...options, model: "another-model" } as unknown as CuratorOptions,
      async () => completion([]),
    ),
    hasCode("CONFIGURATION"),
  );
});

test("rejects invalid input before the provider call", async () => {
  let calls = 0;
  const curate = await createCurator(options, async () => {
    calls++;
    return completion([]);
  });
  await assert.rejects(
    curate({ ...request, transcriptHash: "invalid" }),
    hasCode("INVALID_INPUT"),
  );
  assert.equal(calls, 0);
});

test("rejects invalid model responses", async (t) => {
  const cases: Record<string, unknown> = {
    bounds: completion([{ ...candidate, endWordIndexExclusive: 4 }]),
    duplicate: completion([candidate, candidate]),
    count: completion([
      candidate,
      { ...candidate, endWordIndexExclusive: 3 },
      { ...candidate, startWordIndex: 1, endWordIndexExclusive: 3 },
    ]),
    inverted: completion([{ ...candidate, endWordIndexExclusive: 0 }]),
    duration: completion([
      { ...candidate, startWordIndex: 1, endWordIndexExclusive: 2 },
    ]),
    extraField: completion([{ ...candidate, timestamp: 0 }]),
    malformed: {
      choices: [{ finish_reason: "stop", message: { content: "not JSON" } }],
    },
    truncated: {
      choices: [{ finish_reason: "length", message: { content: "{}" } }],
    },
    oversized: {
      choices: [
        {
          finish_reason: "stop",
          message: { content: "x".repeat(options.maxResponseBytes + 1) },
        },
      ],
    },
  };
  for (const [name, response] of Object.entries(cases)) {
    await t.test(name, async () => {
      const curate = await createCurator(options, async () => response);
      await assert.rejects(curate(request), hasCode("INVALID_RESPONSE"));
    });
  }
});

test("duration uses the maximum selected end, not the last word end", async () => {
  const curate = await createCurator(options, async () =>
    completion([candidate]),
  );
  await assert.rejects(
    curate({
      ...request,
      constraints: { ...request.constraints, maxDurationMs: 900 },
    }),
    hasCode("INVALID_RESPONSE"),
  );
});

test("does not clamp negative source starts", async () => {
  const curate = await createCurator(options, async () =>
    completion([candidate]),
  );
  await assert.rejects(
    curate({
      ...request,
      words: [{ ...request.words[0]!, startMs: -7 }, ...request.words.slice(1)],
    }),
    hasCode("INVALID_RESPONSE"),
  );
});

test("sanitizes provider errors and forwards cancellation", async () => {
  const controller = new AbortController();
  const curate = await createCurator(options, async (_body, config) => {
    assert.ok(config.signal);
    assert.equal(config.signal.aborted, false);
    throw new Error("private transcript and provider credentials");
  });
  await assert.rejects(
    curate(request, controller.signal),
    hasCode("PROVIDER_FAILED"),
  );
  controller.abort();
  await assert.rejects(
    curate(request, controller.signal),
    hasCode("CANCELLED"),
  );
});
