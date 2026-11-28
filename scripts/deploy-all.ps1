[CmdletBinding(DefaultParameterSetName = 'ByIp')]
param(
    [Parameter(ParameterSetName = 'ByTarget', Mandatory)]
    [ValidatePattern('^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+$')]
    [string]$Target,

    [Parameter(ParameterSetName = 'ByIp')]
    [ValidateScript({
        $address = $null
        if (-not [Net.IPAddress]::TryParse($_, [ref]$address) -or
            $address.AddressFamily -ne [Net.Sockets.AddressFamily]::InterNetwork -or
            $address.ToString() -cne $_) {
            throw 'ServerIp must be an IPv4 address such as 203.0.113.10'
        }
        $true
    })]
    [string]$ServerIp
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'deploy-target.ps1')

if ($ServerIp) {
    $Target = "$defaultSshUser@$ServerIp"
} elseif (-not $Target) {
    $Target = $defaultDeployTarget
}

$out = Join-Path (Split-Path -Parent $PSScriptRoot) 'bin'
foreach ($artifact in 'sonoryx-server', 'sonoryx-webgateway-linux-amd64.tar.gz') {
    if (-not (Test-Path -LiteralPath (Join-Path $out $artifact) -PathType Leaf)) {
        throw "Missing $artifact. Run npm run build-all first"
    }
}

& (Join-Path $PSScriptRoot 'deploy-server.ps1') -Target $Target
& (Join-Path $PSScriptRoot 'deploy-webgateway.ps1') -Target $Target
