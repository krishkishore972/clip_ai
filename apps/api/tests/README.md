# API tests

**Last verified:** 2026-11-19. [V] All Go test source files are centralized here, grouped by the application/package they exercise. Tests use imported production APIs rather than private package access. Production code remains in the command, internal and migrations directories; test fixtures and assertions remain in this directory.

## Layout

| Directory                 | Coverage                                                                                                        |
| ------------------------- | --------------------------------------------------------------------------------------------------------------- |
| [apps](apps/)             | Importable server, dispatcher, worker and migration application behavior. CLI entrypoints remain thin wrappers. |
| [auth](auth/)             | Clerk token validation, identity and ownership context.                                                         |
| [config](config/)         | HTTP, auth, storage, queue and worker configuration validation.                                                 |
| [db](db/)                 | Pool behavior and opt-in PostgreSQL connectivity.                                                               |
| [handler](handler/)       | Upload/status HTTP boundaries and OpenAPI drift guards.                                                         |
| [httpserver](httpserver/) | Middleware, request IDs, JSON/error handling and recovery.                                                      |
| [job](job/)               | Producer policy, dispatch, claims, retries, worker behavior and temporary-file safety.                          |
| [media](media/)           | Media limits, metadata validation, local probing and public status projection.                                  |
| [migrations](migrations/) | Embedded migration/schema constraints, owned persistence and opt-in PostgreSQL/Redis pipeline tests.            |
| [storage](storage/)       | Bounded downloads and multipart S3 SDK adapters using offline doubles.                                          |
| [upload](upload/)         | Authenticated upload lifecycle and signed-capability boundaries.                                                |

Keep new `*_test.go` files in these directories, not beside production files. Do not copy production implementations into tests, use linker tricks/overlays to reach private functions, or remove assertions merely to move a test. Reuse the public domain APIs or real dependency-injection boundaries in the internal packages. Tests are deliberately outside the production package even where private helpers were previously tested directly.

## Offline checks

Run from the API module directory, not this tests directory. Clear test database/queue opt-ins to avoid contacting application infrastructure:

```bash
env -u TEST_DATABASE_URL -u TEST_ASYNQ_REDIS_URL \
  -u TEST_REDIS_URL -u VERIFY_MIGRATION_DATABASE_URL \
  go test -race ./...
go vet ./...
go build ./...
```

A focused suite can be selected normally:

```bash
go test ./tests/media ./tests/upload -count=1
go test ./tests/apps/... -count=1
```

FFprobe must be on `PATH` to execute the local probe fixtures rather than skip them. Ordinary storage/upload tests use offline doubles; they do not upload videos to AWS.

## Coverage

Tests now live in separate packages, so plain `go test -cover` does not measure the imported production code correctly. Use explicit production-package instrumentation:

```bash
go test -race \
  -coverpkg=./internal/...,./cmd/...,./migrations \
  -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

The [Makefile](../Makefile) `make cover` target uses the same production-package selection and generates an HTML report. It also loads the local backend environment; review test opt-in variables before invoking it. Tests themselves are excluded from the coverage denominator. A successful offline run does not establish database or end-to-end pipeline coverage.

## Disposable integration infrastructure

- `TEST_DATABASE_URL` must point to a dedicated disposable PostgreSQL database via a direct/session connection. Tests create and drop isolated schemas, run Up/Down/reapply and refuse connections that do not honor schema isolation. Never supply the configured application database or a transaction-pooled URL.
- `TEST_ASYNQ_REDIS_URL` enables the queue pipeline tests and must point to a dedicated, isolated test Redis database. Never use the application's queue, because tests enqueue/process/reconcile tasks.
- `TEST_REDIS_URL` separately enables the producer connectivity test; use isolated test infrastructure for it as well.
- `VERIFY_MIGRATION_DATABASE_URL` is a separate explicit opt-in for read-only applied-schema checks. It is not a substitute for the disposable migration-test database and must not be used to enable rollback tests.

Set only the intended opt-ins in the test process environment, then run:

```bash
go test -race ./tests/migrations -count=1 -v
```

`make test-migrations` targets the relocated migration suite and requires `TEST_DATABASE_URL`. No test target automatically applies migrations to the configured application database. No rollback command is exposed by the migration CLI.

## Production/test boundaries

The [importable applications](../internal/app/) contain process composition formerly held by the command packages. Command arguments, error reporting, build-time version stamping, opt-ins and startup/shutdown policy remain at their existing boundaries. Authentication, upload capabilities, codec/size limits, claim fencing, queue retry budgets, path guards and SQL ownership checks must not be relaxed to make tests easier.

Clock/entropy sources, SDK clients and the probe function can be injected by trusted application code. Default construction continues to use the real clock, cryptographic entropy, configured AWS/Clerk clients and the real media probe. These are code-level dependency seams, not user-controllable HTTP options or new authentication bypasses.
