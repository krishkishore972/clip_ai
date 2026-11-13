<!-- BEGIN:turborepo-agent-rules -->

# This is NOT the Turborepo you know

Turborepo configuration, task behavior, and CLI commands can vary between installed versions and may differ from your training data. Resolve the `turbo` package from this file's directory or relevant workspace; in monorepos, it may not be visible from the repository root. For example, run `node -p "require.resolve('turbo/package.json')"` from a workspace that depends on `turbo`.

Read `docs/README.md` inside that installed package first, then read the relevant pages from its `docs/` directory before changing Turborepo configuration or commands. Heed deprecation notices. These bundled docs match the installed package version and are available without network access.

This block is written and re-added by `turbo` before repository-scoped commands when an AI agent is detected. In the Turborepo source repository, its template is defined in `crates/turborepo-cli/src/cli/agent_guidance.rs`. Removing the managed block while updates are enabled means a later qualifying invocation will add it again. Set `"agentGuidance": true` in the root `turbo.json` or `turbo.jsonc` to opt in; this does not remove an existing block. Keep the block committed with your work to avoid an uncommitted change on the next agent invocation.
<!-- END:turborepo-agent-rules -->

# AGENTS.md — Video AI Editor

Operating guide for AI agents working in this repository.

Read this file, then `docs/implementation/roadmap.md`, before touching code.

---

## 0. Quick reference

| Task | Command | Status |
|---|---|---|
| Install everything | `pnpm install` | Verified |
| Run all dev servers | `pnpm dev` | [V] Web + API scripts exist; combined startup not re-verified 2026-11-12 |
| Web dev server | `pnpm --filter web dev` → `http://localhost:3000` | Verified |
| Build all | `pnpm build` | [V] Web + API scripts exist; combined build not re-verified 2026-11-12 |
| Lint all | `pnpm lint` | Verified |
| Type-check all | `pnpm check-types` | Verified |
| Format | `pnpm format` | Verified |
| API dev server | `make env`, then `make run` (from `apps/api`) | [V] Entrypoint is `cmd/server`; startup not re-run 2026-11-12 |
| API tests | `go test ./...` (from `apps/api`) | [V] Passed with race detector and coverage 2026-11-12 |
| API race tests | `go test -race ./...` (from `apps/api`) | [V] Passed 2026-11-12 |
| LLM service dev | `pnpm --filter llm dev` | **Planned** |
| Contract regen | `pnpm gen:contracts` | **Planned** |
| E2E tests | `pnpm test:e2e` | **Planned** |

Toolchain present on this machine (verified 2026-11-06): Node `v24.21.0`, pnpm `11.25.0`, Go `1.27.1`, FFmpeg `8.0.1`, Docker `29.9.3`.

Never use `npm` or `yarn`. The lockfile is `pnpm-lock.yaml`; do not create other lockfiles and do not hand-edit it.

---

## 1. What we are building

An original AI-assisted short-form video editor.

Core loop:

**Upload video → transcribe with word timestamps → select/curate moments → edit non-destructively → preview → export MP4.**

Around that loop: captions, reframing/layouts, overlays, audio/music, B-roll, and a job system that survives failure.

The reference product's publicly observable behaviour is documented in
`opus-pro-report/REPORT.md`. That report is **evidence, not a specification**. It describes someone else's product; our requirements live in `docs/product/`.

Hard boundaries on that:

- Do not copy source code, private APIs, assets, branding, or protected visual design from the reference product.
- Do not bypass authentication, paywalls, rate limits, or other technical controls.
- Do not claim to know how the reference product works internally. `opus-pro-report/` already separates **Observed** from **Inferred**; keep that discipline.
- Our product decisions are ours. A limit observed in the reference (30 GB uploads, 900-credit caps, 30-day expiry) is *evidence*, not an inherited requirement, until it appears in `docs/product/acceptance-criteria.md`.

### Evidence tags used everywhere

| Tag | Meaning |
|---|---|
| **[V]** | Observed directly — our code, a live response, a tool output, first-party docs. |
| **[I]** | Inferred — a reasonable reading of observations, not confirmed. |
| **[U]** | Unknown — cannot be verified with available access. |
| **[C]** | Vendor/marketing claim, not independently measured. |

Any claim in a doc, commit message, or final report must be one of these. "I think it works" is [I] at best.

---

## 2. Verified repository state (API re-verified 2026-11-12)

This section exists so agents stop guessing. Re-verify before relying on it more than a few weeks out.

[V] API files and checks below were re-verified on 2026-11-12. Other inventory retains its 2026-11-06 verification date unless noted; this is not a full repository re-audit.

