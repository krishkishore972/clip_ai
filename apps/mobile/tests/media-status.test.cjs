const { test } = require("node:test");
const assert = require("node:assert/strict");
const { join } = require("node:path");
const { createMediaStatusPoller } = require(
  join(process.env.UPLOAD_TEST_BUILD_DIR, "uploads/media-status-poller.js"),
);
const { mediaStatusCopy } = require(
  join(process.env.UPLOAD_TEST_BUILD_DIR, "uploads/media-status-copy.js"),
);
const { AuthenticationRequired } = require(
  join(process.env.UPLOAD_TEST_BUILD_DIR, "api/uploads.js"),
);

const status = (state = "uploaded_unvalidated") => ({
  assetId: "asset-1",
  status: state,
  attempts: 1,
  failureCode: null,
  updatedAt: "2026-11-19T10:00:00Z",
  validatedAt: null,
});
const settle = async () => {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
};
function fixture(read) {
  let queued;
  let calls = 0;
  const poller = createMediaStatusPoller(
    (signal) => {
      calls++;
      return read(signal, calls);
    },
    {
      set(callback) {
        assert.equal(queued, undefined);
        queued = callback;
        return 1;
      },
      clear() {
        queued = undefined;
      },
    },
  );
  return {
    poller,
    calls: () => calls,
    queued: () => !!queued,
    async tick() {
      assert.ok(queued);
      const callback = queued;
      queued = undefined;
      callback();
      await settle();
    },
  };
}

test("pending → validating → ready polls sequentially and stops", async () => {
  const states = ["uploaded_unvalidated", "validating", "ready"];
  const f = fixture(async (_, count) => status(states[count - 1]));
  f.poller.setEnabled(true);
  await settle();
  assert.equal(f.poller.getSnapshot().status.status, states[0]);
  await f.tick();
  assert.equal(f.poller.getSnapshot().status.status, states[1]);
  await f.tick();
  assert.equal(f.poller.getSnapshot().status.status, "ready");
  assert.equal(f.queued(), false);
  assert.equal(f.calls(), 3);
  f.poller.setEnabled(false);
});

for (const state of ["rejected", "validation_failed"]) {
  test(`${state} stops polling without changing upload state`, async () => {
    const f = fixture(async () => status(state));
    f.poller.setEnabled(true);
    await settle();
    assert.equal(f.queued(), false);
    assert.equal(f.poller.getSnapshot().status.status, state);
    f.poller.setEnabled(false);
  });
}

test("read errors preserve stale last status; manual check recovers", async () => {
  const f = fixture(async (_, count) => {
    if (count === 2) throw new Error("https://secret.invalid/provider");
    return status(count === 3 ? "ready" : undefined);
  });
  f.poller.setEnabled(true);
  await settle();
  await f.tick();
  assert.equal(f.poller.getSnapshot().status.status, "uploaded_unvalidated");
  assert.equal(f.poller.getSnapshot().stale, true);
  assert.doesNotMatch(f.poller.getSnapshot().error, /secret/);
  f.poller.check();
  await settle();
  assert.equal(f.poller.getSnapshot().status.status, "ready");
  assert.equal(f.poller.getSnapshot().error, null);
  assert.equal(f.poller.getSnapshot().stale, false);
  assert.equal(f.queued(), false);
  f.poller.setEnabled(false);
});

test("background/focus loss aborts, late results ignored, foreground resumes with one in flight", async () => {
  const pending = [];
  const f = fixture(
    (signal) => new Promise((resolve) => pending.push({ signal, resolve })),
  );
  f.poller.setEnabled(true);
  f.poller.check();
  assert.equal(f.calls(), 1);
  f.poller.setEnabled(false);
  assert.equal(pending[0].signal.aborted, true);
  f.poller.setEnabled(true);
  assert.equal(f.calls(), 1, "aborted request must settle before next read");
  pending[0].resolve(status("ready"));
  await settle();
  assert.equal(f.calls(), 2);
  assert.equal(f.poller.getSnapshot().status, null, "late ready must not win");
  pending[1].resolve(status("validating"));
  await settle();
  assert.equal(f.poller.getSnapshot().status.status, "validating");
  f.poller.setEnabled(false);
  assert.equal(f.queued(), false);
});

test("unmount/signout/replacement disposal ignores late responses and starts no more reads", async () => {
  let resolve;
  const f = fixture(
    () =>
      new Promise((done) => {
        resolve = done;
      }),
  );
  f.poller.setEnabled(true);
  f.poller.setEnabled(false);
  resolve(status("ready"));
  await settle();
  assert.equal(f.poller.getSnapshot().status, null);
  assert.equal(f.queued(), false);
  assert.equal(f.calls(), 1);
});

test("401 pauses auto/manual polling even across foreground until new authenticated controller", async () => {
  const f = fixture(async () => {
    throw new AuthenticationRequired();
  });
  f.poller.setEnabled(true);
  await settle();
  assert.equal(f.poller.getSnapshot().authenticationRequired, true);
  assert.equal(f.queued(), false);
  f.poller.check();
  f.poller.setEnabled(false);
  f.poller.setEnabled(true);
  await settle();
  assert.equal(f.calls(), 1);
  f.poller.setEnabled(false);
});

test("manual checks while a read is active never overlap or queue duplicates", async () => {
  let resolve;
  const f = fixture(
    () =>
      new Promise((done) => {
        resolve = done;
      }),
  );
  f.poller.setEnabled(true);
  for (let i = 0; i < 20; i++) f.poller.check();
  assert.equal(f.calls(), 1);
  resolve(status());
  await settle();
  assert.equal(f.calls(), 1);
  f.poller.setEnabled(false);
});

test("replacing the session reader neither resets state nor starts overlapping reads", async () => {
  let finishFirst;
  const f = fixture(
    () =>
      new Promise((resolve) => {
        finishFirst = resolve;
      }),
  );
  f.poller.setEnabled(true);
  let replacements = 0;
  f.poller.setRead(async () => {
    replacements++;
    return status("ready");
  });
  f.poller.check();
  assert.equal(f.calls(), 1);
  assert.equal(replacements, 0);
  assert.equal(f.poller.getSnapshot().checking, true);
  finishFirst(status());
  await settle();
  assert.equal(f.poller.getSnapshot().status.status, "uploaded_unvalidated");
  await f.tick();
  assert.equal(replacements, 1);
  assert.equal(f.poller.getSnapshot().status.status, "ready");
  assert.equal(f.queued(), false);
  f.poller.setEnabled(false);
});

test("human copy uses safe known rejection messages, unknown codes never displayed", () => {
  assert.match(
    mediaStatusCopy({ ...status("rejected"), failureCode: "AUDIO_REQUIRED" })
      .detail,
    /audio track/,
  );
  assert.doesNotMatch(
    mediaStatusCopy({
      ...status("rejected"),
      failureCode: "SECRET_PROVIDER_ERROR",
    }).detail,
    /SECRET/,
  );
  assert.equal(
    typeof mediaStatusCopy({
      ...status("rejected"),
      failureCode: "constructor",
    }).detail,
    "string",
  );
  assert.match(
    mediaStatusCopy(status("ready")).detail,
    /does not mean playback or clips/,
  );
  assert.match(mediaStatusCopy(status()).detail, /Workers may be disabled/);
});
