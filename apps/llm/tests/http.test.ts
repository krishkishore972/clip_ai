import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { createApp } from "../src/http/app.js";
import { createCurator, CurationError } from "../src/tasks/curate.js";

const token = "offline_test_token_".repeat(3);
const input = {
  transcriptHash: `sha256:${"a".repeat(64)}`,
  words: [{ index: 0, text: "hello", startMs: 0, endMs: 1000 }],
  constraints: { candidateCount: 1, minDurationMs: 500, maxDurationMs: 1000 },
};

async function serve(
  service: Parameters<typeof createApp>[0],
  run: (url: string) => Promise<void>,
) {
  const server = createServer(createApp(service));
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  assert.ok(address && typeof address !== "string");
  try {
    await run(`http://127.0.0.1:${address.port}`);
  } finally {
    server.closeAllConnections();
    await new Promise<void>((resolve, reject) =>
      server.close((error) => (error ? reject(error) : resolve())),
    );
  }
}

function post(url: string, body = JSON.stringify(input), auth = token) {
  return fetch(`${url}/internal/curate`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${auth}`,
      "content-type": "application/json",
    },
    body,
  });
}

test("explicit health-only composition works without provider credentials", async () => {
  await serve(undefined, async (url) => {
    assert.equal((await fetch(`${url}/healthz`)).status, 204);
    const response = await post(url);
    assert.equal(response.status, 503);
    assert.equal(response.headers.get("cache-control"), "no-store");
  });
});

test("authenticates before parsing and validates HTTP/input boundaries", async () => {
  let calls = 0;
  await serve(
    {
      token,
      curate: async () => {
        calls++;
        return { candidates: [] };
      },
    },
    async (url) => {
      const unauthorized = await post(url, "{broken", "wrong");
      assert.equal(unauthorized.status, 401);
      assert.equal(
        (await post(url, "{broken", "x".repeat(token.length))).status,
        401,
      );
      assert.equal(
        (
          await fetch(`${url}/internal/curate`, {
            method: "POST",
          })
        ).status,
        401,
      );
      assert.equal((await post(url, "{broken")).status, 400);
      assert.equal((await post(url, JSON.stringify({}))).status, 400);
      assert.equal((await post(url, "x".repeat(1024 * 1024 + 1))).status, 413);
      const nonJson = await fetch(`${url}/internal/curate`, {
        method: "POST",
        headers: {
          authorization: `Bearer ${token}`,
          "content-type": "text/plain",
        },
        body: "private content",
      });
      assert.equal(nonJson.status, 415);
      const compressed = await fetch(`${url}/internal/curate`, {
        method: "POST",
        headers: {
          authorization: `Bearer ${token}`,
          "content-type": "application/json",
          "content-encoding": "gzip",
        },
        body: "not actually compressed",
      });
      assert.equal(compressed.status, 415);
      assert.equal(calls, 0);
      const response = await post(url);
      assert.equal(response.status, 200);
      assert.equal(response.headers.get("cache-control"), "no-store");
      assert.equal(response.headers.get("x-powered-by"), null);
      assert.deepEqual(await response.json(), { candidates: [] });
      assert.equal(calls, 1);
    },
  );
});

test("maps failures without exposing private details and releases capacity", async () => {
  for (const [error, status] of [
    [new CurationError("TIMEOUT"), 504],
    [new CurationError("PROMPT_TOO_LARGE"), 413],
    [new CurationError("PROVIDER_FAILED"), 502],
    [new CurationError("INVALID_RESPONSE"), 502],
    [new Error("secret transcript API key"), 500],
  ] as const) {
    let calls = 0;
    await serve(
      {
        token,
        curate: async () => {
          if (calls++ === 0) throw error;
          return { candidates: [] };
        },
      },
      async (url) => {
        const response = await post(url);
        assert.equal(response.status, status);
        assert.equal(response.headers.get("cache-control"), "no-store");
        const text = await response.text();
        assert.doesNotMatch(text, /secret|transcript|API key/);
        assert.equal(
          typeof (JSON.parse(text) as { traceId: unknown }).traceId,
          "string",
        );
        assert.equal((await post(url)).status, 200);
      },
    );
  }
});

test("one active request per process; rejects overload without calling curator", async () => {
  const entered = Promise.withResolvers<void>();
  const release = Promise.withResolvers<void>();
  let calls = 0;
  await serve(
    {
      token,
      curate: async () => {
        calls++;
        entered.resolve();
        await release.promise;
        return { candidates: [] };
      },
    },
    async (url) => {
      const first = post(url);
      try {
        await entered.promise;
        const second = await post(url);
        assert.equal(second.status, 503);
        assert.equal(second.headers.get("retry-after"), "1");
        assert.equal(calls, 1);
      } finally {
        release.resolve();
        await first;
      }
      assert.equal((await post(url)).status, 200);
    },
  );
});

test("disconnect aborts provider work", async () => {
  const entered = Promise.withResolvers<void>();
  const aborted = Promise.withResolvers<void>();
  await serve(
    {
      token,
      curate: async (_input, signal) => {
        entered.resolve();
        await new Promise<never>((_resolve, reject) => {
          signal.addEventListener(
            "abort",
            () => {
              aborted.resolve();
              reject(new CurationError("CANCELLED"));
            },
            { once: true },
          );
        });
        return { candidates: [] };
      },
    },
    async (url) => {
      const controller = new AbortController();
      const response = fetch(`${url}/internal/curate`, {
        method: "POST",
        headers: {
          authorization: `Bearer ${token}`,
          "content-type": "application/json",
        },
        body: JSON.stringify(input),
        signal: controller.signal,
      });
      const rejected = assert.rejects(response);
      await entered.promise;
      controller.abort();
      await rejected;
      await aborted.promise;
    },
  );
});

test("authenticated HTTP request runs the real adapter with an offline provider", async () => {
  const candidate = {
    startWordIndex: 0,
    endWordIndexExclusive: 1,
    title: "Hello",
    hook: "Greeting",
    reason: "Offline fixture",
  };
  const curate = await createCurator(
    {
      promptPath: fileURLToPath(
        new URL("../src/prompts/curate/v1.md", import.meta.url),
      ),
    },
    async () => ({
      choices: [
        {
          finish_reason: "stop",
          message: { content: JSON.stringify({ candidates: [candidate] }) },
        },
      ],
    }),
  );
  await serve({ token, curate }, async (url) => {
    const response = await post(url);
    assert.equal(response.status, 200);
    assert.deepEqual(await response.json(), { candidates: [candidate] });
  });
});

test("capacity is shared across app instances and health stays available", async () => {
  const entered = Promise.withResolvers<void>();
  const release = Promise.withResolvers<void>();
  await serve(
    {
      token,
      curate: async () => {
        entered.resolve();
        await release.promise;
        return { candidates: [] };
      },
    },
    async (firstUrl) => {
      await serve(
        {
          token,
          curate: async () => {
            assert.fail("second app must not bypass process concurrency");
          },
        },
        async (secondUrl) => {
          const pending = post(firstUrl);
          try {
            await entered.promise;
            assert.equal((await fetch(`${secondUrl}/healthz`)).status, 204);
            assert.equal((await post(secondUrl)).status, 503);
          } finally {
            release.resolve();
            await pending;
          }
        },
      );
    },
  );
});
