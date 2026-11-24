const { test } = require("node:test");
const assert = require("node:assert/strict");
const { join } = require("node:path");
const { createUploadApi, AuthenticationRequired } = require(
  join(process.env.UPLOAD_TEST_BUILD_DIR, "api/uploads.js"),
);
const {
  uploadVideo,
  videoMetadata,
  clipPreferencesFromInput,
  validClipPreferences,
} = require(join(process.env.UPLOAD_TEST_BUILD_DIR, "uploads/upload-video.js"));

const API = "http://127.0.0.1:8080";
const TOKEN = "payload.signature";
const file = {
  ...videoMetadata("sample.mp4", 10),
  clipPreferences: {
    candidateCount: 5,
    minDurationMs: 15000,
    maxDurationMs: 60000,
  },
};
const session = {
  uploadId: "upload-1",
  uploadToken: TOKEN,
  status: "uploading",
  partSizeBytes: 4,
  partCount: 3,
  expiresAt: "2026-12-01T00:00:00Z",
  uploadedParts: [],
};
const response = (status, body, headers = {}) =>
  new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers,
  });

function fixture(options = {}) {
  const events = [],
    reads = [],
    puts = [],
    progress = [];
  const bytes = Uint8Array.from({ length: 10 }, (_, i) => i);
  const original = bytes.slice();
  const controller = new AbortController();
  let closed = false;
  let putActive = false;
  let tokenCalls = 0;
  const source = {
    sizeBytes: 10,
    async read(offset, length) {
      assert.equal(putActive, false, "only one part in flight");
      reads.push([offset, length]);
      if (options.cancelRead) controller.abort();
      return bytes.slice(offset, offset + length - (options.shortRead ? 1 : 0));
    },
    close() {
      closed = true;
      events.push("close");
    },
  };
  const api = createUploadApi(
    API,
    async (url, request) => {
      if (!url.startsWith(API)) {
        assert.equal(request.method, "PUT");
        assert.equal(request.headers["Upload-Token"], undefined);
        assert.equal(request.headers.Authorization, undefined);
        assert.ok(request.body instanceof Uint8Array);
        events.push("put");
        puts.push([...request.body]);
        if (options.cancelPut) {
          putActive = true;
          const pending = new Promise((_, reject) =>
            request.signal.addEventListener(
              "abort",
              () => {
                putActive = false;
                events.push("put-settled");
                reject(new Error("secret signed URL"));
              },
              { once: true },
            ),
          );
          controller.abort();
          return pending;
        }
        if (options.failPut) return response(403, {});
        return response(
          200,
          {},
          options.noETag ? {} : { ETag: '"quoted-tag"' },
        );
      }
      const path = url.slice(API.length);
      assert.equal(request.headers.Authorization, "Bearer clerk-test-token");
      events.push(`${request.method} ${path}`);
      if (path === "/uploads") {
        assert.equal(request.headers["Upload-Token"], undefined);
        assert.deepEqual(JSON.parse(request.body), file);
        if (options.lostCreate) throw new Error("secret signed URL");
        return response(201, { ...session, ...options.plan });
      }
      assert.equal(request.headers["Upload-Token"], TOKEN);
      if (request.method === "DELETE") {
        assert.equal(closed, true, "reader must close before remote abort");
        assert.equal(
          putActive,
          false,
          "active PUT must settle before remote abort",
        );
        assert.equal(
          request.signal.aborted,
          false,
          "cleanup uses fresh signal",
        );
        return response(options.abortFails ? 409 : 204, {});
      }
      if (path.endsWith("/parts/sign")) {
        const partNumber = JSON.parse(request.body).partNumbers[0];
        return response(200, {
          parts: [
            {
              partNumber: options.wrongPart ? 99 : partNumber,
              url: "https://bucket.example.invalid/part?secret=signature",
              headers: options.leakHeader ? { "Upload-Token": TOKEN } : {},
              expiresAt: session.expiresAt,
            },
          ],
        });
      }
      if (path.endsWith("/complete")) {
        assert.deepEqual(
          JSON.parse(request.body).parts,
          [1, 2, 3].map((partNumber) => ({ partNumber, eTag: '"quoted-tag"' })),
        );
        if (options.lostComplete) throw new Error("secret signed URL");
        if (options.cancelComplete) controller.abort();
        return response(200, { ...session, status: "uploaded_unvalidated" });
      }
      assert.fail("unexpected request");
    },
    async () => {
      tokenCalls++;
      return options.expireAfterCreate && tokenCalls > 1
        ? null
        : "clerk-test-token";
    },
  );
  return {
    events,
    reads,
    puts,
    progress,
    controller,
    run: () =>
      uploadVideo({
        api,
        source,
        file: options.file ?? file,
        signal: controller.signal,
        onProgress: (value) => progress.push(value),
      }),
    assertClosed() {
      assert.equal(closed, true);
      assert.deepEqual(bytes, original);
    },
  };
}

