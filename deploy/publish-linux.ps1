[CmdletBinding()]
param(
    [string]$HostName = "tvbot.lmitis.com",
    [string]$RemoteUser = "root",
    [string]$KeyPath = "",
    [string]$Branch = "binance",
    [string]$Revision = "",
    [string]$RemoteAppRoot = "/opt/Price-Prediction-Using-Liquidation-Levels"
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($KeyPath)) {
    $KeyPath = Join-Path $repoRoot "pem\server.pem"
}
$KeyPath = (Resolve-Path $KeyPath).Path

function Invoke-Checked {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )

    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$FilePath failed with exit code $LASTEXITCODE"
    }
}

function Get-CheckedOutput {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )

    $output = (& $FilePath @Arguments | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw "$FilePath failed with exit code $LASTEXITCODE"
    }
    return $output
}

$git = (Get-Command git -ErrorAction Stop).Source
$go = (Get-Command go -ErrorAction Stop).Source
$ssh = (Get-Command ssh -ErrorAction Stop).Source
$sourceScript = Join-Path $repoRoot "deploy\unified-upgrade.sh"

if ($RemoteAppRoot -notmatch '^/[A-Za-z0-9._/-]+$') {
    throw "RemoteAppRoot must be a simple absolute Linux path"
}

$currentBranch = Get-CheckedOutput $git @("-C", $repoRoot, "branch", "--show-current")
if ($currentBranch -ne $Branch) {
    throw "current branch '$currentBranch' does not match requested branch '$Branch'"
}
$dirty = & $git -C $repoRoot status --porcelain
if ($LASTEXITCODE -ne 0) {
    throw "unable to read git status"
}
if (-not [string]::IsNullOrWhiteSpace(($dirty | Out-String))) {
    throw "working tree is not clean; commit and push before publishing"
}

Invoke-Checked $git @("-C", $repoRoot, "fetch", "origin", $Branch)
if ([string]::IsNullOrWhiteSpace($Revision)) {
    $Revision = Get-CheckedOutput $git @("-C", $repoRoot, "rev-parse", "HEAD")
}
$Revision = $Revision.Trim().ToLowerInvariant()
if ($Revision -notmatch "^[0-9a-f]{40}$") {
    throw "revision must be a 40-character commit SHA"
}
$head = Get-CheckedOutput $git @("-C", $repoRoot, "rev-parse", "HEAD")
$remoteHead = Get-CheckedOutput $git @("-C", $repoRoot, "rev-parse", "origin/$Branch")
if ($head -ne $Revision -or $remoteHead -ne $Revision) {
    throw "local HEAD, requested revision, and origin/$Branch must match"
}

$sshCommon = @(
    "-i", $KeyPath,
    "-o", "BatchMode=yes",
    "-o", "ConnectTimeout=10",
    "-o", "StrictHostKeyChecking=yes"
)
$remote = "{0}@{1}" -f $RemoteUser, $HostName
$remoteArch = Get-CheckedOutput $ssh ($sshCommon + @($remote, "uname -m"))
if ($remoteArch -notin @("x86_64", "aarch64", "arm64")) {
    throw "unsupported remote architecture '$remoteArch'"
}

Write-Host "Running local Go tests and vet before remote build..."
Invoke-Checked $go @("test", "./...")
Invoke-Checked $go @("vet", "./...")

Write-Host "Synchronizing source and building Linux binaries on $remote ($remoteArch)..."
# The repository is commonly checked out with CRLF on Windows. Strip CR
# remotely before bash parses the streamed deployment script.
$remoteCommand = "tr -d '\r' | bash -s -- $Revision $Branch $RemoteAppRoot"
Get-Content -Raw -LiteralPath $sourceScript | & $ssh ($sshCommon + @($remote, $remoteCommand))
if ($LASTEXITCODE -ne 0) {
    throw "remote build and replacement failed with exit code $LASTEXITCODE"
}

Write-Host "Published $Revision to $HostName using a remote native Linux build."
