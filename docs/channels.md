# Channel configuration

GoSpeak can create a channel hierarchy from YAML at startup or through the administrator import action. Imports are **create-only**: they add channels that are missing under the named parent, but do not overwrite an existing channel's description, capacity, or sub-channel policy. This preserves live administrative changes.

## Start the server with a channel file

Pass an explicit path with `-channels-file`:

```bash
./gospeak-server -channels-file ./channels.yaml
```

If the path cannot be read or the file is invalid, startup fails before listeners are opened. A valid import and creation of the default Lobby channel are committed in one datastore transaction. If any channel cannot be created, none of that startup import is applied.

Administrators can submit the same format through the client import action while the server is running. That import is also transactional.

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

`max_users: 0`, or omitting `max_users`, means unlimited. `allow_sub_channels` controls whether users may create temporary children; configured children can still be declared in YAML.

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

Names and descriptions must be valid UTF-8 and cannot contain control or Unicode format characters. Sibling names must be unique under the same parent. The same name may be used under different parents.

Field values are decoded by `yaml.v3` into Go string, integer, and Boolean
fields. Unknown mapping fields and unsupported document shapes are rejected,
but canonical YAML tags are not required. Use ordinary untagged scalars as in
the examples; custom tags are not part of the supported configuration contract.

## Parser and import limits

To keep configuration processing bounded and predictable, GoSpeak accepts:

- exactly one YAML document;
- at most 512 KiB of input;
- at most 256 channels in total;
- at most 8 channel levels;
- only the fields listed above; and
- no YAML aliases or merge keys.

Imports reject duplicate sibling names before writing anything. The datastore also enforces sibling uniqueness, so concurrent imports cannot create two channels with the same `(parent_id, name)` pair.

When a channel already exists under the expected parent, GoSpeak keeps that row unchanged and continues processing nested declarations beneath it. It never guesses whether existing settings should be replaced. An upgraded database that already contains duplicate sibling names is rejected at startup; back it up and resolve those rows before retrying.
