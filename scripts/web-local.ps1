# Runs in Windows PowerShell 5.1 and PowerShell 7.
param(
    [Parameter(Mandatory)]
    [ValidateSet('Prepare', 'Server', 'Gateway', 'Browser')]
    [string]$Mode
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $true
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$localDir = Join-Path $repo 'bin/web-local'
$webRoot = Join-Path $localDir 'web'
$certPath = Join-Path $localDir 'localhost.pem'
$keyPath = Join-Path $localDir 'localhost-key.pem'
$seedPath = Join-Path $localDir 'sonoryx.seed'
$configPath = Join-Path $localDir 'gateway.json'
$stateDir = Join-Path $localDir 'gateway-state'
$mediaPrefix = Join-Path $localDir 'media'

function Initialize-Tls {
    if ((Test-Path -LiteralPath $certPath -PathType Leaf) -and (Test-Path -LiteralPath $keyPath -PathType Leaf)) {
        return
    }

    $openssl = Get-Command openssl.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1 -ExpandProperty Source
    if (-not $openssl) {
        $git = Get-Command git.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1 -ExpandProperty Source
        if ($git) {
            $gitRoot = Split-Path (Split-Path $git -Parent) -Parent
            $openssl = Join-Path $gitRoot 'usr/bin/openssl.exe'
        }
    }
    if (-not $openssl -or -not (Test-Path -LiteralPath $openssl -PathType Leaf)) {
        throw 'OpenSSL is required to generate local TLS files. Use the OpenSSL included in Git for Windows or add openssl.exe to PATH.'
    }

    $temporary = Join-Path $localDir ('.tls-' + [guid]::NewGuid().ToString('N'))
    $requestPath = "$temporary.cnf"
    $certTemporary = "$temporary.pem"
    $keyTemporary = "$temporary.key"
    $request = @(
        '[ req ]'
        'distinguished_name = subject'
        'prompt = no'
        'x509_extensions = local_server'
        '[ subject ]'
        'CN = localhost'
        '[ local_server ]'
        'basicConstraints = critical, CA:FALSE'
        'keyUsage = critical, digitalSignature'
        'extendedKeyUsage = serverAuth'
        'subjectAltName = DNS:localhost, IP:127.0.0.1'
    ) -join [Environment]::NewLine
    try {
        [IO.File]::WriteAllText($requestPath, $request + [Environment]::NewLine, [Text.Encoding]::ASCII)
        & $openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -sha256 -days 180 -config $requestPath -out $certTemporary -keyout $keyTemporary
        if ($LASTEXITCODE -ne 0) { throw 'Could not create the local HTTPS certificate.' }
        Move-Item -LiteralPath $keyTemporary -Destination $keyPath -Force
        Move-Item -LiteralPath $certTemporary -Destination $certPath -Force
    } finally {
        foreach ($file in $requestPath, $certTemporary, $keyTemporary) {
            if (Test-Path -LiteralPath $file) { Remove-Item -LiteralPath $file -Force }
        }
    }
}

function Write-GatewayConfig([string]$ServerKey) {
    $fingerprint = ''
    if (Test-Path -LiteralPath "$mediaPrefix.crt") {
        $fingerprint = node (Join-Path $PSScriptRoot 'web-local-key.cjs') "$mediaPrefix.crt" --tls-fingerprint
        if ($LASTEXITCODE -ne 0) { throw 'Could not read the Sonoryx media certificate fingerprint.' }
    }
    if (Test-Path -LiteralPath $configPath) {
        $cfg = Get-Content -Raw -Encoding UTF8 -LiteralPath $configPath | ConvertFrom-Json
        if ($cfg.serverKey -eq $ServerKey -and $cfg.loginMode -eq 'nickname' -and $cfg.mediaFingerprint -eq $fingerprint) { return }
    } else {
        $cfg = Get-Content -Raw -Encoding UTF8 -LiteralPath (Join-Path $repo 'configs/webgateway.example.json') | ConvertFrom-Json
        $cfg.origin = 'https://127.0.0.1:8443'
        $cfg.server = '127.0.0.1:19000'
        $cfg.tlsCertificate = $certPath
        $cfg.tlsKey = $keyPath
        $cfg.webRoot = $webRoot
        $cfg.stateDir = $stateDir
    }
    $cfg.serverKey = $ServerKey
    $cfg | Add-Member -NotePropertyName loginMode -NotePropertyValue 'nickname' -Force
    $cfg | Add-Member -NotePropertyName mediaFingerprint -NotePropertyValue $fingerprint -Force
    $json = $cfg | ConvertTo-Json -Depth 8
    [System.IO.File]::WriteAllText($configPath, $json + [Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))
}

switch ($Mode) {
    'Prepare' {
        New-Item -ItemType Directory -Force $localDir | Out-Null
        Initialize-Tls
        $serverKey = node (Join-Path $PSScriptRoot 'web-local-key.cjs') $seedPath --create
        if ($LASTEXITCODE -ne 0) { throw 'Could not prepare the local Sonoryx server identity.' }
        Write-GatewayConfig $serverKey
        Push-Location $repo
        try {
            npm.cmd --prefix apps/web ci --ignore-scripts
            if ($LASTEXITCODE -ne 0) { throw 'Could not install web client dependencies.' }
            npm.cmd --prefix apps/web run build -- --outDir $webRoot
            if ($LASTEXITCODE -ne 0) { throw 'Could not build the local web client.' }
        } finally {
            Pop-Location
        }
    }
    'Browser' {
        if (-not (Test-Path -LiteralPath $certPath) -or -not (Test-Path -LiteralPath $configPath)) {
            throw 'Run with -Mode Prepare first.'
        }
        $cfg = Get-Content -Raw -Encoding UTF8 -LiteralPath $configPath | ConvertFrom-Json
        $origin = [uri]$cfg.origin
        if ($origin.Scheme -ne 'https' -or -not $origin.IsLoopback) {
            throw 'Browser mode requires a local HTTPS gateway.'
        }

        $browser = @(
            "$env:ProgramFiles\Microsoft\Edge\Application\msedge.exe"
            "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe"
            "$env:ProgramFiles\Google\Chrome\Application\chrome.exe"
            "${env:ProgramFiles(x86)}\Google\Chrome\Application\chrome.exe"
            "$env:LOCALAPPDATA\Google\Chrome\Application\chrome.exe"
        ) | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Select-Object -First 1
        if (-not $browser) { throw 'Microsoft Edge or Google Chrome is required.' }

        $fingerprint = node (Join-Path $PSScriptRoot 'web-local-key.cjs') $certPath --tls-fingerprint
        if ($LASTEXITCODE -ne 0 -or $fingerprint -notmatch '^(?:[0-9A-F]{2}:){31}[0-9A-F]{2}$') {
            throw 'Could not read the local HTTPS public key fingerprint.'
        }
        $hashBytes = [byte[]]($fingerprint.Split(':') | ForEach-Object { [Convert]::ToByte($_, 16) })
        $pin = [Convert]::ToBase64String($hashBytes)
        $profile = Join-Path $localDir 'browser-profile'
        Start-Process -FilePath $browser -ArgumentList @(
            "--user-data-dir=`"$profile`""
            "--ignore-certificate-errors-spki-list=$pin"
            '--no-first-run'
            $origin.AbsoluteUri
        )
    }
    'Server' {
        Push-Location $repo
        try {
            go run ./apps/server -port 19000 -media-port 19002 -media-identity $mediaPrefix -media-min-port 21200 -media-max-port 21300 -file-port 19001 `
                -file-identity (Join-Path $localDir 'files') -file-dir (Join-Path $localDir 'files-data') -db (Join-Path $localDir 'sonoryx.db') `
                -voice-identity $seedPath -log-file (Join-Path $localDir 'sonoryx.log')
            if ($LASTEXITCODE -ne 0) { throw 'The local Sonoryx server exited with an error.' }
        } finally {
            Pop-Location
        }
    }
    'Gateway' {
        if (-not (Test-Path -LiteralPath $seedPath)) {
            throw 'Run with -Mode Prepare first.'
        }
        if (-not (Test-Path -LiteralPath (Join-Path $webRoot 'index.html')) -or
            -not (Test-Path -LiteralPath $certPath) -or -not (Test-Path -LiteralPath $keyPath)) {
            throw 'Run with -Mode Prepare first.'
        }
        $serverKey = node (Join-Path $PSScriptRoot 'web-local-key.cjs') $seedPath
        if ($LASTEXITCODE -ne 0) { throw 'Could not read the local Sonoryx server identity.' }
        Write-GatewayConfig $serverKey

        Push-Location $repo
        $previousCgo = $env:CGO_ENABLED
        try {
            $env:CGO_ENABLED = '1'
            go run ./apps/webgateway -config $configPath
            if ($LASTEXITCODE -ne 0) { throw 'The local web gateway exited with an error.' }
        } finally {
            $env:CGO_ENABLED = $previousCgo
            Pop-Location
        }
    }
}
