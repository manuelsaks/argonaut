$ErrorActionPreference = 'Stop'
$auditRoot = Split-Path -Parent $PSCommandPath
$repositoryRoot = (Resolve-Path (Join-Path $auditRoot '../..')).Path
$triage = @{
    G402 = 'Konfigurowalne insecure=true; ryzyko konfiguracji TLS, nie ukryta furtka. Domyslne TLS pozostaje wlaczone.'
    G703 = 'Sciezka pochodzi z lokalnej konfiguracji/env uzytkownika. Nie wykazano sterowania nia przez odpowiedz Argo CD.'
    G304 = 'Oczekiwany odczyt lokalnych plikow lub aktualizacja; ekstrakcja sprawdza zakres sciezki. Brak potwierdzonego zdalnego LFI.'
    G122 = 'Katalog CA jest lokalnym, zaufanym zrodlem konfiguracji; ograniczyc mozliwosc zapisu przez innych uzytkownikow.'
    G204 = 'Przejrzano argumenty i zrodla. Konfigurowalne komendy sa wykonywalna konfiguracja; dane zasobow nie sa wklejane do powloki.'
    G404 = 'math/rand sluzy tylko jitterowi ponowien; nie tworzy tokenow ani kluczy.'
    G115 = 'Konwersje znakow, kolorow, wymiarow PTY lub trybu pliku; brak wykazanej zdalnej korupcji pamieci. Ograniczac wartosci.'
    G110 = 'Rzeczywisty brak limitu rozpakowanych danych. Niskie ryzyko dostepnosci przy kontrolowanym wydaniu; opis F07.'
    G302 = 'Plik jest programem wykonywalnym; tryb 0755 ma uzasadnienie. Nie jest magazynem tokenow.'
    G301 = 'Katalog konfiguracji lub ekstrakcji; brak tokenow w config.toml. Prywatniejsze uprawnienia sa zalecanym utwardzeniem.'
    G306 = 'Argonaut config.toml nie przechowuje tokenu Argo CD. 0644 nie jest samo w sobie wyciekiem tokenu.'
    G104 = 'Pominiety blad I/O/lifecycle. Glownie niezawodnosc; brak sprzatania manifestow opisano osobno w F04.'
    G118 = 'Port-forward ma celowo niezalezny cykl zycia i jawne Stop(); tlo nie oznacza automatycznie braku sprzatania.'
    G103 = 'unsafe.Pointer wskazuje lokalna strukture winsize dla ioctl TIOCGWINSZ; nie znaleziono wejscia adresowego z sieci.'
}
$rows = foreach ($platform in @('windows', 'linux')) {
    $scan = Get-Content -LiteralPath (Join-Path $auditRoot ('gosec-' + $platform + '.json')) -Raw | ConvertFrom-Json
    foreach ($issue in $scan.Issues) {
        [pscustomobject]@{
            Platform = $platform
            Rule = $issue.rule_id
            ScannerSeverity = $issue.severity
            File = $issue.file.Replace($repositoryRoot + '\', '')
            Line = $issue.line
            Details = $issue.details
            Review = $triage[$issue.rule_id]
        }
    }
}
$rows | Export-Csv -LiteralPath (Join-Path $auditRoot 'gosec-triage.csv') -NoTypeInformation -Encoding utf8

$files = git -C $repositoryRoot ls-files
$inventory = foreach ($relativePath in $files) {
    $path = Join-Path $repositoryRoot $relativePath
    if (Test-Path -LiteralPath $path -PathType Leaf) {
        $item = Get-Item -LiteralPath $path
        [pscustomobject]@{
            File = $relativePath
            Length = $item.Length
            SHA256 = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash
        }
    }
}
$inventory | Export-Csv -LiteralPath (Join-Path $auditRoot 'tracked-files-sha256.csv') -NoTypeInformation -Encoding utf8

[pscustomobject]@{
    TrackedFiles = @($inventory).Count
    SastWindows = @($rows | Where-Object Platform -EQ windows).Count
    SastLinux = @($rows | Where-Object Platform -EQ linux).Count
    TriageWithoutExplanation = @($rows | Where-Object { -not $_.Review }).Count
} | Format-List
