# OpusClip / Opus (`clip.opus.pro`) — Reverse-Engineering Report

**Date:** 2026-11-05
**Target:** `https://clip.opus.pro` (app), `https://www.opus.pro` (marketing), `https://agent.opus.pro`, `https://labs.opus.pro`, `https://help.opus.pro`, `https://api.opus.pro`, `https://mcp.opus.pro`
**Constraint honoured:** no building, coding, or implementing. This report is analysis only.

---

## 0. Confidence legend and method

| Tag | Meaning |
|---|---|
| **[V]** | **Verified.** Directly observed in a public HTTP response, shipped JavaScript/WASM, DOM, OpenAPI document, or first-party documentation. |
| **[I]** | **Inferred.** Supported by multiple observations but not directly confirmed (e.g. which framework powers a gateway). |
| **[C]** | **Vendor claim.** Marketing copy / pricing page statement, not independently measured. |

**Method (public surface only):**

1. Enumerated the Next.js route manifest (`/_next/static/<buildId>/_buildManifest.js`), current build `SGDt3TCUKNRaAUab6ZqjM` → 119 route entries.
2. Read the marketing sitemap (5,525 URLs), the app sitemap (23 public URLs: captions + 22 tools), and the help-centre sitemap (259 URLs).
3. Pulled the complete help-centre corpus via `https://help.opus.pro/llms-full.txt` (571,189 bytes, 259 documents) plus `llms.txt`.
4. Read the public OpenAPI 3.0 document at `https://help.opus.pro/api-reference/openapi.json` (`Clip API 1.0`, 21 paths, 51 schemas).
5. Downloaded and searched 62 route/shared JS chunks for the current build (editor engine, model IDs, layout flags, render flags).
6. Captured network/console traffic on public pages (earlier REA capture, 266 requests / 55 hosts) and re-verified key hosts with `curl`.
7. Captured and read public pages in the in-app browser (screenshots in `assets/`).
8. **No account was created and no credentials were used.** No paid generation was triggered. Authenticated screens (dashboard, editor canvas, scheduler grid) are therefore analysed from shipped code + documentation, not from a live logged-in session.

**Known limitations:** Anything behind authentication (org settings, editor canvas internals, billing portal) is inferred from artifacts. Pricing varies by region/A-B test (observed twice: `$14.5/mo` annual in raw HTML vs `$9/mo` in a live render with a "Save up to 67%" banner), so prices below are the values observed, not guaranteed quotes.

---

## 1. Executive summary

OpusClip is a **video repurposing platform** whose core loop is: *long video in → transcript/scene understanding → moment selection ("curation") → short vertical clip rendering → caption/brand treatment → publish*. Around that loop it has grown into a multi-product suite:

- **OpusClip 4.0** — clipping + "AI Producer/Auto Edit" recipes + a full web editor + social scheduler + analytics + team workspaces.
- **Agent Opus** — a generative-video product (script/audio/song/reference/assets → AI-generated video) with four quality tiers and a Story Mode.
- **Opus Labs** — experimental standalone tools (AI Expand, Multi-Angle, Live Clipping, Motion Studio, Audio-to-Image, Product Ad Studio).
- **OpusSearch / OmniSearch / Trends** — semantic search over a creator's video library.
- **REST API + MCP server** — programmatic clipping and an agent-facing tool surface.

**Strongest implementation findings (all [V] unless noted):**

- The app is a **client-rendered Next.js Pages Router static export** (`nextExport`-style `_buildManifest.js`, `__NEXT_DATA__`, `/_next/static/...`, per-route Turbopack chunk stubs).
- The editor is a **C++ → WebAssembly engine** (`AVEditorEngine-20261105-eb18ce86.wasm.gz` + `.mjs` glue, ~6.4 MB engine) running in a worker, using **WebCodecs `VideoEncoder`/`VideoDecoder`**, **Rive** for motion, and **ONNX Runtime Web 1.14.0** for client-side **speaker matting** (`opusSpeakerMatting`, model `07c308cf…onnx`).
- The backend is an **API gateway** (`api.opus.pro` → `{"message":"Opus Gateway"}`) exposing a **NestJS-style** error envelope (`NotFoundException`, `traceId`) with a documented **`Clip API 1.0`**.
- Media lives in **Google Cloud Storage** fronted by CDNs (`ext.cdn.opus.pro`, `public.cdn.opus.pro`, `prod-ao-ext.cdn.opus.pro`, `ext.gcs.opus.pro`).
- Workflow orchestration shows **Temporal** console links (`temporal.eng.opus.pro`, namespace `opus`, workflow IDs `cat:main:<projectId>`) — observed in bundle strings [V], role [I].
- AI stack is **multi-vendor**: Whisper-class ASR variants (earlier bundle observation), ElevenLabs ASR/voice option, OpenAI image models, ByteDance **Seedance**, MiniMax **Hailuo**, **Vidu**, **Creatify H3**, **Dreamina/Seedance 2.5**, **Seedream**, and **Pexels** stock.
- Social publishing spans **9 documented platforms** and per-platform caption limits in shipped code.

---

## 2. Surface map

| Surface | Host | Nature | Verified |
|---|---|---|---|
| Web app | `clip.opus.pro` | Next.js SPA, auth-gated after `/dashboard` | [V] |
| Marketing | `www.opus.pro` | Webflow (`data-wf-site="6388604483b03a9ecb34d695"`), 5,525 sitemap URLs | [V] |
| Agent Opus | `agent.opus.pro` | Generative video composer, OTP login | [V] |
| Opus Labs | `labs.opus.pro` | Beta tool catalogue (separate apps on sub-domains) | [V] |
| Docs / API ref | `help.opus.pro` | Mintlify, 259 docs URLs, `llms-full.txt` | [V] |
| Public API | `api.opus.pro` | "Opus Gateway", OpenAPI `Clip API 1.0` | [V] |
| MCP | `mcp.opus.pro/mcp` | MCP endpoint (GET → 405, POST expected) | [V] |
| Admin / auth / status / FAQ | `admin.opus.pro`, `auth.opus.pro`, `status.opus.pro`, `faq.opus.pro` | Supporting services | [V] DNS/response |
| Staging | `stg-api`, `stg-clip`, `stg-labs` | Pre-prod | [V] |

---

## 3. Feature inventory

> Every row carries a concrete evidence artifact. The same inventory is expanded analytically in §4 and mapped to architecture in §6.

### 3.1 Ingest & source handling

| Feature | What it does | Evidence | Conf. |
|---|---|---|---|
| Local upload | Up to **30 GB** per video (Pro) | `video-sources-supported` doc; upload UI `drop a file (mp4, mov, mkv, webm)` on `/captions` | [V] |
| Link import (19 sources) | Apple Podcast, YouTube, Google Drive, Vimeo, Zoom, Zoom Clips, Rumble, Twitch, Facebook, LinkedIn, X, Dropbox, Riverside, Loom, Frame.io, StreamYard, Kick, Medal.tv, public MP4 | help doc `video-sources-supported`; OpenAPI `videoUrl` description | [V] |
| Source platform enum | `YOUTUBE, UPLOADED, YTDLP_LINK, GDRIVE, ZOOM, STREAM_YARD` | OpenAPI `ClipProjectRepresentation.sourcePlatform` | [V] |
| Auto Import | Auto-processes latest public YouTube uploads for Starter/Pro | `auto-import` doc; `/api/auto-import` string in bundles | [V] |
| Google Drive OAuth | Connect/disconnect Drive, import from Drive | docs `9058950…`, `9069523…`; `GDRIVE` enum | [V] |
| Signed-URL / multipart upload | Direct-to-GCS upload via signed URL; hidden iframe `opus-upload-frame`; `gcsUploadId` | bundle strings; OpenAPI upload example uses `https://storage.googleapis.com/ext.gcs.opus.pro/upload/...` | [V] |
| Upload limits | 10 h / 30 GB max (API); 30 GB local (Pro) | `api-reference/limitation`; `video-length` doc | [V] |
| Live stream ingest | YouTube/Twitch/Kick/TikTok live, HLS/DASH/RTMP/RTSP/SRT | `opus-labs/live-clipping` doc | [V] |

### 3.2 Clipping / curation

