param(
  [string]$Version = $env:JUEX_INSTALL_VERSION,
  [string]$Prefix = $env:PREFIX,
  [string]$BinDir = $env:JUEX_INSTALL_BIN_DIR,
  [switch]$DryRun
)

$ErrorActionPreference = "Stop"

function Die {
  param([string]$Message)
  Write-Error "error: $Message"
  exit 1
}

function Get-RepoRoot {
  if ($PSScriptRoot) {
    if ((Split-Path -Leaf $PSScriptRoot) -eq "scripts") {
      $parent = Split-Path -Parent $PSScriptRoot
      if (Test-Path -LiteralPath (Join-Path $parent "CLI_CONFIG")) {
        return $parent
      }
    }
    return $PSScriptRoot
  }
  return (Get-Location).Path
}

function Read-CliConfigVersion {
  $config = $env:JUEX_INSTALL_CLI_CONFIG
  if (-not $config) {
    $config = Join-Path (Get-RepoRoot) "CLI_CONFIG"
  }
  if (Test-Path -LiteralPath $config) {
    foreach ($line in Get-Content -LiteralPath $config) {
      if ($line -match '^VERSION=(.+)$') {
        return $Matches[1].TrimEnd("`r")
      }
    }
  }
  return $null
}

function Get-ReleaseTag {
  param([string]$InputVersion)
  return "v$($InputVersion.TrimStart('v'))"
}

function Get-AssetVersion {
  param([string]$InputVersion)
  return $InputVersion.TrimStart('v')
}

function Resolve-LatestVersion {
  if ($env:JUEX_INSTALL_LATEST_VERSION) {
    return $env:JUEX_INSTALL_LATEST_VERSION
  }

  $repoUrl = $env:JUEX_INSTALL_REPO_URL
  if (-not $repoUrl) {
    $repo = $env:JUEX_INSTALL_REPO
    if (-not $repo) {
      $repo = "juex-ai/juex"
    }
    $repoUrl = "https://github.com/$repo"
  }

  try {
    $response = Invoke-WebRequest -Uri "$($repoUrl.TrimEnd('/'))/releases/latest" -MaximumRedirection 5 -UseBasicParsing
  } catch {
    Die "failed to resolve latest release from $repoUrl"
  }

  $effectiveUrl = $null
  try {
    if ($response.BaseResponse -and $response.BaseResponse.ResponseUri) {
      $effectiveUrl = $response.BaseResponse.ResponseUri.AbsoluteUri
    }
  } catch {}
  if (-not $effectiveUrl) {
    try {
      if ($response.BaseResponse -and $response.BaseResponse.RequestMessage -and $response.BaseResponse.RequestMessage.RequestUri) {
        $effectiveUrl = $response.BaseResponse.RequestMessage.RequestUri.AbsoluteUri
      }
    } catch {}
  }
  if (-not $effectiveUrl -or $effectiveUrl -notmatch '/tag/') {
    Die "could not resolve latest release from $repoUrl"
  }
  return ($effectiveUrl -split '/')[-1]
}

function Resolve-Version {
  param([string]$RequestedVersion)
  if ($RequestedVersion) {
    if ($RequestedVersion -eq "latest") {
      return Resolve-LatestVersion
    }
    return $RequestedVersion
  }

  $configured = Read-CliConfigVersion
  if ($configured) {
    return $configured
  }
  return Resolve-LatestVersion
}

function Resolve-OS {
  if ($env:JUEX_INSTALL_OS) {
    switch ($env:JUEX_INSTALL_OS.ToLowerInvariant()) {
      "windows" { return "windows" }
      default { Die "unsupported operating system: $env:JUEX_INSTALL_OS" }
    }
  }
  if ([System.Environment]::OSVersion.Platform -eq [System.PlatformID]::Win32NT) {
    return "windows"
  }
  Die "install.ps1 supports Windows installs only"
}

function Resolve-Arch {
  $raw = $env:JUEX_INSTALL_ARCH
  if (-not $raw) {
    $raw = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
  }
  switch ($raw.ToLowerInvariant()) {
    { $_ -in @("x64", "amd64") } { return "amd64" }
    { $_ -in @("arm64", "aarch64") } { return "arm64" }
    default { Die "unsupported architecture: $raw" }
  }
}

