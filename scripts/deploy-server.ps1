[CmdletBinding()]
param(
    [ValidatePattern('^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+$')]
    [string]$Target,

    [ValidatePattern('^/[A-Za-z0-9._/-]+$')]
    [string]$RemoteDir = '/opt/sonoryx',

    [ValidatePattern('^$|^/[A-Za-z0-9._/-]+$')]
    [string]$ConfigPath = '',

    [ValidatePattern('^$|^/[A-Za-z0-9._/-]+$')]
    [string]$DatabasePath = '',

    [ValidatePattern('^[0-9A-Fa-f:.]+$')]
    [string]$PublicIp,

    [ValidateRange(1, 65535)]
    [int]$VoicePort = 9000,

    [ValidateRange(1, 65535)]
    [int]$MediaPort = 9002,

    # File sharing is enabled by default; use -FilePort 0 to disable it.
    [ValidateRange(0, 65535)]
    [int]$FilePort = 9001,

    [ValidateRange(1, 65535)]
    [int]$MediaMinPort = 20000,

    [ValidateRange(1, 65535)]
    [int]$MediaMaxPort = 20100,

    [ValidatePattern('^[A-Za-z0-9_-]+$')]
    [string]$TmuxSession = 'sonoryx-server',

    # Enable adaptive voice protection from server to clients.
    # Disable with -VoiceRedundancy:$false.
    [switch]$VoiceRedundancy = $true,

    # Enable adaptive voice protection from clients to server.
    # Disable with -VoiceUplinkRedundancy:$false.
    [switch]$VoiceUplinkRedundancy = $true,

    # Expose the connected session count without authentication.
    [switch]$PublicStatus  = $true,

    # Estimate viewer rates, cap the screen source with REMB and pace viewer RTP.
    # Disable with -MediaAdaptive:$false.
    [switch]$MediaAdaptive = $true
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'deploy-target.ps1')

if (-not $Target) { $Target = $defaultDeployTarget }
if (-not $PublicIp) {
    $deployHost = ($Target -split '@', 2)[1]
    $address = $null
    $PublicIp = $defaultServerIp
    if ([Net.IPAddress]::TryParse($deployHost, [ref]$address) -and $address.ToString() -ceq $deployHost) {
        $PublicIp = $deployHost
    }
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$outputDir = Join-Path $repoRoot 'bin'
$binary = Join-Path $outputDir 'sonoryx-server'
$remoteBinary = "$RemoteDir/sonoryx-server.upload"
$remoteScriptUpload = "$RemoteDir/redeploy-server.sh.upload"
$remoteScript = "$RemoteDir/redeploy-server.sh"
if ([string]::IsNullOrWhiteSpace($ConfigPath)) {
    $ConfigPath = "$RemoteDir/server.json"
}
if ([string]::IsNullOrWhiteSpace($DatabasePath)) {
    $DatabasePath = "$RemoteDir/sonoryx.db"
}

if ($VoicePort -eq $MediaPort) {
    throw 'VoicePort and MediaPort must be different'
}
if ($FilePort -ne 0 -and $FilePort -eq $MediaPort) {
    throw 'FilePort and MediaPort must be different TCP ports'
}
if ($MediaMinPort -gt $MediaMaxPort) {
    throw 'MediaMinPort must not exceed MediaMaxPort'
}
if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) {
    throw "server binary not found: $binary. Run .\scripts\build-server.ps1 first"
}

# Check the existing installation before uploading or stopping anything.
$preflightCommand = "test -d '$RemoteDir' && test -w '$RemoteDir' && test -f '$ConfigPath'"
& ssh -- $Target $preflightCommand
if ($LASTEXITCODE -ne 0) {
    throw "remote installation missing or not writable: $RemoteDir (config: $ConfigPath). For a legacy installation, pass -RemoteDir /opt/govts -TmuxSession govts-server."
}

& scp -- $binary "${Target}:$remoteBinary"
if ($LASTEXITCODE -ne 0) {
    throw "binary upload failed with exit code $LASTEXITCODE"
}

$localScript = Join-Path $PSScriptRoot 'redeploy-server.sh'
$normalizedScript = [System.IO.Path]::GetTempFileName()
try {
    # Linux requires LF line endings and a shebang without a UTF-8 BOM.
    $scriptContent = [System.IO.File]::ReadAllText($localScript).Replace("`r`n", "`n").Replace("`r", "`n")
    [System.IO.File]::WriteAllText($normalizedScript, $scriptContent, [System.Text.UTF8Encoding]::new($false))
    & scp -- $normalizedScript "${Target}:$remoteScriptUpload"
    if ($LASTEXITCODE -ne 0) {
        throw "redeploy script upload failed with exit code $LASTEXITCODE"
    }
} finally {
    Remove-Item -LiteralPath $normalizedScript -Force
}

$voiceRedundancyValue = if ($VoiceRedundancy) { '1' } else { '0' }
$voiceUplinkRedundancyValue = if ($VoiceUplinkRedundancy) { '1' } else { '0' }
$publicStatusValue = if ($PublicStatus) { '1' } else { '0' }
$mediaAdaptiveValue = if ($MediaAdaptive) { '1' } else { '0' }
$remoteCommand = "chmod 0755 '$remoteScriptUpload' && mv -f '$remoteScriptUpload' '$remoteScript' && APP_DIR='$RemoteDir' CONFIG='$ConfigPath' DB_PATH='$DatabasePath' PUBLIC_IP='$PublicIp' VOICE_PORT='$VoicePort' MEDIA_PORT='$MediaPort' FILE_PORT='$FilePort' MEDIA_MIN_PORT='$MediaMinPort' MEDIA_MAX_PORT='$MediaMaxPort' TMUX_SESSION='$TmuxSession' VOICE_REDUNDANCY='$voiceRedundancyValue' VOICE_UPLINK_REDUNDANCY='$voiceUplinkRedundancyValue' PUBLIC_STATUS='$publicStatusValue' MEDIA_ADAPTIVE='$mediaAdaptiveValue' '$remoteScript' '$remoteBinary'"
Write-Host "Remote command ($Target): $remoteCommand"
& ssh -- $Target $remoteCommand
if ($LASTEXITCODE -ne 0) {
    throw "remote redeploy failed with exit code $LASTEXITCODE"
}

Write-Host "Deploy completed: $Target ($RemoteDir)"
Write-Host "Console: ssh -t $Target 'tmux attach-session -t $TmuxSession'"