| Feature | What it does | Evidence | Conf. |
|---|---|---|---|
| **ClipBasic** | Default transcript-first clip selection | OpenAPI `model` enum `ClipAnything\|ClipBasic`; docs `clipanything-qa` | [V] |
| **ClipAnything** | Prompt-driven multimodal moment selection using visual + audio + sentiment cues | marketing `/clipanything`; docs `clip-anything`, `clip-anything-prompt-manual` | [V] |
| Genre conditioning | 18 genres: Auto, Q&A, Commentary, Marketing, Webinar, Motivational speech, Podcast, Academic, Listicle, Product reviews, How-to, Comedy, Sports commentary, Church, News, Vlog, Gaming, Others | OpenAPI `Genre` enum | [V] |
| Clip length / timeframe / keywords | CoPilot controls: Auto 0–3 m, 0–1, 1–3, 3–5, 5–10, 10–15 m; date range; topic keywords | docs `select-clip-length`, `select-timeframe`, `select-keywords`; OpenAPI `CurationPreferenceDto` | [V] |
| Virality Score | 0–100 ranking score per clip | docs `virality-score`; demo result page shows 99–83 | [V] |
| Narrative templates | Named storytelling structures that reshape clip selection | docs `narrative-template`; prompt library in bundles | [V] |
| Reprompt / reprocess | Re-run curation with new prompt/params | `can-i-reprocess-a-project`, `can-i-reprompt…` docs; `/project-submit` route | [V] |
| Project visibility / share | `DEFAULT`, `PUBLIC`, `PUBLIC_VIEW`; public demo projects (`/clip/demo-podcast`) | OpenAPI `UpdateProjectVisibilityCommand`; demo URL returns 200 | [V] |
| Stage model | `PENDING → QUEUED → IMPORT → CURATE → REFINE → RENDER → UPLOAD → COMPLETE` (+`STALLED`) | OpenAPI `stage` enum | [V] |
| Retry/dedup artifacts | `curationResultCache`, `transcript.json`, `screenplay.json`, `scene_detection_timestamp.json`, `context.json`, `resize/video.resize.mp4`, `resize/audio.resize.m4a` | bundle artifact paths | [V] |
| 4K export | `renderAsVideoFile4K` / `video4K` only when source is true 3840×2160 | docs `download-and-post-in-4k`; bundle render flags | [V] |

### 3.3 Recipes / Auto Edit ("AI Producer")

| Feature | What it does | Evidence | Conf. |
|---|---|---|---|
| Recipes | Reusable pipeline presets (brand kit + edits) applied at submit | docs `how-to-use-recipes`, `create-and-save-a-custom-recipe`; `/recipes`, `/recipes/[slug]` | [V] |
| Auto Edit | One-click end-to-end polished clip | marketing "AI Producer"; docs `add-motion-graphics` references Auto Edit | [V] |
| Auto hook / Auto headline | One-line overlay on top 10 clips | `auto-headline` doc | [V] |
| Auto captions | Animated captions, emoji, keyword highlight | `captions-and-emojis`, pricing matrix | [V] |
| Auto reframe / layouts | Auto subject tracking, fill/fit/split/three/four/game/speaker-first | `subject-tracking`, `apply-the-layouts`, `layout-and-reframing` | [V] |
| Auto B-roll | AI image / AI video / stock | `ai-broll` doc; model IDs in bundles | [V] |
| Auto SFX | Adds sound effects and places them on the timeline | `auto-sfx` doc | [V] |
| Auto motion graphics | Detects names/numbers/dates and animates them | `add-motion-graphics` doc | [V] |
| Auto color correction | White balance/brightness/contrast | `auto-color-correction` doc | [V] |
| Filler/silence removal | Speech cleanup in the pipeline | `speech-cleanup`, `filler-word-recognition` docs | [V] |
| Music | Library + volume/ducking | `add-music-to-your-clips` | [V] |

### 3.4 Editor

| Feature | What it does | Evidence | Conf. |
|---|---|---|---|
| Web editor | Multi-track timeline, preview, inspector | `/editor/[fullClipId]`, `/editor-ux/[fullClipId]`, `/clip-editor/[curatedClipId]`; WASM engine | [V] |
| Timeline editing | Trim, split, extend, reorder, duplicate, drag sections | docs `trim-extend`, `split-my-clip`, `can-i-drag-rearrange…` | [V] |
| Snap editing | Magnetic snapping to segment/layer edges and playhead; hotkey `S` | `snap-editing` doc | [V] |
| Keyboard precision | Shortcuts for split/zoom/playhead | `keyboard-precise-editing` doc | [V] |
| Layouts (9 flags) | `Auto, Fill, Fit, Split, Three, Four, Game, Screen, SpeakerFirst` | OpenAPI `RenderPreferenceDto`; bundle flags `enable*Layout` | [V] |
| Manual reframe | Crop/pan per segment, subject lock | `how-to-manually-reframe-content`, `can-i-customize-crop-ratio` | [V] |
| Eye contact | Gaze correction for talking heads | `eye-contact-correction` doc | [V] |
| Auto zoom | Punch-ins on emphasis | project settings + `auto-zoom` strings | [V] |
| Transitions | Cross-fades/effects between segments | `add-transition-effects` | [V] |
| Text overlays | Headlines, titles, lower thirds | `add-text-overlays`, `auto-headline` | [V] |
| Intro/outro | Brand intro/outro cards | `intro-outro-cards` | [V] |
| Custom fonts | Upload brand fonts | `custom-fonts`; pricing "Custom fonts" (Pro) | [V] |
| Censor profanity | Beep/mute + caption masking | `how-to-censor-curse-words`; OpenAPI `CensorJobRequest` | [V] |
| Watermark | Free/trial watermark; removed on paid; rerender | `watermark` doc; `watermark-v2` string | [V] |
| Export | MP4, 4K, SRT, XML for Premiere/DaVinci | `import-to-adobe-premiere`, `import-to-davinci-resolve`, `download-and-post-in-4k` | [V] |
| Speaker matting | Client-side portrait cutout (`opusSpeakerMatting`) | WASM strings + ONNX model path | [V] |
| Motion packages | Downloadable motion-graphics packages from CDN | `public.cdn.opus.pro/clip-web/motion-packages/1.1.0/` | [V] |

### 3.5 Captions & transcription

| Feature | What it does | Evidence | Conf. |
|---|---|---|---|
| ASR | Word-level timestamps, speaker labels | OpenAPI `TranscriptExportWord/Paragraph`; `transcript.json` | [V] |
| 25+ languages | EN, DE, ES, FR, PT, IT, NL, RU, PL, ID, UK, SV, TR, NO, HR, RO, SK, EL, DA, FI, HU, CS, JA, KO, VI | `video-languages-supported` | [V] |
| Caption styles | Animated templates, karaoke/highlight styles | `brand-template-caption-*` bundle strings | [V] |
| Emoji & keyword highlight | Auto emoji insertion, keyword colouring | `captions-and-emojis`, `ai-emojis-keywords` | [V] |
| Brand vocabulary / proper nouns | Correct names once, apply everywhere | `brand-vocabulary` doc; `/proper-nouns`, `/word-libraries` endpoints | [V] |
| Word-level editing | Change word + timing | `captions-and-emojis`, `change-captions` | [V] |
| Upload SRT | Use own subtitles | `upload-own-srt` | [V] |
| Download transcript/SRT | Export subtitles | `download-transcripts-subtitles` | [V] |
| Filler-word detection | English-only filler detection | `filler-word-recognition` | [V] |

### 3.6 Audio

| Feature | What it does | Evidence | Conf. |
|---|---|---|---|
| Speech enhancement | Denoise, isolate voice, boost clarity | `speech-enhancement` doc | [V] |
| Speech cleanup | Remove filler words, pauses, bad takes | `speech-cleanup`; `bad-takes/{id}/detect` endpoint | [V] |
| Auto SFX | 7 credits/min | `auto-sfx` | [V] |
| Music library | Add/duck/trim music | `add-music-to-your-clips`; `/musics` endpoint | [V] |
| AI voiceover | TTS + voice cloning inside editor | `ai-voiceover`; voice-clone 30 s sample | [V] |
| Dubbing | 25 languages, cloned voice | `video-dubbing`; `/dub-clip-tasks` endpoint | [V] |
| Voice cloning | 30-second sample, ~39 languages listed for Agent | `ao-faq` voice-cloning language list | [V] |

### 3.7 Visual AI

| Feature | What it does | Evidence | Conf. |
|---|---|---|---|
| AI B-roll (image) | `gpt-image-2.5-flare` | bundle model IDs; `ai-broll` doc | [V] |
| AI B-roll (video) | `seedance-1-5-pro-251215`, `creatify-h3-t2v`, `dreamina-seedance-2-5-260628` (480p/720p) | bundle model IDs | [V] |
| Stock B-roll | Pexels (free) | `pexels` bundle string; docs | [V] |
| Upscale | 5-minute max; base e.g. 23 credits; 4K uplift | `upscale-a-video` | [V] |
| AI Expand (Labs) | Outpainting to 9:16/16:9/1:1/4:3/3:4/21:9; Hailuo 3 / Seedance 2.5 | `opus-labs/ai-expand` | [V] |
| Multi-Angle (Labs) | Generates extra camera angles; 4 GB / 1 h; 480p/720p | `opus-labs/multi-angle` | [V] |
| Eye contact | Gaze redirection | `eye-contact-correction` | [V] |
| Motion graphics | Auto/manual animated text elements | `add-motion-graphics`; motion package CDN | [V] |
| Thumbnail generator | Link → YouTube thumbnail, 7 credits | tool config `generateThumbnailWorkflow`, `creditCost:7` | [V] |

### 3.8 Discovery, library & collaboration

