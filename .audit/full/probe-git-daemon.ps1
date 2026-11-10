$ErrorActionPreference = 'Stop'
$auditRoot = Split-Path -Parent $PSCommandPath
$fixtureRoot = Join-Path $auditRoot ('git-daemon-fixture-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $fixtureRoot | Out-Null

foreach ($repoName in @('intended-demo', 'private-sibling')) {
    $repoPath = Join-Path $fixtureRoot $repoName
    New-Item -ItemType Directory -Path $repoPath | Out-Null
    git init --quiet $repoPath
    if ($LASTEXITCODE -ne 0) { throw 'git init failed' }
    Set-Content -LiteralPath (Join-Path $repoPath 'audit.txt') -Value 'AUDIT-SYNTHETIC-ONLY'
    git -C $repoPath add audit.txt
    git -C $repoPath -c user.name=Audit -c user.email=audit@example.invalid commit --quiet -m 'Synthetic audit fixture'
    if ($LASTEXITCODE -ne 0) { throw 'git commit failed' }
}

$portReservation = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
$portReservation.Start()
$auditPort = $portReservation.LocalEndpoint.Port
$portReservation.Stop()
$gitExecutable = (Get-Command git).Source
$daemon = $null
try {
    $daemon = Start-Process -FilePath $gitExecutable -ArgumentList @(
        'daemon', '--reuseaddr', '--export-all', ('--base-path=' + $fixtureRoot),
        '--listen=127.0.0.1', ('--port=' + $auditPort)
    ) -PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $fixtureRoot 'daemon.out') -RedirectStandardError (Join-Path $fixtureRoot 'daemon.err')
    $ready = $false
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        if ($daemon.HasExited) { throw 'Fixture git daemon exited' }
        $probe = [System.Net.Sockets.TcpClient]::new()
        try { $probe.Connect('127.0.0.1', $auditPort); $ready = $true } catch {} finally { $probe.Dispose() }
        if ($ready) { break }
        Start-Sleep -Milliseconds 100
    }
    if (-not $ready) { throw 'Fixture git daemon did not become ready' }
    $received = Join-Path $fixtureRoot 'received-private-sibling'
    git clone --quiet ('git://127.0.0.1:' + $auditPort + '/private-sibling') $received
    if ($LASTEXITCODE -ne 0) { throw 'Fixture sibling clone failed' }
    $value = Get-Content -LiteralPath (Join-Path $received 'audit.txt') -Raw
    if ($value.Trim() -ne 'AUDIT-SYNTHETIC-ONLY') { throw 'Unexpected fixture content' }
    'CONFIRMED: --export-all permits an unauthenticated clone of a synthetic sibling repository.'
    'Safety: bound only to 127.0.0.1; base path contains newly generated synthetic repositories only.'
} finally {
    if ($null -ne $daemon -and -not $daemon.HasExited) {
        Stop-Process -Id $daemon.Id -Force
        $daemon.WaitForExit()
    }
}
