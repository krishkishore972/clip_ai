# Chorus Clip

A mobile-first video application with a Go API and a private upload, validation and media-preparation pipeline. This repository also retains a separate Next.js web starter; it is not the native mobile application.

**Last reviewed:** 2026-11-20. **Evidence:** [V] verified from source, automated checks and the developer's real-device logs/UI confirmation; [U] explicitly unverified behavior. These tags describe the evidence, not a production-readiness guarantee.

## Current scope

[V] Implemented:

- Clerk sign-in and direct user ownership of media assets; no product workspaces or teams.
- Branded native Home, Upload and Account screens.
- Private multipart S3 uploads with progress and best-effort cancellation. Automatic upload retries, background transfer and resume are intentionally excluded.
- Atomic PostgreSQL asset/outbox persistence, a separate row-lock-based dispatcher and Redis/Asynq processing.
- A validation worker that downloads the original to private temporary storage, probes it with `go-ffprobe`/FFprobe and leaves the S3 original unchanged.
- Owner-only `GET /media/{assetId}/status` and foreground/focus-aware mobile status polling.
- Opt-in automatic preparation after successful validation: a 16 kHz mono PCM WAV derivative for ASR, with independent durable state, queue and worker. Originals remain unchanged.
- Validation-ready and preparation-ready are checked separately; preparation does not expose media playback or storage URLs.

[V] A real-device upload reached `ready` on its first validation attempt, and the mobile client displayed **Validation passed — 1 / 4**. Backend race/integration checks, vet/build, mobile type-check/lint and offline tests passed during the implementation work.

[U] Live rejection, transient-failure retry and worker-crash recovery checks are deferred at the user's request. New preparation code is not yet verified against the application DB or real S3; migration 00006 has not been applied there and preparation defaults disabled. Persistent media browsing, projects, transcription, AI clip suggestions, editing, captions, rendering and secure export downloads remain unimplemented.

### Validation policy

[V] Current limits are 2 GB, two hours, up to 4K including portrait, required audio and explicit container/codec allowlists. One worker handles one task at a time. Download timeout is 15 minutes, probe timeout one minute and whole-task timeout 20 minutes. Retryable validation failures allow three retries after the initial attempt; permanent rejection does not retry. These worker retries are separate from the intentionally absent mobile upload retries.

## Repository layout

| Location                                                | Purpose                                                                               |
| ------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| [Mobile](apps/mobile/)                                  | Expo SDK 58 / React Native client; see its [setup guide](apps/mobile/README.md).      |
| [API](apps/api/)                                        | Go HTTP server, validation worker, dispatcher and migration CLI.                      |
| [Web](apps/web/)                                        | Retained Next.js starter on port 3000; not yet a product editor.                      |
| [Shared UI](packages/ui/)                               | Web React components; the web starter currently uses its Button.                      |
| [ESLint configuration](packages/eslint-config/)         | Shared web/library lint configuration.                                                |
| [TypeScript configuration](packages/typescript-config/) | Shared TypeScript presets.                                                            |
| [OpenAPI](apps/api/openapi/openapi.yaml)                | API contract; generates the [mobile API types](apps/mobile/src/lib/api/generated.ts). |
| [Repository guidance](AGENTS.md)                        | Contribution rules, implementation history and safety constraints.                    |

Product/architecture notes also exist locally under the Git-ignored documentation directory. They are not an additional Next.js application or guaranteed to be present in a fresh clone.

## Prerequisites

- Node.js 24+ and pnpm 11.25.0, as declared by this workspace. Use pnpm only.
- Go 1.27.1 and FFprobe on the worker's `PATH`.
- PostgreSQL, Redis and a private S3 bucket for the upload/validation workflow.
- Clerk backend configuration and a mobile publishable key from the same application.
- An Expo Go client compatible with SDK 58, or an appropriate native development build.

Install JavaScript dependencies from the repository root:

```bash
pnpm install --frozen-lockfile
```

## Backend setup

From the repository root:

```bash
cd apps/api
make env
```

The [Makefile](apps/api/Makefile) creates a local environment file only if it is missing or empty, then loads it for its targets. Configure it using the [API environment example](apps/api/.env.example):

- Keep `API_ENV=development` and use `API_ADDR=:8080` for LAN device access.
- Set the PostgreSQL connection, Clerk issuer/secret and any applicable audience/authorized-party settings.
- Configure the AWS region, private bucket and server-side credential chain/profile; refresh SSO credentials if applicable.
- Keep the upload-signing secret stable while uploads are active.
- Configure Redis, dispatcher budgets and an absolute private worker temporary directory. Allow at least 2 GB free per worker, with additional headroom.

Keep the S3 bucket private, block public access, grant only required IAM permissions and configure incomplete-multipart-upload cleanup. Do not add an expiration rule that deletes original videos. AWS credentials, database/Redis URLs, Clerk secret keys and upload-signing secrets must never go into the mobile application or source control.

### Migrations

Check the target before changing it:

```bash
make migrate-status
```

[V] Versions 00001–00005 were applied and verified on the configured development database on 2026-11-19. That does not mean a different database or fresh clone is migrated.

Only after approving the exact target and pending migrations, run:

```bash
make migrate-up
```

Migrations are explicit, not part of HTTP startup. Applied SQL migrations are immutable. Never run rollback tests against application data; integration tests require a separate disposable test database.

## Run the development processes

