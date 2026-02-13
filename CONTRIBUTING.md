# Contributing to GoSpeak

Thank you for your interest in contributing to GoSpeak! This document provides guidelines for contributing to the project.

## Getting Started

1. **Fork** the repository on GitHub
2. **Clone** your fork locally
3. **Create a branch** for your change: `git checkout -b feature/my-change`
4. **Make your changes** and test them
5. **Push** to your fork and open a **Pull Request**

## Development Setup

### Prerequisites

- Go 1.24 or later
- Podman or Docker (for container builds)
- System dependencies for local builds (see below)

### Local Development (Linux)

```bash
# Install system dependencies
sudo apt-get install -y portaudio19-dev libopus-dev pkg-config \
  libgl1-mesa-dev libx11-dev libxcursor-dev libxrandr-dev \
  libxinerama-dev libxi-dev libxxf86vm-dev

# Build
go build -tags nolibopusfile ./cmd/server/
go build -tags nolibopusfile ./cmd/client/

# Run linter
golangci-lint run
```

### Container Build

```bash
# Build all binaries
docker compose --profile build run builder
```

## Code Standards

### Go Style

- Follow standard Go conventions ([Effective Go](https://go.dev/doc/effective_go))
- Run `gofmt` and `goimports` before committing
- All code must pass `golangci-lint` (see [.golangci.yml](.golangci.yml))

### Commit Messages

Use clear, descriptive commit messages:

```
feat: add channel description editing
fix: prevent duplicate UDP packets in jitter buffer  
docs: update protocol documentation for chat messages
```

Prefix with: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `ci:`, `chore:`

### Pull Requests

- Keep PRs focused — one feature or fix per PR
- Include a clear description of what changed and why
- Ensure CI passes (lint + build + security scan)
- Update documentation if you change behavior

## Project Structure

```
cmd/
  server/          Server CLI entry point
  client/          Client entry point
pkg/
  audio/           PortAudio I/O, Opus codec, VAD
  client/          Client engine, networking, settings
  crypto/          AES-128-GCM, key generation, hashing
  model/           Core domain types
  protocol/        Wire protocol (length-prefixed JSON)
  protocol/pb/     Message type definitions
  rbac/            Role-based access control
  server/          Server core (control, voice, config)
  store/           SQLite persistence
ui/                Fyne desktop GUI
docs/              Documentation with Mermaid diagrams
```

## Areas for Contribution

- **Testing** — unit tests and integration tests
- **Documentation** — improve docs, add examples
- **Platform support** — macOS, Linux ARM builds
- **Performance** — profiling and optimization
- **Accessibility** — GUI improvements

## License

By contributing, you agree that your contributions will be licensed under the [AGPL-3.0 License](LICENSE).

## Questions?

Open a [GitHub Issue](https://github.com/NicolasHaas/gospeak/issues) for questions or discussion.
