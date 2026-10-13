# minimalistic-screen-share

Bildschirm und Systemton teilen wie in Discord, nur klein: ein paar MB groß, ohne eigenes Fenster im Tray, Zuschauer brauchen nur einen Browser.

## Warum

Discord kann Screensharing, aber 1080p/60 gibt es nur mit Nitro, unter Linux ist das Mitschicken von Systemton unter Wayland ein Glücksspiel, und die App zieht gern ein paar hundert MB RAM plus ein eigenes Fenster. Ich wollte was, das im Infobereich sitzt, auf Windows und Arch (auch Omarchy/Hyprland) einfach läuft und sich auf das Nötigste beschränkt: Bildschirm oder Fenster, Ton dazu, Link teilen, fertig.

## Was es kann

- **Feste Sitzungs-ID pro PC** (z. B. `joshua-gerke-4f2a`): Du startest den Stream, die ID bleibt gleich, andere tippen sie ein oder öffnen deinen Link
- **Zuschauer müssen freigegeben werden**: Wer rein will, klopft an – du bekommst eine Benachrichtigung und entscheidest im Panel. Auf Wunsch auch automatisch (ohne Nachfrage)
- **Bildschirm, Monitor oder einzelnes Fenster** streamen, wahlweise mit Mauszeiger
- **Systemton** mitschicken: WASAPI-Loopback unter Windows, Monitor der Standard-Senke unter Linux
- **Pausiert automatisch, wenn niemand zuschaut** (wie Discord) – spart CPU und Traffic
- **Überspringt unveränderte Bilder** – bei statischem Bildschirm geht fast nichts durch die Leitung
- **Verbindungsabbrüche fängt es ab**: Der Host hat 20 Sekunden Reconnect-Fenster, offene Anfragen und Zuschauer bleiben dabei erhalten
- **Dunkle, aufgeräumte Oberfläche** im Discord-Stil, Zuschauer brauchen nur den Link
- Läuft als Tray-App (Windows: Infobereich, Linux: StatusNotifier für waybar & Co.)

## Wie es aussieht

Tray-Menü:

```
Bildschirm streamen
Fenster streamen ▸   Chrome, Steam, Terminal, …
Monitor streamen ▸   Alle Monitore, Monitor 1 (1920×1080), …
Stream stoppen
─────────────────────────
Status: Anfrage von Max
Sitzungs-ID: joshua-gerke-4f2a
Im Browser ansehen
─────────────────────────
Einstellungen…
Beenden
```

Der Viewer im Browser: schwarze Bühne mit dem Stream, oben eine Leiste mit Sitzungs-ID, Status, Ton- und Vollbildknopf, unten eine dünne Zeile mit Auflösung, fps, Datenrate, Ping und Zuschauerzahl. Solange du nicht freigegeben hast, steht dort „Warte auf Freigabe durch den Streamer…“.

## Installation