function Get-ArchiveName {
  param(
    [string]$AssetVersion,
    [string]$OSName,
    [string]$Arch
  )
  if ($OSName -eq "windows") {
    return "juex_${AssetVersion}_${OSName}_${Arch}.zip"
  }
  return "juex_${AssetVersion}_${OSName}_${Arch}.tar.gz"
}

function Get-ReleaseAssetUrl {
  param(
    [string]$Tag,
    [string]$Asset
  )
  if ($env:JUEX_INSTALL_RELEASE_BASE_URL) {
    return "$($env:JUEX_INSTALL_RELEASE_BASE_URL.TrimEnd('/'))/$Asset"
  }

  $repoUrl = $env:JUEX_INSTALL_REPO_URL
  if (-not $repoUrl) {
    $repo = $env:JUEX_INSTALL_REPO
    if (-not $repo) {
      $repo = "juex-ai/juex"
    }
    $repoUrl = "https://github.com/$repo"
  }
  return "$($repoUrl.TrimEnd('/'))/releases/download/$Tag/$Asset"
}

function Copy-Download {
  param(
    [string]$Url,
    [string]$OutFile
  )
  if ($Url -match '^https?://') {
    Invoke-WebRequest -Uri $Url -OutFile $OutFile -UseBasicParsing
    return
  }
  if ($Url -match '^file://') {
    $localPath = ([System.Uri]$Url).LocalPath
    Copy-Item -LiteralPath $localPath -Destination $OutFile -Force
    return
  }
  Copy-Item -LiteralPath $Url -Destination $OutFile -Force
}

function Verify-Checksum {
  param(
    [string]$Archive,
    [string]$Checksums
  )
  $archiveBase = Split-Path -Leaf $Archive
  $expected = $null
  foreach ($line in Get-Content -LiteralPath $Checksums) {
    $parts = $line.Trim() -split '\s+'
    if ($parts.Count -ge 2 -and ($parts[1] -eq $archiveBase -or $parts[1] -eq "*$archiveBase")) {
      $expected = $parts[0]
      break
    }
  }
  if (-not $expected) {
    Die "checksum entry not found for $archiveBase"
  }

  $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $Archive).Hash.ToLowerInvariant()
  if ($actual -ne $expected.ToLowerInvariant()) {
    Die "checksum mismatch for ${archiveBase}: expected $expected, got $actual"
  }
  Write-Host "checksum ok: $archiveBase"
}

function Expand-JuexBinary {
  param(
    [string]$Archive,
    [string]$OutDir,
    [string]$BinaryName
  )
  New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
  Expand-Archive -LiteralPath $Archive -DestinationPath $OutDir -Force
  $binary = Get-ChildItem -LiteralPath $OutDir -Recurse -File -Filter $BinaryName | Select-Object -First 1
  if (-not $binary) {
    Die "binary $BinaryName not found in archive"
  }
  return $binary.FullName
}

function Install-Binary {
  param(
    [string]$Source,
    [string]$Target
  )
  $targetDir = Split-Path -Parent $Target
  New-Item -ItemType Directory -Force -Path $targetDir | Out-Null
  Remove-Item -LiteralPath $Target -Force -ErrorAction SilentlyContinue
  Copy-Item -LiteralPath $Source -Destination $Target -Force
}