test("create → sequential bounded chunks → complete, preserving bytes, token and quoted ETags", async () => {
  const f = fixture();
  assert.equal((await f.run()).status, "uploaded_unvalidated");
  assert.deepEqual(f.reads, [
    [0, 4],
    [4, 4],
    [8, 2],
  ]);
  assert.deepEqual(f.puts.flat(), [0, 1, 2, 3, 4, 5, 6, 7, 8, 9]);
  assert.deepEqual(
    f.progress
      .filter((p) => p.phase === "uploading")
      .map((p) => p.completedBytes),
    [0, 4, 8, 10],
  );
  assert.equal(f.progress.at(-1).phase, "complete");
  assert.equal(
    f.events.some((e) => e.startsWith("DELETE")),
    false,
  );
  f.assertClosed();
});

test("clip input converts exact decimal seconds to integer milliseconds", () => {
  assert.deepEqual(
    clipPreferencesFromInput("5", "15", "60"),
    file.clipPreferences,
  );
  assert.deepEqual(clipPreferencesFromInput("1", "0.001", "1.005"), {
    candidateCount: 1,
    minDurationMs: 1,
    maxDurationMs: 1005,
  });
  for (const values of [
    ["", "15", "60"],
    ["6", "15", "60"],
    ["1", "", "60"],
    ["1", "60", "15"],
    ["1", "15", "60.001"],
    ["1", "0", "60"],
    ["1", "0.0001", "60"],
    ["1", "1e1", "60"],
  ]) {
    assert.throws(() => clipPreferencesFromInput(...values), /Choose/);
  }
});

test("invalid preferences stop before create and close the reader", async () => {
  assert.equal(validClipPreferences(undefined), false);
  assert.equal(
    validClipPreferences({ ...file.clipPreferences, candidateCount: 1.5 }),
    false,
  );
  const f = fixture({ file: { ...file, clipPreferences: undefined } });
  await assert.rejects(f.run(), /Choose valid clip count/);
  assert.equal(f.events.includes("POST /uploads"), false);
  f.assertClosed();
});

test("failed part aborts without automatic retry or duplicate create", async () => {
  const f = fixture({ failPut: true });
  await assert.rejects(f.run(), /part upload failed/);
  assert.equal(f.puts.length, 1);
  assert.equal(f.events.filter((e) => e === "POST /uploads").length, 1);
  assert.equal(f.events.at(-1), "DELETE /uploads/upload-1");
  f.assertClosed();
});

test("session expiry stops the upload without creating a replacement", async () => {
  const f = fixture({ expireAfterCreate: true });
  await assert.rejects(f.run(), /session expired/);
  assert.equal(f.events.filter((event) => event === "POST /uploads").length, 1);
  assert.equal(f.puts.length, 0);
  f.assertClosed();
});

test("signed-out control requests never reach the network", async () => {
  let calls = 0;
  const api = createUploadApi(
    API,
    async () => {
      calls++;
      throw new Error("unexpected network");
    },
    async () => null,
  );
  await assert.rejects(
    api.create(file, new AbortController().signal),
    /session expired/,
  );
  assert.equal(calls, 0);
});

test("401 is safe and is not automatically retried", async () => {
  let calls = 0;
  const api = createUploadApi(
    API,
    async () => {
      calls++;
      return response(401, { message: "sensitive provider data" });
    },
    async () => "test-session",
  );
  await assert.rejects(api.me(new AbortController().signal), (error) => {
    assert.match(error.message, /session expired/);
    assert.doesNotMatch(error.message, /sensitive provider data/);
    return true;
  });
  assert.equal(calls, 1);
});

test("/me uses the current session token and returns internal identity", async () => {
  let reads = 0;
  const api = createUploadApi(
    API,
    async (url, request) => {
      assert.equal(url, `${API}/me`);
      assert.equal(request.headers.Authorization, `Bearer session-${reads}`);
      assert.equal(request.headers["Upload-Token"], undefined);
      return response(200, {
        userId: "internal-user",
      });
    },
    async () => `session-${++reads}`,
  );
  for (let i = 0; i < 2; i++) {
    assert.deepEqual(await api.me(new AbortController().signal), {
      userId: "internal-user",
    });
  }
  assert.equal(reads, 2);
});