```
video_ai/
├── apps/
│   ├── api/                  # Go HTTP starter; no domain features yet
│   │   ├── cmd/server/       # HTTP entrypoint, routes, graceful shutdown
│   │   ├── internal/config/  # Environment configuration + tests
│   │   ├── internal/httpserver/ # Server, middleware, JSON/errors + tests
│   │   ├── go.mod            # Go 1.27.1; AWS SDK v2 dependencies
│   │   ├── Makefile          # Run/build/test/lint helpers; loads local .env
│   │   ├── package.json      # pnpm/Turbo adapter for Go commands
│   │   ├── turbo.json        # Adds bin/** to inherited build outputs
│   │   └── .env.example      # Required environment keys and dev values
│   └── web/                  # Next.js 16 App Router, React 19, TS
├── packages/
│   ├── eslint-config/        # @repo/eslint-config
│   ├── typescript-config/    # @repo/typescript-config
│   └── ui/                   # @repo/ui — stub components
├── opus-pro-report/          # reference-product research (evidence only)
├── package.json              # root scripts: build/dev/lint/format/check-types
├── pnpm-workspace.yaml       # apps/* and packages/*
├── turbo.json                # build/lint/check-types/test/dev tasks
├── docs/                     # Local product/implementation docs exist
└── AGENTS.md
```

Verified facts:

- `apps/web` = Next.js `16.3.8`, React `19.2.8`, TypeScript `7.0.2`, ESLint `10.9.1`, App Router, dev on port `3000`, imports `@repo/ui`.
- [V, 2026-11-13] `apps/mobile` uses Expo `58.0.24`, React Native `0.88.1`, and Expo Router. User approved simplifying the Android/iOS foreground uploader to select → create → sequential bounded read/sign/PUT → complete, with progress and best-effort cancel/abort. Automatic retries, status recovery, and same-context resume were removed intentionally. It reads only `EXPO_PUBLIC_API_URL`; keep AWS credentials/signing secrets server-side. API TS models are generated from `apps/api/openapi/openapi.yaml` via `pnpm --filter mobile gen:api`; full response-schema parsing is deferred. User requested mobile-only scope: default Expo web files remain unchanged, with known scaffold ref-type and effect-lint issues. Do not add web-specific support/fixes without approval. Device interaction and real S3 upload remain unverified; no background transfer or resume support is promised. No new per-handler documentation files are needed; keep changes and verification results in the existing roadmap.
- [V, 2026-11-12] `apps/api` is a Go module (`github.com/krishkishore972/clip_ai`, Go `1.27.1`) with AWS SDK for Go v2 dependencies. It has environment validation, structured JSON logging, request IDs, request logging, panic recovery, JSON/error helpers, HTTP timeouts, and graceful shutdown.
- [V, 2026-11-12] `GET /healthz` returns `204 No Content`. Multipart control routes under `/uploads` exist only for `API_ENV=development`. Startup connects `internal/upload` to the SDK adapter only when `DEV_S3_UPLOAD_ENABLED=true` and configuration is valid; otherwise valid requests return `503`. Enabling in another environment fails startup. The development service is stateless: a secret HMAC-signed `Upload-Token` binds the random object key, AWS upload ID, file/part plan, scope, and expiry. No persistent create-request deduplication is promised; `Idempotency-Key` is no longer required for this dev slice. Completion verifies authoritative S3 part sizes/ETags and final object metadata, and remains `uploaded_unvalidated`. No playback or AI processing is started.
- [V, 2026-11-12] `internal/storage/s3.go` uses AWS SDK v2 with the default credential chain (CLI profiles/SSO supported). `config.LoadDevelopmentUpload` controls opt-in; placeholders remain in `.env.example`, with no changes to local `.env`. Backend HTTP control-flow tests use an offline S3 metadata double; actual video upload to AWS is unverified. No worker, database integration, user authentication, durable upload-session store, project/media/job implementation, or migrations exist yet. Token possession is not user identity; do not expose development routes publicly. Shared mobile contract generation remains deferred; current Go models have lightweight contract drift checks, not full schema conformance/generation.
- [V, 2026-11-13] Database update superseding the older inventory: startup connects the user's `database/sql` + pgx pool using required `DATABASE_URL`. Goose v3.30.1 and `migrations/00001_create_media_assets.sql` now exist; explicit `make migrate-up` / `make migrate-status` use `cmd/migrate` with embedded SQL. Version 1 and its columns are verified in the development DB. Upload handlers still do not write asset records. Ownership/authentication is deferred by user request, not waived for public release. Rollback tests require a direct/session `TEST_DATABASE_URL` to a test DB; the configured connection ignores startup `search_path`, so the test now guards isolation before SQL. Disposable PostgreSQL 16 tests pass; see the existing roadmap for the initial isolation failure and correction. Never run Down against application data or modify an applied migration.
- [V, 2026-11-13] Auth superseding older unauthenticated-upload guidance: user approved Clerk (existing Chorus Clip app), hosted mobile sign-in, and local personal workspaces. `internal/auth`, GET /me, migration 00002, v2 user/workspace-bound upload capabilities, and owned completion persistence now exist. Require `CLERK_ISSUER`/`CLERK_SECRET_KEY`; never bypass auth in development. Legacy v1 tokens are rejected; NULL-owned assets remain inaccessible. New migration was tested only on disposable PostgreSQL, not applied to the application DB; ask first. Existing Clerk provider is preserved; do not rerun init or inspect environment/claim files. Go race/integration checks, 20 offline mobile client tests, and Android/iOS bundling with a fake key pass. Real Clerk/device/S3 sign-in-to-upload workflow and native UI interaction remain unverified. Default web check blockers remain; do not fix them without approval. See ADR-0003 and the existing roadmap for setup and limitations.
- [V, 2026-11-12] `go test -race -cover ./...`, `make lint` (vet + formatting), and `go build ./...` pass from `apps/api` after the handler addition.
- [V, 2026-11-12] HTTP hardening rejects trailing JSON delimiters/values via an EOF check, marshals responses before commitment with a generic `500` fallback, and aborts panicked committed responses rather than appending JSON. Regression tests cover these paths, including real HTTP connections. `WriteJSON` and `WriteError` now return errors for caller-side handling; callers must not write a second response. Streaming/WebSocket support remains deferred; these fixes do not establish production readiness.
- [V, 2026-11-12] `docs/` exists locally, including the roadmap and acceptance criteria. The 2026-11-06 inventory recorded no `apps/llm`, `packages/contracts`, `docker-compose.yml`, or CI workflow; re-check those paths before relying on that older inventory.
- [V, 2026-11-12] Root `turbo.json` declares `build`, `lint`, `check-types`, `test`, and `dev`. The API package extends root build outputs with `bin/**`.
- [V] `pnpm lint` and `pnpm check-types` passed on the scaffold on 2026-11-06. API scripts now participate in monorepo orchestration; combined root commands were not re-run during the 2026-11-12 API review.
- `opus-pro-report/` is **untracked** in git. Treat it as read-only evidence; do not refactor it, and do not let it drift into product code.

