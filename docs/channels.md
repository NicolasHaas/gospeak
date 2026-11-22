# Channel configuration

GoSpeak can create a channel hierarchy from YAML at startup or through the
administrator import action. Imports are **create-only**: they add channels that
are missing under the named parent, but do not overwrite an existing channel's
description, capacity, or sub-channel policy. This preserves live administrative
changes.

## Start the server with a channel file

Pass an explicit path with `-channels-file`:

```bash
./gospeak-server -channels-file ./channels.yaml
```

If the path cannot be read or the file is invalid, startup fails before
listeners are opened. A valid import and creation of the default Lobby channel
are committed in one datastore transaction. If any channel cannot be created,
none of that startup import is applied.

Administrators can submit the same format through the client import action while
the server is running. That import is also transactional.

## Flat example

The document must contain one top-level `channels` sequence:

```yaml channels-config
channels:
  - name: General
    description: Main voice channel
    max_users: 50
    allow_sub_channels: true
  - name: Quiet room
    max_users: 8
```

`max_users: 0`, or omitting `max_users`, means unlimited. `allow_sub_channels`
controls whether users may create temporary children; configured children can
still be declared in YAML.

## Nested example

Use `channels` again at every nested level:

```yaml channels-config
channels:
  - name: Gaming
    description: Game channels
    allow_sub_channels: true
    channels:
      - name: FPS
        max_users: 10
      - name: Strategy
        channels:
          - name: Co-op
            description: Cooperative sessions
```

## Accepted fields

| Field | Required | Meaning |
|---|---:|---|
| `name` | yes | Channel name, at most 64 Unicode characters |
| `description` | no | Description, at most 256 Unicode characters |
| `max_users` | no | `0` for unlimited, otherwise `1`–`256` |
| `allow_sub_channels` | no | Whether users may create temporary child channels |
| `channels` | no | Nested configured channels |

Names and descriptions must be valid UTF-8 and cannot contain control or Unicode
format characters. Sibling names must be unique under the same parent. The same
name may be used under different parents.

Field values are decoded by `yaml.v3` into Go string, integer, and Boolean
fields. Unknown mapping fields and unsupported document shapes are rejected, but
canonical YAML tags are not required. Use ordinary untagged scalars as in the
examples; custom tags are not part of the supported configuration contract.

## Parser and import limits

To keep configuration processing bounded and predictable, GoSpeak accepts:

- exactly one YAML document;
- at most 512 KiB of input;
- at most 256 channel declarations per document;
- at most 8 channel levels;
- only the fields listed above; and
- no YAML aliases or merge keys.

Imports reject duplicate sibling names before writing anything. The datastore
also enforces sibling uniqueness, so concurrent imports cannot create two
channels with the same `(parent_id, name)` pair.

When a channel already exists under the expected parent, GoSpeak keeps that row
unchanged and continues processing nested declarations beneath it. It never
guesses whether existing settings should be replaced. An upgraded database that
already contains duplicate sibling names is rejected at startup; back it up and
resolve those rows before retrying.

## Server-wide state budget

All permanent and temporary channels share a serialized-state budget. Each
channel costs `192 + 6 * (name characters + description characters)` bytes of
that budget, counting Unicode characters rather than UTF-8 bytes. The total must
not exceed 258048 bytes. This conservative calculation covers JSON escaping,
field names, IDs and punctuation. It reserves another 256 bytes for each of the
1024 supported sessions and 4096 bytes for the authentication envelope, keeping
full state and authentication messages within 512 KiB. This is a wire-size
bound, not a throughput guarantee.

The datastore checks the shared budget atomically before insertion. A create
that exceeds it writes nothing; an import that exceeds it rolls back in full,
including when earlier imports already consumed most of the budget. Existing
channels count even when the import does not mention them. Shorter metadata
leaves room for more channels; there is no separate fixed channel-count cap.

Startup rejects persisted channel state that exceeds the budget or contains
invalid metadata before opening listeners or cleaning up temporary channels. It
does not truncate metadata or delete channels, accounts or invites. To recover,
stop the server and back up the database, including any WAL state. Work on a
copy: restore a known-good backup, or use SQLite tooling to shorten channel
names/descriptions while preserving IDs, parent links and sibling uniqueness.
Check the budget with
`SELECT SUM(192 + 6 * (length(name) + length(description))) FROM channels;`.
Keep the original backup and inspect the changes before starting with the
repaired copy. Reimporting YAML cannot shorten existing metadata because imports
are create-only.
