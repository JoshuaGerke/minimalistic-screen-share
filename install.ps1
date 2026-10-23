# Minimalistic Screen Share - Installer für Windows
#
# Nutzung (im entpackten Release-Ordner oder im Repo):
#   .\install.ps1                installiert die Binaries aus diesem Ordner
#                                (baut sie zur Not aus dem Quelltext)
#   .\install.ps1 -FromGitHub    lädt die neueste Version von GitHub
#   .\install.ps1 -Autostart     zusätzlich beim Anmelden starten
#   .\install.ps1 -StartNow      am Ende direkt starten
#   .\install.ps1 -Source DIR    Binaries aus DIR nehmen
#
# Einzeiler (ohne Parameter):  irm <raw-url> | iex
# Deinstallieren: uninstall.ps1 oder Windows-Einstellungen > Apps
#
# Hinweis: absichtlich kein param()-Block und kein BOM. Bei "irm | iex"
# würde PowerShell sonst den Skripttext selbst in den ersten Parameter
# binden bzw. das BOM als Kommando interpretieren.

$ErrorActionPreference = "Stop"

$FromGitHub = $false
$Autostart  = $false
$StartNow   = $false
$Source     = ""

for ($i = 0; $i -lt $args.Count; $i++) {
    $key = ($args[$i] -replace "^--?", "").ToLower()
    switch ($key) {
        "fromgithub" { $FromGitHub = $true }
        "autostart"  { $Autostart = $true }
        "startnow"   { $StartNow = $true }
        "source" {
            if ($i + 1 -lt $args.Count) { $i++; $Source = $args[$i] }
        }
        default { Write-Host "unbekannte Option: $($args[$i])" }
    }
}

$AppName    = "Minimalistic Screen Share"
$Version    = "0.1.0"   # wird beim Release automatisch auf die Tag-Version gesetzt
$Repo       = "JoshuaGerke/minimalistic-screen-share"
$InstallDir = Join-Path $env:LOCALAPPDATA "Programs\minimalistic-screen-share"
$ScriptDir  = if ($PSScriptRoot) { $PSScriptRoot } else { (Get-Location).Path }
$Exes       = @("mss-share.exe")

function Schritt($text) { Write-Host "==> $text" }

# Laufende Instanz beenden, sonst ist die exe gesperrt. Ob sie lief, merken
# wir uns – nach dem Update startet sie dann von selbst wieder.
$wasRunning = @(Get-Process mss-share -ErrorAction SilentlyContinue).Count -gt 0
Get-Process mss-share -ErrorAction SilentlyContinue | Stop-Process -Force
if ($wasRunning) { Start-Sleep -Milliseconds 600 }

# --- Quelle bestimmen -------------------------------------------------------

$sourceDir = $Source
if (-not $sourceDir) {
    foreach ($cand in @($ScriptDir, (Join-Path $ScriptDir "bin"))) {
        if (Test-Path (Join-Path $cand "mss-share.exe")) { $sourceDir = $cand; break }
    }
}

if (-not $sourceDir -and -not $FromGitHub -and (Test-Path (Join-Path $ScriptDir "go.mod"))) {
    Schritt "Baue aus dem Quelltext (Go vorausgesetzt)"
    Push-Location $ScriptDir
    try {
        & go build -trimpath -ldflags "-s -w -H=windowsgui" -o bin\mss-share.exe ./cmd/share
        if ($LASTEXITCODE -ne 0) { throw "go build fehlgeschlagen" }
    } finally {
        Pop-Location
    }
    $sourceDir = Join-Path $ScriptDir "bin"
}

if (-not $sourceDir) { $FromGitHub = $true }

if ($FromGitHub) {
    Schritt "Lade die neueste Version von GitHub"
    try {
        $release = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest"
    } catch {
        throw "Kein Release gefunden. Alternativ das Repo klonen und install.ps1 dort ausführen."
    }
    $asset = $release.assets | Where-Object { $_.name -like "*windows*amd64*.zip" } | Select-Object -First 1
    if (-not $asset) { throw "Im Release $($release.tag_name) steckt kein Windows-Paket." }
    $tmp = Join-Path $env:TEMP ("mss-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    $zip = Join-Path $tmp "mss.zip"
    Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $zip
    Expand-Archive -Path $zip -DestinationPath $tmp -Force
    $sourceDir = $tmp
    if ($release.tag_name) { $Version = $release.tag_name -replace "^v", "" }
}

# --- installieren -----------------------------------------------------------

Schritt "Installiere nach $InstallDir"
New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
foreach ($exe in $Exes) {
    $src = Join-Path $sourceDir $exe
    if (Test-Path $src) { Copy-Item -Force $src $InstallDir }
}
foreach ($f in @("uninstall.ps1", "README.md", "LICENSE")) {
    foreach ($cand in @($sourceDir, $ScriptDir)) {
        $p = Join-Path $cand $f
        if (Test-Path $p) { Copy-Item -Force $p $InstallDir; break }
    }
}
if (-not (Test-Path (Join-Path $InstallDir "mss-share.exe"))) {
    throw "mss-share.exe nicht gefunden. Bitte im Release-Ordner oder im Repo ausführen."
}

# --- Verknüpfungen ----------------------------------------------------------

Schritt "Lege Verknüpfungen an"
$shell  = New-Object -ComObject WScript.Shell
$target = Join-Path $InstallDir "mss-share.exe"

$startMenu = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs"
$lnk = $shell.CreateShortcut((Join-Path $startMenu "$AppName.lnk"))
$lnk.TargetPath = $target
$lnk.WorkingDirectory = $InstallDir
$lnk.Description = "Bildschirm und Ton teilen wie in Discord"
$lnk.Save()

if ($Autostart) {
    $startupDir = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs\Startup"
    $autoLnk = $shell.CreateShortcut((Join-Path $startupDir "$AppName.lnk"))
    $autoLnk.TargetPath = $target
    $autoLnk.WorkingDirectory = $InstallDir
    $autoLnk.Save()
    Write-Host "    Autostart aktiviert"
}

# --- Deinstallationseintrag -------------------------------------------------

Schritt "Trage in Apps & Features ein"
$key = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\minimalistic-screen-share"
New-Item -Path $key -Force | Out-Null
Set-ItemProperty $key -Name DisplayName     -Value $AppName
Set-ItemProperty $key -Name DisplayVersion  -Value $Version
Set-ItemProperty $key -Name Publisher       -Value "Joshua Gerke"
Set-ItemProperty $key -Name DisplayIcon     -Value $target
Set-ItemProperty $key -Name UninstallString -Value "powershell.exe -NoProfile -ExecutionPolicy Bypass -File `"$(Join-Path $InstallDir 'uninstall.ps1')`""
Set-ItemProperty $key -Name NoModify        -Value 1 -Type DWord
Set-ItemProperty $key -Name NoRepair        -Value 1 -Type DWord

# --- PATH -------------------------------------------------------------------

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$InstallDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$InstallDir", "User")
    Write-Host "    $InstallDir zum Benutzer-PATH hinzugefügt (gilt in neuen Terminals)"
}

# --- fertig -----------------------------------------------------------------

Write-Host ""
Write-Host "Fertig. Das Symbol sitzt im Infobereich - bei Windows 11 hinter dem Pfeil."
Write-Host "Start über Startmenü oder im Terminal: mss-share"
Write-Host "Deinstallieren: uninstall.ps1"

if ($StartNow -or $wasRunning) {
    Start-Process $target
    if ($wasRunning) { Write-Host "Laufende Instanz wurde neu gestartet." }
}