### What this means for you

1. Most "existing conventions" you would expect in a video product do not exist yet. When you create them, you are setting the convention — so do it deliberately and document it in `docs/architecture/decisions.md`.
2. Do not invent history. If something is missing, say "does not exist yet" rather than describing it as if it does.
3. The Go module and HTTP starter already exist; extend them rather than creating a second scaffold. Creating the LLM app and adding architectural foundations remain **Phase 0/4 tasks**, not incidental changes. Get them reviewed.

---

## 3. Target structure and ownership

This is the intended end state, not the current state. Create directories only when the phase needs them.

```
apps/
  web/                        # TypeScript — editor UI, no secrets, no DB access
    app/                      # App Router routes
    components/               # editor shell, timeline, preview, inspector
    lib/                      # api client, editor state, media helpers
  api/                        # Go — the only writer to Postgres and object storage
    cmd/server/               # Existing HTTP server entrypoint
    cmd/worker/               # job worker entrypoint
    internal/                 # config, httpserver, auth, project, media, job, render, store
    migrations/               # SQL migrations, forward-only + tested rollback
    openapi/openapi.yaml      # the API contract (source of truth)
  llm/                        # TypeScript — model calls only, stateless
    src/providers/            # one adapter per vendor, nothing vendor-specific outside
    src/tasks/                # transcribe, curate, captions, broll, classify
    src/prompts/              # versioned prompt files + manifest
packages/
  contracts/                  # generated TS types from apps/api/openapi/openapi.yaml
  ui/                         # shared React components
  eslint-config/              # do not modify without reason
  typescript-config/          # do not modify without reason
docs/                         # required — see §10
opus-pro-report/              # research evidence — read-only
```

### Ownership rules (enforced by review, not by tooling)

| Concern | Owner | Never |
|---|---|---|
| Project/edit state, persistence | `apps/api` | web or llm writing to Postgres |
| Auth, authorization, signed URLs | `apps/api` | trusting a client-supplied `orgId`/`userId` |
| Media probing, transcode, render | `apps/api` workers | render inside a request handler |
| Model/provider calls | `apps/llm` | provider keys in `apps/web` or the browser bundle |
| Provider API keys | `apps/llm` env only | logging keys, committing `.env` |
| Shared types | `packages/contracts` (generated) | hand-editing generated output |
| Editorial/visual components | `apps/web` + `packages/ui` | product logic inside `packages/ui` |

**One source of truth:** the canonical project document (§7) drives preview and export. There is no second representation for "what the video looks like".

---

## 4. Commands and how the monorepo runs

### Root (verified)

```bash
pnpm install
pnpm dev          # turbo run dev
pnpm build        # turbo run build
pnpm lint         # turbo run lint
pnpm check-types  # turbo run check-types
pnpm format       # prettier
```

### Workspace-scoped (verified)

```bash
pnpm --filter web dev
pnpm --filter web build
pnpm --filter web lint
pnpm --filter web check-types
pnpm --filter @repo/ui lint
```

### Go API ([V] starter exists; checks verified 2026-11-12)

```bash
cd apps/api
make env                # creates local .env from .env.example if missing/empty
make run                # loads .env; runs ./cmd/server (example address :8080)
make build              # builds commands into bin/; current executable: bin/server
go test ./...
go test -race ./...
go vet ./...
gofmt -l .              # must print nothing
```

[V] Direct `go run ./cmd/server` requires all documented environment variables to be injected; Go does not load `.env` itself. `cmd/worker` does not exist yet.

### How Turbo runs Go

[V] `apps/api/package.json` already exposes the Go tasks to pnpm/Turbo:

```jsonc
// apps/api/package.json
{
  "name": "api",
  "private": true,
  "scripts": {
    "dev": "make run",
    "build": "make build",
    "lint": "make lint",
    "test": "make test",
    "check-types": "go build ./..."
  }
}
```