test("/me rejects missing or empty user ownership", async () => {
  for (const data of [null, {}, { userId: "" }, { userId: 123 }]) {
    const api = createUploadApi(
      API,
      async () => response(200, data),
      async () => "test-session",
    );
    await assert.rejects(
      api.me(new AbortController().signal),
      // The shared request wrapper intentionally sanitizes response errors.
      /Request failed or timed out/,
    );
  }
});

test("cancellation settles active PUT and closes reader before remote abort", async () => {
  const f = fixture({ cancelPut: true });
  await assert.rejects(f.run(), /Upload stopped/);
  assert.ok(f.events.indexOf("put-settled") < f.events.indexOf("close"));
  assert.equal(f.events.at(-1), "DELETE /uploads/upload-1");
  f.assertClosed();
});

test("cancellation during a read prevents signing or sending bytes", async () => {
  const f = fixture({ cancelRead: true });
  await assert.rejects(f.run(), /Upload stopped/);
  assert.equal(f.puts.length, 0);
  assert.equal(
    f.events.some((e) => e.endsWith("/parts/sign")),
    false,
  );
  f.assertClosed();
});

test("already cancelled upload never creates a remote upload", async () => {
  const f = fixture();
  f.controller.abort();
  await assert.rejects(f.run(), /Upload stopped/);
  assert.deepEqual(f.events, ["close"]);
  f.assertClosed();
});

for (const [name, options, message] of [
  ["short read", { shortRead: true }, /expected video bytes/],
  ["invalid plan", { plan: { partCount: 99 } }, /invalid upload plan/],
  [
    "oversized chunk plan",
    { plan: { partSizeBytes: 32 * 1024 * 1024, partCount: 1 } },
    /invalid upload plan/,
  ],
  ["wrong signed part", { wrongPart: true }, /wrong upload part/],
  [
    "token header leak",
    { leakHeader: true },
    /invalid part upload instructions/,
  ],
  ["missing ETag", { noETag: true }, /ETag/],
]) {
  test(`rejects ${name}, closes source and attempts abort`, async () => {
    const f = fixture(options);
    await assert.rejects(f.run(), message);
    assert.equal(f.events.at(-1), "DELETE /uploads/upload-1");
    f.assertClosed();
  });
}

test("lost create response is not retried and secrets are not shown", async () => {
  const f = fixture({ lostCreate: true });
  await assert.rejects(f.run(), (error) => {
    assert.match(error.message, /creation reached the server/);
    assert.doesNotMatch(error.message, /secret signed URL/);
    return true;
  });
  assert.deepEqual(f.events, ["POST /uploads", "close"]);
  f.assertClosed();
});

test("failed abort explicitly reports unconfirmed cleanup", async () => {
  const f = fixture({ failPut: true, abortFails: true });
  await assert.rejects(f.run(), /cleanup was not confirmed/);
  f.assertClosed();
});

test("lost completion does not report cancellation or retry the upload", async () => {
  const f = fixture({ lostComplete: true, abortFails: true });
  await assert.rejects(
    f.run(),
    /video may already be stored; it was not deleted/,
  );
  assert.equal(f.events.filter((e) => e.endsWith("/complete")).length, 1);
  assert.equal(f.puts.length, 3);
  assert.notEqual(f.progress.at(-1).phase, "complete");
  f.assertClosed();
});

test("confirmed completion wins a late cancellation", async () => {
  const f = fixture({ cancelComplete: true });
  assert.equal((await f.run()).status, "uploaded_unvalidated");
  assert.equal(
    f.events.some((e) => e.startsWith("DELETE")),
    false,
  );
  f.assertClosed();
});

test("metadata and API configuration checks", () => {
  assert.equal(videoMetadata("clip.MOV", 1).contentType, "video/quicktime");
  for (const name of ["clip.txt", "../clip.mp4", "clip\n.mp4"])
    assert.throws(() => videoMetadata(name, 1));
  for (const size of [0, -1, 1.5, 2_000_000_001])
    assert.throws(() => videoMetadata("clip.mp4", size));
  for (const base of [
    "",
    "file:///tmp",
    "https://user:password@host",
    "https://host?secret=1",
  ])
    assert.throws(() => createUploadApi(base));
});

const mediaStatus = {
  assetId: "asset-1",
  status: "uploaded_unvalidated",
  attempts: 0,
  maxAttempts: 2,
  failureCode: null,
  updatedAt: "2026-11-19T10:00:00Z",
  validatedAt: null,
};

