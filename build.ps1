# Baut die App unter Windows. Die Share-App bekommt -H=windowsgui,
# damit kein Konsolenfenster im Infobereich herumsteht.
# Hinweis: bewusst ohne Sonderzeichen, damit PowerShell 5.1 die Datei
# unabhaengig von der Kodierung lesen kann.
$ErrorActionPreference = "Stop"

New-Item -ItemType Directory -Path bin -Force | Out-Null

Write-Host "baue mss-share.exe (GUI)"
go build -trimpath -ldflags "-s -w -H=windowsgui" -o bin\mss-share.exe ./cmd/share

Write-Host ""
Write-Host "fertig - die Dateien liegen in bin\"
Get-ChildItem bin | Select-Object Name, Length
