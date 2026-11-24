<#
.SYNOPSIS
  Builds the MSIX packages of RemoHop quick support (RemoHop.exe) for the Microsoft Store.

.DESCRIPTION
  Runs on Windows with the Windows 10/11 SDK (makeappx.exe, makepri.exe, and signtool.exe when
  signing). Input: the RemoHop.exe programs that `make quick` (packaging/quick/build.sh) writes to
  dist/quick/windows-amd64 and dist/quick/windows-arm64. Output in -Out:

    RemoHop_<version>_x64.msix, RemoHop_<version>_arm64.msix   one package per architecture
    RemoHop_<version>.msixbundle                                 both, for Partner Center

  Each package holds RemoHop.exe, LICENSE, NOTICE, THIRD_PARTY_LICENSES.txt, the logos
  (go run ./packaging/quick/gen -msix-assets), resources.pri (makepri) and AppxManifest.xml
  (AppxManifest.xml.in with the values below).

  Store submissions are uploaded unsigned: the Store signs them. To install a build locally,
  pass -CertPfx with a code-signing certificate whose subject equals -Publisher (a self-signed
  one is fine, once it is in Cert:\LocalMachine\TrustedPeople); see README.md.

.EXAMPLE
  pwsh packaging/windows/msix/build.ps1 -Version v1.0.0 `
    -IdentityName 12345Readori.RemoHop -Publisher 'CN=XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX' `
    -PublisherDisplayName Readori
#>
[CmdletBinding()]
param(
  # The release tag (v1.2.3) or version (1.2.3). A prerelease suffix is dropped.
  [Parameter(Mandatory = $true)] [string] $Version,
  # Partner Center > Product identity. Environment variables are the fallback.
  [string] $IdentityName = $env:MSIX_IDENTITY_NAME,
  [string] $Publisher = $env:MSIX_PUBLISHER,
  [string] $PublisherDisplayName = $env:MSIX_PUBLISHER_DISPLAY_NAME,
  # Where `make quick` put RemoHop.exe (windows-amd64\, windows-arm64\).
  [string] $QuickDir = 'dist/quick',
  [ValidateSet('x64', 'arm64')] [string[]] $Arch = @('x64', 'arm64'),
  [string] $Out = 'dist/msix',
  # THIRD_PARTY_LICENSES.txt; made with packaging/third_party_licenses.sh (needs bash) when absent.
  [string] $ThirdPartyLicenses = 'dist/THIRD_PARTY_LICENSES.txt',
  # Optional, for a local test install only: sign every output with this .pfx.
  [string] $CertPfx = '',
  [string] $CertPassword = $env:MSIX_CERT_PASSWORD,
  # For comparison only: keep MSIX's AppData virtualization (drops the desktop6 settings and the
  # unvirtualizedResources capability). The window then cannot open; see README.md.
  [switch] $Virtualized
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 3.0
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..\..')).Path
Set-Location $root

function Fail([string] $msg) { Write-Error "build.ps1: $msg" -ErrorAction Continue; exit 1 }

# ---- inputs
if ($Version -cnotmatch '\Av?(\d{1,5})\.(\d{1,5})\.(\d{1,5})(-[0-9A-Za-z.]+)?\z') { Fail "version must look like v1.2.3 (got '$Version')" }
$parts = @([int]$Matches[1], [int]$Matches[2], [int]$Matches[3])
if ($parts | Where-Object { $_ -gt 65535 }) { Fail 'each part of the version must be at most 65535' }
# The Store requires the fourth part to be 0 (it uses it itself).
$pkgVersion = '{0}.{1}.{2}.0' -f $parts
if (-not $IdentityName -or $IdentityName -cnotmatch '\A[A-Za-z0-9][A-Za-z0-9.-]{2,49}\z') {
  Fail 'set -IdentityName (Partner Center: Package/Identity/Name; letters, digits, . and -, 3 to 50 characters)'
}
if (-not $Publisher -or $Publisher -cnotmatch '\ACN=[^<>"&]{1,200}\z') {
  Fail 'set -Publisher (Partner Center: Package/Identity/Publisher, e.g. CN=XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX)'
}
if (-not $PublisherDisplayName -or $PublisherDisplayName -match '[<>&"]' -or $PublisherDisplayName.Length -gt 256) {
  Fail 'set -PublisherDisplayName (Partner Center: Package/Properties/PublisherDisplayName)'
}

# ---- tools: the newest Windows SDK, preferring this machine's architecture
$hostArch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'x64' }
function Find-SdkTool([string] $name) {
  $kits = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\10\bin'
  $found = Get-ChildItem -Path $kits -Filter $name -Recurse -ErrorAction SilentlyContinue |
    Where-Object { $_.Directory.Name -in @($hostArch, 'x64', 'x86') -and $_.Directory.Parent.Name -match '^10\.' } |
    Sort-Object @{ Expression = { [version]$_.Directory.Parent.Name }; Descending = $true },
                @{ Expression = { if ($_.Directory.Name -eq $hostArch) { 0 } else { 1 } } }
  if (-not $found) { Fail "$name not found under $kits (install the Windows 10/11 SDK)" }
  return @($found)[0].FullName
}
$makeappx = Find-SdkTool 'makeappx.exe'
$makepri = Find-SdkTool 'makepri.exe'
Write-Output "makeappx: $makeappx"