Have PostgreSQL and Redis running first. Use separate terminals, each starting in the repository root:

```bash
# Terminal 1: API (per-command development S3 opt-in)
cd apps/api
make run DEV_S3_UPLOAD_ENABLED=true
```

```bash
# Terminal 2: validation worker
cd apps/api
make worker VALIDATION_WORKER_ENABLED=true
```

```bash
# Terminal 3: dispatcher, after the worker starts
cd apps/api
make dispatcher OUTBOX_DISPATCH_ENABLED=true
```

Starting the worker/dispatcher can process existing queued or pending assets, not only a new test upload, and can incur S3 charges. Keep opt-in settings disabled when processing is not intended. Make command-line overrides take precedence over local defaults.

Check API liveness:

```bash
curl -i http://localhost:8080/healthz
```

Expected: `204 No Content`. This is liveness, not a full S3/queue readiness check.

For the native client, configure its device-reachable API URL and start Expo as described in the [mobile guide](apps/mobile/README.md).

For the retained web starter only:

```bash
pnpm --filter web dev
```

It serves at `http://localhost:3000`. The root `pnpm dev` script runs registered development tasks for the API/web; it does not start Expo, Redis, PostgreSQL, the dispatcher or the worker.

Stop each foreground process with Ctrl+C and allow the worker to finish its shutdown. Do not flush Redis or remove database rows to reset a test.

## Media preparation

The new media-processing feature requires separately approved migration **00006**. Do not enable it before applying the reviewed migration to the intended database. New completed uploads enqueue one combined processing job when `MEDIA_PREPARATION_ENABLED=true`; the job validates the original and extracts audio from the same download. Existing ready uploads are not automatically backfilled. The current-upload card offers an explicit **Prepare media** action only when a preparation record is missing. It cannot reset failed attempts or restart terminal work.

After the migration is approved/applied, opt in explicitly in separate terminals:

```bash
cd apps/api
# API: uploads remain development-only.
make run DEV_S3_UPLOAD_ENABLED=true MEDIA_PREPARATION_ENABLED=true
# Combined processing worker: downloads each original once, validates it and extracts audio.
make media-worker MEDIA_PREPARATION_ENABLED=true MEDIA_PREPARATION_WORKER_ENABLED=true
# Dispatcher: deliver media-processing events.
make dispatcher OUTBOX_DISPATCH_ENABLED=true
```

Preparation concurrency is one per process, with two FFmpeg codec threads, a 90-minute task budget and at most two durable attempts. FFmpeg and FFprobe must be installed; the private preparation temporary root must be separate from validation and have at least 8 GiB free at startup. Normalized WAV files are capped at 240 MB. CPU/thread/deadline limits are not a full OS sandbox or memory/disk quota; deployment isolation remains a release requirement.

Grant the preparation role only the required original reads and conditional writes under the private derived prefix `dev-uploads/<assetId>/derived/v1/`. Do not grant public access or original-object deletion/overwrite rights. Output namespaces are immutable and unrelated to DB fencing tokens. Private unpublished derivatives can remain after ambiguous failures; a safe orphan-retention policy is still needed before release.

The owner-authorized preparation/status routes do not expose storage locations. The normalized audio is internal to the processing pipeline and is not signed or returned to the mobile client.

## Checks and generated contracts

From the repository root:

```bash
pnpm --filter mobile test:uploads
pnpm --filter mobile test:design-system
pnpm --filter mobile check-types
pnpm --filter mobile lint
pnpm --filter web check-types
pnpm --filter web lint
pnpm --filter @repo/ui check-types
pnpm --filter @repo/ui lint
```

All Go test files are grouped under the [central API test directory](apps/api/tests/README.md). Application composition lives in importable internal packages, while CLI entrypoints remain thin wrappers. Standard `go test ./...` still discovers the complete suite.

Backend checks, with no application/test database opt-in variables injected:

```bash
cd apps/api
env -u TEST_DATABASE_URL -u TEST_ASYNQ_REDIS_URL \
  -u TEST_REDIS_URL -u VERIFY_MIGRATION_DATABASE_URL \
  go test -race ./...
go vet ./...
go build ./...
gofmt -l .
```

The formatting check should print nothing. For coverage across the imported production packages, use `make cover` or `go test -coverpkg=./internal/...,./cmd/...,./migrations -coverprofile=coverage.out ./...`; plain test-package coverage is not representative after centralization. Database/Redis integration checks are separately opt-in; normal offline checks do not prove a live S3/device workflow. Root build/lint/type-check/test scripts orchestrate the tasks that workspaces actually register; run the mobile suites explicitly rather than assuming the root test command includes them.

After changing the OpenAPI contract, regenerate from the repository root rather than editing generated TypeScript:

```bash
pnpm --filter mobile gen:api
node apps/mobile/scripts/generate-api-types.mjs --check
```

The contract is JSON-compatible YAML because the generator and drift guards use JSON parsing. When formatting it, explicitly use `pnpm exec prettier --parser json --write apps/api/openapi/openapi.yaml`, not the YAML printer.

## Next verification

[I] The user deferred live invalid-video/network/retry/crash checks; they remain release gaps, not verified behavior. New preparation needs separately approved migration/IAM/service setup. The next implementation milestone is a reviewed canonical project model and basic clip rendering from immutable originals, followed by separately selected transcription/AI providers. The broader editing application and its phase exits are not complete.
