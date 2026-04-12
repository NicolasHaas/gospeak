param(
    [Parameter(Mandatory = $true)]
    [string]$Version,

    [string]$BinaryPath = "bin/gospeak-client-win.exe",

    [string]$IconPath = "ui/gospeak-client.ico",

    [string]$OutputDir = "bin",

    [string]$IsccPath,

    [switch]$InstallInnoSetup
)

$ErrorActionPreference = 'Stop'

function Find-IsccPath {
    param([string]$PreferredPath)

    if ($PreferredPath) {
        $resolvedPreferredPath = Resolve-Path -Path $PreferredPath -ErrorAction Stop
        return $resolvedPreferredPath.Path
    }

    $command = Get-Command ISCC.exe -ErrorAction SilentlyContinue
    if ($command) {
        return $command.Source
    }

    $candidatePaths = @(
        "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe",
        "${env:ProgramFiles}\Inno Setup 6\ISCC.exe"
    ) | Where-Object { $_ -and $_.Trim() -ne '' }

    foreach ($candidatePath in $candidatePaths) {
        if (Test-Path $candidatePath) {
            return $candidatePath
        }
    }

    return $null
}

function Install-InnoSetup {
    $winget = Get-Command winget.exe -ErrorAction SilentlyContinue
    if ($winget) {
        Write-Host 'Installing Inno Setup via winget...'
        & $winget.Source install --id JRSoftware.InnoSetup -e --accept-package-agreements --accept-source-agreements
        return
    }

    $choco = Get-Command choco.exe -ErrorAction SilentlyContinue
    if ($choco) {
        Write-Host 'Installing Inno Setup via Chocolatey...'
        & $choco.Source install innosetup -y
        return
    }

    throw 'Inno Setup is not installed and no supported package manager was found. Install Inno Setup 6 manually, or rerun with -IsccPath <path-to-ISCC.exe>.'
}

function Resolve-RepoPath {
    param([string]$Path)

    $resolved = Resolve-Path -Path $Path -ErrorAction Stop
    return $resolved.Path
}

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot "../..")
Set-Location $repoRoot

$normalizedVersion = $Version
if ($normalizedVersion.StartsWith('v')) {
    $normalizedVersion = $normalizedVersion.Substring(1)
}

$binaryFullPath = Resolve-RepoPath $BinaryPath
$iconFullPath = Resolve-RepoPath $IconPath

New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null

$isccPath = Find-IsccPath -PreferredPath $IsccPath
if (-not $isccPath -and $InstallInnoSetup) {
    Install-InnoSetup
    $isccPath = Find-IsccPath
}

if (-not $isccPath) {
    throw "ISCC.exe not found. Install Inno Setup 6, rerun with -InstallInnoSetup, or pass -IsccPath <path-to-ISCC.exe>."
}

& $isccPath "/DAppVersion=$normalizedVersion" "/DAppExePath=$binaryFullPath" "/DSetupIconFile=$iconFullPath" "packaging/windows/gospeak-client.iss"

$installerPath = Join-Path (Resolve-RepoPath $OutputDir) 'gospeak-client-installer-win.exe'
if (-not (Test-Path $installerPath)) {
    throw "Expected installer output was not created: $installerPath"
}

Write-Host "Installer created:" $installerPath