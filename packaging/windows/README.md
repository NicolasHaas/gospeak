# Windows Packaging Assets

This directory contains the local installer scaffolding for the GoSpeak client.

The installer defaults to a per-user installation under `%LOCALAPPDATA%\Programs\GoSpeak`.

Still pending:
- Validate the installer locally on a Windows runner and confirm Add/Remove Programs metadata

Local build on Windows:

```powershell
./packaging/windows/build-installer.ps1 -Version v0.2.0
```

If Inno Setup is not installed yet:

```powershell
./packaging/windows/build-installer.ps1 -Version v0.2.0 -InstallInnoSetup
```

If `ISCC.exe` is installed in a custom location:

```powershell
./packaging/windows/build-installer.ps1 -Version v0.2.0 -IsccPath 'C:\Path\To\ISCC.exe'
```

Expected output:
- `bin/gospeak-client-installer-win.exe`