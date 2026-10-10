[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repoRoot = Split-Path -Parent $PSScriptRoot
$outputDir = Join-Path $repoRoot 'bin'
$binary = Join-Path $outputDir 'sonoryx-server'

New-Item -ItemType Directory -Force $outputDir | Out-Null

$oldGoos = $env:GOOS
$oldGoarch = $env:GOARCH
$oldCgoEnabled = $env:CGO_ENABLED
try {
    Push-Location $repoRoot
    try {
        $env:GOOS = 'linux'
        $env:GOARCH = 'amd64'
        $env:CGO_ENABLED = '0'
        & go build -trimpath -o $binary ./cmd/server
        if ($LASTEXITCODE -ne 0) {
            throw "go build failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }
}
finally {
    $env:GOOS = $oldGoos
    $env:GOARCH = $oldGoarch
    $env:CGO_ENABLED = $oldCgoEnabled
}

$artifact = Get-Item $binary
Write-Host "Build completed: $($artifact.FullName) ($($artifact.Length) bytes)"
