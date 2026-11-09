# Acceptance Criteria — MVP

**Status:** Phase 3 draft
**Last verified:** 2026-11-09
**Scope:** the 14 capabilities listed as "In" in [`docs/implementation/roadmap.md`](../implementation/roadmap.md) §2.

## How to read this

- Each criterion is **observable**: a person or a test can check it without asking the author.
- `AC-xx` ids are stable. Reference them in PRs and test names.
- Tolerances are deliberate. A criterion without a number is a wish, not a criterion.
- Evidence tags: [V] verified today, [I] inferred, [U] unknown. New criteria start as [I] and become [V] when tested.
- A feature is done only when **all** of its criteria pass **and** the Definition of Done in `AGENTS.md` §12 holds.

---

## AC-01 Sign in

| # | Given | When | Then |
|---|---|---|---|
| AC-01.1 | A signed-out visitor | opens any project URL | redirected to sign-in; no project data in the response body |
| AC-01.2 | A valid account | completes OAuth or magic link | lands on the project list with a session |
| AC-01.3 | An expired session | calls any API route | `401` with code `UNAUTHENTICATED`; no stack trace |
| AC-01.4 | User A | requests user B's project id | `404` (not `403`), and nothing about B's project leaks |

## AC-02 Upload

| # | Given | When | Then |
|---|---|---|---|
| AC-02.1 | A 500 MB mp4 | uploads | completes; asset recorded with size matching bytes on disk |
| AC-02.2 | A `.txt` renamed to `.mp4` | uploads | rejected before storage with `UNSUPPORTED_MEDIA` |
| AC-02.3 | A 3 GB file | uploads | rejected with `FILE_TOO_LARGE` and the actual limit stated |
| AC-02.4 | A 61-minute video | uploads | rejected with `DURATION_LIMIT` |
| AC-02.5 | A valid upload | completes | the original object's SHA-256 is unchanged after any later edit or export |
| AC-02.6 | An interrupted upload | resumes | continues from the last part; no duplicate asset |
| AC-02.7 | Any upload | is stored | the object is private; reading requires a signed URL that expires in ≤ 15 min |

## AC-03 Probe and project creation

| # | Given | When | Then |
|---|---|---|---|
| AC-03.1 | An uploaded asset | project is created | persisted `durationMs`, `width`, `height`, `fps` match ffprobe within 1 ms / 1 px |
| AC-03.2 | A portrait source | project is created | it is not silently rotated; rotation metadata is applied and recorded |
| AC-03.3 | A variable-frame-rate source | project is created | normalised timing is stored alongside the original timing |
| AC-03.4 | A client-supplied duration | differs from ffprobe | the server value wins; the mismatch is logged |
| AC-03.5 | Any project | is created | `schemaVersion` and `version = 1` are present |

## AC-04 Transcript

| # | Given | When | Then |
|---|---|---|---|
| AC-04.1 | A 30-minute English video | transcription completes | words have non-decreasing `startMs < endMs`, and total span ≤ source duration |
| AC-04.2 | 5 fixture videos | compared to manual spot checks | ≥ 95% of sampled words within ±150 ms |
| AC-04.3 | A transcript | is displayed | clicking a word seeks the player to its `startMs` ±100 ms |
| AC-04.4 | Provider returns malformed output | task runs | job fails with `PROVIDER_INVALID_OUTPUT`; no partial transcript is persisted |
| AC-04.5 | Provider is unavailable | task runs | job retries with backoff; after the limit it fails visibly and is retryable |
| AC-04.6 | Test suite | runs | no network call to a model provider |

## AC-05 AI clip proposals

| # | Given | When | Then |
|---|---|---|---|
| AC-05.1 | A 30-minute video with a transcript | curation completes | 3–10 proposals, each with `startMs < endMs`, inside source bounds |
| AC-05.2 | Any proposal set | is displayed | every proposal has a title, a hook line, and a one-sentence reason |
| AC-05.3 | A proposal | is accepted | a project is created from it; the original project is unchanged |
| AC-05.4 | All proposals | are rejected | the UI shows an empty state and offers regenerate; no error |
| AC-05.5 | A curation run | finishes | no edit has been applied to any project |
| AC-05.6 | 5 real videos | reviewed by 2 people | ≥ 3 proposals per video are judged usable without re-cutting |

## AC-06 Transcript-based editing

| # | Given | When | Then |
|---|---|---|---|
| AC-06.1 | A project with a transcript | a 4.2 s sentence is deleted | exported duration decreases by 4.2 s ± 1 frame |
| AC-06.2 | The same project | undo is pressed | document returns to the prior `version`; export matches the pre-delete result |
| AC-06.3 | A deletion | is applied | remaining words are not re-timed unless the ripple rule says so, and the rule is documented |
| AC-06.4 | A project | is reloaded after refresh | identical document, identical `version` |
| AC-06.5 | Two edits | are applied concurrently from two tabs | one wins, the other receives `VERSION_CONFLICT` and refreshes; no silent overwrite |

## AC-07 Timeline trim and split

