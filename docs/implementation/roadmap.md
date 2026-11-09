# Roadmap

**Status:** Phase 3 — MVP defined, not yet approved
**Last verified:** 2026-11-09
**Owner:** product + engineering
**Source of truth for scope:** this document. If a feature is not in §2, it is not in MVP.

---

## 1. MVP thesis

**User:** a creator, marketer, or podcaster who already has long-form video (20–60 min) and needs short vertical clips for social, but does not want to open Premiere or CapCut.

**Problem:** finding the good moment takes longer than editing it, and vertical reformatting plus captions is tedious and repetitive.

**Promise:** *upload one long video, get postable vertical clips with captions, in under 10 minutes, without editing skills.*

**What makes it ours (not just a trimmer):**

1. Word-level transcript that is editable — cut by deleting text.
2. AI-proposed moments the user reviews before anything is applied.
3. Captions and vertical framing baked into the same render path as the cut.

**What we are explicitly not proving in MVP:** that we can beat the reference product's curation quality. We are proving the loop works, is deterministic, and is worth using. Curation quality is measured in M4 and improved after.

---

## 2. Scope

### In

| # | Capability | Notes |
|---|---|---|
| 1 | Sign in | Google OAuth or email magic link. Single workspace per user; `orgId` modelled but always personal. |
| 2 | Upload | mp4 / mov / webm, resumable, presigned. Limits in §8. |
| 3 | Probe + normalise | ffprobe metadata; proxy + normalised audio for ASR. |
| 4 | Transcript | English, word-level timestamps, speaker optional. |
| 5 | AI clip proposals | 3–10 candidates: start, end, title, hook line, one-sentence reason, score. |
| 6 | Review + accept | Accept a proposal into a project; reject and regenerate. |
| 7 | Transcript editing | Delete words/sentences → ripple cut. Split, trim, undo/redo. |
| 8 | Captions | Word-level, 3–4 presets, burn-in, highlight current word. |
| 9 | Vertical reframe | 9:16, center crop + adjustable offset; per-clip. |
| 10 | Preview | `<video>` + canvas overlay, playback, timeline selection. |
| 11 | Export | 1080×1920 MP4, H.264 + AAC, deterministic from the project document. |
| 12 | Jobs | Progress, retry, cancel, visible failure. |
| 13 | Persistence | Autosave, reload, versioned project document. |
| 14 | Share | Read-only link + download, expiry-bound. |

### Out (do not build in MVP)

Multi-track NLE · transitions · B-roll · SFX · music library · motion graphics · brand kits · team seats · social scheduling/publishing · analytics · dubbing · voice cloning · 4K · C++/WASM engine · mobile apps · public API · non-English · smart face tracking · eye contact · filler-word removal · OpusSearch-style library search.

If a request needs one of these, it is a post-MVP decision, not an implementation detail.

---

## 3. Golden path (the demo we are building toward)

1. Sign in.
2. Upload a 30-minute podcast.
3. Watch progress; transcript ready in ~3–6 min.
4. See 6 proposed clips with titles and reasons.
5. Open one → vertical preview with captions.
6. Delete a rambling sentence from the transcript → clip shortens.
7. Switch caption preset.
8. Nudge the crop.
9. Export → download a 1080×1920 MP4 in 60–90 s.

**Target: under 10 minutes end to end, unaided.**

Every milestone below is judged against this script.

---

## 4. Milestones

Estimates assume 2–3 engineers. Treat as [I] until each milestone is measured.

### M0 — Walking skeleton (week 1)

**Build:** Go module, `cmd/api` + `cmd/worker`, config from env, structured logging, `/healthz`, Postgres baseline migration, MinIO via compose, Turbo shim for `api`, CI running `lint` + `check-types` + `go test` + `build`.

**Demo:** `pnpm dev` starts web and api; `curl localhost:8080/healthz` → 200.

**Exit:** CI green on a PR; no service secrets in the repo; `docker compose up` gives Postgres + MinIO.

### M1 — Render spine (weeks 2–3)

**Build:** presigned upload, ffprobe ingestion, `MediaAsset` + `VideoProject` persistence, single-range clip, export worker, FFmpeg trim/scale/mux, download link.