| Feature | What it does | Evidence | Conf. |
|---|---|---|---|
| OpusSearch | Semantic index over library; topic/speaker/vibe/phrase | `opussearch` doc; `/search*` routes; `/searchable-clips` endpoint | [V] |
| OmniSearch | ⌘K global search with tabs | `opussearch` §OmniSearch; UI on demo page | [V] |
| Trends | Trending clip/format discovery | `opussearch` §Trends; `/trending-clip-groups` | [V] |
| Collections | Group clips by campaign/client | `collections` doc; `/api/collections*` | [V] |
| Bulk download | Multi-select export | `bulk-download` | [V] |
| Bulk scheduler | Schedule many clips at once | `bulk-scheduler` | [V] |
| Share project | Public/private share links | `share-project`; OpenAPI visibility | [V] |
| Duplicate / rename / delete | Project ops | docs + `/projects` | [V] |
| Team workspace | Seats, orgs, roles | `/auth/orgs`, pricing "2 seats", `team-management` | [V] |
| Invitations | Join by code | `/join/[invitationCode]` | [V] |
| Brand kits | Logo, captions, headline, layout, intro/outro, music | `how-to-use-brand-kit`, `/brand-kits` | [V] |
| Brand templates | Layout/caption presets; 8 named styles | `/brand-templates`, `brand-template-caption-{mozi,beasty,youshaei,think-media,popline,simple,deep-diver,karaoke}-v2` | [V] |
| Campaigns | Group projects/campaigns | `/campaigns`, `/campaigns/[campaignId]` | [V] |
| Agent approvals | Human review of agent actions | `/agent-approvals`, `/api/agent-approvals/preview\|execute` | [V] |
| Agent chat | Per-project AI chat | `/arc-chat/[projectId]`, `/api/agent-sessions` | [V] |
| Analytics | Post performance | `/analytics`; marketing "Track performance" | [V] |
| Referral / affiliate | Credit + commission programs | `/referral`, `/affiliate/[slug]`, docs `referral`, `affiliate-program-faq` | [V] |

### 3.9 Publishing

| Feature | What it does | Evidence | Conf. |
|---|---|---|---|
| Connect accounts | YouTube, Instagram Reels, TikTok, Facebook Page, LinkedIn Business, X, Pinterest, Threads, Bluesky (bundle also shows Dailymotion, TikTok Business/User) | `add-social-accounts`; bundle platform strings | [V] |
| Calendar | Monthly scheduling view | `/auto-post/calendar` | [V] |
| Scheduler / auto-post | Queue posts per platform | `schedule-clips`, `scheduler-faq`; `/api/publish-schedules`, `/api/post-tasks` | [V] |
| Social copy generation | Per-platform captions/hashtags | `/api/social-copy-jobs`; `social-copy` strings | [V] |
| Per-platform limits | X 280 weighted, Threads 500, Bluesky 300, Pinterest 800, LinkedIn 3000, YouTube 5000, Facebook 63206 | bundle limit table | [V] |
| Posting rate limits | 500 posts / rolling 24 h / workspace (API) | `api-reference/limitation` | [V] |
| PlanSocial integration | Scheduling partner callback | `/post/plansocial`, `plansocial.co` | [I] |
| Mobile apps | iOS/Android with documented feature gaps | docs `ios-app-feature-limitations`, `android-app-*` | [V] |

### 3.10 Agent Opus (generative video)

| Feature | What it does | Evidence | Conf. |
|---|---|---|---|
| 4 quality modes | Standard 1 cr/s, Standard+ 3, Premium 7, Ultra 12 | `ao-faq`, `ao-faq-credits-faq` | [V] |
| Creation tools | Script to Video, Audio to Video, Song to Video, Reference to Video, Assets to Ads, Motion Graphics | `ao-faq` §4 | [V] |
| Lengths | Script ≤1,700 words / ≤10 min; Premium/Ultra ≤3 min | `ao-faq` | [V] |
| Style Maker | Up to 8 images / 1 video / description → palette + previews | `ao-faq` §5 | [V] |
| Voiceover | Built-in voices, clone (30 s), upload narration | `ao-faq` §5 | [V] |
| Assets | Up to 8 images: Object / Character / Logo; tags like `[Character image 1]` | `ao-faq` §5 | [V] |
| Story Mode | 100% generative, "Long Take" transitions, 450 words / 4 min | `ao-story-mode-faq` | [V] |
| Review stages | Summary → Script/Voiceover/Style/Assets → Storyboard (Std); Summary → Screenplay → References → Clips → Video (Premium) | `ao-faq` §7 | [V] |
| Auto/Manual | Auto-confirm when idle; Manual pauses each checkpoint | `ao-faq` §7 | [V] |
| Editing | Preview-based edits, 1 free retry, history, Reimagine | `ao-faq` §8 | [V] |
| Credits | Free 60 one-time (+25 onboarding); Pro 300/mo; Max 1,500/mo; top-ups 100–10,000 | `ao-faq-credits-faq` | [V] |

### 3.11 Opus Labs

| Product | What it does | Evidence | Conf. |
|---|---|---|---|
| AI Expand | Generative outpainting to new aspect ratio; 4–90 s; 500 MB | `opus-labs/ai-expand` | [V] |
| Audio to Image Video | Voiceover → illustrated scenes; 5 min; 25 styles; 200 scenes; 8 refs | `opus-labs/audio-to-image-video` | [V] |
| Live Clipping | Records live streams, cuts into 15/30/60 min chunks, auto-clips each | `opus-labs/live-clipping` | [V] |
| Motion Studio | Prompt → shot plan → motion-graphics render; 30 img/10 vid/10 audio refs | `opus-labs/motion-studio` | [V] |
| Multi-Angle | Adds AI camera angles to podcasts/interviews; 4 GB; 1 h | `opus-labs/multi-angle` | [V] |
| Product Ad Studio | Product photos/idea → ad images/posters | `opus-labs/product-ad-studio` | [V] |
| Product Launch Video Plugin | Launch-video workflow | `opus-labs/product-launch-video-plugin` | [V] |

### 3.12 Platform / developer

| Feature | What it does | Evidence | Conf. |
|---|---|---|---|
| REST API | 21 documented paths | OpenAPI | [V] |
| Webhooks | Project-conclusion callbacks | `ConclusionActionDto` type `EMAIL\|WEBHOOK`; `api-reference/webhook` | [V] |
| MCP server | 6 tools: Clip, Caption, Reframe, Edit, Repurpose, Schedule | marketing `/mcp`; `mcp.opus.pro/mcp` (405 on GET) | [V] |
| API keys | Org key in dashboard | `api-reference/overview` | [V] |
| Zapier | Connect Zap workflows | `zapier-integration` doc | [V] |
| Premiere / DaVinci | XML + SRT + source export | `import-to-adobe-premiere`, `import-to-davinci-resolve` | [V] |
| Free SEO tools | 22 tools in the sitemap / 23 tool route families in the manifest (names, titles, hashtags, transcript, summarizer, show notes, thumbnail, relighting, compress/convert, noise remover) | `sitemap.xml`; `/tools/[tool]`; tool configs | [V] |

---

## 4. Feature analysis

### 4.1 The core clipping engine: ClipBasic vs ClipAnything

**Observed behaviour.** Both models take a long source and emit ranked short clips with `startSec`/`endSec`, a title, description, hashtags, transcript text, genre, and a virality score. The difference is the selection signal:

- **ClipBasic** is transcript-first: it segments the ASR transcript, scores candidate windows on linguistic/structural signals, and cuts.
- **ClipAnything** is explicitly multimodal: marketing says "visual, audio, and sentiment cues" and the docs provide a full prompt manual ("clip the moment the speaker gets emotional", "find the demo where the product is unboxed"). Prompts can also exclude content ("do not use…").
- **Genre** is a conditioning input (18 values) rather than a separate model.
- **Narrative templates** re-weight what counts as a complete moment (e.g. hook → payoff), so two runs on the same source with different templates produce different cuts.

**Pipeline stages exposed.** The public stage enum is the cleanest evidence of the internal DAG:

```
PENDING → QUEUED → IMPORT → CURATE → REFINE → RENDER → UPLOAD → COMPLETE
                                                             ↘ STALLED
```

`IMPORT` = download/transcode; `CURATE` = moment selection; `REFINE` = boundary/hook/layout refinement; `RENDER` = per-clip composition; `UPLOAD` = push artefacts to GCS/CDN.

**Artifacts seen in shipped code.** `transcript.json`, `screenplay.json`, `scene_detection_timestamp.json`, `context.json`, `resize/video.resize.mp4`, `resize/audio.resize.m4a`, plus `curationResultCache (*.FP)` folders. This is consistent with: normalize media → ASR + scene detection → build a context object → curate → cache candidate results → render.

**Economics.** 1 credit = 1 minute of processing (API docs). Web plans meter credits monthly. API projects have a 10-credit (≈10-minute) minimum.

**Confidence.** Pipeline shape [V]; exact scoring model [I].

---

### 4.2 Recipes / Auto Edit and "AI Producer"

Recipes are stored presets combining a **Brand Kit** with a set of edit toggles. The docs describe creating a recipe once and reusing it. Auto Edit is the "turn everything on" recipe: auto hook, captions, reframe, B-roll, SFX, motion graphics, colour correction, filler/silence removal, music.

**Why it matters technically.** Recipes turn a long list of per-project preferences into a **single serialisable config** that the backend can hand to the render DAG. The OpenAPI `RenderPreferenceDto` is the public shadow of that config:

