# Flatpak Packaging Assets

This directory contains the GoSpeak client Flatpak manifest and desktop-integration metadata.

Application ID:
- `ch.haas_nicolas.gospeak`

Local build prerequisites:
- `flatpak`
- `flatpak-builder`
- `appstream-compose`
- Flathub configured as a user remote

Local build:
- `flatpak-builder --user --install-deps-from=flathub --force-clean flatpak-build packaging/flatpak/ch.haas_nicolas.gospeak.yaml`

Go module source refresh:
- Regenerate `go.mod.yml` and `modules.txt` after any `go.mod` or `go.sum` change with `flatpak-go-mod -out packaging/flatpak`.

WSL note:
- If `flatpak-builder` fails with `unlinkat: Permission denied`, rerun with `--disable-rofiles-fuse`.
- The final compose/export step requires `appstream-compose` on the host system path. `--build-only` is enough to validate dependency resolution and the client build if that host tool is not installed yet.

Known follow-up work:
- Re-run `flatpak-go-mod -out packaging/flatpak` whenever Go dependencies change so the committed module source list stays in sync.