**Demo:** upload a 30-minute MP4, export a 15-second 1080×1920 clip.

**Exit:**
- ffprobe confirms output duration within ±1 frame, 1080×1920, H.264/AAC.
- Source file SHA-256 unchanged before/after.
- Same project document renders byte-comparable duration twice (determinism check).

### M2 — Transcription (weeks 3–4)

**Build:** `apps/llm` scaffold, provider adapter, `transcribe` task, word-level transcript persisted, transcript viewer in web.

**Demo:** upload → transcript with timestamps, click a word to seek.

**Exit:**
- 30-minute video transcribed in under 8 minutes p50.
- On 5 fixture videos, ≥95% of words have timings within ±150 ms of a manual spot check.
- Provider is mocked in tests; no test hits the network.

### M3 — Transcript editing and cuts (weeks 4–5)

**Build:** canonical operation log, delete-by-word, ripple timeline, split, trim, undo/redo, autosave, reload.

**Demo:** delete a sentence → export is shorter by the expected amount.

**Exit:**
- Deleting a 4.2 s sentence changes exported duration by 4.2 s ± 1 frame.
- Undo restores both document and export result.
- Reload after refresh shows identical document version.

### M4 — AI clip proposals (weeks 5–6)

**Build:** `curate` task, proposal schema, review UI, accept → project, regenerate.

**Demo:** 6 proposals with titles; accept one into the editor.

**Exit:**
- 3–10 proposals returned for each of 5 real videos.
- At least 3 per video judged "usable without re-cutting" by 2 independent reviewers.
- Rejecting all proposals is a valid, non-error state.
- No proposal is applied without explicit user action.

### M5 — Captions and reframe (weeks 6–8)

**Build:** caption presets, ASS burn-in, word highlight, 9:16 crop with offset, preview overlay that matches export layout.

**Demo:** styled captions on a vertical clip.

**Exit:**
- Caption text at 3 sampled timestamps matches the transcript word within 100 ms.
- Preview framing and export framing agree within 2% on the crop rect.
- Export succeeds with captions disabled, enabled, and with an empty transcript.

### M6 — Preview and hardening (weeks 8–9)

**Build:** playback, timeline selection, job progress/cancel/retry, share link with expiry, error and empty states, Playwright E2E on the golden path.

**Exit:**
- Golden path E2E passes in CI.
- Killing a worker mid-export leaves a retryable job, not a stuck one.
- Every failure surfaces a user-readable message with a `traceId`.

### M7 — Private beta (week 10)

**Build:** 5–10 real users, instrumentation for §9, support runbook.

**Exit:**
- ≥80% complete a first export unaided.
- ≥1 clip per active user is published or downloaded and kept.
- Export success rate ≥95%.

---

## 5. Work breakdown

| Area | M0 | M1 | M2 | M3 | M4 | M5 | M6 |
|---|---|---|---|---|---|---|---|
| **Go API** | skeleton, config, health | upload, probe, project CRUD, render worker | transcript storage | op log, cuts, undo | proposal endpoints | caption/reframe fields | jobs hardening, share |
| **TS web** | shell, api client | upload UI, project list | transcript viewer | transcript editor | proposal review | caption/crop panel | preview, E2E |
| **TS llm** | — | — | adapter, transcribe | — | curate | — | retry/fallback |
| **Infra** | compose, CI | storage buckets | queue config | — | — | — | metrics, alerts |
| **Docs** | current-state | schema, render pipeline | job design | — | feature spec | parity check | runbook |

---

## 6. Acceptance criteria

Every MVP feature has testable criteria in [`docs/product/acceptance-criteria.md`](../product/acceptance-criteria.md). A feature is not done until its criteria are verified with evidence, per the Definition of Done in `AGENTS.md` §12.

---

## 7. Decisions to lock before M1

Answer these in `docs/architecture/decisions.md` as ADRs. Recommendations below favour the fewest moving parts.