[V] `apps/api/turbo.json` already extends the root configuration and adds `bin/**` using `$TURBO_EXTENDS$`. When the planned LLM app is created, give it its own package scripts and configure its build outputs. Read the installed Turbo docs before changing orchestration — the managed block at the top of this file says why.

Do not add `apps/api` or `apps/llm` to `pnpm-workspace.yaml` exclusions; the `apps/*` glob already covers them.

### Ports

| Service | Port |
|---|---|
| `web` | 3000 |
| `api` | 8080 |
| `llm` | 8090 |
| Postgres | 5432 |
| Redis | 6379 |

Ports are local defaults; read them from env, never hardcode them in libraries.

---

## 5. Stack decisions

| Layer | Choice | Rationale |
|---|---|---|
| Web | Next.js 16 App Router, React 19, TypeScript | Already installed; keep it |
| Editor state | Zustand in `apps/web/lib/state` | Small, testable, no provider tree; [I] recommended, confirm in ADR |
| Preview | Canvas/WebGL + WebCodecs, worker-based | Must not block the UI thread |
| API | Go 1.27, stdlib `net/http` `ServeMux` first | Minimises dependencies; add a router only if routing genuinely outgrows stdlib |
| DB | PostgreSQL | Projects, jobs, assets, transcripts |
| Queue | Redis-backed queue (or `asynq`-style) | Retries, visibility, cancellation |
| Object storage | S3-compatible (MinIO locally) | Originals immutable, renders derived |
| Media | FFmpeg / FFprobe | Probe, transcode, burn-in, mux |
| LLM service | TypeScript on Node 24, ESM | Matches "TypeScript for LLM"; shares types with web |
| Contracts | OpenAPI 3.1 in `apps/api/openapi/` → generated TS | One contract, two consumers |
| Tests | Go `testing` (+ Docker Postgres), Vitest, Playwright | Existing tooling plus what each layer needs |

These are defaults, not doctrine. Changing one requires an entry in `docs/architecture/decisions.md` with the alternative considered and why it lost.

**Do not add** a second frontend framework, a second ORM layer, a Python service, a vector database, or a message broker other than the chosen one, without explicit approval. Each of those is a Phase 3/4 decision, not an implementation detail.

---

## 6. Non-negotiable rules

**R1. Research before implementation.** No feature code until its flow, edge cases, and acceptance criteria exist in `docs/`.

**R2. Evidence over assumption.** Tag claims [V]/[I]/[U]/[C]. Never fabricate a test result, a benchmark, or a capability.

**R3. Phase by phase.** Work only inside the requested phase. Do not silently skip exit criteria. Do not start Phase 6 features while Phase 5 is incomplete.

**R4. Preserve what works.** Inspect before changing. Do not rewrite working infrastructure, and do not "clean up" code you were not asked to touch.

**R5. Source media is immutable.** Never overwrite, trim in place, or delete an original upload. Edits are data; renders are new objects.

**R6. One canonical project document.** Preview and export both interpret the same versioned document. No feature-local shadow state.

**R7. No fake functionality.** A control that does not change the canonical document (or is not wired to the pipeline) does not ship. No placeholder buttons, no hardcoded "success" states.

**R8. Reviewable changes.** Before coding: state the files/modules you will touch, the risks, and the assumptions. After: summarise changes, tests actually run, limitations, next step.

**R9. Test real workflows.** Unit tests for edit logic; integration tests for persistence, jobs, and media; E2E for the critical path.

**R10. Validate and protect media.** Check type, size, duration, dimensions, and codec. Enforce limits at the API, not only in the UI. Serve private media only via short-lived signed URLs.

**R11. Ask before destructive or expensive actions.** No destructive migrations, no deleting user data, no production changes, no paid provider calls at scale, no history rewrites.

**R12. Keep docs current.** Update the feature spec, parity status, architecture note, and roadmap in the same change as the behaviour.

**R13. Do not commit, push, or open PRs unless asked.** Never force-push. Never amend someone else's commit.

**R14. Do not touch `opus-pro-report/` contents** beyond adding a dated appendix. It is evidence.

**R15. Escalate rather than guess** when a missing decision affects architecture, cost, security, or user-visible behaviour (§13).

---

## 7. Canonical project model

Defined in `docs/architecture/editor-project-schema.md`; this is the short version. Treat the doc as authoritative once it exists.

```ts
interface VideoProject {
  id: string;              // stable, opaque (e.g. uuidv7)
  version: number;         // monotonic; every accepted mutation increments
  orgId: string;
  sourceAssets: MediaAsset[];
  settings: { width: number; height: number; fps: Rational; sampleRate: number };
  tracks: Track[];         // ordered; index = z-order for visual tracks
  transcript?: Transcript;
  markers?: Marker[];
  createdAt: string;       // RFC 3339 UTC
  updatedAt: string;
}

interface Rational { num: number; den: number }   // 30000/1001, never 29.97
```

Rules:

1. **Integers only for time.** Persist `startMs`, `endMs`, `durationMs` as integer milliseconds. No floats in the stored model. Compute frame indices at the render boundary from `fps` as a rational.
2. **Half-open intervals.** A clip occupies `[startMs, endMs)`. Adjacent clips never overlap by a millisecond.
3. **Stable IDs.** Any id referenced by a render, job, or client is stable across versions. Never reuse ids.
4. **Immutable source.** `sourceAssets[].storageKey` points at the original and is never rewritten. Derived media (proxies, renders, thumbnails) carries `derivedFrom` and a `kind`.
5. **Versioned writes.** Mutations go through commands that produce a new `version`. Persist an append-only operation log for undo/redo and audit; the snapshot is a cache of that log.
6. **No absolute hostnames.** Store storage keys, not URLs. URLs are minted per request via signing.
7. **Validation at the boundary.** Reject: negative durations, `endMs <= startMs`, unknown asset refs, overlapping clips on the same track, tracks longer than the declared duration, unknown schema version.
8. **Schema versioning.** A `schemaVersion` field plus migrations. Old projects must still load. Never silently drop fields.

### Time and frame discipline

- Probe with `ffprobe` and store real stream metadata; do not trust client-reported duration.
- Variable-frame-rate sources are normalised at import; record both original and normalised timing.
- Audio and video are muxed from a single timeline evaluation — no independent clocks.
- Any rounding rule (e.g. `round(ms * fps.num / (1000 * fps.den))`) lives in one shared function, used by both preview and export.

---

## 8. Service boundaries and contracts

```
Browser (apps/web)
   │  HTTPS, JSON, short-lived bearer token
   ▼
apps/api (Go)  ──────────► PostgreSQL
   │      │                Redis (jobs)
   │      └──────────────► S3-compatible storage (signed URLs)
   │  internal HTTP, service token, typed JSON
   ▼
apps/llm (TypeScript) ────► model providers (keys live here only)
```

Rules:

1. **web → api only.** The browser never talks to Postgres, S3, or a model provider directly. Uploads use a presigned URL issued by `api`.
2. **api → llm only.** `llm` is stateless: it receives input (transcript, frames, prompt, limits) and returns a **proposal**. It does not persist project state and does not decide what gets applied.
3. **AI proposes, the user (or an explicit rule) applies.** No irreversible edit is applied silently.
4. **Auth is server-side.** `api` derives `orgId`/`userId` from the token. A client-supplied identity is never trusted.
5. **Contract-first.** `apps/api/openapi/openapi.yaml` is the API truth. Go handler types come from it; `packages/contracts` is generated from it. A handler that diverges from the spec fails the contract test.
6. **Idempotency.** Mutating endpoints accept an idempotency key. Job creation is safe to retry.
7. **Errors are structured.** `{ code, message, details?, traceId }` with a stable machine-readable `code`. No stack traces or provider responses in client-facing errors.

### LLM service contract rules

- Every task has a typed input and a **schema-validated** output (zod). Invalid model output is a failure, not a guess.
- Prompts are files under `apps/llm/src/prompts/<task>/<version>.md` with a manifest recording task, version, model family, and expected schema. Prompt changes are version bumps.
- Providers sit behind adapters with a common interface. Business logic never imports a vendor SDK directly.
- Timeouts, bounded retries with jitter, and a documented fallback for every task. A model outage degrades the feature; it does not 500 the product.
- Log token counts, latency, model id, and cost estimate — never raw media, transcripts, or user content in logs.
- Every response carries `confidence` where the task supports it, and the caller decides the threshold.

---

## 9. Code conventions

### Go (`apps/api`)

- Standard layout: `cmd/` entrypoints, `internal/` packages, `migrations/`.
- No `pkg/` grab-bag. If a package is not importable by others, it belongs in `internal/`.
- Context first: `func (s *Service) Do(ctx context.Context, in Input) (Out, error)`.
- Wrap errors with `%w`; return typed/sentinel errors at boundaries; map to HTTP once, in the handler layer.
- Interfaces are declared by the consumer, kept small (1–3 methods), and never "just in case".
- Table-driven tests. `t.Parallel()` where safe. Golden files for render plans and FFmpeg argument construction.
- `gofmt` and `go vet` clean before review. No `panic` outside startup.
- Jobs are idempotent and resumable; state transitions are explicit and tested.

### TypeScript (`apps/web`, `apps/llm`)

- ESM everywhere (`"type": "module"`), Node 24 on the server.
- `strict` is on via `@repo/typescript-config`; do not weaken it per-package.
- No `any` in new code. Use `unknown` + a parser at boundaries.
- Server code never imports React. Client code never imports provider SDKs or secrets.
- Names: `PascalCase` components/types, `camelCase` values, `SCREAMING_SNAKE` env names.
- State: the canonical project document is fetched and cached; optimistic updates must reconcile to the server `version` or roll back.

### Shared packages

- `packages/ui` is presentational only: props in, elements out. No fetching, no business rules.
- `packages/contracts` is generated. Change the OpenAPI file, regenerate, commit both.
- `packages/eslint-config` and `packages/typescript-config` are infrastructure. Change them in their own PR-sized diff with a reason.

---

## 10. Documentation contract

Required structure. `[ ]` = does not exist yet; create it in the phase that needs it, never as empty placeholder files.