test("media status only GETs the encoded API asset with fresh Clerk bearer and isolated headers", async () => {
  let tokenReads = 0;
  let requests = 0;
  const api = createUploadApi(
    API,
    async (url, request) => {
      requests++;
      assert.equal(url, `${API}/media/asset%3A1/status`);
      assert.equal(request.method, "GET");
      assert.equal(request.body, undefined);
      assert.equal(request.redirect, "error");
      assert.deepEqual(request.headers, {
        Accept: "application/json",
        Authorization: `Bearer token-${tokenReads}`,
      });
      return response(200, {
        ...mediaStatus,
        assetId: "asset:1",
        privateProviderField: "discard",
      });
    },
    async () => `token-${++tokenReads}`,
  );
  for (let i = 0; i < 2; i++) {
    assert.deepEqual(
      await api.mediaStatus("asset:1", new AbortController().signal),
      { ...mediaStatus, assetId: "asset:1" },
    );
  }
  assert.equal(requests, 2);
  assert.equal(tokenReads, 2);
});

test("media status fail-closed parser rejects unknown state, mismatched ID and malformed required fields", async () => {
  const malformed = [
    null,
    [],
    {},
    { ...mediaStatus, status: "processing" },
    { ...mediaStatus, assetId: "other" },
    ...[-1, 5, 0.5, "1", null].map((attempts) => ({
      ...mediaStatus,
      attempts,
    })),
    ...[-1, 0, 3, 5, 0.5, "2", null, undefined].map((maxAttempts) => ({
      ...mediaStatus,
      maxAttempts,
    })),
    { ...mediaStatus, failureCode: undefined },
    { ...mediaStatus, failureCode: 1 },
    { ...mediaStatus, failureCode: "https://secret.invalid" },
    { ...mediaStatus, failureCode: "lowercase_code" },
    { ...mediaStatus, failureCode: "A".repeat(65) },
    { ...mediaStatus, updatedAt: "not a time" },
    { ...mediaStatus, updatedAt: "2026-02-30T10:00:00Z" },
    { ...mediaStatus, updatedAt: "2026-11-19T24:00:00Z" },
    { ...mediaStatus, validatedAt: undefined },
    { ...mediaStatus, validatedAt: "yesterday" },
  ];
  for (const data of malformed) {
    const api = createUploadApi(
      API,
      async () => response(200, data),
      async () => "token",
    );
    await assert.rejects(
      api.mediaStatus("asset-1", new AbortController().signal),
      /valid video status/,
    );
  }
  for (const state of [
    "uploaded_unvalidated",
    "validating",
    "ready",
    "rejected",
    "validation_failed",
  ]) {
    const data = {
      ...mediaStatus,
      status: state,
      attempts: 4,
      failureCode: "AUDIO_REQUIRED",
      validatedAt: "2026-11-19T10:00:00.123456789+00:00",
    };
    const api = createUploadApi(
      API,
      async () => response(200, data),
      async () => "token",
    );
    assert.deepEqual(
      await api.mediaStatus("asset-1", new AbortController().signal),
      data,
    );
  }
});

test("media status safely handles 401, 404, 503, invalid JSON and secret-bearing network errors", async () => {
  for (const [httpStatus, expected] of [
    [401, /session expired/],
    [404, /unavailable.*not found/],
    [503, /temporarily unavailable/],
  ]) {
    const api = createUploadApi(
      API,
      async () => response(httpStatus, { message: "https://secret.invalid" }),
      async () => "token",
    );
    await assert.rejects(
      api.mediaStatus("asset-1", new AbortController().signal),
      (error) => {
        assert.match(error.message, expected);
        assert.doesNotMatch(error.message, /secret/);
        if (httpStatus === 401)
          assert.ok(error instanceof AuthenticationRequired);
        return true;
      },
    );
  }
  for (const fetcher of [
    async () => new Response("private invalid JSON", { status: 200 }),
    async () => {
      throw new Error("https://secret.invalid");
    },
  ]) {
    const api = createUploadApi(API, fetcher, async () => "token");
    await assert.rejects(
      api.mediaStatus("asset-1", new AbortController().signal),
      (error) => {
        assert.doesNotMatch(error.message, /secret|private/);
        return true;
      },
    );
  }
});

test("status read failure after completion never reuploads or aborts the asset", async () => {
  const f = fixture();
  const completed = await f.run();
  const api = createUploadApi(
    API,
    async () => response(503, {}),
    async () => "token",
  );
  await assert.rejects(
    api.mediaStatus(completed.uploadId, new AbortController().signal),
  );
  assert.equal(f.progress.at(-1).phase, "complete");
  assert.equal(f.events.filter((event) => event === "POST /uploads").length, 1);
  assert.equal(
    f.events.some((event) => event.startsWith("DELETE")),
    false,
  );
});