| Decision | Recommendation | Why now |
|---|---|---|
| Postgres access | `pgx` + `sqlc` | Typed SQL, no ORM, reviewable queries |
| HTTP router | stdlib `ServeMux` (Go 1.22+ patterns) | Zero dependency, sufficient for MVP |
| Job queue | Postgres `FOR UPDATE SKIP LOCKED` | Transactional enqueue, one less service; revisit if throughput demands |
| Renderer | FFmpeg filter graph in Go workers | One renderer → parity is cheap |
| Preview | `<video>` + canvas overlay | No WASM; see preview ladder in roadmap discussion |
| Storage | S3-compatible (MinIO local) | Presigned URLs, standard tooling |
| Auth | Google OAuth + email magic link | Low support burden |
| Transcription | Hosted provider behind adapter | No GPU ops; swap after measuring |
| Public API | Internal only; OpenAPI maintained | No support surface in MVP |
| Limits | 60 min / 2 GB / 1 concurrent export | Protects workers; see §8 |

Deferred until after MVP: provider for ASR, embedding store, analytics vendor, billing.

---

## 8. Non-functional requirements

| Concern | MVP target |
|---|---|
| Upload limit | 2 GB, 60 minutes, mp4/mov/webm |
| Rendering | 60 s clip in under 60 s wall clock on 4 vCPU |
| API latency | p95 < 300 ms excluding jobs |
| Job success | ≥ 95%, all failures retryable |
| Concurrency | 1 export per user, 4 per instance |
| Signed URLs | ≤ 15 minutes, single object |
| Source integrity | Original never mutated; verified by hash in tests |
| Temp storage | Deleting on success, failure, and cancel |
| Browser support | Latest Chrome, Edge, Safari |

Limits are **our** decision. Reference-product numbers observed during research are not inherited.

---

## 9. Metrics

Instrument from M0; read from M6.

- Time to first export (median, p90).
- Funnel: signup → upload → transcript ready → proposal viewed → export started → export done.
- Export success rate and failure reasons.
- Proposal acceptance rate (accepted / shown).
- Edits per exported clip.
- Processing cost per minute of source video.

**Cost assumption [I]:** for a 30-minute source, transcription, curation, and one 60 s 1080p render should land well under US$1 at current hosted pricing. Measure in M2 and replace this estimate with a number.

---

## 10. Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Transcript latency on 60-min input | Blocks the 10-minute promise | Benchmark in M2; chunk and parallelise if needed |
| FFmpeg timing drift (VFR, rotation, audio priming) | Captions off by frames | Golden fixtures from M1; single rounding function |
| Proposal quality below "usable" | Product feels like a random cutter | Human review from day one; never auto-apply; measure in M4 |
| Scope creep toward a full NLE | MVP slips indefinitely | §2 out-of-scope list is binding |
| Long-video memory blowup | Worker OOM | Streaming pipeline, per-job memory cap, proxy resolution |
| Provider outage or price change | Feature down, margin hit | Adapter + retry; fallback provider evaluated |
| Authz bug exposing another user's media | Security incident | Server-derived identity from M0; signed, scoped URLs |

---

## 11. Post-MVP (ordered, not committed)

1. Filler-word and silence removal.
2. Smart reframe with face tracking.
3. Brand kits and caption presets per brand.
4. B-roll (stock first, generated later).
5. Music and audio ducking.
6. Batch export and presets per platform.
7. Social scheduling.
8. Multi-language transcription.
9. Team workspaces.
10. Public API + MCP surface.

Ordering is by value-per-effort against beta feedback, not by parity with the reference product.

---

## 12. Cadence

- One milestone per PR-sized theme; each ends with a demo and an exit-criteria check.
- Docs updated in the same change as the behaviour.
- No milestone starts while the previous exit criteria are unmet; if a criterion is waived, record it in `risks-and-open-questions.md` with the reason.
- Weekly: re-measure the golden path and the cost estimate.

---

## 13. Definition of MVP done

- [ ] Golden path completes in under 10 minutes, unaided, by someone who did not build it.
- [ ] Every In-scope feature meets its acceptance criteria with recorded evidence.
- [ ] Export is deterministic from the project document (repeat renders match).
- [ ] Original uploads are provably unmodified.
- [ ] Job failures are visible, retryable, and traced.
- [ ] No source media is reachable without authorization.
- [ ] E2E test covers the golden path and runs in CI.
- [ ] `docs/` reflects the shipped behaviour, and the parity matrix records what we did **not** match.
