[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$toolsRoot = Join-Path $repositoryRoot ".tools"
$toolchainConfigPath = Join-Path $PSScriptRoot "go-toolchain.env"
$toolchainConfig = @{}
foreach ($line in Get-Content -LiteralPath $toolchainConfigPath) {
    $trimmedLine = $line.Trim()
    if ($trimmedLine.Length -eq 0 -or $trimmedLine.StartsWith("#")) {
        continue
    }
    if ($trimmedLine -notmatch "^([A-Z0-9_]+)=([A-Za-z0-9.]+)$") {
        throw "Invalid portable Go toolchain setting in '$toolchainConfigPath': $line"
    }
    $toolchainConfig[$Matches[1]] = $Matches[2]
}

$goVersion = $toolchainConfig["GO_VERSION"]
if ([string]::IsNullOrWhiteSpace($goVersion)) {
    throw "GO_VERSION is missing from '$toolchainConfigPath'."
}

$architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
switch ($architecture) {
    "X64" {
        $goArchitecture = "amd64"
        $expectedSha256 = $toolchainConfig["GO_SHA256_WINDOWS_AMD64"]
    }
    "Arm64" {
        $goArchitecture = "arm64"
        $expectedSha256 = $toolchainConfig["GO_SHA256_WINDOWS_ARM64"]
    }
    default {
        throw "Automatic Go bootstrap does not support Windows architecture '$architecture'. Install Go 1.24 or newer manually."
    }
}
if ($expectedSha256 -notmatch "^[0-9a-f]{64}$") {
    throw "The SHA-256 setting for windows/$goArchitecture is missing or invalid in '$toolchainConfigPath'."
}

$goRoot = Join-Path $toolsRoot "go\windows-$goArchitecture"
$goExecutable = Join-Path $goRoot "bin\go.exe"
$downloadsRoot = Join-Path $toolsRoot "downloads"
$lockFile = Join-Path $toolsRoot ".go-bootstrap-windows-$goArchitecture.lock"

function Test-ExpectedGo {
    if (-not (Test-Path -LiteralPath $goExecutable -PathType Leaf)) {
        return $false
    }

    $reportedVersion = & $goExecutable version 2>$null
    return $LASTEXITCODE -eq 0 -and $reportedVersion -like "go version go$goVersion windows/$goArchitecture"
}

if (Test-ExpectedGo) {
    exit 0
}

if (Test-Path -LiteralPath $goRoot) {
    throw "Existing portable Go at '$goRoot' is incomplete or not Go $goVersion for windows/$goArchitecture. Remove only that directory and retry."
}

$archiveName = "go$goVersion.windows-$goArchitecture.zip"
$archivePath = Join-Path $downloadsRoot $archiveName
$downloadUrl = "https://go.dev/dl/$archiveName"

New-Item -ItemType Directory -Force -Path $downloadsRoot | Out-Null

$lockStream = $null
try {
    $lockStream = [IO.File]::Open(
        $lockFile,
        [IO.FileMode]::OpenOrCreate,
        [IO.FileAccess]::ReadWrite,
        [IO.FileShare]::None
    )
} catch {
    if (Test-ExpectedGo) {
        exit 0
    }
    throw "Another Go bootstrap is running for windows/$goArchitecture. Wait for it to finish before retrying."
}

$partialArchive = "$archivePath.part-$([Guid]::NewGuid().ToString('N'))"
$stagingDirectory = Join-Path $toolsRoot ".go-bootstrap-windows-$goArchitecture-$([Guid]::NewGuid().ToString('N'))"

try {
    $stagingPrefix = ".go-bootstrap-windows-$goArchitecture-"
    foreach ($staleStaging in Get-ChildItem -LiteralPath $toolsRoot -Directory -Filter "$stagingPrefix*" -ErrorAction SilentlyContinue) {
        $resolvedToolsRoot = [IO.Path]::GetFullPath($toolsRoot).TrimEnd([IO.Path]::DirectorySeparatorChar)
        $resolvedStaleStaging = [IO.Path]::GetFullPath($staleStaging.FullName)
        if (-not $resolvedStaleStaging.StartsWith($resolvedToolsRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to clear unexpected bootstrap staging directory '$resolvedStaleStaging'."
        }
        Remove-Item -LiteralPath $resolvedStaleStaging -Recurse -Force
    }

    if (Test-Path -LiteralPath $archivePath -PathType Leaf) {
        $actualSha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash.ToLowerInvariant()
        if ($actualSha256 -ne $expectedSha256) {
            throw "Cached Go archive '$archivePath' failed SHA-256 verification. Remove only that file and retry."
        }
    } else {
        Write-Host "sessionmgr: Go was not found; downloading Go $goVersion for windows/$goArchitecture..."
        Invoke-WebRequest -UseBasicParsing -Uri $downloadUrl -OutFile $partialArchive
        $actualSha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $partialArchive).Hash.ToLowerInvariant()
        if ($actualSha256 -ne $expectedSha256) {
            throw "Downloaded Go archive failed SHA-256 verification."
        }
        Move-Item -LiteralPath $partialArchive -Destination $archivePath
    }

    New-Item -ItemType Directory -Path $stagingDirectory | Out-Null
    Expand-Archive -LiteralPath $archivePath -DestinationPath $stagingDirectory
    $stagedGoRoot = Join-Path $stagingDirectory "go"
    $stagedGoExecutable = Join-Path $stagedGoRoot "bin\go.exe"
    if (-not (Test-Path -LiteralPath $stagedGoExecutable -PathType Leaf)) {
        throw "The verified Go archive did not contain go/bin/go.exe."
    }
    if (Test-Path -LiteralPath $goRoot) {
        throw "Portable Go destination '$goRoot' appeared during bootstrap; refusing to overwrite it."
    }
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $goRoot) | Out-Null
    Move-Item -LiteralPath $stagedGoRoot -Destination $goRoot

    if (-not (Test-ExpectedGo)) {
        throw "Portable Go installation completed but version verification failed."
    }
    Write-Host "sessionmgr: bootstrapped Go $goVersion for windows/$goArchitecture at $goRoot"
} finally {
    if (Test-Path -LiteralPath $partialArchive -PathType Leaf) {
        Remove-Item -LiteralPath $partialArchive -Force
    }
    if (Test-Path -LiteralPath $stagingDirectory -PathType Container) {
        $resolvedToolsRoot = [IO.Path]::GetFullPath($toolsRoot).TrimEnd([IO.Path]::DirectorySeparatorChar)
        $resolvedStaging = [IO.Path]::GetFullPath($stagingDirectory)
        if ($resolvedStaging.StartsWith($resolvedToolsRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
            Remove-Item -LiteralPath $stagingDirectory -Recurse -Force
        }
    }
    if ($null -ne $lockStream) {
        $lockStream.Dispose()
    }
    if (Test-Path -LiteralPath $lockFile -PathType Leaf) {
        Remove-Item -LiteralPath $lockFile -Force
    }
}
