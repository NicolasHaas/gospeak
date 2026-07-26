# Deployment

This folder contains a Rocky Linux 10 cloud-init example for common cloud providers.

Files
- `deploy/cloud-init.yaml`: cloud-init user-data for Rocky Linux 10

## Quickstart (Rocky 10)

1. Edit `deploy/cloud-init.yaml`:
   - Replace the SSH public key placeholder.
   - Adjust the open mode, schedules, and ports to your needs.
2. In your cloud provider console, create a server with a Rocky Linux 10 image and paste the contents of `deploy/cloud-init.yaml` into the user-data field.
3. At the cloud provider firewall, only allow inbound `9600/tcp`, `9601/udp`, and `9603/tcp` from the internet. Allow `22/tcp` only from your public IP for admin access.
4. Boot the server and check the service:

```bash
systemctl status gospeak
```

## Hands-off secure install

This configuration is designed to be hands-off after first boot while staying secure.

- SSH key-only access, root login disabled.
- Firewall allows only SSH, the control port, the voice port, and the screen-share port.
- OS updates are applied automatically via `dnf-automatic` with a reboot when needed.
- The GoSpeak container auto-updates via Podman auto-update and a systemd timer.

## Open server mode

The provided cloud-init starts GoSpeak with `-open` and `-screen-share`, which allows anyone to join without a token and enables per-channel screen sharing.
To require tokens, remove `-open` from `deploy/cloud-init.yaml` and then use the admin token
printed on first start to generate user tokens.

## Bandwidth example (budget VPS)

As a rough example, a small VPS with 20 TB/month bandwidth (e.g., a ~EUR 3.5/month Hetzner CX11)
can serve many users.

Very rough math (assume ~64 kbps per audio stream including overhead):

- Streams (Mbps) = listeners * concurrent speakers * 0.064
- Example: 20 listeners and 2 speakers => 20 * 2 * 0.064 ~= 2.56 Mbps

12h/day usage estimates (simple model, not a guarantee):

- 10 active users, 1-2 speakers: ~0.6-1.3 Mbps => ~1.9-4.2 TB/month
- 50 active users, 3-5 speakers: ~3.0-5.0 Mbps => ~9.7-16.2 TB/month
- 100 active users, 5-10 speakers: ~6.0-10.0 Mbps => ~19.4-32.4 TB/month

You should be able to handle around 100 active users at ~12h/day in common cases.

## Server options

These flags map directly to `gospeak-server` and are all supported options.

| Flag | Default | Description |
|------|---------|-------------|
| `-control` | `:9600` | TCP/TLS control plane bind address |
| `-voice` | `:9601` | UDP voice plane bind address |
| `-db` | `gospeak.db` | SQLite database file path |
| `-cert` | *(empty)* | Custom TLS certificate, including self-signed; must be used with `-key` |
| `-key` | *(empty)* | Matching TLS private key; must be used with `-cert`. When both flags are empty, a self-signed pair is loaded or generated in `-data` |
| `-data` | `.` | Data directory for generated files (certs, DB, etc.) |
| `-open` | `false` | Allow users to join without a token |
| `-screen` | `:9603` | TCP/TLS screen-share relay bind address |
| `-screen-share` | `false` | Enable per-channel screen sharing |
| `-channels-file` | *(empty)* | YAML file defining channels created on startup |
| `-metrics` | `:9602` | Prometheus /metrics bind address (empty to disable) |
| `-export-users` | `false` | Export all users as YAML and exit |
| `-export-channels` | `false` | Export all channels as YAML and exit |
| `-log-level` | `info` | Log level (`debug`, `info`, `warn`, `error`) |
| `-log-format` | `text` | Log format: `text` or `json` |

## Ports

- `9600/tcp`: TLS control plane
- `9601/udp`: encrypted voice
- `9603/tcp`: encrypted screen-share relay (required when `-screen-share` is enabled)
- `9602/tcp`: metrics (optional; not exposed by default)
