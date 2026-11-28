[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repoRoot = Split-Path -Parent $PSScriptRoot
$out = Join-Path $repoRoot 'bin'
$binary = Join-Path $out 'sonoryx-webgateway'
$pending = "$binary.$([guid]::NewGuid().ToString('N')).tmp"

Get-Command go, zig -ErrorAction Stop | Out-Null
New-Item -ItemType Directory -Force $out | Out-Null
$oldGoos = $env:GOOS
$oldGoarch = $env:GOARCH
$oldCgo = $env:CGO_ENABLED
$oldCC = $env:CC
Push-Location $repoRoot
try {
    # RNNoise and VAD need CGO. Zig cross-builds without a target libc dependency.
    $env:GOOS = 'linux'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '1'
    $env:CC = 'zig cc -target x86_64-linux-musl'
    & go build -trimpath '-ldflags=-w -s -linkmode=external -extldflags=-static' -o $pending ./apps/webgateway
    if ($LASTEXITCODE -ne 0) { throw 'web gateway build failed' }
    Move-Item -LiteralPath $pending -Destination $binary -Force
} finally {
    Pop-Location
    $env:GOOS = $oldGoos
    $env:GOARCH = $oldGoarch
    $env:CGO_ENABLED = $oldCgo
    $env:CC = $oldCC
    if (Test-Path -LiteralPath $pending) { Remove-Item -LiteralPath $pending -Force }
}

Write-Host "Build completed: $binary"
