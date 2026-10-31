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
$scp = (Get-Command scp -ErrorAction Stop).Source
$sourceScript = Join-Path $repoRoot "deploy\unified-upgrade.sh"
$buildDir = Join-Path $repoRoot "build"

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
$goArch = switch ($remoteArch) {
    "x86_64" { "amd64"; break }
    "aarch64" { "arm64"; break }
    "arm64" { "arm64"; break }
    default { throw "unsupported remote architecture '$remoteArch'" }
}

New-Item -ItemType Directory -Force -Path $buildDir | Out-Null
$tempBinary = Join-Path $buildDir ("liquidation-predictor-linux-{0}" -f $Revision)
$tempCollectorBinary = Join-Path $buildDir ("market-collector-linux-{0}" -f $Revision)
$buildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$ldflags = "-s -w -X github.com/pcdogyu/price-prediction-liquidation-levels/internal/httpapi.BuildTime=$buildTime -X github.com/pcdogyu/price-prediction-liquidation-levels/internal/httpapi.BuildRevision=$Revision -X github.com/pcdogyu/price-prediction-liquidation-levels/internal/httpapi.BuildBranch=$Branch"
$previousGoos = $env:GOOS
$previousGoarch = $env:GOARCH
$previousCgo = $env:CGO_ENABLED
$published = $false
$remoteUploadPath = "$RemoteAppRoot/liquidation-predictor.upload.$Revision"
$remoteCollectorUploadPath = "$RemoteAppRoot/market-collector.upload.$Revision"

try {
    Write-Host "Running local Go tests and vet..."
    Invoke-Checked $go @("test", "./...")
    Invoke-Checked $go @("vet", "./...")

    Write-Host "Building GOOS=linux GOARCH=$goArch..."
    $env:CGO_ENABLED = "0"
    $env:GOOS = "linux"
    $env:GOARCH = $goArch
    Invoke-Checked $go @(
        "build",
        "-buildvcs=false",
        "-trimpath",
        ("-ldflags=$ldflags"),
        "-o",
        $tempBinary,
        "./cmd/server"
    )
	Invoke-Checked $go @(
		"build",
		"-buildvcs=false",
		"-trimpath",
		("-ldflags=$ldflags"),
		"-o",
		$tempCollectorBinary,
		"./cmd/market-collector"
	)

    $sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $tempBinary).Hash.ToLowerInvariant()
	$collectorSha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $tempCollectorBinary).Hash.ToLowerInvariant()
	Write-Host "Uploading Linux binaries for $Revision..."
	Invoke-Checked $scp ($sshCommon + @($tempBinary, $tempCollectorBinary, ("{0}:{1}" -f $remote, $remoteUploadPath)))

	Write-Host "Replacing the server and collector binaries atomically..."
    # The repository is commonly checked out with CRLF on Windows. Strip CR
    # remotely before bash parses the streamed deployment script.
	$remoteCommand = "tr -d '\r' | bash -s -- $Revision $Branch $remoteUploadPath $remoteCollectorUploadPath $sha256 $collectorSha256"
    Get-Content -Raw -LiteralPath $sourceScript | & $ssh ($sshCommon + @($remote, $remoteCommand))
    if ($LASTEXITCODE -ne 0) {
        throw "remote replacement failed with exit code $LASTEXITCODE"
    }
    $published = $true
}
finally {
    $env:GOOS = $previousGoos
    $env:GOARCH = $previousGoarch
    $env:CGO_ENABLED = $previousCgo
    if (-not $published) {
        try {
			& $ssh ($sshCommon + @($remote, "rm -f -- $remoteUploadPath $remoteCollectorUploadPath")) | Out-Null
        }
        catch {
            # Preserve the original publish error; the remote path is exact and
            # can be removed by the next publish attempt.
        }
    }
    if (Test-Path -LiteralPath $tempBinary) {
        Remove-Item -LiteralPath $tempBinary -Force
    }
	if (Test-Path -LiteralPath $tempCollectorBinary) {
		Remove-Item -LiteralPath $tempCollectorBinary -Force
	}
}

Write-Host "Published $Revision to $HostName."
