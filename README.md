# minimalistic-screen-share

Ein kleines Programm für Räume: Raum-ID und Passwort eingeben, drin. Im Raum sprecht ihr miteinander, teilt eure Bildschirme, schreibt im Chat und schickt euch Dateien. Alles ist Ende-zu-Ende verschlüsselt, ein Konto oder einen Server braucht es nicht. Gebaut fürs Co-Working: läuft still im Infobereich und braucht kaum Leistung.

**Schnellstart:** [GUIDE.md](GUIDE.md)

## Was es kann

- **Räume statt Links:** Raum-ID und Passwort ausmachen, jeder gibt beides ein. Gibt es den Raum noch nicht, legt ihn der Erste damit an.
- **Ende-zu-Ende verschlüsselt:** Aus Raum-ID und Passwort entsteht auf jedem PC derselbe Schlüssel (PBKDF2, 310 000 Runden). Bild, Systemton, Stimme, Chat, Dateien, Zeige-Klicks und Namen gehen nur mit AES-256-GCM verschlüsselt raus. Wer den Raum weiterleitet, sieht nur Größe und Art der Pakete, aber keinen Inhalt.
- **Kein Server nötig:** Wer zuerst da ist, betreibt den Raum in seiner App und macht ihn über einen kostenlosen Cloudflare-Tunnel erreichbar. Geht er, übernimmt nach ein paar Sekunden automatisch ein anderer. Gefunden wird der Raum über ntfy.sh, dort steht nur die Tunnel-Adresse unter einem Hash der Raum-ID.
- **Sprachkanal wie bei Discord:** Opus bis 510 kbit/s in Stereo, Echo- und Rauschunterdrückung, automatische Lautstärke, Sprachaktivierung mit Pegel oder Push-to-Talk, stumm/taub, Lautstärke für jede Person.
- **Bildschirm teilen, alle gleichzeitig:** Aufnahme und H.264 laufen auf der Grafikkarte (Windows: Desktop Duplication + NVENC/AMF/QuickSync, Linux: wf-recorder + VAAPI). Dekodiert wird im Fenster per WebCodecs. 1440p mit 60 fps kostet ein paar Prozent eines Kerns. Auf Wunsch teilst du den Systemton mit, ohne die Stimmen aus dem Raum.
- **Chat und Dateien:** Nachrichten, Links und Bilder aus der Zwischenablage. Dateien bis 2 GB schickst du per Büroklammer oder ziehst sie ins Fenster.
- **Zeigen per 5-fach-Klick:** Wer 5× schnell auf deinen Bildschirm klickt, lässt bei dir an der Stelle einen Ring erscheinen. Das lässt sich abschalten.
- **Overlay:** schwebende Teilnehmerliste, die anzeigt, wer gerade spricht. Unter Windows ist das ein echtes halbtransparentes Fenster.
- **Beim Start automatisch** in einen gemerkten Raum, in den Sprachkanal (auf Wunsch stumm) und den Bildschirm teilen.
- **Rückkopplungsschutz:** Dein eigener Bildschirm und Ton werden bei dir nie abgespielt. Unter Windows nimmt die Aufnahme die App samt Fenster aus, die Stimmen der anderen landen also nicht im geteilten Ton.
- **Diagnosemodus**, der alle 2 s Leistungsdaten ins Log schreibt, und **Update per Knopf**.

## Installation

### Windows

