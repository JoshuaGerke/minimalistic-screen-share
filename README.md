# minimalistic-screen-share

Bildschirm und Systemton teilen, so einfach wie AnyDesk, nur ohne Fernsteuerung: exe doppelklicken, „Teilen starten“, Link in den Chat. Zuschauer brauchen nur einen Browser. Gebaut für Co-Working: läuft still im Infobereich, ist ein paar MB groß und lässt sich jederzeit pausieren.

**Schnellstart:** [GUIDE.md](GUIDE.md)

## Warum

Discord kann Screensharing, aber 1080p gibt es nur mit Nitro, unter Linux ist Systemton unter Wayland ein Glücksspiel, und die App zieht ein paar hundert MB RAM. AnyDesk & Co. wollen Fernsteuerung und Konten. Ich wollte was, das im Infobereich sitzt, auf Windows und Arch (auch Omarchy/Hyprland) einfach läuft und nur eins macht: Bild und Ton teilen, Link schicken, fertig.

## Was es kann

- **Ein Klick, ein Link**: „Teilen starten“ legt den Link direkt in die Zwischenablage. Er funktioniert überall – die App bringt ihren eigenen Server mit und stellt ihn über einen kostenlosen Cloudflare-Tunnel ins Netz. Kein Konto, kein Router, keine Ports.
- **Installation per Doppelklick**: eine exe, installiert sich selbst ohne Admin-Rechte. Update = neue exe doppelklicken.
- **Anklopfen wie bei Discord**: Wer zuschauen will, klopft an, du klickst „Reinlassen“. Wer einmal drin war, kommt den ganzen Tag ohne neues Anklopfen wieder rein. Auf Wunsch alle sofort reinlassen.
- **Bildschirm, Monitor oder einzelnes Fenster**, Quelle wechselbar mitten im Stream
- **Pause-Knopf** für private Momente – Zuschauer sehen „Pausiert“, die Verbindung bleibt
- **Alle Einstellungen live**: Bildrate (5–60), Qualität, Auflösung (bis Original), Mauszeiger, Ton – ohne Neustart. Vier Voreinstellungen von „Sparsam“ bis „Flüssig“.
- **Ton ohne Knacken**: Ringpuffer mit durchgehender Umrechnung im AudioWorklet, passt sich an Netzschwankungen und Taktunterschiede der Soundkarten an. Bei engem Upload gehen Bilder verloren, nie der Ton.
- **Rückkopplungsschutz**: Schaut man auf dem PC zu, der gerade streamt, bleibt der Viewer automatisch stumm.
- **Viewer** mit Lautstärke, Bild-in-Bild (schwebt über anderen Fenstern), Vollbild, Originalgröße 1:1, Verbindungsdaten und Tastenkürzeln. Hört der Streamer auf, verbindet sich der Viewer von selbst wieder, sobald er weitermacht.
- **Pausiert automatisch, wenn niemand zuschaut** – spart CPU und Traffic. Unveränderte Bilder werden gar nicht erst verschickt.
- Programme beim Teilen **stummschalten** (z. B. Discord), Autostart per Schalter
- Läuft als Tray-App (Windows: Infobereich, Linux: StatusNotifier für waybar & Co.)

## Wie es aussieht

Tray-Menü:

```
Bildschirm teilen
Fenster teilen ▸     Chrome, Steam, Terminal, …
Monitor teilen ▸     Alle Monitore, Monitor 1 (1920×1080), …
Pausieren
Teilen beenden
─────────────────────────
Status: Live – 2 Zuschauer
Link kopieren
Eigenen Stream ansehen
─────────────────────────
Öffnen…
Sitzungs-ID: joshua-4f2a
Beenden
```

Das Fenster (öffnet sich im Browser): oben die Quellen als Kacheln und der große „Teilen starten“-Knopf, darunter dein Link mit Kopieren-Knopf, Anfragen erscheinen ganz oben gelb umrandet. Die Einstellungen speichern sich selbst.

Der Viewer: Das Bild füllt das ganze Fenster, die Leiste oben blendet sich aus, wenn die Maus ruht.

## Installation

### Windows

