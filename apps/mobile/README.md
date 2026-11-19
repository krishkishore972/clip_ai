# Chorus Clip mobile

Expo SDK 58 / React Native application for private video uploads and validation status, with Clerk sign-in and branded Home, Upload and Account screens. The separate Next.js web starter is retained; this native client is the current product path.

**Last reviewed:** 2026-11-19. **Evidence:** [V] source/automated checks or the developer's real-device logs and UI confirmation; [U] unverified behavior. This is a development slice, not a production-ready editor.

## What works

[V] The real-device happy path has been verified:

```text
Clerk sign-in → private multipart S3 upload → PostgreSQL/outbox
             → dispatcher → Redis/Asynq → validation worker
             → mobile shows Validation passed (attempt 1 / 4)
```

Uploads show confirmed-part progress and support best-effort cancellation. They require the foreground; automatic upload retries, background transfer and resume are intentionally excluded. Validation status is read separately, so a failed status check does not turn a completed upload into an upload failure or start another upload.

[U] Live invalid-video rejection, transient-failure retry and worker-crash recovery scenarios remain to be verified. A persistent asset library, playback, transcription, generated clips, editing, captions and export are not implemented. Validation passing does not mean playback or clips are available.

## Install and configure

Run commands below from the repository root unless stated otherwise. Use Node.js 24+ and the workspace's pnpm 11.25.0; do not install dependencies with npm or yarn.

```bash
pnpm install --frozen-lockfile
```

Create a local environment file from the [mobile example](.env.example) only if one does not already exist. Preserve your existing Clerk setup and keys. Configure:

```dotenv
EXPO_PUBLIC_API_URL=http://YOUR_COMPUTER_LAN_IP:8080
EXPO_PUBLIC_CLERK_PUBLISHABLE_KEY=YOUR_CLERK_PUBLISHABLE_KEY
```

- The API URL must be reachable from the phone. On a physical device, `localhost` points to the phone, not the Go API host.
- Keep the phone/computer on the same LAN and allow the required local-network/firewall access. Prefer HTTPS; native development builds may need explicit local HTTP configuration.
- Public environment values are bundled into the client. Never add AWS credentials, database/Redis URLs, Clerk secret keys, upload-signing secrets or upload tokens.
- The publishable key must match the Clerk application configured on the backend. Do not recreate/reinitialize Clerk to troubleshoot an existing installation.
- Restart Expo after changing environment configuration.

## Start the backend and Expo

Configure PostgreSQL, Redis, private S3/IAM, Clerk and migration status first using the [root backend guide](../../README.md#backend-setup). Migrations 00001–00005 were applied to the existing development DB, but this does not establish migration state on another database.

Run these in separate terminals from the repository root:

```bash
# API: opt into development uploads
cd apps/api
make run DEV_S3_UPLOAD_ENABLED=true
```

```bash
# Worker: requires FFprobe on PATH and private temporary storage
cd apps/api
make worker VALIDATION_WORKER_ENABLED=true
```

```bash
# Dispatcher: start after the worker
cd apps/api
make dispatcher OUTBOX_DISPATCH_ENABLED=true
```

These processing services may consume existing queued/pending assets and incur normal S3 charges. They are not started by Expo, and should not be enabled accidentally against another database or queue.

Start the native client from the repository root:

```bash
pnpm --filter mobile start --go --lan --clear
```

Open the QR code in an SDK 58-compatible Expo Go client. For a configured development build instead, use the appropriate Expo launch target; native modules not included in Expo Go require a development build. See the [Expo CLI guide](https://docs.expo.dev/more/expo-cli/) and [SDK 58 reference](https://docs.expo.dev/versions/v58.0.0/).

An Expo tunnel only exposes the bundler; it does not automatically expose the local Go API. The API needs its own reachable address. For this first test, prefer the shared LAN setup.

## Manual smoke test

1. Sign in and open Upload.
2. Choose a small known-good MP4 with H.264 video and AAC audio.
3. Tap **Upload video** and keep the app foregrounded until finalization finishes.
4. Confirm **Upload complete**, then the separate validation card.
5. Expect **Waiting for validation → Validating video → Validation passed**. A fast task may finish between polls, so intermediate states can be skipped visually.
6. Check the worker terminal for `validation attempt finished`, the matching asset ID, `status: ready` and its attempt count.
7. After upload completes, background/return to the app or temporarily disconnect the network. Confirm that checks pause/resume or show a safe status-read error without reuploading.

The current status card polls sequentially, approximately five seconds after each settled nonterminal check, only while focused and foregrounded. It stops automatic polling at ready/rejected/failed states or an authentication failure. **Check status** reads state only; it does not retry validation or reupload media.

Keep the upload screen mounted during observation: the current completed asset ID is component state, not a persistent asset library. Selecting another video or remounting the screen does not restore the previous card.

If testing upload cancellation, use a disposable test video and remember that cleanup is best-effort. Do not interpret cancellation as proof that all remote parts were removed; the bucket's incomplete-upload lifecycle rule is the backstop.

## Checks and contracts

From the repository root:

```bash
pnpm --filter mobile test:uploads
pnpm --filter mobile test:design-system
pnpm --filter mobile check-types
pnpm --filter mobile lint
```

[V] These suites cover offline upload/client/poller behavior, API type drift, design-system/asset invariants and static checks. They do not replace live device, authentication, S3 or worker-recovery testing.

The [OpenAPI contract](../api/openapi/openapi.yaml) is the source for the [generated API types](src/lib/api/generated.ts). After a contract change:

```bash
pnpm --filter mobile gen:api
node apps/mobile/scripts/generate-api-types.mjs --check
```

Do not hand-edit generated types. Backend routes and middleware remain responsible for authorization and authoritative media validation.

## Source map

- [Routes](src/app/): Expo Router Home, Upload, Account and root layout.
- [Design system](src/design-system/): semantic tokens, native fonts and UI wrappers.
- [API client](src/lib/api/): authenticated control requests and defensive status parsing.
- [Upload helpers](src/lib/uploads/): native file access, sequential upload engine, status polling and safe copy.
- [UI components](components/ui/): provider, button and progress primitives, including platform-specific provider implementations.
- [Metro configuration](metro.config.js): NativeWind and asset-registry compatibility wiring; its helpers are runtime dependencies, not unused scripts.

The native flow is the verified target. The existing Expo web provider/configuration is preserved; it is not proof of a fully verified browser upload flow. The root Next.js app is separate and also remains in the repository.