$exeFor = @{ x64 = 'windows-amd64'; arm64 = 'windows-arm64' }
foreach ($a in $Arch) {
  $exe = Join-Path $QuickDir "$($exeFor[$a])\RemoHop.exe"
  if (-not (Test-Path -LiteralPath $exe)) { Fail "$exe is missing (run make quick, or packaging/quick/build.sh with TARGETS=windows)" }
}
if (-not (Test-Path -LiteralPath $ThirdPartyLicenses)) {
  New-Item -ItemType Directory -Force -Path (Split-Path $ThirdPartyLicenses) | Out-Null
  & bash packaging/third_party_licenses.sh | Out-File -LiteralPath $ThirdPartyLicenses -Encoding utf8NoBOM
  if ($LASTEXITCODE -ne 0) { Fail 'packaging/third_party_licenses.sh failed' }
}

# ---- layout, one per architecture
$work = Join-Path ([IO.Path]::GetTempPath()) ("remohop-msix-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $work, $Out | Out-Null
try {
  $assets = Join-Path $work 'Assets'
  & go run ./packaging/quick/gen -msix-assets $assets
  if ($LASTEXITCODE -ne 0) { Fail 'gen -msix-assets failed' }

  $template = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'AppxManifest.xml.in')
  $priconfig = Join-Path $work 'priconfig.xml'
  & $makepri createconfig /cf $priconfig /dq en-US /pv 10.0.0 /o | Out-Null
  if ($LASTEXITCODE -ne 0) { Fail 'makepri createconfig failed' }

  $packages = @()
  foreach ($a in $Arch) {
    $layout = Join-Path $work "layout-$a"
    New-Item -ItemType Directory -Force -Path $layout | Out-Null
    Copy-Item -LiteralPath (Join-Path $QuickDir "$($exeFor[$a])\RemoHop.exe") -Destination $layout
    Copy-Item -LiteralPath LICENSE, NOTICE -Destination $layout
    Copy-Item -LiteralPath $ThirdPartyLicenses -Destination (Join-Path $layout 'THIRD_PARTY_LICENSES.txt')
    Copy-Item -Recurse -LiteralPath $assets -Destination (Join-Path $layout 'Assets')
    $manifest = $template.Replace('@IDENTITY_NAME@', $IdentityName).Replace('@PUBLISHER@', $Publisher).
      Replace('@PUBLISHER_DISPLAY_NAME@', $PublisherDisplayName).Replace('@VERSION@', $pkgVersion).Replace('@ARCH@', $a)
    if ($Virtualized) {
      $manifest = $manifest -replace '(?m)^\s*<desktop6:\w+WriteVirtualization>disabled</desktop6:\w+WriteVirtualization>\r?\n', '' `
        -replace '(?m)^\s*<rescap:Capability Name="unvirtualizedResources" />\r?\n', ''
      if ($manifest -match '<rescap:Capability Name="unvirtualizedResources"|<desktop6:') { Fail 'could not remove the virtualization settings' }
    }
    if ($manifest -match '@[A-Z_]+@') { Fail "unfilled placeholder in the manifest: $($Matches[0])" }
    [IO.File]::WriteAllText((Join-Path $layout 'AppxManifest.xml'), $manifest, (New-Object Text.UTF8Encoding $false))

    # resources.pri lets Windows pick Square44x44Logo.scale-200.png etc. for "Assets\Square44x44Logo.png".
    & $makepri new /pr $layout /cf $priconfig /mn (Join-Path $layout 'AppxManifest.xml') /of (Join-Path $layout 'resources.pri') /o | Out-Null
    if ($LASTEXITCODE -ne 0) { Fail "makepri new failed ($a)" }

    $msix = Join-Path $Out "RemoHop_${pkgVersion}_$a.msix"
    & $makeappx pack /d $layout /p $msix /o
    if ($LASTEXITCODE -ne 0) { Fail "makeappx pack failed ($a)" }
    $packages += $msix
  }

  $outputs = @($packages)
  if ($packages.Count -gt 1) {
    $bdir = Join-Path $work 'bundle'
    New-Item -ItemType Directory -Force -Path $bdir | Out-Null
    Copy-Item -LiteralPath $packages -Destination $bdir
    $bundle = Join-Path $Out "RemoHop_$pkgVersion.msixbundle"
    & $makeappx bundle /d $bdir /p $bundle /bv $pkgVersion /o
    if ($LASTEXITCODE -ne 0) { Fail 'makeappx bundle failed' }
    $outputs += $bundle
  }

  if ($CertPfx) {
    $signtool = Find-SdkTool 'signtool.exe'
    foreach ($f in $outputs) {
      & $signtool sign /q /fd SHA256 /f $CertPfx /p $CertPassword $f
      if ($LASTEXITCODE -ne 0) { Fail "signtool failed for $f" }
    }
    Write-Output 'signed with the test certificate (local install only; the Store signs submissions itself)'
  }
  foreach ($f in $outputs) {
    $h = (Get-FileHash -Algorithm SHA256 -LiteralPath $f).Hash.ToLowerInvariant()
    Write-Output ("{0}  {1}  ({2:N1} MB)" -f $h, (Split-Path -Leaf $f), ((Get-Item -LiteralPath $f).Length / 1MB))
  }
} finally {
  Remove-Item -Recurse -Force -LiteralPath $work -ErrorAction SilentlyContinue
}