```
docs/
  research/
    editor-map.md                  [ ]  region-by-region map of the reference editor
    feature-inventory.md           [ ]  every candidate feature + evidence tag
    evidence-log.md                [ ]  dated observations, URLs, artifacts
    workflows/
      timeline-editing.md          [ ]
      transcript-editing.md        [ ]
      captions.md                  [ ]
      reframing.md                 [ ]
      audio.md                     [ ]
      b-roll.md                    [ ]
  product/
    feature-specifications.md      [ ]
    acceptance-criteria.md         [x]
    parity-matrix.md               [ ]
  architecture/
    current-state.md               [ ]  Phase 0 deliverable
    system-overview.md             [ ]
    editor-project-schema.md       [ ]
    rendering-pipeline.md          [ ]
    job-processing.md              [ ]
    decisions.md                   [ ]  ADRs: context, options, decision, consequence
  implementation/
    roadmap.md                     [x]
    risks-and-open-questions.md    [ ]
```

Rules:

- If an equivalent document already exists, **update it**. Do not create a parallel one.
- Every doc states its evidence tags and its "last verified" date.
- ADRs are numbered `ADR-0001`, append-only; superseding an ADR adds a new one.
- `opus-pro-report/REPORT.md` is the reference for Phase 1 evidence. Cite sections rather than re-deriving them, and re-verify claims older than the current product build before treating them as current.

---

## 11. Phase playbook

Current status:

| Phase | Status |
|---|---|
| 0 — Repository & product discovery | **In progress** — scaffold verified, `docs/architecture/current-state.md` missing |
| 1 — Reverse-engineer reference editor | **Complete for public surface** — `opus-pro-report/` |
| 2 — Per-feature deep specs | **Partial** — inventory/analysis exist; `docs/research/workflows/*` not split out |
| 3 — Product definition & prioritisation | **In progress** — MVP plan + acceptance criteria drafted, awaiting approval |
| 4 — Architecture & data model | Not started |
| 5 — Editor foundation | Not started |
| 6 — Editing tools, one at a time | Not started |
| 7 — AI capability integration | Not started |
| 8 — Preview / render / export parity | Not started |
| 9 — Quality, performance, parity | Not started |
| 10 — Release readiness | Not started |

Do not start a phase whose predecessor's exit criteria are unmet. If a phase is partially done, finish it or record explicitly what is deferred and why.

### Phase 0 — Repository and product discovery

Deliver `docs/architecture/current-state.md` and `docs/implementation/risks-and-open-questions.md`.

Prompt:

> Act as a senior product engineer joining this project. Do not implement features.
>
> Inspect the full repo (including `apps/web`, the Go HTTP starter in `apps/api`, `packages/*`, and `turbo.json`), run the documented commands, and record real results. Inventory what exists, what is reusable, and what is missing for a browser video editor. Identify languages, versions, and conventions already in use. Produce `docs/architecture/current-state.md` with a diagram and a gap analysis, and `docs/implementation/risks-and-open-questions.md`. Distinguish verified facts from recommendations. Do not modify application code.

Exit: app runs or blockers are documented; capabilities inventoried; risks recorded; no app code changed.

### Phase 1 — Reference research

Already delivered in `opus-pro-report/`. New work here is limited to closing named gaps (authenticated surfaces, unverified internals) **without** bypassing access controls.

Exit: claims are evidenced or explicitly marked unknown.

### Phase 2 — Feature deep-dives

One document per feature in `docs/research/workflows/`. Required sections: purpose, entry points, full flow, UI states, inputs/outputs, persisted data, dependencies, edge cases, acceptance criteria, [V]/[I]/[U], proposed implementation, complexity.

Prompt:

> Investigate **[FEATURE]** in our documented reference evidence. Research only; do not implement.
>
> Trace it end to end: how it opens, its default state, every control, the visible result of each, interactions with preview/timeline/transcript, selection and keyboard behaviour, undo/redo and persistence, and edge cases (very short/long media, missing assets, overlaps, invalid values). State what is exported, what is only a preview, and what remains unknown. Write the spec under `docs/research/workflows/` and update the feature inventory.

Exit: every priority feature has a testable spec; unknowns are explicit.

### Phase 3 — Product definition

Deliver `docs/product/feature-specifications.md`, `acceptance-criteria.md`, `parity-matrix.md`, and an updated roadmap.

Prompt:

> Act as product manager. Using Phase 0–2 evidence, define our first useful release. Group features by user problem, find the minimum end-to-end workflow that delivers value, and prioritise by value, effort, risk, and dependency. Separate MVP / next / later. State measurable acceptance criteria and non-functional requirements. Flag features needing paid APIs, licensed assets, or heavy compute. Recommend a scope one small team can ship incrementally. Do not write code.

Suggested first slice, to be confirmed against the repo:

**Upload → word-timestamped transcript → transcript-based cuts → preview → MP4 export.**

Exit: MVP approved; every MVP feature has acceptance criteria; deferrals explicit.

### Phase 4 — Architecture and data model

Deliver `system-overview.md`, `editor-project-schema.md`, `rendering-pipeline.md`, `job-processing.md`, `decisions.md`.

Prompt:

> Act as a staff video-infrastructure architect. Design the approved MVP against the actual repo (Go API, TS web, TS LLM service, Postgres, Redis, S3, FFmpeg). Cover: editor state boundaries, canonical project representation and versioning, source vs derived assets, tracks/clips/transforms/captions/overlays/audio, word-level transcript mapping, non-destructive operations and undo/redo, preview vs export consistency, FFmpeg/FFprobe integration and whether a composition framework is justified, job lifecycle (retry, cancellation, progress, recovery), schema/API/storage permissions/cleanup, observability, limits, security, cost. Include diagrams, API contracts, data flow, failure modes, migrations. Do not code.

Exit: schema reviewed; parity strategy defined; job lifecycle designed; ADRs recorded.

### Phase 5 — Editor foundation

Before coding: inspect current code and tests, list exact files, list assumptions/blockers, break into reviewable steps.

Scope: project create/load, secure upload, metadata extraction, preview and playback, editor layout, canonical state, basic timeline display and selection, save/load with error states.

Exit: valid video uploads and creates a project; metadata available; preview works; state persists; source file unchanged.

### Phase 6 — Editing tools, one vertical slice at a time

Order: preview/state → timeline selection → trim/split → transcript cuts → captions → crop/aspect/reframe → overlays → audio/music → filler/silence removal → B-roll → transitions → export presets.

Per feature, before coding: read the spec and acceptance criteria, inspect reusable modules, describe the implementation and exact files, list risks, and **ask** before a material assumption.

Per feature, implement: UI and interaction, canonical state update, validation and errors, preview update, persistence/reload, export support where required, undo/redo where required, tests.

Exit per feature: **Definition of Done** (§12) fully satisfied. Do not start the next complex feature until the current one is green.

### Phase 7 — AI capabilities

Order to evaluate: word-level transcription → diarisation → filler/silence detection → face/person tracking → smart reframing → B-roll retrieval → speech enhancement → voice-over.

Prompt:

> Act as an AI systems engineer implementing **[AI FEATURE]**. Define the exact input/output contract first. Evaluate candidates on quality, latency, cost, licensing, privacy, and deployment. Check whether a deterministic method suffices before using a model. Define confidence thresholds, fallbacks, retries, and human review. Implement as an isolated task in `apps/llm` behind a provider adapter with typed, schema-validated output. Add job status/progress, timeout and retry behaviour, structured logs without private media, usage/cost tracking, fixture-based tests with mocked providers, and a documented degraded mode. Integrate results into the canonical project as **reviewable proposals**, not silent edits.

Exit: contract, adapter, validation, fallback, cost tracking, and tests exist; limitations documented.

### Phase 8 — Preview, render, export parity

Verify, with evidence: timing and frame boundaries; clip order/trims/splits/gaps; crop/scale/position/aspect; caption timing/style/animation; overlay placement and layering; audio mix/volume/sync; transitions; output dimensions/fps/codec/audio; progress/cancel/retry; storage, download permissions, temp cleanup.

Method: deterministic fixture projects, FFprobe assertions on output metadata, frame/audio comparison against preview expectations. Optimise only after correctness.

Exit: representative projects export; metadata matches settings; timing/captions/overlays/audio pass; failures visible and recoverable; temp media cleaned up. **Never claim parity without test evidence.**

### Phase 9 — Quality, performance, parity

Run documented workflows, boundary and failure cases, compare against evidenced reference behaviour, check preview/export consistency, accessibility and responsive behaviour, and measure performance on representative media. File bugs with steps, expected, actual, severity. Prioritise by user impact.

Exit: parity matrix, regression report, performance report, prioritised backlog.

### Phase 10 — Release readiness

Audit auth and media authorization, upload validation and abuse controls, signed-URL expiry, secrets, job isolation/concurrency/quota/timeouts, worker crashes and stuck jobs, migrations and backup/restore, logs/metrics/traces/alerts, retention/deletion, accessibility/browser support, render cost and capacity, and user-facing errors.

Exit: release checklist, risk register, rollback plan, go/no-go. Do not deploy or modify production without explicit approval.

---

## 12. Definition of Done

A feature is done only when all of these hold:

- [ ] Behaviour matches an approved spec in `docs/`.
- [ ] The full user workflow works end to end (not just the happy-path component).
- [ ] State lives in the canonical project document (§7).
- [ ] Save and reload work.
- [ ] Preview reflects the change.
- [ ] Export reflects the change where applicable — verified, not assumed.
- [ ] Errors, empty states, and boundaries are handled.
- [ ] Original source media is unchanged.
- [ ] Tests added and passing (`go test ./...`, `pnpm check-types`, `pnpm lint`, plus E2E where the flow is critical).
- [ ] OpenAPI updated and contracts regenerated, if the API changed.
- [ ] Docs, parity matrix, and roadmap updated.
- [ ] Limitations listed honestly.

If any box is unchecked, the feature is not done. Say so.

---

## 13. Escalate — do not guess

Stop and ask when a decision affects:

1. **Architecture** — new service, new datastore, new framework, changing §7 or §8.
2. **Cost** — paid model/provider selection, render capacity, storage class, per-minute pricing.
3. **Security** — auth model, token handling, storage permissions, PII handling.
4. **User-visible behaviour** — what an ambiguous requirement means, or which of two reasonable interpretations wins.
5. **Data** — destructive migration, deleting assets, changing id semantics, changing time units.
6. **Scope** — a request that implies features outside the approved MVP.

