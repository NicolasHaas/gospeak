# GoSpeak

A privacy-focused voice communication server and client, inspired by TeamSpeak. The application is written primarily in Go; the desktop client integrates native PortAudio and Opus libraries through CGO.

GoSpeak uses a selective forwarding architecture: the control plane handles signalling, the voice plane relays encrypted audio packets, and the screen plane relays encrypted screen-share frames.

## Features

- **Real-time voice chat**: Opus codec at 48 kHz, 20ms frames via PortAudio
- **Encrypted voice**: AES-128-GCM, AES-256-GCM, or ChaCha20-Poly1305 with authenticated headers; server relays without decoding (see [Security](docs/security.md) for key model caveats)
- **Authenticated TLS 1.3 control plane**: system PKI for public certificates and explicit TOFU fingerprint pinning for self-signed servers
- **Channel system**: hierarchical channels with sub-channels, temporary channels, max-user limits
- **Role-based access control**: Admin, Moderator, User roles with granular permissions
- **Token-based authentication**: 256-bit random tokens, SHA-256 hashed storage
- **Text chat**: ephemeral per-channel messaging for connected channel members
- **Basic screen sharing**: opt-in per-channel viewing with a dedicated encrypted relay plane
- **Desktop GUI**: native cross-platform UI built with [Fyne](https://fyne.io/)
- **Server bookmarks**: save and manage server connections
- **YAML configuration**: server channels, client settings, bookmarks
- **Admin tools**: create/delete channels, manage tokens, kick/ban, import/export config
- **Global hotkeys**: configurable push-to-mute/deafen (Windows; F11/F12 default)
- **Voice Activity Detection**: energy-based VAD with configurable threshold
- **Containerized builds**: multi-stage Podman/Docker builds for Linux and Windows

> **Note:** The server generates voice and screen-share keys for its selected media cipher and distributes them over TLS. It relays media without decoding it, but a compromised server could decrypt it. See the [security threat model](docs/security.md).

## Quick Start

### Run the Server (Container)

```bash
docker compose up --build
```

The server listens on:
- **TCP :9600**: TLS control plane
- **UDP :9601**: Encrypted voice
- **Screen sharing**: disabled by default; `-screen-share` enables the encrypted TCP relay on `:9603`
- **Metrics**: disabled by default; enable with `-metrics :9602` and keep the plaintext endpoint on a trusted network

Use `-media-cipher aes256` or `-media-cipher chacha20` to change the default `aes128` suite for both voice and screen sharing. Clients and servers must both support the explicit TLS-authenticated choice; older versions cannot connect to this protocol revision.

The bundled monitoring overlay enables metrics only on the Compose network and does not publish port 9602 on the host:

```bash
GOSPEAK_METRICS_ADDR=:9602 \
GRAFANA_ADMIN_PASSWORD='<choose-password>' \
docker compose -f compose.yaml -f compose.monitoring.yaml up -d
```

Grafana is then available only on `127.0.0.1:3000`; Prometheus is not
published on the host. See [Monitoring and Capacity](docs/monitoring.md) for
metric semantics, dashboard thresholds, authenticated-session limits,
control-message budget costs, rejection labels, and throttled-log behavior.

On first run, GoSpeak writes an admin bootstrap credential to
`bootstrap-admin.token` in the data directory. For this bind-mounted Compose
setup, read it on the host with `cat ./data/bootstrap-admin.token`.
Use it for the first admin login and
save the personal token returned by the server. Interrupted first logins can be
retried for the same administrator. The server removes the bootstrap file once
that personal token is used; the credential is never written to normal logs.

### Run the Server (Binary)

```bash
# Extract binaries from container build
docker compose --profile build run builder

# Run server
./bin/gospeak-server -open -screen-share   # -open allows token-less connections
```

### Run the Client

Download the appropriate binary for your platform from [Releases](https://github.com/NicolasHaas/gospeak/releases), or build from source:

```bash
./bin/gospeak-client-win.exe   # Windows
./bin/gospeak-client-lin       # Linux
```

Enter the server address, your username, and (optionally) an invite token to connect. On first login the server issues a personal token; keep it to reconnect with the same username.

For a self-signed server, the client displays its SHA-256 public-key fingerprint before sending credentials. Verify that fingerprint with the server operator through a trusted channel, then choose **Trust and Connect**. The saved pin is checked for both control and screen connections. A later identity change is a hard connection failure and requires an explicit **Re-trust and Connect** confirmation after the new fingerprint has been verified. Publicly trusted certificates are validated with the operating system's CA store and hostname checks without a TOFU prompt.

Existing bookmark files remain compatible. They gain a `trusted_server_pins` section after the first self-signed server is trusted; no pin is silently created during migration.

## Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│                    GoSpeak Server                                    │
│                                                                      │
│  ┌──────────────┐  ┌───────────────┐  ┌──────────────┐  ┌──────────┐ │
│  │ Control Plane │  │  Voice SFU   │  │Screen Relay* │  │  SQLite  │ │
│  │ TCP/TLS 1.3  │  │  UDP Relay    │  │  TCP/TLS     │  │  Store   │ │
│  │   :9600      │  │   :9601       │  │   :9603      │  │          │ │
│  └──────────────┘  └───────────────┘  └──────────────┘  └──────────┘ │
└──────────────────────────────────────────────────────────────────────┘
  │                    │                 │
   JSON/TLS            Selected AEAD      Selected AEAD
  │                    │                 │
┌─────────────────────────────────────────────────────┐
│                   GoSpeak Client                    │
│                                                     │
│  ┌──────────┐  ┌──────────┐  ┌────────┐  ┌────────┐ │
│  │ Fyne GUI │  │  Engine  │  │ Audio  │  │ Crypto │ │
│  │          │  │  Control │  │ Opus   │  │ Media  │ │
│  │          │  │  + Voice │  │ + VAD  │  │ AEAD   │ │
│  └──────────┘  └──────────┘  └────────┘  └────────┘ │
└─────────────────────────────────────────────────────┘
```

`*` The screen relay is disabled by default and exists only when the server is
started with `-screen-share`.

## Documentation

| Document | Description |
|----------|-------------|
| [Architecture](docs/architecture.md) | Package structure, data models, server/client lifecycle |
| [Protocol](docs/protocol.md) | Control plane messages, voice packet format, wire protocol |
| [Security](docs/security.md) | Encryption details, key distribution, RBAC, threat model |
| [Channel configuration](docs/channels.md) | Validated YAML format, nesting, import behavior, and limits |
| [Monitoring and Capacity](docs/monitoring.md) | Prometheus/Grafana setup, metric semantics, limits, and control budgets |
| [Audio Pipeline](docs/audio.md) | Capture/playback, Opus codec, VAD, jitter buffer |
| [Building](docs/building.md) | Container builds, local dev setup, build targets |
| [Deployment](deploy/README.md) | Rocky Linux 10 cloud-init example |

## Server Configuration

| Flag | Default | Description |
|------|---------|-------------|
| `-control` | `:9600` | TCP/TLS bind address |
| `-voice` | `:9601` | UDP voice bind address |
| `-screen` | `:9603` | TCP/TLS screen-share relay bind address |
| `-db` | `gospeak.db` | SQLite database path |
| `-data` | `.` | Data directory for generated TLS files and the first-run `bootstrap-admin.token` |
| `-open` | `false` | Allow connections without a token |
| `-screen-share` | `false` | Enable per-channel screen sharing |
| `-channels-file` | | YAML file for initial channel setup |
| `-cert` / `-key` | *(empty)* | Custom matching TLS pair, including self-signed certificates; provide both. When both are empty, GoSpeak loads or creates `server.crt` and `server.key` in `-data`. On the first new TLS connection within 30 days of expiry, it renews the automatic certificate without changing the private key or TOFU identity |
| `-metrics` | *(empty)* | Prometheus `/metrics` and `/healthz` HTTP bind address; opt in with a trusted bind such as `127.0.0.1:9602` |
| `-max-sessions` | `1024` | Maximum concurrent authenticated sessions |
| `-max-sessions-per-user` | `8` | Maximum concurrent sessions for one account |
| `-control-message-burst` | `60` | Per-session and aggregate per-account control-message cost burst |
| `-control-messages-per-second` | `20` | Per-session and aggregate per-account control-message cost replenished each second |
| `-control-global-burst` | `300` | Server-wide control-message cost burst |
| `-control-global-messages-per-second` | `100` | Server-wide control-message cost replenished each second |
| `-export-users` | `false` | Export all users as YAML and exit |
| `-export-channels` | `false` | Export all channels as YAML and exit |
| `-log-level` | `info` | Log level |
| `-log-format` | `text` | Log format: `text` or `json` |

### Channel Configuration (YAML)

```yaml
channels:
  - name: General
    description: Main voice channel
    max_users: 50
  - name: Gaming
    description: Gaming channels
    allow_sub_channels: true
    channels:
      - name: FPS
      - name: MMO
```

See [Channel configuration](docs/channels.md) for the full field reference,
strict parser limits, transactional create-only behavior, nested examples, and
upgrade guidance for duplicate sibling names.

## Tech Stack

| Component | Technology |
|-----------|-----------|
| Language | Go 1.24 |
| GUI | [Fyne](https://fyne.io/) v2 |
| Audio I/O | [PortAudio](http://www.portaudio.com/) via [gordonklaus/portaudio](https://github.com/gordonklaus/portaudio) |
| Voice Codec | [Opus](https://opus-codec.org/) via [hraban/opus](https://github.com/hraban/opus) |
| Encryption | AES-GCM (stdlib), ChaCha20-Poly1305 and Argon2id (`golang.org/x/crypto`) |
| Database | SQLite via [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) (pure Go) |
| TLS | Go stdlib `crypto/tls` (TLS 1.3) |
| Config | [gopkg.in/yaml.v3](https://pkg.go.dev/gopkg.in/yaml.v3) |
| Containers | Podman / Docker with multi-stage builds |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development guidelines.

## License

This project is licensed under the **GNU Affero General Public License v3.0 (AGPL-3.0)**.

Commercial use is permitted under AGPL-3.0. If you distribute a modified
version, the AGPL's copyleft and Corresponding Source requirements apply. If a
modified version supports remote network interaction, section 13 requires you
to offer its Corresponding Source to those users. Contact the author to discuss
an alternative license if you cannot comply with the AGPL.

See [LICENSE](LICENSE) for the full license text.

Copyright (c) 2026 Nicolas Haas
