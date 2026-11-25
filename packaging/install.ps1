<#
.SYNOPSIS
  Install the remohop binary from a RemoHop GitHub release (Windows 10/11, amd64 or arm64).

.DESCRIPTION
  Downloads remohop_windows_<arch>.zip and SHA256SUMS from the release, checks the zip
  against SHA256SUMS, checks SHA256SUMS' signature when cosign is installed, and copies
  remohop.exe to -BinDir. No administrator rights needed: the default directory is
  %LOCALAPPDATA%\Programs\RemoHop, and your USER (not machine) PATH gets that directory
  unless you pass -NoPath. Nothing else is touched. Release binaries are not code-signed
  yet, so SmartScreen may warn the first time remohop.exe runs.

  Read it before you run it:
    Invoke-WebRequest https://github.com/readori/RemoHop/releases/latest/download/install.ps1 -OutFile install.ps1
    Get-Content install.ps1 | more
    powershell -ExecutionPolicy Bypass -File .\install.ps1

.PARAMETER Version   Release tag such as v1.2.3 (default: the latest release).
.PARAMETER BinDir    Install directory (default: $env:LOCALAPPDATA\Programs\RemoHop).
.PARAMETER Repo      GitHub repository, OWNER/NAME (default: readori/RemoHop).
.PARAMETER Verify    auto (default: verify with cosign if installed, else say so),
                     require (fail without a valid signature) or off.
.PARAMETER BaseUrl   Directory holding the assets, instead of GitHub (a mirror or a test server).
.PARAMETER NoPath    Do not add BinDir to the user PATH.
.PARAMETER App       Also install the desktop app: RemoHop.exe (from RemoHop-windows-<arch>.zip,
                     checked against the same SHA256SUMS) next to remohop.exe, with a Start menu
                     shortcut. The app needs the Microsoft Edge WebView2 runtime, which Windows
                     10 and 11 have; without it, it opens in an Edge window.

  Trust: the checksum alone only detects a damaged download, since SHA256SUMS comes from
  the same place as the zip. The signature ties SHA256SUMS to this repository's release
  workflow run for the requested tag (cosign keyless; "latest" is resolved to its tag
  first), so an older signed release cannot pass for a newer one; install cosign and use
  -Verify require for that guarantee. With -BaseUrl, give -Version too.

  Upgrading: run it again. A running remohop keeps the old version until restarted; the
  RemoHop service runs its own copy in Program Files, updated by running
  `remohop service install` again from an elevated prompt (the script reminds you).
