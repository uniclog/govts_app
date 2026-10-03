[CmdletBinding()]
param(
    [ValidatePattern('^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+$')]
    [string]$Target = 'admin@82.25.190.126',

    [ValidatePattern('^/[A-Za-z0-9._/-]+$')]
    [string]$RemoteDir = '/opt/govts',

    [ValidatePattern('^$|^/[A-Za-z0-9._/-]+$')]
    [string]$ConfigPath = '',

    [ValidatePattern('^[0-9A-Fa-f:.]+$')]
    [string]$PublicIp = '82.25.190.126',

    [ValidateRange(1, 65535)]
    [int]$VoicePort = 9000,

    [ValidateRange(1, 65535)]
    [int]$MediaPort = 9002,

    [ValidateRange(1, 65535)]
    [int]$MediaMinPort = 20000,

    [ValidateRange(1, 65535)]
    [int]$MediaMaxPort = 20100,

    [ValidatePattern('^[A-Za-z0-9_-]+$')]
    [string]$TmuxSession = 'govts-server',

    # Repeat the previous voice frame in each server -> client datagram.
    [switch]$VoiceRedundancy = $true
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repoRoot = Split-Path -Parent $PSScriptRoot
$outputDir = Join-Path $repoRoot 'bin'
$binary = Join-Path $outputDir 'govts-server'
$remoteBinary = "$RemoteDir/govts-server.upload"
$remoteScriptUpload = "$RemoteDir/redeploy-server.sh.upload"
$remoteScript = "$RemoteDir/redeploy-server.sh"
if ([string]::IsNullOrWhiteSpace($ConfigPath)) {
    $ConfigPath = "$RemoteDir/server.json"
}

if ($VoicePort -eq $MediaPort) {
    throw 'VoicePort and MediaPort must be different'
}
if ($MediaMinPort -gt $MediaMaxPort) {
    throw 'MediaMinPort must not exceed MediaMaxPort'
}
if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) {
    throw "server binary not found: $binary. Run .\scripts\build-server.ps1 first"
}

& scp -- $binary "${Target}:$remoteBinary"
if ($LASTEXITCODE -ne 0) {
    throw "binary upload failed with exit code $LASTEXITCODE"
}

& scp -- (Join-Path $PSScriptRoot 'redeploy-server.sh') "${Target}:$remoteScriptUpload"
if ($LASTEXITCODE -ne 0) {
    throw "redeploy script upload failed with exit code $LASTEXITCODE"
}

$voiceRedundancyValue = if ($VoiceRedundancy) { '1' } else { '0' }
$remoteCommand = "chmod 0755 '$remoteScriptUpload' && mv -f '$remoteScriptUpload' '$remoteScript' && APP_DIR='$RemoteDir' CONFIG='$ConfigPath' PUBLIC_IP='$PublicIp' VOICE_PORT='$VoicePort' MEDIA_PORT='$MediaPort' MEDIA_MIN_PORT='$MediaMinPort' MEDIA_MAX_PORT='$MediaMaxPort' TMUX_SESSION='$TmuxSession' VOICE_REDUNDANCY='$voiceRedundancyValue' '$remoteScript' '$remoteBinary'"
Write-Host "Remote command ($Target): $remoteCommand"
& ssh -- $Target $remoteCommand
if ($LASTEXITCODE -ne 0) {
    throw "remote redeploy failed with exit code $LASTEXITCODE"
}

Write-Host "Deploy completed: $Target ($RemoteDir)"
Write-Host "Console: ssh -t $Target 'tmux attach-session -t $TmuxSession'"