function Install-ManagedPackage {
  param(
    [string]$SourceRoot,
    [string]$ManagedHome,
    [string]$ReleaseKey,
    [string]$InstallTarget
  )

  $required = @(
    "juex-package.json",
    "bin/juex.exe",
    "juex-path/rg.exe",
    "juex-resources/licenses/ripgrep/LICENSE-MIT",
    "juex-resources/licenses/ripgrep/UNLICENSE"
  )
  foreach ($relative in $required) {
    if (-not (Test-Path -LiteralPath (Join-Path $SourceRoot $relative) -PathType Leaf)) {
      Die "managed release is missing $relative"
    }
  }

  $releasesDir = Join-Path $ManagedHome "releases"
  $generation = [Guid]::NewGuid().ToString("N")
  $releaseName = "$ReleaseKey-$generation"
  $releaseDir = Join-Path $releasesDir $releaseName
  $stage = Join-Path $releasesDir ".$releaseName.tmp"
  New-Item -ItemType Directory -Force -Path $releasesDir | Out-Null
  Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue
  New-Item -ItemType Directory -Force -Path $stage | Out-Null
  Copy-Item -Path (Join-Path $SourceRoot "*") -Destination $stage -Recurse -Force
  Move-Item -LiteralPath $stage -Destination $releaseDir
  Install-Binary -Source (Join-Path $releaseDir "bin/juex.exe") -Target $InstallTarget
  Set-Content -LiteralPath (Join-Path $ManagedHome "current.txt") -Value $releaseName -NoNewline
}

$resolvedVersion = Resolve-Version -RequestedVersion $Version
$assetVersion = Get-AssetVersion -InputVersion $resolvedVersion
$tag = Get-ReleaseTag -InputVersion $resolvedVersion
$osName = Resolve-OS
$arch = Resolve-Arch
$archive = Get-ArchiveName -AssetVersion $assetVersion -OSName $osName -Arch $arch
$assetUrl = Get-ReleaseAssetUrl -Tag $tag -Asset $archive
$checksumsUrl = Get-ReleaseAssetUrl -Tag $tag -Asset "checksums.txt"

if (-not $Prefix) {
  $homeDir = $env:USERPROFILE
  if (-not $homeDir) {
    $homeDir = $HOME
  }
  $Prefix = Join-Path $homeDir ".local"
}
if (-not $BinDir) {
  $BinDir = Join-Path $Prefix "bin"
}
$BinDir = [System.IO.Path]::GetFullPath($BinDir)
$PackageHome = Join-Path (Split-Path -Parent $BinDir) "lib/juex"
$binaryName = "juex.exe"
$installTarget = Join-Path $BinDir $binaryName
$releaseKey = "$assetVersion-$osName-$arch"

Write-Host "JueX release install plan"
Write-Host "version: $assetVersion"
Write-Host "release tag: $tag"
Write-Host "platform: $osName/$arch"
Write-Host "archive: $archive"
Write-Host "asset url: $assetUrl"
Write-Host "checksum url: $checksumsUrl"
Write-Host "install target: $installTarget"
Write-Host "package home: $PackageHome"
Write-Host "uninstall: Remove-Item -Force $installTarget; Remove-Item -Recurse -Force $PackageHome"

if ($DryRun) {
  exit 0
}

$tmp = $null
try {
  $tmp = New-Item -ItemType Directory -Path (Join-Path ([System.IO.Path]::GetTempPath()) ([System.Guid]::NewGuid().ToString()))
  $archivePath = Join-Path $tmp.FullName $archive
  $checksumsPath = Join-Path $tmp.FullName "checksums.txt"
  $extractDir = Join-Path $tmp.FullName "extract"

  Write-Host ""
  Write-Host "Downloading $archive..."
  Copy-Download -Url $assetUrl -OutFile $archivePath
  Copy-Download -Url $checksumsUrl -OutFile $checksumsPath
  Verify-Checksum -Archive $archivePath -Checksums $checksumsPath
  $extracted = Expand-JuexBinary -Archive $archivePath -OutDir $extractDir -BinaryName $binaryName
  $packageManifest = Get-ChildItem -LiteralPath $extractDir -Recurse -File -Filter "juex-package.json" | Select-Object -First 1
  if ($packageManifest) {
    Install-ManagedPackage -SourceRoot $packageManifest.DirectoryName -ManagedHome $PackageHome -ReleaseKey $releaseKey -InstallTarget $installTarget
  } else {
    Install-Binary -Source $extracted -Target $installTarget
  }
  Write-Host "Installed juex to $installTarget"
} finally {
  if ($tmp) {
    Remove-Item -LiteralPath $tmp.FullName -Recurse -Force -ErrorAction SilentlyContinue
  }
}