#>
[CmdletBinding()]
param(
  [string]$Version = $env:VERSION,
  [string]$BinDir = $env:BINDIR,
  [string]$Repo = $(if ($env:REPO) { $env:REPO } else { 'readori/RemoHop' }),
  [ValidateSet('auto', 'require', 'off')][string]$Verify = $(if ($env:VERIFY) { $env:VERIFY } else { 'auto' }),
  [string]$BaseUrl = $env:BASE_URL,
  [switch]$NoPath,
  [switch]$App
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'   # Invoke-WebRequest is much faster without the progress bar

function Fail($msg) { Write-Error "install.ps1: $msg"; exit 1 }

if ($Repo -notmatch '^[\w.-]+/[\w.-]+$') { Fail 'Repo must be OWNER/NAME' }
if ($PSVersionTable.PSVersion.Major -ge 6 -and -not $IsWindows) { Fail 'this script is for Windows; on Linux and macOS use install.sh' }

# PROCESSOR_ARCHITEW6432 is set when a 32-bit PowerShell runs on a 64-bit system.
$procArch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
switch ($procArch) {
  'AMD64' { $arch = 'amd64' }
  'ARM64' { $arch = 'arm64' }
  default { Fail "unsupported architecture $procArch" }
}
$asset = "remohop_windows_$arch.zip"

$tagPattern = '^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$'
if ($Version) {
  if ($Version -match '^\d') { $Version = "v$Version" }
  if ($Version -notmatch $tagPattern) { Fail 'Version must look like v1.2.3' }
}
if (-not $BinDir) { $BinDir = Join-Path $env:LOCALAPPDATA 'Programs\RemoHop' }

# Older Windows PowerShell defaults to TLS 1.0/1.1, which GitHub refuses.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

if ($BaseUrl) {
  $base = $BaseUrl.TrimEnd('/')
} else {
  if (-not $Version) {
    # Resolve "latest" to its tag, so that the signature can be required to come from that
    # tag's release run (and the download cannot change half-way).
    $loc = $null
    try {
      $req = [Net.HttpWebRequest]::Create("https://github.com/$Repo/releases/latest")
      $req.Method = 'HEAD'
      $req.AllowAutoRedirect = $false
      $resp = $req.GetResponse()
      $loc = $resp.Headers['Location']
      $resp.Close()
    } catch { }
    if ($loc) { $Version = ($loc -split '/')[-1] }
    if (-not $Version -or $Version -notmatch $tagPattern) { Fail "cannot find the latest release of $Repo (got '$loc'); use -Version" }
  }
  $base = "https://github.com/$Repo/releases/download/$Version"
}

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("remohop-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "Downloading $asset from $base"
  try { Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile (Join-Path $tmp $asset) }
  catch { Fail "download failed: $base/$asset ($($_.Exception.Message))" }
  try { Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS" -OutFile (Join-Path $tmp 'SHA256SUMS') }
  catch { Fail "download failed: $base/SHA256SUMS ($($_.Exception.Message))" }

  # 1. The signature on SHA256SUMS (who made this list).
  $sigOk = $false
  if ($Verify -ne 'off') {
    $cosign = Get-Command cosign -ErrorAction SilentlyContinue
    if ($cosign) {
      $bundle = Join-Path $tmp 'SHA256SUMS.sigstore.json'
      try { Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS.sigstore.json" -OutFile $bundle }
      catch { Fail "download failed: $base/SHA256SUMS.sigstore.json (use -Verify off to skip the signature check)" }
      # The certificate must name this repository's release workflow, run for the tag that
      # was asked for: a mirror cannot answer with an older signed release.
      if ($Version) {
        $idArgs = @('--certificate-identity', "https://github.com/$Repo/.github/workflows/release.yml@refs/tags/$Version")
      } elseif ($Verify -eq 'require') {
        Fail '-Verify require with -BaseUrl needs -Version: the signature is checked against that release''s tag'
      } else {
        Write-Warning "no -Version given with -BaseUrl: a signature from any release of $Repo is accepted"
        $repoRe = [Regex]::Escape($Repo)
        $idArgs = @('--certificate-identity-regexp', "^https://github\.com/$repoRe/\.github/workflows/release\.yml@refs/tags/v.+`$")
      }
      # cosign reports success on stderr. Windows PowerShell 5.1 turns redirected stderr lines
      # of a native command into error records, which 'Stop' would make fatal: decide by the
      # exit code alone.
      $prevEap = $ErrorActionPreference
      $ErrorActionPreference = 'Continue'
      try {
        $out = & $cosign.Source verify-blob --bundle $bundle @idArgs `
          --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' (Join-Path $tmp 'SHA256SUMS') 2>&1 |
          ForEach-Object { "$_" }
        $code = $LASTEXITCODE
      } finally { $ErrorActionPreference = $prevEap }
      if ($code -ne 0) { $out | ForEach-Object { Write-Host $_ }; Fail 'the signature of SHA256SUMS is not valid; nothing was installed' }
      $sigOk = $true
      $what = if ($Version) { $Version } else { 'any tag' }
      Write-Host "Signature OK (cosign: $Repo release workflow, $what)"
    } elseif ($Verify -eq 'require') {
      Fail '-Verify require, but cosign is not installed'
    } else {
      Write-Warning 'cosign is not installed: the signature was NOT checked (the checksum below only detects a damaged download).'
    }
  }

  # 2. The zip against SHA256SUMS (is this the file that was listed).
  function Test-Listed($name) {
    $want = $null
    foreach ($line in Get-Content (Join-Path $tmp 'SHA256SUMS')) {
      $parts = $line -split '\s+', 2
      if ($parts.Count -eq 2 -and ($parts[1].TrimStart('*') -eq $name)) { $want = $parts[0].ToLowerInvariant(); break }
    }
    if (-not $want) { Fail "$name is not listed in SHA256SUMS" }
    $got = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $name)).Hash.ToLowerInvariant()
    if ($got -ne $want) { Fail "checksum mismatch for $name (expected $want, got $got); nothing was installed" }
  }
  Test-Listed $asset
  $appAsset = "RemoHop-windows-$arch.zip"
  if ($App) {
    Write-Host "Downloading $appAsset"
    try { Invoke-WebRequest -UseBasicParsing -Uri "$base/$appAsset" -OutFile (Join-Path $tmp $appAsset) }
    catch { Fail "download failed: $base/$appAsset ($($_.Exception.Message))" }
    Test-Listed $appAsset
  }
  Write-Host 'Checksum OK'

  Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath (Join-Path $tmp 'x') -Force
  $exe = Join-Path $tmp 'x\remohop.exe'
  if (-not (Test-Path $exe)) { Fail 'the zip has no remohop.exe' }
  if ($Version) {
    $got = "$(& $exe version)".Trim()
    if ($got -ne "remohop $Version") { Fail "the zip holds '$got', not remohop $Version; nothing was installed" }
  }

  New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
  $target = Join-Path $BinDir 'remohop.exe'
  # Copies moved aside by earlier installs, deleted once nothing runs them any more.
  Get-ChildItem -Path $BinDir -Filter 'remohop.exe.old*' -ErrorAction SilentlyContinue |
    ForEach-Object { Remove-Item $_.FullName -Force -ErrorAction SilentlyContinue }
  # A running remohop.exe cannot be overwritten, but it can be renamed: move it aside under a
  # name of its own, so that a host still running an older copy does not block the next upgrade.
  $runningOld = $false
  if (Test-Path $target) {
    $old = "$target.old-" + [Guid]::NewGuid().ToString('N').Substring(0, 8)
    Move-Item $target $old -Force
    Remove-Item $old -Force -ErrorAction SilentlyContinue
    $runningOld = Test-Path $old
  }
  Copy-Item $exe $target -Force
  Write-Host "Installed $target"
  & $target version

  if ($App) {
    Expand-Archive -Path (Join-Path $tmp $appAsset) -DestinationPath (Join-Path $tmp 'app') -Force
    $appExe = Join-Path $tmp 'app\RemoHop.exe'
    if (-not (Test-Path $appExe)) { Fail 'the app zip has no RemoHop.exe' }
    $appTarget = Join-Path $BinDir 'RemoHop.exe'
    if (Test-Path $appTarget) {
      # A running app cannot be overwritten but can be renamed (see remohop.exe above).
      $old = "$appTarget.old-" + [Guid]::NewGuid().ToString('N').Substring(0, 8)
      Move-Item $appTarget $old -Force
      Remove-Item $old -Force -ErrorAction SilentlyContinue
    }
    Copy-Item $appExe $appTarget -Force
    Get-ChildItem -Path $BinDir -Filter 'RemoHop.exe.old*' -ErrorAction SilentlyContinue |
      ForEach-Object { Remove-Item $_.FullName -Force -ErrorAction SilentlyContinue }
    # Start menu shortcut (per user).
    $programs = [Environment]::GetFolderPath('Programs')
    $lnk = (New-Object -ComObject WScript.Shell).CreateShortcut((Join-Path $programs 'RemoHop.lnk'))
    $lnk.TargetPath = $appTarget
    $lnk.WorkingDirectory = $BinDir
    $lnk.Description = 'RemoHop'
    $lnk.Save()
    Write-Host "Installed $appTarget (Start menu: RemoHop)"
  }

  if (-not $NoPath) {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $entries = @($userPath -split ';' | Where-Object { $_ })
    if ($entries -notcontains $BinDir) {
      [Environment]::SetEnvironmentVariable('Path', (($entries + $BinDir) -join ';'), 'User')
      Write-Host "Added $BinDir to your user PATH. Open a new terminal to use 'remohop'."
    }
  }
  if ($sigOk) { Write-Host 'Verified: checksum and signature.' } else { Write-Host 'Verified: checksum only.' }
  # What still runs the previous version.
  if ($runningOld) { Write-Host 'Note: a remohop started from the previous copy is still running; restart it to use the new version.' }
  $svc = Get-Service -Name RemoHop -ErrorAction SilentlyContinue
  if ($svc) {
    Write-Host 'Note: the RemoHop service runs its own copy (under Program Files). To update and restart it,'
    Write-Host "      from an elevated prompt: & '$target' service install"
  }
  Write-Host 'Next: remohop host   (or, from an elevated prompt: remohop service install, which copies remohop.exe'
  Write-Host '      to Program Files and runs it as a service). See README.txt in the zip for the Windows notes.'
}
finally {
  Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