```
enableAutoLayout, enableScreenLayout, enableCrop, enableCaption,
enableUppercase, enableHighlight, enableEmoji, enableFillLayout,
enableSplitLayout, enableThreeLayout, enableFourLayout,
enableFitLayout, fitLayoutCropRatio, enableVisualHook,
enableWatermark, layoutAspectRatio, enableBroll, font, highlightColor
```

Bundle-only flags extend this with `enableGameLayout` and `enableSpeakerFirstLayout`, i.e. the public schema lags the shipped editor.

**Cost interactions.** Motion graphics in Auto Edit = 6 credits/min; Auto SFX = 7 credits/min; auto colour = 1 credit; AI B-roll is priced per B-roll in the editor or per minute at project creation.

**Confidence.** [V] for flags and pricing; [I] for how the config is persisted.

---

### 4.3 The editor: a C++/WASM non-linear editor in the browser

This is the most technically distinctive part of the product.

**Verified artifacts (current build):**

- `https://public.cdn.opus.pro/clip-web/wasm/AVEditorEngine-20261105-eb18ce86.mjs` (450,499 B loader/glue)
- `https://public.cdn.opus.pro/clip-web/wasm/AVEditorEngine-20261105-eb18ce86.wasm.gz` (6,435,950 B engine binary)
- The loader references `AVEditorEngine.wasm`, `AVEditorEngine.worker.*`, and `AVEditorEngine.aw.*`; the CDN stores the gzip variant (the plain `.wasm` key returns GCS `NoSuchKey`).
- `VideoEncoder` (1 reference) and `VideoDecoder` (4 references) → **WebCodecs** encode/decode in the browser.
- Rive runtime (`rive-wasm`, `@rive-app/*`, 73 `Rive` references) → vector motion graphics.
- ONNX Runtime Web **1.14.0** loaded from jsDelivr; SIMD and non-SIMD WASM paths.
- Speaker-matting model: `https://public.cdn.opus.pro/clip-web/motion-packages/1.1.0/07c308cf0fc7e6e8b2065a12ed7fc07e1de8febb7dc7839d7b7f15dd66584df9.onnx` (25,888,640 B) with a matching SHA-256.
- Motion packages: `.../motion-packages/1.1.0/` with hashed PNG assets.
- Render preference flags include `renderAsVideoFile` and `renderAsVideoFile4K`, plus `video4K`.

**Architecture reading.** The editor runs the compositor locally in WASM (timeline eval, decode, effects, layout transforms, caption layout, matting) and uses WebCodecs for hardware-accelerated decode/encode. Heavy AI (face/gaze/speaker segmentation) is partly client-side: the ONNX model plus `opusSpeakerMattingRequest` in the WASM import table, with a graceful fallback log `"speaker cutout unavailable; drawing unoccluded"`. Server rendering remains the source of truth for final exports (`renderAsVideoFile*` flags), which is why watermark changes trigger a rerender.

**Layouts.** Nine layout modes are represented in code: Auto, Fill, Fit, Split, Three, Four, Game, Screen, Speaker-First. Each is a versioned render feature (`layout-fit-v2`, `layout-split-v2`, etc.), which is how the team ships layout changes without breaking old projects.

**Editing model.** The docs confirm: segment trimming/splitting, snapping, keyboard shortcuts, per-segment layout, B-roll segments with extra framing, text overlays, transitions, intro/outro, custom fonts, censor, eye contact, auto zoom, 4K.

**Confidence.** [V] engine, WebCodecs, ONNX, model path, flags. [I] exact threading/worker topology.

---

### 4.4 Captions and transcription

The caption system is a **word-level document**, not a video burn-in:

- ASR returns words with `start`, `end`, `isFillerWord`, and paragraphs with `speaker` (OpenAPI `TranscriptExportWord` / `TranscriptExportParagraph`).
- The editor edits that document (word text, timing, emoji) and the renderer burns the styled result.
- Brand vocabulary / proper nouns are a correction layer applied across clips (`/word-libraries`, `/proper-nouns` endpoints).
- 25+ languages; filler detection is English-only.
- 8 named caption template families appear in code: `mozi`, `beasty`, `youshaei`, `think-media`, `popline`, `simple`, `deep-diver`, `karaoke`, each at `-v2`.

**Why word-level matters.** It enables karaoke highlighting, keyword colouring, censor masking, filler removal, transcript export, and precise caption timing — all from one data structure.

**Confidence.** [V].

---

### 4.5 Audio stack

Three distinct audio functions:

1. **Repair** — speech enhancement (denoise/isolate/boost) and speech cleanup (filler, pauses, bad takes). Bad-take detection is a dedicated endpoint (`/bad-takes/{id}/detect`).
2. **Produce** — Auto SFX (7 credits/min) places sound-effect clips on the timeline; music library; AI voiceover (TTS + voice clone).
3. **Translate** — dubbing into 25 languages using a clone of the speaker's voice (`/dub-clip-tasks`).

The Agent Opus voice-clone spec (30-second sample, 39 languages) and the dubbing spec (25 languages) suggest a shared voice-cloning provider layer, but the vendor is not confirmed [I].

**Confidence.** [V] features/limits; [I] vendor.

---

### 4.6 AI B-roll and motion graphics

B-roll is a **three-source** system:

| Source | Model / provider (verified strings) | Cost |
|---|---|---|
| AI image | `gpt-image-2.5-flare` | credits per B-roll / per minute |
| AI video | `seedance-1-5-pro-251215`, `creatify-h3-t2v`, `dreamina-seedance-2-5-260628` | credits per B-roll / per minute |
| Stock | `pexels` | free |

The docs add that AI images are planned **for the whole video at once** so they share one style, and AI video defaults to an "AI Style" that matches the source's colour/lighting/camera. That implies a planning step that sees the full transcript/context before generating individual assets — consistent with `context.json` and `screenplay.json`.

Motion graphics are both automatic (entity detection in the transcript: names, numbers, dates) and manual (editor elements). The render side is the motion-package CDN + Rive.

**Confidence.** [V] model IDs and pricing; [I] the planning implementation.

---

### 4.7 OpusSearch, OmniSearch, Trends

OpusSearch is a **semantic index of the user's own library**:

- `/search`, `/search/indexing`, `/search/onboarding`, `/search/results` routes.
- Backend surface: `/searchable-clips`, `/trending-clip-groups`.
- Query dimensions documented: topic, phrase, person/speaker, mood/vibe.
- OmniSearch (⌘K) is the global entry point with tabs; Trends is a separate discovery feed for what is performing.

This is architecturally separate from clipping: it needs embeddings over transcripts + metadata, plus an indexing job per project. The `/search/indexing` route and "Setup & Indexing" FAQ confirm an explicit index lifecycle.

**Confidence.** [V] features/routes; [I] embedding vendor.

---

### 4.8 Social scheduling

Observed data model (OpenAPI + bundle):

- `social-accounts?q=mine`
- `social-copy-jobs` (generate per-platform copy)
- `post-tasks` (one publish action)
- `publish-schedules` (recurring/queued schedule)
- Per-platform caption limits are enforced client-side (X 280 weighted, Threads 500, Bluesky 300, Pinterest 800, LinkedIn 3000, YouTube 5000, Facebook 63206).
- API posting cap: 500 posts per rolling 24 h per workspace; per-endpoint rate limits are separate.

Nine platforms are documented; the bundle additionally references Dailymotion and TikTok Business/User account types, suggesting broader connector coverage than the help page.

**Confidence.** [V].

---

### 4.9 Brand kits, templates, collections, campaigns

- **Brand Kit** = visual identity (logo, caption style, headline style, layout, intro/outro, music) applied per aspect ratio.
- **Brand Template** = a render preset (layout/caption/font/highlight) that can be set as default and passed via API (`brandTemplateId`).
- **Collections** = many-to-many clip grouping (`collection-contents` join endpoints).
- **Campaigns** = higher-level grouping of projects/clips.

The OpenAPI confirms `BrandTemplateRepresentation` with `preferences` (the `RenderPreferenceDto`) and `type`.

**Confidence.** [V].

---

### 4.10 Agent Opus and Story Mode

Agent Opus is a **separate generative pipeline**, not the clipping pipeline:

- The composer collects a mode (Standard/Standard+/Premium/Ultra), a creation tool, a style, assets, audio, aspect ratio, and quality.
- **Standard/Standard+** are narration-driven: script review, voiceover, optional storyboard, timeline editor, voice cloning, ≤10 min.
- **Premium/Ultra** are shot-driven: screenplay → references → clips → assembled video, character dialogue with lip sync, reference-video style matching, agentic editor, ≤3 min.
- **Story Mode** is a distinct "Long Take" mode: 100% generated frames, morph transitions, 450 words / 4 min, 12+ styles, custom style by image or text.
- Pricing is **per second of finished video** (1/3/7/12 credits), with custom models priced at their own displayed rate.
- Review is checkpointed; Auto mode auto-confirms after idle, Manual waits. Generation cannot be cancelled once started; credits are frozen then released on failure.

**Verified model names** from docs: **Vidu Q3**, **Seedream 5.0 Pro**, **Seedance 1.5**, **Seedance 2.5**, **Hailuo 3**. This is direct evidence of a multi-model routing layer with per-model pricing.

