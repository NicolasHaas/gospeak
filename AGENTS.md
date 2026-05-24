# GoSpeak — Agent Guide

## Build & Verify
- **Build**: `go build -tags nolibopusfile ./...`
- **Test**: `go test -tags nolibopusfile -count=1 ./...`
- **Lint**: `golangci-lint run ./...` (config in `.golangci.yml`)
- **Format**: `gofmt -w .` before committing
- Always run all three (format, lint, test) after any change.

## Pull Requests
- The PR description must include the **prompt** (context of what changed and why).
- A **human must verify** the changes before opening the PR.

## Module
- `github.com/NicolasHaas/gospeak`

## Architecture (Onion)
Dependencies flow inward — inner packages never import outer ones.

- `pkg/crypto/` — no deps on other gospeak packages
- `pkg/model/` — pure data structs + validation
- `pkg/rbac/` — permission checks (depends on `model`)
- `pkg/protocol/` — wire format, encoding/decoding (depends on `model`, `protocol/pb`)
- `pkg/datastore/` — SQLite persistence, `DataProviderFactory` interface (depends on `model`)
- `pkg/audio/`, `pkg/screenshare/` — capture, encode, platform backends
- `pkg/server/` — listeners, sessions, control/voice/screen handlers (depends on `crypto`, `model`, `protocol`, `datastore`, `rbac`)
- `pkg/client/` — client engine, GUI interop (depends on `audio`, `screenshare`, `crypto`, `protocol`)
- `cmd/`, `ui/` — entry points that wire dependencies together

### Network Planes
- **Control** (TCP 9600) — TLS 1.3 signalling, JSON messages framed with 4-byte length prefix
- **Voice** (UDP 9601) — AES-128-GCM encrypted Opus audio relay
- **Screen** (TCP 9603) — AES-128-GCM encrypted screen-share relay
- **Metrics** (TCP 9602) — Prometheus `/metrics` HTTP endpoint

## Testing
- **Package tests**: `go test -tags nolibopusfile -count=1 ./...`
- **Server tests**: use `datastore.NewProviderFactory(cfg.DBPath)` + `server.Dependencies{Store: st}` + `nopConn` (satisfies `net.Conn`) in `newTestServer(t)` helper
- **Model tests**: direct struct construction in `pkg/model/` (no DB needed)
- **RBAC tests**: unit tests over `HasPermission`/`RequirePermission` in `pkg/rbac/`
- No in-memory store — tests write to `gospeak.db`

### Model Validation Pattern
- Factory constructors (`NewChannel()`) return structs with defaults
- `(*Channel).Validate() error` returns first validation failure or nil
- Sentinel errors (`ErrChannelNameEmpty`) for each constraint
- Table-driven tests over `{name, value, wantErr}` cases
- Constants for limits (`MaxChannelNameLength`)

## Conventions
- **No panics** in library/pkg code — return errors. Panics only in `main()` or truly unrecoverable OS failures.
- **No unused code** — remove dead code, don't comment it out.
- **No TODO/FIXME/HACK** — open a GitHub issue instead.
- **`//nolint`** must name a specific linter and include a brief justification.
- **Errors**: log server-side with `slog.Error`/`slog.Warn`; send generic messages to clients (don't leak internals).
- **Imports**: standard lib → third-party → internal, grouped by blank line.
- **Commit messages**: prefix with `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `ci:`, `chore:`.
- **All commits must be GPG-signed.**

## Docs & Observability
- Keep metrics (`pkg/server/metrics.go`), logging, `docs/grafana-dashboard.json`, and all `.md` docs up to date when changing behaviour.

## Security (non-negotiable)
- **No `math/rand`** — always `crypto/rand`.
- **SQL** — parameterized queries only (`?` placeholders), no string concatenation.
- **User input** — validate with model validators, sanitize control characters.
- **No secrets in code** — use env vars or config files in `.gitignore`.
