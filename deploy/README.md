# Deployment

This folder contains a Rocky Linux 10 cloud-init example for common cloud providers.

## Files

- `deploy/cloud-init.yaml`: cloud-init user-data for Rocky Linux 10
- `deploy/compose.yaml`: standalone Compose deployment
- `deploy/prometheus.yml`: Prometheus scrape configuration
- `deploy/grafana-datasources.yml`: Grafana datasource provisioning
- `deploy/grafana-dashboards.yml`: Grafana dashboard provisioning

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
- The supplied container services drop all capabilities and use
  `no-new-privileges`.

## Open server mode

The provided cloud-init starts GoSpeak with `-open` and `-screen-share`, which allows anyone to join without a token and enables per-channel screen sharing.
To require tokens, remove `-open` from `deploy/cloud-init.yaml`. On first start,
GoSpeak writes an admin bootstrap credential to
`/var/lib/gospeak/bootstrap-admin.token`; read it over SSH with:

```bash
sudo cat /var/lib/gospeak/bootstrap-admin.token
```

Use that credential for the first admin login and save the personal token
returned by the server. Interrupted first logins can be retried for the same
administrator. GoSpeak removes the bootstrap file after the personal token is
used and logs only the file path, never the credential itself.

With `deploy/compose.yaml`, the same file is in the named data volume. Read it
without requiring a shell in the server image:

```bash
docker compose cp gospeak:/data/bootstrap-admin.token ./bootstrap-admin.token
cat ./bootstrap-admin.token
```

## Backup and restore

Stop the server before copying its files. SQLite uses WAL mode: copying only
`gospeak.db` while a writer is running can lose committed data. YAML exports
are not database backups. The users export contains IDs, names, roles and
creation times, but omits `channel_scope` and personal-token hashes. There is
no full user importer. Channel YAML omits stored IDs, temporary channels,
credentials, bans and chat history.

### Backup

1. Record the server version and effective startup command, including `-db`,
   `-data`, `-cert`, `-key`, `-channels-file` and retention settings. Relative
   paths resolve from the process working directory. `-data` does not relocate
   a separately configured `-db`. Save the service/Compose configuration,
   channel YAML and any environment/secret files needed to reproduce startup.
2. Stop the service (`systemctl stop gospeak`, or `docker compose stop gospeak`
   for the supplied Compose deployment). Wait for it to exit successfully and
   ensure no other GoSpeak process or SQLite tool is using the database. Keep
   auto-update/restart automation from starting it during the copy.
3. Copy the complete database set and data directory to private backup storage:
   the database, any remaining matching `-wal` and `-shm` sidecars,
   `server.crt`, `server.key`, and `bootstrap-admin.token` if present. Also copy
   separately configured TLS files and the configuration recorded above.
   A clean shutdown normally checkpoints/removes sidecars; never discard a
   remaining WAL just because the process stopped. Do not mix files from
   different backup times.

For a host deployment where the database and automatic files all live under
one directory, run the following as the service owner after stopping it. Set
absolute paths for your installation; the backup destination must be new and
outside the source directory. This example does not cover external config or
custom TLS paths, which must be copied separately while still stopped.

```bash
set -eu
umask 077
source=/var/lib/gospeak
backup=/srv/private-backups/gospeak-2026-11-01
mkdir -m 700 "$backup"
cp -a "$source/." "$backup/"
```

For a named container volume, stop the container first and copy the complete
volume through a trusted host-side volume backup mechanism; the server image
has no shell or SQLite CLI. Preserve ownership for the account that will run
the restored service. On Unix, protect backup directories with `0700` and
secret/database files with `0600`; check these modes and ownership rather
than relying on `umask` to tighten files copied with `cp -a`. On Windows,
retain owner-only ACLs. Restrict access to backup storage and encrypt off-host
copies. The database contains chat text, names, account scopes, token hashes
and explicit IP bans; private TLS keys and bootstrap credentials are secrets.
Do not put them in Git, logs or support reports. Server backups do not include
client bookmarks/personal tokens stored on client machines, or live sessions
and in-memory media keys. Restoring the TLS key preserves saved TOFU identity.

### Restore and verify

1. Keep the destination service and restart/update automation stopped. Retain
   the current files separately for rollback, including their own sidecars.
   Restore into a new empty directory, never over an existing database with
   stale `-wal`/`-shm` files. Copy the entire matching backup set before any
   SQLite tool or server opens it. For the host example, set a new absolute
   destination and use:

   ```bash
   set -eu
   umask 077
   restore=/var/lib/gospeak-restored
   mkdir -m 700 "$restore"
   cp -a "$backup/." "$restore/"
   ```

2. Restore external config/custom TLS files too. Check directory access,
   ownership and file permissions for the service account, especially the
   bootstrap file and private key. Repoint all affected startup paths. Use the
   backed-up server version first: opening a database can run migrations, so
   keep an untouched backup before testing an upgrade. A YAML channel file can
   create channels on startup; use the corresponding saved configuration.
3. On a disposable restored copy, run SQLite `PRAGMA integrity_check;` (expect
   `ok`) and `PRAGMA foreign_key_check;` (expect no rows). Compare saved channel,
   user/scope and message counts/content without printing secrets. Start the
   server with isolated loopback listeners and the restored paths before
   switching production. Check successful startup, then test account access,
   channel scope and history. Startup retention sweeps and empty temporary
   channel cleanup can remove rows according to the saved settings.

