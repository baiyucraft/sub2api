# bootstrap-wiki CodeGraph 研究

phase: exploration
service-boundary: project-architecture-and-wiki-bootstrap

## Evidence

- CodeGraph status: index current, 3,568 files, 109,935 nodes, 389,114 edges; Go 2,642, TypeScript 503, Vue 327, Python 72.
- `initializeApplication` in `backend/cmd/server/wire_gen.go:35` assembles config, Ent/SQL, Redis, repositories, services, handlers and application.
- `SetupRouter/registerRoutes` in `backend/internal/server/router.go:35-161` install middleware and register common, v1, gateway, payment and page routes.
- `backend/internal/config/config.go:64-103` aggregates runtime configuration.
- `backend/internal/repository/migrations_runner.go:23-54,159-167` defines schema_migrations, non-transaction suffix and execution entry; migration plan code provides catalog/checksum validation.
- `frontend/src/main.ts` creates Vue/Pinia/i18n, loads injected settings and waits for router readiness; frontend API/router/views are separate ownership areas.

## Queries

- `codegraph status`: index available and up to date.
- `codegraph explore SetupRouter registerRoutes`: route and middleware source/call paths.
- `codegraph impact SetupRouter`: ProvideRouter/http.go dependency radius.
- `codegraph impact initializeApplication`: wire.go/main.go initialization radius.
- `codegraph callers/callees registerRoutes`: SetupRouter caller and route dependencies.
- `codegraph affected`: no source tests affected by Wiki-only files.
- CLI has no `context` or `trace` subcommand; this is recorded as fallback.

## Delivery Shape

single-change: all pages share one architecture/navigation fact set and one review/archive boundary.

## Unknowns

Deep schema details, provider-specific product semantics and production runbooks remain owned by source/deploy docs and should become separate future changes when needed.

