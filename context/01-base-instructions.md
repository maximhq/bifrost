# Bifrost Base Instructions

## Module and stack
- Modules: `github.com/maximhq/bifrost/{core,framework,transports,cli}` plus plugins; Go 1.26.4.
- HTTP: `fasthttp` with `github.com/fasthttp/router`; provider calls also use `fasthttp`, except Bedrock's signed HTTP/2 paths use `net/http`.
- Persistence: GORM over PostgreSQL (`pgx`), SQLite, and ClickHouse; stores are optional.
- Jobs: custom DB-backed `framework/sidekiq` runner; core request dispatch uses channels and per-provider queues.
- Config: `config.json` is reconciled with ConfigStore, flags, and direct `os.Getenv`/`envutils` lookups. `transports/config.schema.json` defines its shape, but startup does not invoke the schema validator. No Viper.
- Logging: zerolog behind `schemas.Logger`. Tests use `testing`, testify, and handwritten mocks.
- Migrations: in-repo GORM-based `framework/migrator`, derived from gormigrate.

## Go idioms
- Wrap operational errors with `fmt.Errorf("context: %w", err)`; classify with `errors.Is`. Use sentinel errors for expected conditions and `schemas.BifrostError` for provider/API failures.
- Pass `context.Context` first on store/service operations; handlers receive `*fasthttp.RequestCtx`; inference uses `*schemas.BifrostContext`.
- Prefer narrow consumer-side interfaces; the large core `Provider` interface is a central contract.
- Use `NewXxx` and constructor/field injection; there is no DI container.
- `transports/bifrost-http/main.go` configures process concerns; `server.Bootstrap` performs manual application wiring and lifecycle setup.

## Architecture
- `core`: engine, schemas, providers, MCP. `framework`: persistence/runtime services. `transports`: HTTP boundary/integrations. `plugins`: versioned hooks. Keep `ui`, `tests`, and `docs` separate.
- Store interfaces abstract persistence, but concrete `RDBConfigStore`/log stores use GORM directly; there is no universal repository/service layer.
- Validate untrusted wire input in handlers; validate/default shared configuration in schema/config packages. Update `config.schema.json` for configuration shape changes.
- Middleware is constructor-built and composed with `lib.ChainMiddlewares`; order is significant. Plugin pre-hooks run forward and post-hooks in reverse.

## Non-negotiables
- Preserve auth, virtual-key, scope, and route checks; never expose stored secrets.
- Propagate cancellation/deadlines; streaming must use the timeout-free streaming client plus per-chunk idle timeout.
- Reset every pooled object before release. Never close provider request queues; signal `done`, stop sends, and drain.
- Never store stream-sized data in `BifrostContext`; store manager IDs/handles only. Async plugin work must capture `ctx.Root()`.
- Keep DB job claims atomic/idempotent and webhook signatures byte-exact.

## Do not
- Introduce `net/http` handlers, chi/gin, Viper, Zap, or a new DI framework.
- Bypass stores with ad-hoc SQL or invent repository layers.
- Return string-matched errors when a sentinel/custom error exists.
- Drop context propagation or start untracked goroutines.
- Assume JSON Schema rejects invalid runtime configuration; enforce required validation explicitly.
- Mutate atomically published config/provider slices in place.
- Use `encoding/json` in established sonic hot paths.
- Add provider operations without updating every provider contract implementation.
