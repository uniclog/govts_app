[CmdletBinding()]
param(
    [ValidatePattern('^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+$')]
    [string]$Target
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$out = Join-Path (Split-Path -Parent $PSScriptRoot) 'bin'
foreach ($artifact in 'sonoryx-server', 'sonoryx-webgateway-linux-amd64.tar.gz') {
    if (-not (Test-Path -LiteralPath (Join-Path $out $artifact) -PathType Leaf)) {
        throw "Missing $artifact. Run npm run build-all first"
    }
}

$deployOptions = @{}
if ($PSBoundParameters.ContainsKey('Target')) { $deployOptions.Target = $Target }

& (Join-Path $PSScriptRoot 'deploy-server.ps1') @deployOptions
& (Join-Path $PSScriptRoot 'deploy-webgateway.ps1') @deployOptions