[`MinimalisticScreenShare.exe`](https://github.com/JoshuaGerke/minimalistic-screen-share/releases/latest/download/MinimalisticScreenShare.exe) herunterladen und doppelklicken. Die exe installiert sich ohne Admin-Rechte nach `%LOCALAPPDATA%\Programs\minimalistic-screen-share`, legt einen Startmenü-Eintrag an und öffnet das Fenster. Das Fenster läuft als App-Fenster von Edge, das bei Windows dabei ist.

- **Update:** Einstellungen → „Auf … aktualisieren“, oder die neue exe doppelklicken.
- **Entfernen:** Einstellungen → Apps → Minimalistic Screen Share (oder `mss-share --uninstall`).
- SmartScreen warnt beim ersten Start („Weitere Informationen → Trotzdem ausführen“), weil die exe nicht kostenpflichtig signiert ist.

### Linux

```bash
curl -fsSL https://raw.githubusercontent.com/JoshuaGerke/minimalistic-screen-share/main/install.sh | bash
```

Das legt `mss-share` nach `~/.local/bin` und einen Eintrag ins Anwendungsmenü. Das Fenster braucht Chromium, Brave, Chrome oder Edge. Extras: `--autostart`, `--from-github`, `--bin-dir`. Deinstallieren mit `uninstall.sh` (`--purge` löscht auch die Einstellungen).

| Paket   | Wofür |
|---------|-------|
| `wf-recorder` | H.264-Video über die Grafikkarte |
| `grim`  | Aufnahme unter Wayland (Hyprland/Sway, …) |
| `libpulse` | Systemton über `parec` |
| `waybar` o. ä. | ein Tray-Host, der StatusNotifier versteht |

Autostart unter Hyprland ohne uwsm: `exec-once = mss-share --background`.

### Aus dem Quelltext

```bash
make            # bin/mss-share für das laufende System
make windows    # Cross-Build für Windows
```

Unter Windows baut `.\build.ps1` die GUI-Version. Die Logs landen in `%APPDATA%\minimalistic-screen-share\share.log`.

## Erreichbarkeit

Wenn dein PC den Raum betreibt, steht unter **Einstellungen → Allgemein**:

- **Überall (Standard):** eingebauter Relay hinter einem Cloudflare-Quick-Tunnel. `cloudflared` wird beim ersten Mal einmalig geladen (~40 MB). Der Relay lauscht dabei nur auf 127.0.0.1, die Firewall fragt also nicht nach.
- **Nur im eigenen Netzwerk:** Der Relay lauscht im LAN auf Port 8080–8090, ganz ohne Internet-Dienste außer ntfy fürs Finden.

## Kommandozeile

```bash
mss-share                  # App mit Fenster
mss-share --background     # still starten (Autostart)
mss-share --update         # auf die neueste Version aktualisieren
mss-share --diag           # Diagnosemodus
mss-share --list-monitors  # Monitore auflisten
mss-share --list-windows   # Fenster auflisten
mss-share --portable       # ohne Installation aus diesem Ordner
mss-share --debug          # Log auf die Konsole
mss-share --uninstall      # App entfernen
```

## Technik in drei Sätzen

Die App leitet aus Raum-ID und Passwort einen Verschlüsselungs- und einen Anmeldeschlüssel ab. Der Relay speichert vom Anmeldeschlüssel nur den SHA-256-Wert und verteilt sonst nur verschlüsselte Pakete mit lesbarem 16-Byte-Kopf (Art, Absender, Nummer). Den eigenen Bildschirm verschlüsselt und sendet die App selbst, alles andere (Stimme, Chat, Dateien) das Fenster über WebCrypto, und beide Seiten nutzen dasselbe Paketformat (`internal/e2e`, `internal/web/dist/e2e.js`).

Gegenproben ohne Browser: `node internal/web/e2e_check.cjs` (gleiches Paketformat wie Go) und `node internal/web/audio_core_check.cjs` (Ton-Puffer).

## Grenzen

- Höchstens 16 Teilnehmer pro Raum (Personen und geteilte Bildschirme zusammen).
- Wer den Raum betreibt, schickt alles an alle. Bei vielen gleichzeitig geteilten Bildschirmen zählt sein Upload.
- Dateien erreichen nur, wer beim Senden im Raum ist. Den Chat-Verlauf hält der Betreiber (verschlüsselt), bis der Raum umzieht.
- Ein Passwort lässt sich nicht nachträglich ändern. Neues Passwort heißt neuer Raum, am besten mit neuer ID.
- Wer zuerst mit einem falschen Passwort einen noch leeren Raum betritt, legt ihn mit diesem Passwort an. Die anderen sehen dann „Falsches Passwort“.
- Unter Linux geht der Ton der anderen beim Teilen mit Systemton aus. Sonst würde er mit aufgenommen und käme bei ihnen als Echo zurück.

## Lizenz

MIT, siehe [LICENSE](LICENSE).
