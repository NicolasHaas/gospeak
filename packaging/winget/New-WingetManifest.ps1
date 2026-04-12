param(
    [Parameter(Mandatory = $true)]
    [string]$Version,

    [Parameter(Mandatory = $true)]
    [string]$InstallerUrl,

    [string]$InstallerPath,

    [string]$Sha256,

    [string]$PackageIdentifier = 'NicolasHaas.GoSpeak',

    [string]$Publisher = 'Nicolas Haas',

    [string]$PackageName = 'GoSpeak',

    [string]$Moniker = 'gospeak',

    [string]$ShortDescription = 'Privacy-focused voice communication client',

    [string]$License = 'AGPL-3.0-only',

    [string]$LicenseUrl = 'https://github.com/NicolasHaas/gospeak/blob/main/LICENSE',

    [string]$PublisherUrl = 'https://haas-nicolas.ch',

    [string]$PublisherSupportUrl = 'https://github.com/NicolasHaas/gospeak/issues',

    [string]$PackageUrl = 'https://github.com/NicolasHaas/gospeak',

    [ValidateSet('user', 'machine')]
    [string]$Scope = 'user',

    [string]$ManifestVersion = '1.12.0',

    [string]$OutputRoot = 'packaging/winget/manifests'
)

$ErrorActionPreference = 'Stop'

function Get-NormalizedVersion {
    param([string]$Value)

    if ($Value.StartsWith('v')) {
        return $Value.Substring(1)
    }

    return $Value
}

function ConvertTo-WingetPartition {
    param([string]$Identifier)

    $publisherFolder = $Identifier.Split('.')[0]
    return $publisherFolder.Substring(0, 1).ToLower()
}

$normalizedVersion = Get-NormalizedVersion $Version

if (-not $Sha256) {
    if (-not $InstallerPath) {
        throw 'Provide either -Sha256 or -InstallerPath.'
    }

    $installerHash = Get-FileHash -Path $InstallerPath -Algorithm SHA256
    $Sha256 = $installerHash.Hash.ToUpperInvariant()
}

$partition = ConvertTo-WingetPartition $PackageIdentifier
$manifestDir = Join-Path $OutputRoot (Join-Path $partition ($PackageIdentifier -replace '\.', '/'))
$manifestDir = Join-Path $manifestDir $normalizedVersion
New-Item -ItemType Directory -Force -Path $manifestDir | Out-Null

$defaultLocalePath = Join-Path $manifestDir "$PackageIdentifier.locale.en-US.yaml"
$installerManifestPath = Join-Path $manifestDir "$PackageIdentifier.installer.yaml"
$versionManifestPath = Join-Path $manifestDir "$PackageIdentifier.yaml"

$defaultLocale = @"
# yaml-language-server: `$schema=https://aka.ms/winget-manifest.defaultLocale.$ManifestVersion.schema.json

PackageIdentifier: $PackageIdentifier
PackageVersion: $normalizedVersion
PackageLocale: en-US
Publisher: $Publisher
PublisherUrl: $PublisherUrl
PublisherSupportUrl: $PublisherSupportUrl
PackageName: $PackageName
PackageUrl: $PackageUrl
License: $License
LicenseUrl: $LicenseUrl
ShortDescription: $ShortDescription
Moniker: $Moniker
ManifestType: defaultLocale
ManifestVersion: $ManifestVersion
"@

$installerManifest = @"
# yaml-language-server: `$schema=https://aka.ms/winget-manifest.installer.$ManifestVersion.schema.json

PackageIdentifier: $PackageIdentifier
PackageVersion: $normalizedVersion
InstallerType: inno
Scope: $Scope
InstallModes:
  - interactive
  - silent
  - silentWithProgress
InstallerSwitches:
    Silent: /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP-
    SilentWithProgress: /SILENT /SUPPRESSMSGBOXES /NORESTART /SP-
    Interactive: /SP-
Installers:
  - Architecture: x64
    InstallerUrl: $InstallerUrl
    InstallerSha256: $Sha256
ManifestType: installer
ManifestVersion: $ManifestVersion
"@

$versionManifest = @"
# yaml-language-server: `$schema=https://aka.ms/winget-manifest.version.$ManifestVersion.schema.json

PackageIdentifier: $PackageIdentifier
PackageVersion: $normalizedVersion
DefaultLocale: en-US
ManifestType: version
ManifestVersion: $ManifestVersion
"@

Set-Content -Path $defaultLocalePath -Value $defaultLocale
Set-Content -Path $installerManifestPath -Value $installerManifest
Set-Content -Path $versionManifestPath -Value $versionManifest

Write-Host "Winget manifests written to:" $manifestDir
Write-Host "PackageIdentifier:" $PackageIdentifier
Write-Host "PackageVersion:" $normalizedVersion