**Confidence.** [V] behaviour/pricing; [I] internal routing.

---

### 4.11 Opus Labs

Labs is a **catalogue of separate beta apps** sharing the OpusClip account and credit balance, each on its own sub-domain:

- `labs.opus.pro/ai-expand` (also `labs.opus.pro/ai-expand-intro`)
- `labs.opus.pro/storyslides` (Audio to Image Video)
- `live-clipping.labs.opus.pro`
- `labs.opus.pro/motion-studio`
- `multi-angle.labs.opus.pro`
- `product-ad-studio.labs.opus.pro`

This "one account, many apps" pattern is visible in the docs' repeated "sign in with your OpusClip account" and "same credit balance" statements. It implies a shared identity + credits service and independent frontends.

**Confidence.** [V].

---

### 4.12 Credits and billing

**Verified rules:**

- 1 credit = 1 minute of video processing (API).
- Agent Opus: Standard 1 cr/s, Standard+ 3, Premium 7, Ultra 12; Motion Graphics 3 or 10 cr/s.
- Auto SFX 7 credits/min; Auto motion graphics 6 credits/min; Auto colour 1 credit.
- Posting to X via API costs 3 credits per post.
- API monthly cap (Pro Beta/Max): 15 h / 900 credits; 403 `API_MONTHLY_CAP_REACHED` with `reset_at` + `upgrade_url`.
- Web plans: Free 60 credits/mo, Starter $15/mo 150 credits, Pro $29/mo (annual $14.5/mo, $174/yr) 300 credits, Business custom.
- Pro storage: 100 GB. Default project expiry: 30 days.
- Agent plans: Free 60 one-time (+25 onboarding), Pro 300/mo, Max 1,500/mo; monthly credits expire after 60 days; top-ups 12 months.

**Payment rails.** Stripe references, dLocal endpoints (`/dlocal-payments/*`), Apple Pay / Google Pay docs, Apple/Google IAP management docs. This indicates global card + local-method + mobile-store billing.

**Confidence.** [V] numbers; [I] exact processor split per region.

---

## 5. Implementation evidence matrix

> One row per feature family, with the concrete artifact that proves it. "Bundle" = shipped JS for build `SGDt3TCUKNRaAUab6ZqjM`.

| # | Feature family | Primary implementation evidence | Secondary evidence |
|---|---|---|---|
| 1 | App shell / routing | `_buildManifest.js` (119 routes), `__NEXT_DATA__` `"page":"/captions"`, `"buildId":"SGDt3TCUKNRaAUab6ZqjM"` | `/_next/static/...` chunk URLs |
| 2 | Static export | Per-route `__turbopack_load_page_chunks__()` stubs; no server-rendered page body | `/captions` HTML is an app shell |
| 3 | Auth | `workos-auth.opus.pro`, Google OAuth client `603062173391-...apps.googleusercontent.com`; legacy `opuspro.us.auth0.com` | `/auth/callback`, `/auth/oauth`, `/auth/orgs` routes |
| 4 | Core clipping | Stage enum in OpenAPI | `/api/clip-projects`, `/api/exportable-clips` |
| 5 | ClipAnything | Marketing `/clipanything`; prompt manual doc | `model: ClipAnything` enum |
| 6 | Virality Score | `virality-score` doc | Demo page scores 99–83 |
| 7 | Recipes | `/recipes`, `/recipes/[slug]`; docs | Brand kit + edit config |
| 8 | Editor engine | `AVEditorEngine-20261105-eb18ce86.wasm.gz` + `.mjs` | Loader references `AVEditorEngine.worker` |
| 9 | WebCodecs | `VideoEncoder`, `VideoDecoder` symbols | Browser-only render path |
| 10 | Speaker matting | `opusSpeakerMatting`, ONNX model SHA-256 | ONNX Runtime 1.14.0 from jsDelivr |
| 11 | Motion graphics | `motion-packages/1.1.0/` assets, Rive runtime | `mg-portrait-*` strings |
| 12 | Layouts | `enable{Fill,Fit,Split,Three,Four,Game,Screen,SpeakerFirst,Auto}Layout` | `layout-*-v2` feature strings |
| 13 | 4K | `renderAsVideoFile4K`, `video4K` | `download-and-post-in-4k` doc |
| 14 | Captions | `TranscriptExportWord/Paragraph` schemas | 8 `brand-template-caption-*-v2` families |
| 15 | Brand vocabulary | `/word-libraries`, `/proper-nouns` endpoints | `brand-vocabulary` doc |
| 16 | AI B-roll | `gpt-image-2.5-flare`, `seedance-1-5-pro-251215`, `creatify-h3-t2v`, `dreamina-seedance-2-5-260628`, `pexels` | `ai-broll` doc |
| 17 | Auto SFX | 7 credits/min | `auto-sfx` doc |
| 18 | Speech cleanup | `/bad-takes/{id}/detect` | `speech-cleanup` doc |
| 19 | Dubbing | `/dub-clip-tasks` | `video-dubbing` doc (25 languages) |
| 20 | AI voiceover | `/ai-voiceover` editor panel, 30 s clone | `ai-voiceover` doc |
| 21 | OpusSearch | `/search`, `/search/indexing`, `/search/results`; `/searchable-clips` | `opussearch` doc |
| 22 | Trends | `/trending-clip-groups` | `opussearch` §Trends |
| 23 | Scheduler | `/api/publish-schedules`, `/api/post-tasks` | `/auto-post/calendar` |
| 24 | Social copy | `/api/social-copy-jobs` | Per-platform limits in bundle |
| 25 | Collections | `/api/collections`, `/api/collection-contents` | `collections` doc |
| 26 | Brand templates | `/api/brand-templates`; `BrandTemplateRepresentation` | `/brand-templates` routes |
| 27 | Censor | `/api/censor-jobs`; `CensorJobRequest` | `how-to-censor-curse-words` |
| 28 | Team / orgs | `/auth/orgs`, `/join/[code]`, `orgId` on projects | Pricing "2 seats" |
| 29 | Agent chat | `/arc-chat/[projectId]`, `/api/agent-sessions` | `agent-sessions?q=findByScope` observed |
| 30 | Approvals | `/agent-approvals`, `/api/agent-approvals/{preview,execute}` | Human-in-the-loop copy |
| 31 | MCP | `https://mcp.opus.pro/mcp` (405 GET) | Marketing `/mcp`, 6 tools |
| 32 | Webhooks | `ConclusionActionDto` `type: EMAIL|WEBHOOK` | `api-reference/webhook` |
| 33 | Payments | Stripe strings, `/dlocal-payments/*` | Apple/Google Pay + IAP docs |
| 34 | Feature flags | `featureassets.org` (GrowthBook) in app HTML | Statsig in marketing/Webflow |
| 35 | Observability | `sentry.io` in HTML; Mixpanel/Guance/Datadog in bundles | `temporal.eng.opus.pro` links |
| 36 | Storage | `ext.cdn.opus.pro`, `public.cdn.opus.pro`, `prod-ao-ext.cdn.opus.pro`, `ext.gcs.opus.pro` | Signed-URL upload example |

---

## 6. Technical architecture

### 6.1 High-level view

```
                              ┌──────────────────────────────┐
  www.opus.pro (Webflow) ───▶ │  Marketing / SEO / tools     │
                              └──────────────────────────────┘
  clip.opus.pro (Next.js SPA)
        │  auth redirect
        ▼
  workos-auth.opus.pro ──▶ Google OAuth / OTP
        │
        ▼
  api.opus.pro  ("Opus Gateway", Clip API 1.0)
        │
        ├── identity/orgs/credits/subscriptions
        ├── projects / curation / clips
        ├── social accounts / copy / schedules / post tasks
        ├── collections / brand templates / transcripts
        ├── agent sessions / approvals / copilot tasks
        └── uploads (signed GCS URLs)
        │
        ▼
  ┌───────────────────────────────────────────────────────────┐
  │ Orchestration: Temporal (namespace "opus")                │
  │ workflow id pattern: cat:main:<projectId>                 │
  │ PENDING→QUEUED→IMPORT→CURATE→REFINE→RENDER→UPLOAD→COMPLETE│
  └───────────────────────────────────────────────────────────┘
        │                │                  │
        ▼                ▼                  ▼
   ASR / scene      LLM curation        Render farm
   (Whisper-class,  (genre, hooks,      (FFmpeg/GPU,
    ElevenLabs)      virality, B-roll)   layouts, captions)
        │                │                  │
        └────────────────┴──────────────────┘
                         ▼
              Google Cloud Storage
        ext.cdn.opus.pro / public.cdn.opus.pro
                         │
                         ▼
                 Cloudflare edge
```

### 6.2 Frontend

**Verified.**