A pending bootstrap requires the original `bootstrap-admin.token` from the
same backup. If it is missing, malformed or insecurely permissioned, startup
refuses it; this is fail-closed recovery, not a request to generate a replacement.
A different valid-looking token cannot recover the original credential's
hash or retry-derived personal token. Recover the original file from a matching
backup, or keep the service stopped for operator-local recovery. Do not delete
bootstrap rows, reset the database or fabricate a credential to bypass refusal.
After the bootstrap administrator authenticates with its personal token, the
server retires the bootstrap file; a finalized backup need not contain it.

## Bandwidth example (budget VPS)

As a rough example, a small VPS with 20 TB/month bandwidth (e.g., a ~EUR 3.5/month Hetzner CX11)
can serve many users.

Very rough math (assume ~64 kbps per audio stream including overhead):

- Streams (Mbps) = listeners * concurrent speakers * 0.064
- Example: 20 listeners and 2 speakers => 20 * 2 * 0.064 ~= 2.56 Mbps

12h/day usage estimates (simple model, not a guarantee):

- 10 active users, 1-2 speakers: ~0.6-1.3 Mbps => ~0.1-0.2 TB/month
- 50 active users, 3-5 speakers: ~9.6-16.0 Mbps => ~1.6-2.6 TB/month
- 100 active users, 5-10 speakers: ~32.0-64.0 Mbps => ~5.2-10.4 TB/month

You should be able to handle around 100 active users at ~12h/day in common cases.

## Server options

These flags map directly to `gospeak-server` and are all supported options.

| Flag | Default | Description |
|------|---------|-------------|
| `-control` | `:9600` | TCP/TLS control plane bind address |
| `-voice` | `:9601` | UDP voice plane bind address |
| `-db` | `gospeak.db` | SQLite database file path |
| `-cert` | *(empty)* | Custom TLS certificate, including self-signed; must be used with `-key` |
| `-key` | *(empty)* | Matching TLS private key; must be used with `-cert`. When both flags are empty, a self-signed pair is loaded or generated in `-data`; the first new TLS connection within 30 days of expiry renews the certificate without rotating the key |
| `-data` | `.` | Data directory for generated TLS and bootstrap files; database location is controlled separately by `-db` |
| `-open` | `false` | Allow users to join without a token |
| `-screen` | `:9603` | TCP/TLS screen-share relay bind address |
| `-screen-share` | `false` | Enable per-channel screen sharing |
| `-channels-file` | *(empty)* | YAML file defining channels created on startup |
| `-metrics` | *(empty)* | Prometheus `/metrics` and `/healthz` bind address; disabled by default |
| `-max-sessions` | `1024` | Maximum concurrent authenticated sessions; non-positive values use the default |
| `-max-sessions-per-user` | `8` | Maximum concurrent sessions for one account; non-positive values use the default |
| `-max-sessions-per-source` | `64` | Active plus pending sessions per IPv4 address or IPv6 /64; non-positive values use the default |
| `-control-message-burst` | `60` | Per-session and aggregate per-user control-message cost burst; non-positive values use the default and values below 5 are raised to 5 |
| `-control-messages-per-second` | `20` | Per-session and aggregate per-user control-message cost replenished each second; non-positive values use the default |
| `-control-global-burst` | `300` | Server-wide control-message cost burst; non-positive values use the default and values below 5 are raised to 5 |
| `-control-global-messages-per-second` | `100` | Server-wide control-message cost replenished each second; non-positive values use the default |
| `-media-cipher` | `aes128` | Voice and screen AEAD: `aes128`, `aes256`, or `chacha20` |
| `-chat-history-limit` | `500` | Messages retained per channel (`0` disables persistence, 1..10000 retains history) |
| `-chat-max-age` | `720h` | Maximum message age (`0` disables expiry) |
| `-export-users` | `false` | Export all users as YAML and exit |
| `-export-channels` | `false` | Export all channels as YAML and exit |
| `-log-level` | `info` | Log level (`debug`, `info`, `warn`, `error`) |
| `-log-format` | `text` | Log format: `text` or `json` |

Channel files use a single `channels:` YAML document; nested channels use the same `channels:` key. The parser rejects unknown fields, aliases, merges, oversized files, and duplicate sibling names. An upgrade also stops if the existing database already has two channels with the same name under one parent; back up the database and resolve those conflicts rather than deleting an arbitrary row.

The supplied Compose and cloud-init commands inherit the source limit of 64.
For a larger shared NAT, add `-max-sessions-per-source <limit>` to the Compose
`command` list or the cloud-init Quadlet `Exec` line. This limits pending and
active sessions across accounts without changing `-open` or exact-address bans.
See [capacity semantics](../docs/monitoring.md#capacity-semantics).

## Ports

- `9600/tcp`: TLS control plane
- `9601/udp`: encrypted voice
- `9603/tcp`: encrypted screen-share relay (required when `-screen-share` is enabled)
- `9602/tcp`: optional plaintext metrics and health HTTP. It is disabled and not published by default. If enabled, bind it to a trusted interface or require explicit controls such as IP allowlisting, authenticated proxy access, and TLS.
