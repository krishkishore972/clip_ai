const { test } = require("node:test");
const assert = require("node:assert/strict");
const { join } = require("node:path");
const lib = (name) => require(join(process.env.UPLOAD_TEST_BUILD_DIR, name));
const { createPreparationApi, parsePreparationStatus } =
  lib("api/preparation.js");
const { AuthenticationRequired } = lib("api/uploads.js");
const { createPreparationPoller } = lib("uploads/preparation-poller.js");
const { preparationCopy } = lib("uploads/preparation-copy.js");
const status = (state = "pending") => ({
  assetId: "asset-1",
  status: state,
  attempts: state === "not_prepared" || state === "pending" ? 0 : 1,
  failureCode: state === "failed" ? "PREPARATION_FAILED" : null,
  updatedAt: "2026-11-20T10:00:00Z",
  preparedAt: state === "ready" ? "2026-11-20T09:59:00Z" : null,
});
const settle = async () => {
  for (let i = 0; i < 12; i++) await Promise.resolve();
};

test("preparation public parser strips unknown fields and accepts every state", () => {
  for (const state of [
    "not_prepared",
    "pending",
    "preparing",
    "ready",
    "failed",
  ]) {
    const result = parsePreparationStatus(
      {
        ...status(state),
        storageKey: "secret",
      },
      "asset-1",
    );
    assert.deepEqual(result, status(state));
  }
});
test("preparation parser rejects mismatched identity, state, counters, dates and invariants", () => {
  for (const patch of [
    { assetId: "other" },
    { status: "unknown" },
    { attempts: 3 },
    { attempts: -1 },
    { attempts: 0.5 },
    { attempts: "1" },
    { updatedAt: "2026-02-30T00:00:00Z" },
    { failureCode: "https://secret" },
    { preparedAt: "2026-11-20T09:00:00Z" },
    { status: "ready" },
    { status: "failed" },
    { status: "preparing", attempts: 0 },
  ])
    assert.throws(() =>
      parsePreparationStatus({ ...status(), ...patch }, "asset-1"),
    );
  assert.throws(() => parsePreparationStatus(status(), "../asset-1"));
  assert.throws(() =>
    parsePreparationStatus({ ...status("ready"), attempts: 0 }, "asset-1"),
  );
  assert.throws(() =>
    parsePreparationStatus(
      { ...status("ready"), preparedAt: "2026-11-21T00:00:00Z" },
      "asset-1",
    ),
  );
});
test("preparation calls bearer-only API, explicit ensure has no body, never fetches signed S3", async () => {
  const requests = [];
  let tokens = 0;
  const api = createPreparationApi(
    "https://api.invalid",
    async (url, init) => {
      requests.push({ url, init });
      return Response.json(status(), {
        status: init.method === "POST" ? 202 : 200,
      });
    },
    async () => `token-${++tokens}`,
  );
  await api.status("asset-1", new AbortController().signal);
  await api.ensure("asset-1", new AbortController().signal);
  assert.equal(tokens, 2);
  requests.forEach(({ url, init }, i) => {
    assert.equal(url, "https://api.invalid/media/asset-1/preparation");
    assert.deepEqual(init.headers, {
      Accept: "application/json",
      Authorization: `Bearer token-${i + 1}`,
    });
    assert.equal(init.body, undefined);
    assert.equal(init.credentials, "omit");
    assert.equal(init.cache, "no-store");
  });
});
for (const [code, pattern] of [
  [401, /session expired/],
  [404, /unavailable/],
  [409, /not ready/],
  [503, /temporarily/],
  [500, /valid preparation/],
])
  test(`preparation HTTP ${code} is safe and does not expose server data`, async () => {
    const api = createPreparationApi(
      "https://api.invalid",
      async () =>
        new Response("https://secret.invalid/token", { status: code }),
      async () => "token",
    );
    await assert.rejects(
      api.status("asset-1", new AbortController().signal),
      (error) => {
        assert.match(error.message, pattern);
        assert.doesNotMatch(error.message, /secret/);
        return true;
      },
    );
  });
