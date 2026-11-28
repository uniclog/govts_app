[CmdletBinding()]
param(
    [ValidatePattern('^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+$')]
    [string]$Target = 'admin@193.187.92.89',

    [ValidatePattern('^/[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$')]
    [string]$RemoteDir = '/opt/sonoryx/webgateway',

    [ValidatePattern('^[A-Za-z0-9_-]+\.service$')]
    [string]$Service = 'sonoryx-webgateway.service',

    [ValidateSet('All', 'Gateway', 'Web')]
    [string]$Component = 'All'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repoRoot = Split-Path -Parent $PSScriptRoot
$out = Join-Path $repoRoot 'bin'
$archive = Join-Path $out 'sonoryx-webgateway-linux-amd64.tar.gz'
$partRoot = ''
$partEntry = ''
switch ($Component) {
    'All' {
        if (-not (Test-Path -LiteralPath $archive -PathType Leaf)) {
            throw 'Web release archive missing. Run npm run build:webgateway first'
        }
    }
    'Gateway' {
        $partRoot = $out
        $partEntry = 'sonoryx-webgateway'
        if (-not (Test-Path -LiteralPath (Join-Path $partRoot $partEntry) -PathType Leaf)) {
            throw 'Gateway binary missing. Run npm run build:gateway first'
        }
    }
    'Web' {
        $partRoot = Join-Path $repoRoot 'apps/web'
        $partEntry = 'dist'
        if (-not (Test-Path -LiteralPath (Join-Path $partRoot 'dist/index.html') -PathType Leaf)) {
            throw 'Web assets missing. Run npm run build:web first'
        }
    }
}

$temporaryArchive = ''
$upload = ''
$localScript = [IO.Path]::GetTempFileName()
try {
    if ($partEntry) {
        $temporaryArchive = [IO.Path]::GetTempFileName()
        & tar -czf $temporaryArchive -C $partRoot $partEntry
        if ($LASTEXITCODE -ne 0) { throw 'Component archive creation failed' }
        $archive = $temporaryArchive
    }
    $remoteUpload = [string](& ssh -- $Target "test -w '$RemoteDir' && test -f '$RemoteDir/webgateway.json' && mktemp -d '$RemoteDir/.upload.XXXXXXXX'")
    if ($LASTEXITCODE -ne 0 -or $remoteUpload -cnotmatch ('^' + [regex]::Escape($RemoteDir) + '/\.upload\.[A-Za-z0-9]{8}$')) {
        throw "Web gateway installation not ready at $RemoteDir. See apps/webgateway/README.md"
    }
    $upload = $remoteUpload
    $scriptContent = [IO.File]::ReadAllText((Join-Path $PSScriptRoot 'redeploy-webgateway.sh')).Replace("`r`n", "`n")
    [IO.File]::WriteAllText($localScript, $scriptContent, [Text.UTF8Encoding]::new($false))
    & scp -- $archive "${Target}:$upload/sonoryx-webgateway-linux-amd64.tar.gz"
    if ($LASTEXITCODE -ne 0) { throw 'web gateway upload failed' }
    & scp -- $localScript "${Target}:$upload/redeploy-webgateway.sh"
    if ($LASTEXITCODE -ne 0) { throw 'web gateway deploy script upload failed' }
    & ssh -t -- $Target "bash '$upload/redeploy-webgateway.sh' '$RemoteDir' '$Service' '$upload/sonoryx-webgateway-linux-amd64.tar.gz' '$($Component.ToLowerInvariant())'"
    if ($LASTEXITCODE -ne 0) { throw 'web gateway deployment failed' }
} finally {
    Remove-Item -LiteralPath $localScript -Force
    if ($temporaryArchive) { Remove-Item -LiteralPath $temporaryArchive -Force }
    if ($upload) {
        & ssh -- $Target "rm -f -- '$upload/redeploy-webgateway.sh' '$upload/sonoryx-webgateway-linux-amd64.tar.gz' && rmdir -- '$upload'"
        if ($LASTEXITCODE -ne 0) { Write-Warning "Could not clean up $upload on $Target" }
    }
}
