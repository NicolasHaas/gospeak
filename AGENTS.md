# GoSpeak agent guide

## Build and verify
- Before editing, fetch `origin`, check the branch and worktree, and read the code path you are changing. Keep unrelated work intact.
- Format changed Go files with `gofmt`. Do not run `gofmt -w .` across the whole repository.
- Run `go test -tags nolibopusfile -count=1 ./...`, `go vet -tags nolibopusfile ./...`, `go build -tags nolibopusfile ./...`, and `golangci-lint run ./...` (config in `.golangci.yml`). Use race tests when the host supports them; report checks that could not run.
- Client and UI checks need native audio and graphics libraries. Do not treat a missing local dependency as a passing test.

## Pull Requests
- The PR description must include the **prompt** (context of what changed and why).
- A **human must verify** the changes before opening the PR.

## Module
- `github.com/NicolasHaas/gospeak`

## Architecture (Onion)
Dependencies flow inward. Inner packages never import outer ones.

- `pkg/crypto/`: no deps on other GoSpeak packages
- `pkg/model/`: pure data structs and validation
- `pkg/rbac/`: permission checks (depends on `model`)
- `pkg/protocol/`: wire format, encoding/decoding (depends on `protocol/pb`)
- `pkg/datastore/`: SQLite persistence, `DataProviderFactory` interface (depends on `model`)
- `pkg/audio/`, `pkg/screenshare/`: capture, encode, platform backends
- `pkg/server/`: listeners, sessions, control/voice/screen handlers (depends on `crypto`, `model`, `protocol`, `datastore`, `rbac`)
- `pkg/client/`: client engine, GUI interop (depends on `audio`, `screenshare`, `crypto`, `protocol`)
- `cmd/`, `ui/`: entry points that wire dependencies together

### Network Planes
- **Control** (TCP 9600): TLS 1.3 signalling, JSON messages framed with 4-byte length prefix
- **Voice** (UDP 9601): Opus audio encrypted with the server-selected media cipher (`aes128`, `aes256`, or `chacha20`; `aes128` by default)
- **Screen** (TCP 9603 by default): optional screen-share relay using the same server-selected cipher with a separate share key
- **Metrics** (operator-selected TCP address): optional Prometheus `/metrics` and `/healthz` HTTP endpoint, disabled by default

## Protocol and media
- Control messages have three hand-maintained contracts: `proto/control.proto`, `pkg/protocol/pb/messages.go`, and the JSON field allowlist in `pkg/protocol/protocol.go`. Keep them in sync and run the protocol parity tests when changing a message.
- The server is an SFU. It authenticates voice packets before updating replay state, then forwards the original ciphertext without decoding media. Voice authenticates the complete plaintext header as AEAD additional data.
- The server selects one media cipher for voice and screen sharing. Reject missing or mismatched choices rather than falling back. Preserve nonce uniqueness and replay checks across all supported ciphers.
- Voice uses direct UDP without TURN or STUN. Restrictive NAT may prevent voice; do not add a relay as a compatibility workaround.

## Conventions
- **No panics** in library/pkg code: return errors. Panics only in `main()` or truly unrecoverable OS failures.
- **No unused code**: remove dead code, don't comment it out.
- **No TODO/FIXME/HACK**: open a GitHub issue instead.
- **`//nolint`** must name a specific linter and include a brief justification.
- **Errors**: log server-side with `slog.Error`/`slog.Warn`; send generic messages to clients (don't leak internals).
- **Imports**: standard lib → third-party → internal, grouped by blank line.
- **Commit messages**: use a scoped Conventional Commit, such as `feat(media): add cipher selection`.
- **All commits must be GPG-signed.**

## Docs and observability
- Update affected docs and observability surfaces when behavior changes. Do not rewrite unrelated documentation.

## Security (non-negotiable)
- **No `math/rand`**: always `crypto/rand`.
- **SQL**: parameterized queries only (`?` placeholders), no string concatenation.
- **User input**: validate with model validators, sanitize control characters.
- **No secrets in code or logs**: use env vars or ignored config files. Do not log tokens, media plaintext, or peer addresses in ordinary server logs.
