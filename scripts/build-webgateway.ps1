[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repoRoot = Split-Path -Parent $PSScriptRoot
$out = Join-Path $repoRoot 'bin'
$archive = Join-Path $out 'sonoryx-webgateway-linux-amd64.tar.gz'
$staging = Join-Path $out ('.webgateway-build-' + [guid]::NewGuid().ToString('N'))
$pending = "$staging.tar.gz"

Get-Command npm.cmd, tar -ErrorAction Stop | Out-Null
New-Item -ItemType Directory -Force $staging | Out-Null
$staging = (Resolve-Path -LiteralPath $staging).Path
$outputPrefix = (Resolve-Path -LiteralPath $out).Path + [IO.Path]::DirectorySeparatorChar
if (-not $staging.StartsWith($outputPrefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'web gateway staging must be inside the repository bin directory'
}
Push-Location $repoRoot
try {
    & (Join-Path $PSScriptRoot 'build-gateway.ps1')
    & npm.cmd run build:web
    if ($LASTEXITCODE -ne 0) { throw 'web build failed' }

    Copy-Item -LiteralPath (Join-Path $out 'sonoryx-webgateway') -Destination $staging
    Copy-Item -LiteralPath 'apps/web/dist' -Destination (Join-Path $staging 'web') -Recurse
    & tar -czf $pending -C $staging sonoryx-webgateway web
    if ($LASTEXITCODE -ne 0) { throw 'web deploy archive creation failed' }
    Move-Item -LiteralPath $pending -Destination $archive -Force
} finally {
    Pop-Location
    Remove-Item -LiteralPath $staging -Recurse -Force
    if (Test-Path -LiteralPath $pending) { Remove-Item -LiteralPath $pending -Force }
}

$versions = Get-Content -LiteralPath (Join-Path $repoRoot 'version/version.json') -Raw | ConvertFrom-Json
Write-Host "Build completed: $archive (webgateway $($versions.apps.webgateway), web $($versions.apps.web))"