State the options, the trade-off, and your recommendation. Then wait.

Never: invent a requirement, silently widen scope, or paper over a failing test.

---

## 14. Session workflow

**Start**

1. Read this file and `docs/implementation/roadmap.md` (when it exists).
2. Identify the current phase and its exit criteria. Confirm the task is inside that scope.
3. `git status` — note uncommitted work that is not yours. Do not revert or reformat it.
4. Inspect the files you intend to change and their neighbours. Match existing style.
5. State your plan: files to create/modify, assumptions, risks, test plan.

**During**

6. Smallest complete vertical slice. No drive-by refactors.
7. Run the checks that cover your change. Fix real failures; never delete or skip a test to pass.
8. If you hit a §13 item, stop and ask.

**End — report in this shape**

1. What was completed.
2. Files created or changed.
3. Tests run and their **actual** results.
4. Known limitations and unresolved questions.
5. Whether the phase's exit criteria are met.
6. The next smallest recommended task.

---

## 15. Security and media safety

- Secrets live in `.env` files that are gitignored; `.env.example` documents keys with fake values. Never commit a real key or log one.
- Validate uploads server-side: MIME by content, extension, size, duration, dimensions, codec. Reject before storing.
- Enforce our own limits (to be defined in `docs/product/`), with an explicit per-plan ceiling. Do not inherit reference-product numbers silently.
- Private media only through short-lived signed URLs scoped to one object; no public bucket ACLs.
- Render workers run in isolation with CPU/memory/time limits; a malformed file must fail one job, not the host.
- Temp files are namespaced per job and deleted on success, failure, and cancellation.
- Log identifiers, not content. No transcripts, frames, or prompt bodies in logs by default.
- Every mutating endpoint authorizes against the owning `orgId`, not just authentication.

---

## 16. Open decisions (answer before Phase 4 closes)

Record outcomes as ADRs in `docs/architecture/decisions.md`.

| # | Question | Blocks |
|---|---|---|
| 1 | Postgres access: `pgx` + `sqlc`, or an ORM? | Phase 4 schema |
| 2 | Router: stdlib `ServeMux` or `chi`? | Phase 5 API surface |
| 3 | Queue: Redis + `asynq`, or Postgres-backed jobs? | Phase 4 job design |
| 4 | Render: FFmpeg filter graph vs a composition framework (e.g. Remotion)? | Phase 4/8 |
| 5 | Preview strategy: WebCodecs + canvas, or server-rendered proxies? | Phase 5 |
| 6 | Transcript model: our own ASR vs a hosted provider? | Phase 7 |
| 7 | Storage: which S3-compatible provider, and what is the local dev stand-in? | Phase 5 |
| 8 | Auth: which identity provider, and is `orgId` a first-class concept on day one? | Phase 5 |
| 9 | Upload ceiling and duration ceiling for v1? | Phase 3/5 |
| 10 | Do we ship a public API in v1, or is OpenAPI internal-only at first? | Phase 4 |

Until answered, choose the option that adds the fewest dependencies and note it as provisional.

---

## Appendix A — File map you will actually touch

| Path | Purpose |
|---|---|
| `apps/web/app/` | routes; `(editor)` group for the editor shell |
| `apps/web/components/` | `timeline/`, `preview/`, `inspector/`, `panels/` |
| `apps/web/lib/api/` | typed client from `packages/contracts`; never `fetch` raw in components |
| `apps/web/lib/state/` | canonical project store, selection, playback clock |
| `apps/api/cmd/server/` | Existing HTTP entrypoint — wiring, no business logic |
| `apps/api/cmd/worker/` | Planned worker entrypoint — does not exist yet |
| `apps/api/internal/media/` | ffprobe/ffmpeg, validation, signed URLs |
| `apps/api/internal/project/` | canonical document, commands, validation, versions |
| `apps/api/internal/job/` | enqueue, claim, retry, cancel, progress |
| `apps/api/openapi/` | contract; regenerate `packages/contracts` after edits |
| `apps/api/migrations/` | forward-only SQL; tested rollback |
| `apps/llm/src/tasks/` | one module per AI task |
| `apps/llm/src/providers/` | vendor adapters |
| `docs/` | specs, ADRs, roadmap — required |

## Appendix B — Vocabulary

| Term | Meaning here |
|---|---|
| **Project** | The versioned document describing one edited video. Not a file. |
| **Source asset** | An immutable uploaded or imported original. |
| **Derived asset** | Anything produced from a source: proxy, thumbnail, render. |
| **Clip** | A reference into a source asset plus a time range and transforms. |
| **Track** | An ordered container of clips; visual tracks order by z-index. |
| **Curation** | Selecting candidate moments from a long source. |
| **Proposal** | AI output that a human or explicit rule still has to apply. |
| **Preview** | Browser-rendered approximation that must match export. |
| **Export** | Server-rendered output from the same canonical document. |
| **Parity** | Measured agreement between preview and export, with test evidence. |

---

*Last verified: API starter and related guidance 2026-11-12; other inventory 2026-11-06 unless noted. If the repository has moved on, re-run §2 before trusting this file.*