Fertige Pakete liegen auf der [Release-Seite](https://github.com/JoshuaGerke/minimalistic-screen-share/releases) – oder gleich installieren:

### Windows

PowerShell öffnen:

```powershell
irm https://raw.githubusercontent.com/JoshuaGerke/minimalistic-screen-share/main/install.ps1 | iex
```

Der Installer holt die neueste Version, legt sie unter `%LOCALAPPDATA%\Programs\minimalistic-screen-share` ab und richtet Startmenü-Eintrag, PATH und einen Eintrag in „Apps & Features“ ein. Kein Admin nötig, es wird nichts in die Registry geschrieben außer dem Deinstallations-Eintrag für den eigenen Benutzer.

Alternativ das ZIP von der Release-Seite entpacken und darin `.\install.ps1` starten – der nimmt dann die mitgelieferten Binaries. Liegt der Installer in einem Repo-Checkout, baut er die Binaries selbst (Go vorausgesetzt). `-Autostart` startet die App zusätzlich beim Anmelden, `-StartNow` direkt nach der Installation.

Deinstallieren: `.\uninstall.ps1` (oder Windows-Einstellungen → Apps); `-Purge` löscht auch die Einstellungen.

### Linux

```bash
curl -fsSL https://raw.githubusercontent.com/JoshuaGerke/minimalistic-screen-share/main/install.sh | bash
```

Der Installer legt die Binaries nach `~/.local/bin` und dazu einen Eintrag ins Anwendungsmenü. Extras: `--autostart` (beim Anmelden starten), `--relay` (Relay als systemd-User-Dienst auf Port 8080 einrichten), `--from-github` (Download erzwingen), `--bin-dir` (anderer Zielordner).

Deinstallieren: `uninstall.sh` (`--purge` löscht auch die Einstellungen).

Untermotor für die Aufnahme – einfach per pacman nachinstallieren, falls nicht schon da:

| Paket   | Wofür                                        |
|---------|----------------------------------------------|
| `grim`  | Aufnahme unter Wayland (Hyprland/Sway, …)     |
| `libpulse` | Ton über `parec` (Monitor der Standard-Senke) |
| `slurp` | optional: Bereich zum Aufnehmen mit der Maus auswählen |
| `waybar` o. ä. | irgendein Tray-Host, der StatusNotifier versteht |

Fensteraufnahme läuft unter Hyprland über `hyprctl`. Unter X11 fängt `mss-share` auch ohne Extra-Pakete den Bildschirm ein.

### Arch Linux / Omarchy

Wer es lieber über pacman verwaltet, nimmt das PKGBUILD:

```bash
cd packaging
makepkg -si
mss-share --settings
```

### Aus dem Quelltext

```bash
make            # baut bin/mss-share, bin/mss-relay, bin/mss-viewer für das laufende System
make windows    # oder: Cross-Build für Windows
```

Unter Windows: `.\build.ps1` baut `bin\mss-share.exe` als reine GUI-Anwendung (kein Konsolenfenster); Logs landen in `%APPDATA%\minimalistic-screen-share\share.log`.

## Relay aufsetzen

Irgendwer muss vermitteln. Fürs Heimnetz reicht der Rechner, auf dem auch gestreamt wird:

```bash
mss-relay -addr :8080
```

Für das Internet einen kleinen VPS nehmen, `mss-relay` laufen lassen (systemd-Unit liegt in `packaging/mss-relay.service`) und einen Reverse Proxy davorhängen, damit es `wss://` wird:

```
# Caddy
share.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Im Panel dann `https://share.example.com` als Server eintragen. Der Relay ist absichtlich dumm: Er kennt Räume, schickt Daten stumpf weiter und hat keine Datenbank. Wer den Raumcode kennt, schaut zu – der Code ist das Passwort.

## Benutzung

1. `mss-share` starten – das Symbol sitzt im Infobereich.
2. Rechtsklick (oder Linksklick) → **Einstellungen…** – das Panel öffnet sich im Browser.
3. Server eintragen und die **Sitzungs-ID** prüfen. Sie wird beim ersten Start aus dem Rechnernamen gebaut (z. B. `joshua-gerke-4f2a`) und bleibt danach gleich – damit finden dich andere wieder. „Neu“ würfelt eine andere.
4. **Stream starten** – oder im Tray direkt auf „Bildschirm streamen“.
5. Link oder Sitzungs-ID verschicken. Zuschauer öffnen den Link im Browser und tippen ihren Namen ein.
6. Wenn jemand zuschauen will, **klopft er an**: Du bekommst eine Benachrichtigung, im Panel stehen die Anfragen mit **Annehmen** und **Ablehnen**. Erst danach sieht er dein Bild. Wer immer rein darf, hakt „Zuschauer automatisch freigeben“ an – dann läuft es wie ein offener Raum.

Ton aktivieren Zuschauer bei Bedarf per Knopf (Browser blockieren Ton bis zum ersten Klick).

Einstellungen liegen in `%APPDATA%\minimalistic-screen-share\config.json` (Windows) bzw. `~/.config/minimalistic-screen-share/config.json` (Linux).

## Kommandozeile

Alles, was man zum Testen und für Autostart braucht:

```bash
mss-share                          # Tray-App (Standard)
mss-share --settings               # Panel öffnen (startet die App bei Bedarf)
mss-share --stream                 # ohne Tray direkt losstreamen, Strg+C beendet
mss-share --stream --test-pattern  # mit Testbild statt Bildschirm – gut zum Prüfen
mss-share --snapshot bild.jpg      # ein einzelnes Bild aufnehmen und speichern
mss-share --list-windows           # Fenster auflisten
mss-share --list-monitors          # Monitore auflisten
```

Nützliche Schalter: `--server`, `--room` (Sitzungs-ID), `--fps`, `--quality`, `--width`, `--window`, `--monitor`, `--region x,y,w,h`, `--no-audio`, `--debug`.

Zum Gegenprüfen ohne Browser (klopft an und wartet auf Freigabe wie jeder Zuschauer):

```bash
mss-viewer --server http://localhost:8080 --room deine-sitzungs-id --name Max --save latest.jpg
```

Zählt fps, Datenrate und Tonpakete und schreibt das jeweils letzte Bild nach `latest.jpg`.

## Technik in drei Sätzen

Aufnahme und Ton werden im Client zu JPEG-Bildern und rohem PCM verpackt und über einen WebSocket zum Relay geschoben, der sie an alle freigegebenen Zuschauer im Raum verteilt; im Browser malt ein Canvas die Bilder und eine kleine WebAudio-Pipeline puffert den Ton. Zuschauer landen erst in einer Warteliste und klopfen beim Host an – der Relay merkt sich den Raum, die Sitzungs-ID und offene Anfragen, bis der Host freigibt (oder automatisch durchwinkt). JPEG statt H.264 ist Absicht: keine Encoder-Abhängigkeiten, jedes Bild ist ein Keyframe, spät dazugekommene Zuschauer haben sofort ein Bild.

## Grenzen

- Ton ist unkomprimiertes PCM. Stille kostet fast nichts, laute Musik etwa 1,5 Mbit/s. Opus wäre der nächste Schritt.
- Fensteraufnahme unter Windows läuft über `PrintWindow`. Bei dem einen oder anderen Spiel kommt da nur Schwarz an – dann wird auf den sichtbaren Bildschirmbereich des Fensters ausgewichen, das Fenster sollte also vorn sein.
- Unter Wayland braucht die Aufnahme `grim`; Fensteraufnahme setzt Hyprland voraus (`hyprctl`). X11 und Bildschirm/Bereich unter Wayland klappen ohne Hyprland.
- Keine Ende-zu-Ende-Verschlüsselung. Fürs Internet TLS über den Reverse Proxy und den Relay selbst betreiben, dem du traust.
- Kein Zuschauer-Limit jenseits von `-max-viewers` am Relay (Standard 8); maximal 20 offene Anfragen pro Raum.
- Die Sitzungs-ID ist wie ein Discord-Name plus Passwort in einem: Wer sie kennt, kann anfragen – annehmen musst trotzdem du.

## Roadmap (mal sehen)

- Opus-Ton statt PCM
- effizientere Frames (WebP oder VP8) für Bewegung
- Bereichsauswahl unter Windows
- Autostart-Schalter im Panel (bisher nur über die Installer)
- AUR-Paket

## Lizenz

MIT – siehe [LICENSE](LICENSE).
