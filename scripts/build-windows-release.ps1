[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[0-9]+\.[0-9]+\.[0-9]+$')]
    [string]$Version
)

$ErrorActionPreference = 'Stop'

$repositoryRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$distributionRoot = Join-Path $repositoryRoot 'dist'
$goExecutable = (Get-Command go -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
$iconPath = Join-Path $repositoryRoot 'assets\sessionmgr.png'
$resourceTool = 'github.com/tc-hib/go-winres@v0.3.3'
$resourcePrefix = 'cmd/sessionmgr/rsrc'
$resourceFiles = @(
    (Join-Path $repositoryRoot 'cmd\sessionmgr\rsrc_windows_amd64.syso'),
    (Join-Path $repositoryRoot 'cmd\sessionmgr\rsrc_windows_arm64.syso')
)
$linkerFlags = "-s -w -X github.com/sessionmgr/sessionmgr/internal/app.version=$Version"
$assets = @()

New-Item -ItemType Directory -Path $distributionRoot -Force | Out-Null

foreach ($resourceFile in $resourceFiles) {
    if (Test-Path -LiteralPath $resourceFile) {
        throw "Refusing to overwrite an existing Windows resource file: $resourceFile"
    }
}

$previousCgo = $env:CGO_ENABLED
$previousGoos = $env:GOOS
$previousGoarch = $env:GOARCH
$resourcesGenerated = $false
Push-Location $repositoryRoot
try {
    $resourcesGenerated = $true
    & $goExecutable run $resourceTool simply `
        --arch amd64,arm64 `
        --out $resourcePrefix `
        --manifest cli `
        --product-version $Version `
        --file-version $Version `
        --file-description 'Session Manager' `
        --product-name 'Session Manager' `
        --original-filename 'sessionmgr.exe' `
        --icon $iconPath
    if ($LASTEXITCODE -ne 0) {
        throw 'Failed to generate Windows application resources.'
    }
    foreach ($resourceFile in $resourceFiles) {
        if (-not (Test-Path -LiteralPath $resourceFile)) {
            throw "Windows resource generation did not produce $resourceFile"
        }
    }

    $env:CGO_ENABLED = '0'
    $env:GOOS = 'windows'
    foreach ($architecture in @('amd64', 'arm64')) {
        $env:GOARCH = $architecture
        $asset = Join-Path $distributionRoot "sessionmgr-v$Version-windows-$architecture.exe"
        # Go may leave a tilde-suffixed executable when an older build is still
        # running. Check that locked backup before touching the current asset so
        # a refused rebuild preserves the existing release file.
        foreach ($staleAsset in @("$asset~", $asset)) {
            Remove-Item -LiteralPath $staleAsset -Force -ErrorAction SilentlyContinue
            if (Test-Path -LiteralPath $staleAsset) {
                throw "Cannot replace the existing release build artifact: $staleAsset"
            }
        }
        & $goExecutable build -trimpath -ldflags $linkerFlags -o $asset ./cmd/sessionmgr
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
    if ($resourcesGenerated) {
        foreach ($resourceFile in $resourceFiles) {
            Remove-Item -LiteralPath $resourceFile -ErrorAction SilentlyContinue
        }
    }
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

foreach ($asset in $assets) {
    $resourceCheckDirectory = Join-Path $distributionRoot ".resource-check-$([Guid]::NewGuid().ToString('N'))"
    try {
        & $goExecutable run $resourceTool extract --dir $resourceCheckDirectory $asset
        if ($LASTEXITCODE -ne 0) {
            throw "Failed to inspect Windows resources in $asset."
        }
        $extractedIcon = Join-Path $resourceCheckDirectory '#1_0000.ico'
        $resourceManifest = Join-Path $resourceCheckDirectory 'winres.json'
        if (-not (Test-Path -LiteralPath $extractedIcon) -or (Get-Item -LiteralPath $extractedIcon).Length -eq 0) {
            throw "Release asset does not contain the expected application icon: $asset"
        }
        if (-not (Select-String -LiteralPath $resourceManifest -SimpleMatch "`"ProductVersion`": `"$Version`"" -Quiet)) {
            throw "Release asset does not contain product version ${Version}: $asset"
        }
    }
    finally {
        $resolvedDistributionRoot = [IO.Path]::GetFullPath($distributionRoot) + [IO.Path]::DirectorySeparatorChar
        $resolvedCheckDirectory = [IO.Path]::GetFullPath($resourceCheckDirectory)
        if (-not $resolvedCheckDirectory.StartsWith($resolvedDistributionRoot, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to remove unsafe resource-check directory: $resolvedCheckDirectory"
        }
        Remove-Item -LiteralPath $resolvedCheckDirectory -Recurse -Force -ErrorAction SilentlyContinue
    }
}

Write-Host "Built Session Manager $Version Windows release assets:"
foreach ($asset in $assets) {
    Write-Host "  $asset"
}
