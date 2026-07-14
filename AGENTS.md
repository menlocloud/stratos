# AGENTS.md

## What this repo is

Stratos — multi-tenant OpenStack cloud billing and self-service portal. Single Go binary (`cmd/api`) + two React SPAs (`web/client`, `web/admin`). PostgreSQL jsonb document store, RabbitMQ, OIDC auth, Helm deployment.

## Build & verify commands

```sh
# Backend — run from repo root
go build ./...             # Go 1.25+
gofmt -l .                 # must print nothing
go vet ./...
go test ./...              # unit tests only (no Docker)
make test-integration      # needs Docker — testcontainers spins up throwaway Postgres

# Frontend (each SPA is independent)
cd web/client && npm install && npm run lint && npm run build
cd web/admin  && npm install && npm run lint && npm run build
```

`npm run build` = `tsc -b && vite build` (type-check + production build). Linter is `oxlint`, not eslint.

## Quick local dev

```sh
docker compose up --build   # API :8080, mgmt :8081, client :8082, admin :8083
```

Auth-gated and cloud routes need external OIDC + OpenStack — put `STRATOS_OAUTH2_*` / `OS_*` in `.env`. Without them, public endpoints and the management port work fine.

## Architecture conventions

- **Layering**: handler → service → repository per domain. No DI container — explicit constructor injection in `cmd/api/main.go`.
- **Domains live in** `internal/platform/<domain>/` (29 domains). Each typically has handler, service, repo.
- **OpenStack integration** lives in `internal/cloud/` (providers, sync, metrics, notifications).
- **Reusable libs** in `pkg/` — `auth`, `httpx`, `money`, `audit`, `textcrypt`.
- **Three HTTP surfaces** on one process: `/api/v1` (customer, :8080), `/admin-api/v1` (operator, SigV4-signed, :8080), `/mcp` (AI agents, :8080). Router is `chi`.
- **PostgreSQL**: every document is `(id text primary key, doc jsonb)` — no ORM, standard `encoding/json`. See `internal/pgdoc/codec.go` for marshal/unmarshal.
- **Config**: `application.yml` (mounted at `/opt/stratos/api/application.yml` in deployment) + env overlay, env wins. Source of truth: `internal/config/config.go`.
- **Route wiring**: `server.AppRouter` (:8080) and `server.MgmtRouter` (:8081) in `internal/server/server.go`.

## Testing rules

- Unit tests go next to code (`*_test.go`), no build tag, package `X`.
- Integration tests go in `test/integration/` with `//go:build integration` tag. Use `freshPG(t)` for an isolated DB — don't start your own Postgres.
- `cloud_ceph_e2e_test.go` uses dual tag `//go:build integration && cephlive` — it will NOT run on `go test -tags=integration`.
- **Windows**: set `DOCKER_HOST=npipe:////./pipe/dockerDesktopLinuxEngine` and `TESTCONTAINERS_RYUK_DISABLED=true` for integration tests.
- Pass explicit `now time.Time` into time-dependent code — the scheduler and rating paths accept injected clocks.

## Adding a domain or endpoint

1. Create `internal/platform/<domain>/` with handler, service, repo.
2. Wire routes in `internal/server/server.go` (AppRouter) or `cmd/api/main.go` for the appropriate surface.
3. Cloud/OpenStack behavior belongs in `internal/cloud/`, not in the domain.
4. Prefer unit tests next to the code; use `test/integration/` for storage-dependent flows.

## CI & releases

- `go test ./...` and `go test -tags=integration ./test/integration/...` run on every push/PR (`.github/workflows/test.yml`).
- On push to `main`, Docker images publish to `ghcr.io` as `dev-<sha>` (`.github/workflows/docker.yml`).
- `v*` tags create GitHub Releases; `deploy/chart` changes require a `Chart.yaml` version bump.
- Commit subjects follow Conventional Commits (`feat:`, `fix:`, `chore:`, `docs:`).

## Key gotchas

- `go test ./...` does NOT run integration tests — they're behind `-tags=integration`.
- `npm run build` is the type-check; `npm run lint` is separate.
- Management port (`:8081`) is internal only — health, debug triggers, cloud probe. Never expose it.
- `STRATOS_JOBS_DEBUG_TRIGGERS=true` exposes on-demand job triggers without starting crons — use this in dev, not `STRATOS_JOBS_SCHEDULER_ENABLED`.
- Fresh DBs need seed config (`deploy/seed/`) before billing/project endpoints work.
- No `application.yml` in the repo — it's provided at deploy time. Run `go run ./cmd/api` with env vars or provide the file at `/opt/stratos/api/application.yml`.
- Binary builds are `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/stratos-api ./cmd/api`.

## Reference docs

- `docs/architecture.md` — boot sequence, module map, request flow
- `docs/data-model.md` — ERD, document tables
- `docs/auth.md` — OIDC, RBAC, SigV4
- `docs/testing.md` — test layers, adding tests
- `docs/development.md` — full local dev setup
- `CONTRIBUTING.md` — branch/PR flow, pre-PR checklist