[`MinimalisticScreenShare.exe`](https://github.com/JoshuaGerke/minimalistic-screen-share/releases/latest/download/MinimalisticScreenShare.exe) herunterladen und doppelklicken. Die exe kopiert sich nach `%LOCALAPPDATA%\Programs\minimalistic-screen-share`, legt einen Startmenü-Eintrag und einen Eintrag unter „Apps“ an und startet von dort. Kein Admin, keine Dienste.

- **Update:** neue exe doppelklicken – die laufende Version wird beendet und ersetzt.
- **Entfernen:** Einstellungen → Apps → Minimalistic Screen Share (oder `mss-share --uninstall`). Die Einstellungen bleiben unter `%APPDATA%\minimalistic-screen-share` liegen.
- **Ohne Installation:** `mss-share.exe --portable`
- SmartScreen meckert beim ersten Start („Weitere Informationen → Trotzdem ausführen“), weil die exe nicht kostenpflichtig signiert ist.

Für Skript-Fans geht weiterhin `irm https://raw.githubusercontent.com/JoshuaGerke/minimalistic-screen-share/main/install.ps1 | iex` (installiert zusätzlich `mss-relay` und `mss-viewer` in den PATH).

### Linux

```bash
curl -fsSL https://raw.githubusercontent.com/JoshuaGerke/minimalistic-screen-share/main/install.sh | bash
```

Der Installer legt die Binaries nach `~/.local/bin` und einen Eintrag ins Anwendungsmenü. Extras: `--autostart`, `--relay` (Relay als systemd-User-Dienst auf Port 8080), `--from-github`, `--bin-dir`. Deinstallieren: `uninstall.sh` (`--purge` löscht auch die Einstellungen).

| Paket   | Wofür |
|---------|-------|
| `grim`  | Aufnahme unter Wayland (Hyprland/Sway, …) |
| `libpulse` | Ton über `parec` (Monitor der Standard-Senke) |
| `wl-clipboard` | Link automatisch in die Zwischenablage (X11: `xclip`) |
| `slurp` | optional: Bereich mit der Maus auswählen |
| `waybar` o. ä. | irgendein Tray-Host, der StatusNotifier versteht |

Fensteraufnahme läuft unter Hyprland über `hyprctl`. Unter X11 klappt die Aufnahme ohne Extra-Pakete. Autostart unter Hyprland ohne uwsm: `exec-once = mss-share --background`.

### Arch Linux / Omarchy

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

Unter Windows: `.\build.ps1` baut `bin\mss-share.exe` als reine GUI-Anwendung; Logs landen in `%APPDATA%\minimalistic-screen-share\share.log`. Builds aus dem Repo installieren sich nicht selbst.

## Verbindung

Im Fenster unter **Einstellungen → Verbindung**:

- **Überall (Standard):** eingebauter Relay plus Cloudflare-Quick-Tunnel. Beim ersten Start wird `cloudflared` einmalig geladen (~40 MB). Der Link gilt, solange die App läuft; bricht der Tunnel weg, baut die App ihn selbst neu auf und legt den neuen Link in die Zwischenablage.
- **Nur im selben Netzwerk:** eingebauter Relay, Link zeigt auf die LAN-Adresse. Unter Linux mit Firewall muss dafür Port 8080 eingehend offen sein (`sudo ufw allow 8080/tcp`).
- **Eigener Server:** siehe unten.

### Eigener Server

Für einen festen Link auf einem VPS: `mss-relay` laufen lassen (systemd-Unit in `packaging/mss-relay.service`) und einen Reverse Proxy davorhängen:

```
# Caddy
share.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Im Fenster dann **Eigener Server** wählen und `https://share.example.com` eintragen. Der Relay ist absichtlich dumm: Er kennt Räume, schickt Daten weiter und hat keine Datenbank. Wer die Sitzungs-ID kennt, kann anklopfen – zuschauen darf nur, wen du reinlässt.

## Kommandozeile

```bash
mss-share                          # Tray-App (Standard)
mss-share --settings               # Fenster öffnen (startet die App bei Bedarf)
mss-share --background             # still starten, kein Fenster (Autostart)
mss-share --stream                 # ohne Tray direkt losstreamen, Link steht im Log, Strg+C beendet
mss-share --stream --test-pattern  # mit Testbild statt Bildschirm
mss-share --snapshot bild.jpg      # ein einzelnes Bild aufnehmen
mss-share --list-windows           # Fenster auflisten
mss-share --list-monitors          # Monitore auflisten
mss-share --uninstall              # App entfernen (Windows)
```

Weitere Schalter: `--server`, `--room`, `--fps`, `--quality`, `--width`, `--window`, `--monitor`, `--region x,y,w,h`, `--no-audio`, `--portable`, `--debug`.

Zum Gegenprüfen ohne Browser (klopft an wie jeder Zuschauer):

```bash
mss-viewer --server http://localhost:8080 --room deine-sitzungs-id --name Max --save latest.jpg
```

## Technik in drei Sätzen

Aufnahme und Ton werden im Client zu JPEG-Bildern und rohem PCM verpackt und über einen WebSocket zum Relay geschoben; eine eigene Schreibschleife schickt Ton immer zuerst, Bilder in 32-KB-Stücken dazwischen, und vom Bild zählt nur das neueste. Der Relay verteilt alles an freigegebene Zuschauer und reicht später Dazukommenden die Stream-Beschreibung (Tonrate, Pause) nach. Im Browser malt ein Canvas die Bilder, und ein AudioWorklet spielt den Ton aus einem selbstregelnden Ringpuffer.

Den Ton-Kern prüft `node internal/web/audio_core_check.cjs` ohne Browser (Taktdrift, Netz-Hänger, Knacken).

## Grenzen

- Ton ist unkomprimiertes PCM: Stille kostet fast nichts, laute Musik etwa 1,5 Mbit/s. Opus wäre der nächste Schritt.
- JPEG statt H.264: Jedes Bild ist ein Keyframe (Zuschauer haben sofort ein Bild), bei viel Bewegung kostet das aber Bandbreite. Für Videos lieber 1280 px und „Flüssig“.
- Der Quick-Tunnel-Link ändert sich bei jedem Neustart der App. Für einen festen Link: eigener Server.
- Fensteraufnahme unter Windows läuft über `PrintWindow`; bei manchen Spielen kommt nur Schwarz – dann wird der sichtbare Bildschirmbereich des Fensters genommen.
- Keine Ende-zu-Ende-Verschlüsselung. Über den Tunnel ist die Verbindung per TLS geschützt, Cloudflare sieht den Inhalt aber.
- Höchstens 16 Zuschauer pro Stream (eingebauter Relay), 20 offene Anfragen.

## Lizenz

MIT – siehe [LICENSE](LICENSE).