test("native request failures and malformed JSON are sanitized", async () => {
  for (const fetcher of [
    async () => {
      throw new Error("https://secret.invalid");
    },
    async () => new Response("not json"),
  ]) {
    const api = createPreparationApi(
      "https://api.invalid",
      fetcher,
      async () => "token",
    );
    await assert.rejects(
      api.status("asset-1", new AbortController().signal),
      (error) => !/secret|json/i.test(error.message),
    );
  }
});
test("preparation never sends request after auth/cancellation failure", async () => {
  let calls = 0;
  const api = createPreparationApi(
    "https://api.invalid",
    async () => {
      calls++;
    },
    async () => null,
  );
  await assert.rejects(
    api.status("asset-1", new AbortController().signal),
    AuthenticationRequired,
  );
  const abort = new AbortController();
  abort.abort();
  await assert.rejects(api.status("asset-1", abort.signal));
  assert.equal(calls, 0);
});
function fixture(read, ensure = async () => status()) {
  let queued;
  const poller = createPreparationPoller(read, ensure, {
    set(cb) {
      assert.equal(queued, undefined);
      queued = cb;
      return 1;
    },
    clear() {
      queued = undefined;
    },
  });
  return {
    poller,
    queued: () => !!queued,
    tick: async () => {
      const cb = queued;
      queued = undefined;
      cb();
      await settle();
    },
  };
}
test("preparation waits 60 seconds between automatic checks; manual refresh stays immediate", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  let calls = 0;
  const poller = createPreparationPoller(
    async () => {
      calls++;
      return status();
    },
    async () => status(),
  );
  try {
    poller.setEnabled(true);
    await settle();
    assert.equal(calls, 1);
    t.mock.timers.tick(59_999);
    await settle();
    assert.equal(calls, 1);
    t.mock.timers.tick(1);
    await settle();
    assert.equal(calls, 2);
    poller.check();
    await settle();
    assert.equal(calls, 3);
    t.mock.timers.tick(59_999);
    await settle();
    assert.equal(calls, 3);
    t.mock.timers.tick(1);
    await settle();
    assert.equal(calls, 4);
    poller.setEnabled(false);
    t.mock.timers.tick(60_000);
    await settle();
    assert.equal(calls, 4);
  } finally {
    poller.setEnabled(false);
  }
});

test("preparation sequential pending/preparing/ready polling stops terminal", async () => {
  const states = ["pending", "preparing", "ready"];
  let calls = 0;
  const f = fixture(async () => status(states[calls++]));
  f.poller.setEnabled(true);
  await settle();
  await f.tick();
  await f.tick();
  assert.equal(calls, 3);
  assert.equal(f.queued(), false);
  f.poller.setEnabled(false);
});
for (const state of ["not_prepared", "failed", "ready"])
  test(`preparation ${state} stops automatic polling`, async () => {
    const f = fixture(async () => status(state));
    f.poller.setEnabled(true);
    await settle();
    assert.equal(f.queued(), false);
    f.poller.setEnabled(false);
  });
test("archival failure before first claim parses with zero attempts and stops polling", async () => {
  const failed = {
    ...status("failed"),
    attempts: 0,
    failureCode: "PREPARATION_RETRIES_EXHAUSTED",
  };
  assert.deepEqual(parsePreparationStatus(failed, "asset-1"), failed);
  let reads = 0;
  let writes = 0;
  const f = fixture(
    async () => {
      reads++;
      return parsePreparationStatus(failed, "asset-1");
    },
    async () => {
      writes++;
      return status();
    },
  );
  f.poller.setEnabled(true);
  await settle();
  assert.deepEqual(f.poller.getSnapshot().status, failed);
  assert.equal(f.poller.getSnapshot().error, null);
  assert.equal(f.queued(), false);
  f.poller.prepare();
  await settle();
  assert.equal(reads, 1);
  assert.equal(writes, 0);
  f.poller.setEnabled(false);
});
test("ensure is explicit and only available for no-record legacy status", async () => {
  let writes = 0;
  const f = fixture(
    async () => status("not_prepared"),
    async () => {
      writes++;
      return status("pending");
    },
  );
  f.poller.setEnabled(true);
  await settle();
  assert.equal(writes, 0);
  f.poller.prepare();
  await settle();
  assert.equal(writes, 1);
  assert.equal(f.queued(), true);
  f.poller.prepare();
  await settle();
  assert.equal(writes, 1);
  f.poller.setEnabled(false);
});
test("background/stale session ignores late results; resumed request never overlaps", async () => {
  const pending = [];
  const f = fixture(
    (signal) => new Promise((resolve) => pending.push({ signal, resolve })),
  );
  f.poller.setEnabled(true);
  f.poller.check();
  f.poller.setEnabled(false);
  f.poller.setEnabled(true);
  assert.equal(pending.length, 1);
  assert.equal(pending[0].signal.aborted, true);
  pending[0].resolve(status("ready"));
  await settle();
  assert.equal(pending.length, 2);
  assert.equal(f.poller.getSnapshot().status, null);
  f.poller.setEnabled(false);
  pending[1].resolve(status());
  await settle();
  assert.equal(f.queued(), false);
});
test("auth pauses preparation; network errors preserve separate stale state and safe copy", async () => {
  let calls = 0;
  const f = fixture(async () => {
    if (++calls === 1) return status();
    if (calls === 2) throw new Error("https://secret.invalid");
    throw new AuthenticationRequired();
  });
  f.poller.setEnabled(true);
  await settle();
  await f.tick();
  assert.equal(f.poller.getSnapshot().status.status, "pending");
  assert.equal(f.poller.getSnapshot().stale, true);
  assert.doesNotMatch(f.poller.getSnapshot().error, /secret/);
  await f.tick();
  f.poller.check();
  await settle();
  assert.equal(calls, 3);
  assert.equal(f.queued(), false);
  f.poller.setEnabled(false);
  assert.doesNotMatch(
    preparationCopy({ ...status("failed"), failureCode: "SECRET_ERROR" })
      .detail,
    /SECRET/,
  );
});
