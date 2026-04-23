# Winget Packaging Assets

This directory contains local helpers for preparing a Winget submission for the GoSpeak Windows installer.

Example:

```powershell
./packaging/windows/build-installer.ps1 -Version v0.2.0

./packaging/winget/New-WingetManifest.ps1 \
  -Version v0.2.0 \
  -InstallerUrl https://github.com/NicolasHaas/gospeak/releases/download/v0.2.0/gospeak-client-installer-win.exe \
  -InstallerPath bin/gospeak-client-installer-win.exe
```

Notes:
- The scripts normalize `v0.2.0` to `0.2.0` so installer metadata and Winget `PackageVersion` match.
- Generated manifests default to `Scope: user` to match the installer's per-user default.
- `PackageName` and `Publisher` should continue to match Add/Remove Programs metadata from the installer.