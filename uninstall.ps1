# Minimalistic Screen Share – Deinstallation unter Windows
#
# Nutzung:
#   .\uninstall.ps1            entfernt Programm und Verknüpfungen
#   .\uninstall.ps1 -Purge     entfernt zusätzlich die Einstellungen

param([switch]$Purge)

$ErrorActionPreference = "Stop"

$AppName    = "Minimalistic Screen Share"
$InstallDir = Join-Path $env:LOCALAPPDATA "Programs\minimalistic-screen-share"
$ConfigDir  = Join-Path $env:APPDATA "minimalistic-screen-share"

Get-Process mss-share -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Milliseconds 500

foreach ($p in @(
    (Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs\$AppName.lnk"),
    (Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs\Startup\$AppName.lnk")
)) {
    if (Test-Path $p) {
        Remove-Item -Force $p
        Write-Host "entfernt: $p"
    }
}

$key = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\minimalistic-screen-share"
if (Test-Path $key) {
    Remove-Item -Recurse -Force $key
    Write-Host "entfernt: Eintrag in Apps & Features"
}

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -like "*$InstallDir*") {
    $clean = ($userPath -split ";" | Where-Object { $_ -and ($_ -ne $InstallDir) }) -join ";"
    [Environment]::SetEnvironmentVariable("Path", $clean, "User")
    Write-Host "entfernt: PATH-Eintrag"
}

if (Test-Path $InstallDir) {
    Remove-Item -Recurse -Force $InstallDir
    Write-Host "entfernt: $InstallDir"
}

if ($Purge) {
    if (Test-Path $ConfigDir) {
        Remove-Item -Recurse -Force $ConfigDir
        Write-Host "entfernt: $ConfigDir"
    }
} else {
    Write-Host "Einstellungen bleiben erhalten in $ConfigDir (mit -Purge löschen)."
}

Write-Host "Fertig."