- **Next.js Pages Router** with client-side rendering and a static export. Evidence: `_buildManifest.js`, `__NEXT_DATA__` with `"page"` + `"buildId"`, per-route chunk stubs calling `__turbopack_load_page_chunks__`, and app HTML that is only a shell.
- **Turbopack** runtime chunk (`turbopack-*.js`) and hashed chunk names.
- **i18n** via `/locales/<lang>/` JSON and localized routes (`/es-es/...`).
- **Build ID** rotates frequently: earlier `zCa4gl2e2ewQGDkCpyXau`, current `SGDt3TCUKNRaAUab6ZqjM`.
- **Editor** is a WASM/C++ engine + WebCodecs + Rive (see §4.3).
- **Marketing** is Webflow (`data-wf-site`), **docs** are Mintlify (`llms.txt`, `llms-full.txt`, `mintcdn.com` images).
- **Feature flags**: GrowthBook host `featureassets.org` in the app; Statsig in the marketing/Webflow surface (earlier observation).

**Inferred.**

- The app is probably served as static assets behind Cloudflare, with all data fetched from `api.opus.pro`.
- Route-level code splitting is per-page; the editor loads ~50 chunks on demand.

### 6.3 Backend / API

**Verified.**

- `GET https://api.opus.pro/` → `{"message":"Opus Gateway"}`.
- OpenAPI `Clip API 1.0` served from `help.opus.pro/api-reference/openapi.json`, server `https://api.opus.pro`.
- Error envelope: `{"status":404,"message":"Cannot GET /openapi.json","name":"NotFoundException","path":"...","traceId":"..."}` — strongly characteristic of **NestJS** [I].
- 21 documented paths, 51 schemas (see Appendix A).
- Enums: `stage`, `model`, `sourcePlatform`, `visibility`, `Genre`, `CensorJobStatus`, `GenerativeJobStatus`.
- API limits: 30 req/min, 4 parallel (Pro/Max) / 50 (Business), 10 h / 30 GB, 10-credit min, 30-day expiry, 500 posts/24 h.
- **Webhooks** with `EMAIL` / `WEBHOOK` conclusion actions.
- **MCP** endpoint at `mcp.opus.pro/mcp` (405 on GET; POST/streamable HTTP expected).

**Inferred.**

- A gateway layer ("Opus Gateway") fronts multiple domain services (projects, social, billing, agents). The `traceId` suggests distributed tracing.
- Curation and render are asynchronous jobs, not request/response; the API returns a `stage` and clients poll or receive webhooks.

### 6.4 AI / ML stack

| Layer | Evidence | Confidence |
|---|---|---|
| ASR | `transcript.json`; OpenAPI word/paragraph schema; docs "Whisper"-class options (`x-medium`, `x-large-v3`) and `eleven-labs` seen in earlier bundle inspection | [V] schema; [I] exact model routing |
| Scene detection | `scene_detection_timestamp.json` | [V] |
| Curation LLM | Genre enum, narrative templates, virality score, prompt manual | [V] feature; [I] model |
| Image generation | `gpt-image-2.5-flare`; Agent docs `Seedream 5.0 Pro` | [V] IDs |
| Video generation | `seedance-1-5-pro-251215`, `creatify-h3-t2v`, `dreamina-seedance-2-5-260628`; Agent docs `Vidu Q3`, `Hailuo 3`, `Seedance 2.5` | [V] IDs |
| Stock | `pexels` | [V] |
| Voice clone / TTS | Agent voice cloning (30 s), dubbing 25 languages, `eleven-labs` option | [V] features; [I] provider |
| Speaker matting | ONNX `07c308cf…onnx` + `opusSpeakerMatting` in WASM | [V] |
| Trends | Marketing says "powered by" Gemini icon (earlier capture) | [I] |
| Genre detection | OpenAI referenced for genre detection (earlier bundle strings) | [I] |

**Notable:** the product is deliberately **model-agnostic**. Model IDs are data, not hard-coded prompts: Agent Opus exposes a "Custom → Video model / Image model" picker with per-model credit rates. That is a routing/pricing abstraction over multiple vendors.

### 6.5 Video pipeline and lifecycle

Public stage machine:

| Stage | Meaning | Evidence |
|---|---|---|
| `PENDING` | Accepted, not scheduled | OpenAPI enum |
| `QUEUED` | Waiting for capacity | OpenAPI enum |
| `IMPORT` | Download/transcode/normalise | `resize/video.resize.mp4`, `resize/audio.resize.m4a` |
| `CURATE` | Transcript + scene understanding + moment selection | `transcript.json`, `scene_detection_timestamp.json`, `context.json` |
| `REFINE` | Hook/boundary/layout refinement | `screenplay.json`, `curationResultCache` |
| `RENDER` | Compose clips (layouts, captions, B-roll, SFX) | `renderAsVideoFile*` flags |
| `UPLOAD` | Push to GCS/CDN | `ext.cdn.opus.pro/.../esd.mp4`, `ehd.mp4` |
| `COMPLETE` | Ready | OpenAPI enum |
| `STALLED` | Failure/hang path | OpenAPI enum |

Output URIs documented: `https://ext.cdn.opus.pro/media/<org>/<user>/<project>/c.<curationId>/esd.mp4` and `ehd.mp4` (standard/HD variants), with clip IDs of the form `{project_id}.{curation_id}`.

### 6.6 Storage and CDN

**Verified hosts and roles:**

| Host | Role |
|---|---|
| `ext.gcs.opus.pro` | GCS bucket front for uploads (`/upload/org_.../google-upload`) |
| `ext.cdn.opus.pro` | Serving rendered media (`/media/<org>/<user>/<project>/...`) |
| `public.cdn.opus.pro` | Public static assets: WASM, motion packages, fonts, tool images, LUTs |
| `prod-ao-ext.cdn.opus.pro` | Production Agent Opus asset host (observed) |
| Cloudflare | Edge/CDN/WAF (observed fronting) |

**Upload flow.** Client requests a signed URL → `PUT` to GCS → passes `uploadId`/`gcsUploadId` to `POST /api/clip-projects`. The `/captions` page uses a hidden iframe `opus-upload-frame` for multipart uploads.

**Lifecycle.** Default project expiry 30 days; Pro auto-save to 100 GB cloud storage; downgrade removes saved projects from storage.

### 6.7 Infrastructure and operations

| Concern | Evidence | Confidence |
|---|---|---|
| Orchestration | `temporal.eng.opus.pro/namespaces/opus/workflows?query=WorkflowId STARTS WITH "cat:main:<projectId>"` | [V] string; [I] role |
| Error tracking | `sentry.io` script/host in app | [V] |
| Product analytics | Mixpanel, Datadog browser agent, Guance console links | [V] strings |
| Experimentation | GrowthBook (`featureassets.org`) in app; Statsig in marketing | [V] |
| Consent | Cookiebot | [V] |
| Support/CRM | HubSpot, Brevo, Intercom | [V] |
| Status/changelog | Instatus, Canny | [V] |
| Social proof | Product Hunt API | [V] |
| Tag management | GTM proxied at `/opusgt/` | [V] |

### 6.8 Auth, identity, multi-tenancy

- App root redirects to `/dashboard`; unauthenticated users go to **WorkOS AuthKit** at `workos-auth.opus.pro`.
- Google OAuth client `603062173391-nvit3fv6so5ns9ft9ja8dd76r0vdfn5p.apps.googleusercontent.com`.
- Legacy Auth0 tenant `opuspro.us.auth0.com` and user IDs of the form `google-oauth2|...` still appear in API examples — evidence of a migration.
- Tenancy is **organization-scoped**: `orgId` on projects, `/auth/orgs`, `/join/[invitationCode]`, seats in pricing.
- Roles/permissions exist (`team-management`, orgs), and agent actions can require human approval (`/agent-approvals`).

**Confidence.** [V] providers/flows; [I] exact session/token implementation.

### 6.9 Data model (public projection)

```
Org 1─* User
Org 1─* BrandTemplate
Org 1─* SocialAccount
Org 1─* Collection 1─* CollectionContent *─1 Clip
Org 1─* Project (ClipProject)
Project 1─* Clip (ExportableClip)
Project 1─* CensorJob
Project 1─* AgentSession 1─* AgentApproval
Clip 1─* PostTask / PublishSchedule
Project 1─* Transcript
```

Key fields:

- `ClipProject`: `id`, `userId`, `orgId`, `sourcePlatform`, `sourceId`, `sourceUri`, `stage`, `model`, `visibility`, `productTier`.
- `ExportableClip`: `id`, `projectId`, `runId`, `curationId`, `orgId`, `userId`, `uriForPreview`, `uriForExport`, `storageUsed`, `durationMs`, `timeRanges`, `keywords`, `promptName`, `title`, `description`, `hashtags`, `text`, `genre`, `subgenre`, `clipCopilotTaskId`, `productTier`, `renderPref`.
- `CensorJob`: `projectId`, `clipId`, `options.beepSound`, status enum.
- `GenerativeJob`: `jobType: thumbnail`, status `QUEUED|PROCESSING|CONCLUDED|FAILED|UNKNOWN`, result `generatedThumbnailUris`.

### 6.10 Integrations and billing