| # | Given | When | Then |
|---|---|---|---|
| AC-07.1 | A clip | is trimmed | `startMs`/`endMs` stay within source bounds and `endMs > startMs` |
| AC-07.2 | A clip | is split at `t` | two adjacent clips meet at `t` with no gap and no overlap |
| AC-07.3 | A clip | is shortened below one frame | rejected with `CLIP_TOO_SHORT` |
| AC-07.4 | Adjacent clips | are rendered | frames are not duplicated or dropped at the boundary (verified by frame count) |
| AC-07.5 | Any timeline | is edited | the preview reflects the change within 100 ms of the state update |

## AC-08 Captions

| # | Given | When | Then |
|---|---|---|---|
| AC-08.1 | A transcript | captions are enabled | burned-in text at 3 sampled timestamps matches the transcript word within 100 ms |
| AC-08.2 | A caption preset | is switched | the export changes; the preview changes to match |
| AC-08.3 | A word | is edited | both preview and export use the edited text |
| AC-08.4 | An empty transcript | export runs | export succeeds with no captions and no error |
| AC-08.5 | A long word | exceeds the safe area | it wraps or scales; it does not clip outside the frame |
| AC-08.6 | Captions | are disabled | output contains no burned-in text |

## AC-09 Vertical reframe

| # | Given | When | Then |
|---|---|---|---|
| AC-09.1 | A 16:9 source | exported as 9:16 | output is 1080×1920 with no letterboxing |
| AC-09.2 | A crop offset | is changed | preview and export differ by ≤ 2% of frame width in the crop rect |
| AC-09.3 | A portrait source | exported as 9:16 | no additional crop is applied beyond the declared one |
| AC-09.4 | Any crop | is applied | the source asset is untouched |

## AC-10 Preview

| # | Given | When | Then |
|---|---|---|---|
| AC-10.1 | A project | is opened | playback starts within 2 s on a 60 s clip |
| AC-10.2 | Playback | runs | audio and video stay within 100 ms of each other for 60 s |
| AC-10.3 | The playhead | is scrubbed | the frame updates within 150 ms |
| AC-10.4 | Preview is approximate | export differs | the difference is documented; preview never claims to be exact |

## AC-11 Export

| # | Given | When | Then |
|---|---|---|---|
| AC-11.1 | A 60 s project | exports | a 1080×1920 H.264/AAC MP4 is produced in under 60 s on 4 vCPU |
| AC-11.2 | The same document | exports twice | duration and frame count match exactly |
| AC-11.3 | Export completes | ffprobe runs | resolution, fps, and duration match the project settings |
| AC-11.4 | A render | is cancelled | partial output is deleted; no orphan objects remain |
| AC-11.5 | A render worker | is killed mid-job | the job returns to `pending` and completes on retry |
| AC-11.6 | Export completes | the user downloads | link is signed, expiring, and scoped to that object |

## AC-12 Jobs

| # | Given | When | Then |
|---|---|---|---|
| AC-12.1 | A job | is created | status is queryable and progresses monotonically |
| AC-12.2 | A job | fails | status is `failed` with a machine-readable `code` and a `traceId` |
| AC-12.3 | A job | is retried | it is safe to retry; no duplicate output or double charge |
| AC-12.4 | A job | is cancelled | the worker stops within 5 s and cleans its temp files |
| AC-12.5 | A user | has one export running | a second export is queued, not run concurrently |

## AC-13 Persistence

| # | Given | When | Then |
|---|---|---|---|
| AC-13.1 | An edit | is made | it is persisted within 2 s without a manual save |
| AC-13.2 | The browser | is closed and reopened | the project restores at the last persisted version |
| AC-13.3 | A save | fails | the UI states it is unsaved and retries; it does not silently discard |
| AC-13.4 | Any document | is loaded | unknown `schemaVersion` is rejected with `UNSUPPORTED_SCHEMA` |

## AC-14 Share and download

| # | Given | When | Then |
|---|---|---|---|
| AC-14.1 | A share link | is created | it works without sign-in and is read-only |
| AC-14.2 | A share link | expires | it returns `410`, not the media |
| AC-14.3 | A share link | is revoked | it stops working immediately |
| AC-14.4 | A shared page | is loaded | it exposes no transcript, no other projects, and no API credentials |

---

## Global non-functional criteria

| # | Criterion |
|---|---|
| NFR-1 | p95 API latency < 300 ms for non-job routes |
| NFR-2 | Export success rate ≥ 95% over a 50-job run |
| NFR-3 | No secret appears in the web bundle, logs, or error responses |
| NFR-4 | No source asset is mutated by any operation, verified by hash in CI |
| NFR-5 | Temp media is deleted on success, failure, and cancellation |
| NFR-6 | Every user-facing error has a stable `code` and a `traceId` |
| NFR-7 | Golden path E2E passes in CI in under 15 minutes |
| NFR-8 | Editing works at 1440×900 and 1920×1080 without horizontal scroll |

---

## Out of scope for MVP criteria

Deliberately absent (see roadmap §2): multi-track editing, transitions, B-roll, music, motion graphics, brand kits, scheduling, analytics, dubbing, 4K, mobile, public API, non-English transcription, face tracking, filler-word removal.

Adding a criterion for any of these means the scope changed — record that decision in the roadmap first.
