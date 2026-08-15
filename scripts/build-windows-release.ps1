[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[0-9]+\.[0-9]+\.[0-9]+$')]
    [string]$Version
)

$ErrorActionPreference = 'Stop'

$repositoryRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$distributionRoot = Join-Path $repositoryRoot 'dist'
$goWrapper = Join-Path $PSScriptRoot 'go.cmd'
$linkerFlags = "-s -w -X github.com/sessionmgr/sessionmgr/internal/app.version=$Version"
$assets = @()

New-Item -ItemType Directory -Path $distributionRoot -Force | Out-Null

$previousCgo = $env:CGO_ENABLED
$previousGoos = $env:GOOS
$previousGoarch = $env:GOARCH
Push-Location $repositoryRoot
try {
    $env:CGO_ENABLED = '0'
    $env:GOOS = 'windows'
    foreach ($architecture in @('amd64', 'arm64')) {
        $env:GOARCH = $architecture
        $asset = Join-Path $distributionRoot "sessionmgr-v$Version-windows-$architecture.exe"
        & $goWrapper build -trimpath -ldflags $linkerFlags -o $asset ./cmd/sessionmgr
        if ($LASTEXITCODE -ne 0) {
            throw "Go failed to build the Windows/$architecture release asset."
        }

        $stream = [IO.File]::OpenRead($asset)
        try {
            if ($stream.ReadByte() -ne 0x4d -or $stream.ReadByte() -ne 0x5a) {
                throw "Release asset is not a Windows PE executable: $asset"
            }
        }
        finally {
            $stream.Dispose()
        }
        $assets += $asset
    }
}
finally {
    if ($null -eq $previousCgo) { Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue } else { $env:CGO_ENABLED = $previousCgo }
    if ($null -eq $previousGoos) { Remove-Item Env:GOOS -ErrorAction SilentlyContinue } else { $env:GOOS = $previousGoos }
    if ($null -eq $previousGoarch) { Remove-Item Env:GOARCH -ErrorAction SilentlyContinue } else { $env:GOARCH = $previousGoarch }
    Pop-Location
}

$processorArchitecture = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
$nativeArchitecture = switch ($processorArchitecture.ToUpperInvariant()) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "Cannot execute a release version check on unsupported Windows architecture '$processorArchitecture'." }
}
$nativeAsset = $assets | Where-Object { $_ -like "*-windows-$nativeArchitecture.exe" } | Select-Object -First 1
$reportedVersion = & $nativeAsset version
if ($LASTEXITCODE -ne 0 -or $reportedVersion.Trim() -ne "sessionmgr $Version") {
    throw "Release version verification failed for $nativeAsset; reported '$reportedVersion'."
}

$checksumLines = foreach ($asset in $assets) {
    $hash = (Get-FileHash -LiteralPath $asset -Algorithm SHA256).Hash.ToLowerInvariant()
    "$hash  $([IO.Path]::GetFileName($asset))"
}
$checksumPath = Join-Path $distributionRoot 'SHA256SUMS.txt'
[IO.File]::WriteAllLines($checksumPath, $checksumLines, [Text.UTF8Encoding]::new($false))

Write-Host "Built Session Manager $Version Windows release assets:"
foreach ($asset in $assets) {
    Write-Host "  $asset"
}
Write-Host "  $checksumPath"