- **Social**: YouTube, Instagram, TikTok, Facebook, LinkedIn, X, Pinterest, Threads, Bluesky (+ Dailymotion / TikTok Business in bundle). OAuth per platform; `/post/blueskyOauthCallback` is a dedicated callback route.
- **Scheduling partner**: `/post/plansocial` and `plansocial.co`.
- **Storage**: Google Drive OAuth; Dropbox/Riverside/Loom/Frame.io/StreamYard ingest.
- **Editing**: Adobe Premiere Pro / DaVinci Resolve XML export.
- **Automation**: Zapier, REST API, webhooks, MCP.
- **Billing**: Stripe, dLocal, Apple Pay, Google Pay, Apple/Google IAP, invoices, VAT, subscription pause/cancel.

### 6.11 Delivery / deployment

**Verified.**

- Build ID changes (`zCa4…` → `SGDt3…`) with a dated WASM artefact (`AVEditorEngine-20261105-…`), implying frequent deploys and cache-busted assets.
- Assets served from `public.cdn.opus.pro` with content-hashed filenames and SHA-256 for the ONNX model, implying integrity checks.
- `stg-clip`, `stg-api`, `stg-labs` staging hosts exist.
- Marketing (Webflow), docs (Mintlify), app (Next.js), and Labs (separate apps) deploy independently.

**Inferred.** Cloudflare + GCS + containerised services; likely Kubernetes or a similar scheduler for render workers [I].

---

## 7. Evidence & references

### 7.1 Screenshots (in `assets/`)

| File | URL | What it shows |
|---|---|---|
| `assets/captions-page.jpg` | `https://clip.opus.pro/captions` | Free captioning funnel: "Caption your videos for FREE like Alex Hormozi", drop zone (mp4/mov/mkv/webm, 9:16/1:1/16:9), creator examples, Product Hunt badge, Discord/FAQ. Also shows a live `401 Unauthorized` on `/api/subscriptions/payment-method-currency-operation` — unauthenticated shell behaviour. |
| `assets/clipanything.jpg` | `https://www.opus.pro/clipanything` | ClipAnything hero: "First-ever multimodal AI clipping… visual, audio, and sentiment cues", CTA, trailer. |
| `assets/ai-video-summarizer.jpg` | `https://clip.opus.pro/tools/ai-video-summarizer` | Public tool: link input, Summary Format, Language, Summary Length, Generate button; "Free" badge. |

> Note: `www.opus.pro/pricing` was fetched twice. Raw HTML showed Starter `$15/mo`, Pro `$29/mo` or `$14.5/mo` annual (`$174/yr`). A live render showed `$9 USD/mo` for Starter with "Save up to 67%". Treat pricing as **observed values subject to region/experiment**.

### 7.2 URLs fetched / verified

| URL | Result | Use |
|---|---|---|
| `https://clip.opus.pro/` | 200 → app shell → `/dashboard` | Entry |
| `https://clip.opus.pro/captions` | 200 | Free funnel + network capture |
| `https://clip.opus.pro/clip/demo-podcast` | 200 | Public result page (25 clips, scores 99–83) |
| `https://clip.opus.pro/tools/ai-video-summarizer` | 200 | Tool page |
| `https://clip.opus.pro/sitemap.xml` | 200, 23 URLs | 22 tools + captions |
| `https://clip.opus.pro/_next/static/SGDt3TCUKNRaAUab6ZqjM/_buildManifest.js` | 200, 87 KB | 119 routes |
| `https://www.opus.pro/pricing` | 200, 642 KB | Plans/credits |
| `https://www.opus.pro/clipanything` | 200 | Multimodal clipping |
| `https://www.opus.pro/sitemap.xml` | 200, 3.6 MB, 5,525 URLs | Marketing surface |
| `https://help.opus.pro/llms-full.txt` | 200, 571,189 B, 259 docs | Feature corpus |
| `https://help.opus.pro/sitemap.xml` | 200, 259 URLs | Docs index |
| `https://help.opus.pro/api-reference/openapi.json` | 200, 103 KB | API contract |
| `https://api.opus.pro/` | 200 `{"message":"Opus Gateway"}` | Gateway |
| `https://mcp.opus.pro/mcp` | 405 on GET | MCP endpoint |
| `https://agent.opus.pro/` | 200 | Agent product |
| `https://labs.opus.pro/` | 200 | Labs catalogue |

### 7.3 Network / console capture (earlier REA capture)

- **Evidence ID:** `ev_040f463ef3e2b27aa22aad897443a9ee98c3ee2fd7c37fef1aa5409f1fe8874e` (`https://clip.opus.pro/captions`).
- **266 requests, 55 hosts.** Includes: GrowthBook, Statsig, Cookiebot, HubSpot, Brevo, ProfitWell, Canny, Instatus, Product Hunt API, Sentry, Microsoft Clarity, Google Tag Manager proxied at `/opusgt/`, Mixpanel, Datadog.
- Public demo API calls observed:
  - `GET /api/org-credits/check-second-trial`
  - `GET /api/agent-sessions?q=findByScope&scopeType=project&scopeId=demo-podcast`
  - `GET /api/clip-copilot-tasks/?q=findByProjectId`
  - `GET /api/engine-clips/demo-podcast.CU8145b9`
  - `GET /api/curated-clips?q=findByProjectIdAndCluster&cluster=tin`
  - `GET /api/post-tasks`
  - `GET /api/social-accounts?q=mine`
  - `GET /api/source-media-groups`

### 7.4 Shipped-artifact references (current build)

| Artefact | Value |
|---|---|
| Build ID | `SGDt3TCUKNRaAUab6ZqjM` |
| Editor WASM (gzip) | `public.cdn.opus.pro/clip-web/wasm/AVEditorEngine-20261105-eb18ce86.wasm.gz` (6,435,950 B) |
| Editor ESM glue | `.../AVEditorEngine-20261105-eb18ce86.mjs` (450,499 B) |
| ONNX runtime | `cdn.jsdelivr.net/npm/onnxruntime-web@1.14.0/dist/ort.min.js` |
| Speaker-matting model | `.../motion-packages/1.1.0/07c308cf0fc7e6e8b2065a12ed7fc07e1de8febb7dc7839d7b7f15dd66584df9.onnx` |
| Motion packages | `public.cdn.opus.pro/clip-web/motion-packages/1.1.0/` |
| Layout flags | `enableAutoLayout`, `enableFillLayout`, `enableFitLayout`, `enableSplitLayout`, `enableThreeLayout`, `enableFourLayout`, `enableGameLayout`, `enableScreenLayout`, `enableSpeakerFirstLayout` |
| Render flags | `renderAsVideoFile`, `renderAsVideoFile4K`, `video4K` |
| B-roll models | `gpt-image-2.5-flare`, `seedance-1-5-pro-251215`, `creatify-h3-t2v`, `dreamina-seedance-2-5-260628`, `pexels` |
| Caption templates | `brand-template-caption-{mozi,beasty,youshaei,think-media,popline,simple,deep-diver,karaoke}-v2` |
| Feature versions | `rough-cut-2026-10-v1`, `layout-{fit,fill,split,three,four,gameplay}-v2`, `watermark-v2`, `video-render-nx-v2`, `google-storage-v3`, `upload-links-v2`, `mg-portrait-*` |

### 7.5 Documentation references

- `https://help.opus.pro/docs/article/introduction-to-opusclip`
- `https://help.opus.pro/docs/article/clip-anything`, `.../clip-anything-prompt-manual`
- `https://help.opus.pro/docs/article/virality-score`
- `https://help.opus.pro/docs/article/opussearch`
- `https://help.opus.pro/docs/article/ai-broll`, `.../add-motion-graphics`, `.../auto-sfx`
- `https://help.opus.pro/docs/article/speech-enhancement`, `.../speech-cleanup`, `.../ai-voiceover`, `.../video-dubbing`
- `https://help.opus.pro/docs/article/eye-contact-correction`, `.../upscale-a-video`, `.../apply-the-layouts`
- `https://help.opus.pro/docs/article/add-social-accounts`, `.../schedule-clips`, `.../bulk-scheduler`
- `https://help.opus.pro/api-reference/limitation`, `.../overview`
- `https://help.opus.pro/agent-opus/article/ao-faq`, `.../ao-faq-credits-faq`, `.../ao-story-mode-faq`
- `https://help.opus.pro/opus-labs/{ai-expand,audio-to-image-video,live-clipping,motion-studio,multi-angle,product-ad-studio,product-launch-video-plugin}`

### 7.6 Public demo observation

`https://clip.opus.pro/clip/demo-podcast` renders a real-looking result page without login:

- Project: "Tal Wilkenfeld … Lex Fridman Podcast #408".
- 25 clips; virality scores 99–83; some marked "Edited".
- Left rail: Home `/dashboard`, My projects `/projects`, Asset library `/brand-kits`, Calendar `/auto-post/calendar`, Analytics `/analytics`, Labs, API, Book.
- OmniSearch `⌘K`; credits badge `10`.

This is the strongest public evidence of the end-user results UI.

---

## 8. Verified vs inferred

| Claim | Status | Basis |
|---|---|---|
| App is Next.js static export, client-rendered | **Verified** | Manifest, `__NEXT_DATA__`, chunk stubs |
| Editor is C++/WASM + WebCodecs | **Verified** | `AVEditorEngine.wasm`, `VideoEncoder/Decoder` |
| Client-side speaker matting | **Verified** | `opusSpeakerMatting`, ONNX model |
| API is `Clip API 1.0` on `api.opus.pro` | **Verified** | OpenAPI + gateway response |
| Backend is NestJS | **Inferred** | Error envelope (`NotFoundException`, `traceId`) |
| Temporal orchestrates projects | **Verified string / inferred role** | `temporal.eng.opus.pro`, `cat:main:<id>` |
| GCS + Cloudflare for media | **Verified** | `ext.gcs.opus.pro`, CDN hosts, signed uploads |
| GrowthBook = app flags, Statsig = marketing | **Verified hosts / inferred split** | `featureassets.org`; earlier Statsig capture |
| ASR uses Whisper variants + ElevenLabs | **Partially verified** | Earlier bundle strings; docs mention languages, not vendors |
| AI B-roll vendors | **Verified IDs** | Bundle strings |
| Agent models (Vidu, Seedream, Hailuo, Seedance) | **Verified** | Docs name them |
| 25+ languages | **Verified** | Language table |
| 9 social platforms | **Verified** | Docs; bundle adds Dailymotion |
| 500 posts/24 h API cap | **Verified** | API limitation doc |
| 100 GB Pro storage | **Verified** | Cloud storage doc |
| Pricing numbers | **Verified as observed; variable** | Two renders differed |
| "20M+ creators" | **Vendor claim** | Marketing |
| PlanSocial is the scheduler backend | **Inferred** | `/post/plansocial` route + partner site |
| Embedding vendor for OpusSearch | **Unknown** | Not exposed publicly |

---

## 9. Gaps and how to close them

1. **Authenticated dashboard/editor**: would confirm org roles, editor canvas internals, scheduler grid, and actual API payloads. Requires an account.
2. **Exact ASR/LLM routing**: model IDs are not exposed in public docs; would need authenticated network capture or vendor disclosures.
3. **Temporal topology**: the console link proves Temporal usage; worker/activity names need internal access.
4. **Render farm**: GPU vs CPU, FFmpeg vs custom compositor — only inferred from WASM + output flags.
5. **OpusSearch embeddings**: no public evidence of the vector store or embedding model.
6. **Pricing**: region/A-B variation means only ranges are reliable.
7. **Mobile app internals**: docs list feature gaps but not architecture.

---

## Appendix A — Public API paths (`Clip API 1.0`)

| Method | Path |
|---|---|
| GET | `/api/brand-templates` |
| POST | `/api/clip-projects` |
| GET | `/api/clip-projects/{projectId}` |
| POST | `/api/clip-projects/{projectId}/update-visibility` |
| GET | `/api/exportable-clips` |
| POST | `/api/collection-contents` |
| POST | `/api/collection-contents/delete-collection-contents` |
| GET | `/api/collections` |
| POST | `/api/collections` |
| DELETE | `/api/collections/{collectionId}` |
| POST | `/api/collections/{collectionId}/export` |
| POST | `/api/censor-jobs` |
| GET | `/api/censor-jobs/{jobId}` |
| GET | `/api/social-accounts` |
| POST | `/api/social-copy-jobs` |
| GET | `/api/social-copy-jobs/{jobId}` |
| POST | `/api/post-tasks` |
| POST | `/api/publish-schedules` |
| DELETE | `/api/publish-schedules/{scheduleId}` |
| POST | `/api/generative-jobs` |
| GET | `/api/generative-jobs/{jobId}` |
| GET | `/api/transcripts` |

**Internal endpoints observed in bundles** (not in public OpenAPI): `/api/engine-clips/{id}.{ext}`, `/api/curated-clips`, `/api/agent-sessions`, `/api/agent-approvals/preview|execute`, `/api/auto-import`, `/api/dub-clip-tasks`, `/api/bad-takes/{id}/detect`, `/api/searchable-clips`, `/api/trending-clip-groups`, `/api/musics`, `/api/social-copy`, `/api/upload-links-v2`, `/api/signed-urls`, `/api/workflows`, `/api/word-libraries`, `/api/proper-nouns`, `/api/org-credits*`, `/api/subscription*`.

---

## Appendix B — Route inventory (selected, 119 total)

**Core:** `/dashboard/[[...workflowType]]`, `/projects`, `/clip/[projectId]`, `/clip-create`, `/submit-video`, `/project-submit`, `/recipes`, `/recipes/[slug]`, `/collections`, `/collections/[id]`, `/campaigns`, `/campaigns/[campaignId]`, `/analytics`, `/activity`.

**Editor:** `/editor/[fullClipId]`, `/editor-ux/[fullClipId]`, `/clip-editor/[curatedClipId]`, `/editor-start`, `/captions/project/[projectId]`.

**Brand:** `/brand-kits`, `/brand-template`, `/brand-templates`, `/brand-templates-ux`, `/brandkit-canary`, `/brands`.

**Search:** `/search`, `/search/indexing`, `/search/onboarding`, `/search/results`.

**Publishing:** `/auto-post/calendar`, `/post/plansocial`, `/post/blueskyOauthCallback`.

**Agent:** `/arc-chat/[projectId]`, `/agent-approvals`.

**Account/billing:** `/checkout`, `/subscription`, `/subscription/redirect`, `/referral`, `/referral/landing`, `/affiliate/[slug]`, `/join/[invitationCode]`, `/email-preferences`, `/questions/delete-account`, `/cookies`.

**Auth:** `/auth/callback`, `/auth/connect`, `/auth/logout`, `/auth/oauth`, `/auth/oauth/login`, `/auth/orgs`, `/auth/redirect`, `/oauth-google-callback`.

**Growth/landing:** `/lp-auto-close`, `/lp-embed`, `/lp-linkedin-badge`, `/lp-social`, `/linkedin-badge`, `/r`, `/mini-app/launch`, `/third-party/landing/ecamm`, `/third-party/landing/spotlight`.

**Tools:** `/tools/[tool]`, `/tools/[tool]/[jobId]`, plus 23 concrete tool routes (YouTube name/description/hashtag/title, TikTok username/caption/bio/hashtag, transcript, summarizer, show notes, thumbnail, relighting, compressor, converter, mov↔mp4, avi→mp4, mkv→mp4, webm↔mp4, video noise remover, video script).

**Other:** `/workflow`, `/workflow-canary`, `/empty-org`, `/captions`, `/es-es/*`.

---

## Appendix C — Enums, limits, and model IDs

**Stage:** `PENDING, QUEUED, IMPORT, CURATE, REFINE, RENDER, UPLOAD, COMPLETE, STALLED`

**Model:** `ClipAnything, ClipBasic`

**Source platform:** `YOUTUBE, UPLOADED, YTDLP_LINK, GDRIVE, ZOOM, STREAM_YARD`

**Visibility:** `DEFAULT, PUBLIC, PUBLIC_VIEW`

**Genre (18):** `Auto, Q&A, Commentary, Marketing, Webinar, Motivational speech, Podcast, Academic, Listicle, Product reviews, How-to, Comedy, Sports commentary, Church, News, Vlog, Gaming, Others`

**Censor status:** `CONCLUDED, FAILED, PROCESSING, QUEUED, UNKNOWN`

**Generative job status:** `QUEUED, PROCESSING, CONCLUDED, FAILED, UNKNOWN`

**Caption limits:** Facebook 63,206 · YouTube 5,000 · LinkedIn 3,000 · Pinterest 800 · Threads 500 · Bluesky 300 · X 280 (weighted)

**API limits:** 30 req/min · 10 h / 30 GB · 10-credit min · 4 parallel (Pro/Max) · 50 parallel (Business) · 900 credits/month (Pro Beta/Max) · 500 posts/24 h · 30-day project expiry · 100 GB Pro storage

**Agent pricing:** Standard 1 cr/s (≤10 min) · Standard+ 3 cr/s (≤10 min) · Premium 7 cr/s (≤3 min) · Ultra 12 cr/s (≤3 min) · Motion Graphics 3 or 10 cr/s

**Credits:** 1 credit = 1 minute · Auto SFX 7/min · Auto motion graphics 6/min · Auto colour 1 · X post 3 · Upscale base ~23 (example) · API min 10/project

**AI model IDs observed:** `gpt-image-2.5-flare` · `seedance-1-5-pro-251215` · `creatify-h3-t2v` · `dreamina-seedance-2-5-260628` · `pexels` · docs: Vidu Q3, Seedream 5.0 Pro, Seedance 1.5/2.5, Hailuo 3

---

## Appendix D — Corpus statistics

| Artifact | Size / count |
|---|---|
| Help-centre `llms-full.txt` | 571,189 bytes · 259 documents |
| Help-centre sitemap | 259 URLs (184 docs, 58 API ref, 8 Labs, 6 Agent Opus, 3 meta) |
| Marketing sitemap | 5,525 URLs |
| App sitemap | 23 URLs (captions + 22 tools) |
| App route manifest | 119 route entries |
| Public OpenAPI | 21 paths · 51 schemas |
| Shipped chunks sampled (current build) | 62 JS files (~5 MB) |
| Network capture (`/captions`) | 266 requests · 55 hosts |

---

*End of report. All observations are from public, unauthenticated surfaces on 2026-11-05. No code was written and no product functionality was modified.*
