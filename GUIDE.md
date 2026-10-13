# In fünf Minuten loslegen

Diese Anleitung deckt genau einen Fall ab: **zwei Leute, zwei Rechner** – einer Windows, einer Arch/Omarchy. Beide können streamen und zuschauen.

## 1. Einmal installieren

**Du (Windows)** – PowerShell aufmachen:

```powershell
irm https://raw.githubusercontent.com/JoshuaGerke/minimalistic-screen-share/main/install.ps1 | iex
```

Danach sitzt das Symbol im Infobereich (bei Windows 11 hinter dem kleinen Pfeil `^`).

**Dein Kumpel (Omarchy/Arch)** – Terminal:

```bash
curl -fsSL https://raw.githubusercontent.com/JoshuaGerke/minimalistic-screen-share/main/install.sh | bash
sudo pacman -S grim slurp libpulse    # falls noch nicht da (Aufnahme + Ton + Bereichsauswahl)
mss-share --settings                  # startet und öffnet das Panel
```

## 2. Verbindung herstellen (einmal pro Sitzung)

Der Relay läuft schon im Client mit – es fehlt nur ein Weg ins Internet. **Einer von euch beiden ist der Treffpunkt**, am besten der, dessen PC sowieso an bleibt:

1. Panel öffnen: Windows → Rechtsklick aufs Symbol → **Einstellungen…** · Omarchy → `mss-share --settings`
2. Häkchen bei **„Im Internet erreichbar machen (Tunnel)“**
3. Kurz warten – beim ersten Mal lädt er ~40 MB. Dann steht darunter: „Öffentlich erreichbar unter `https://…trycloudflare.com`“
4. Diese Adresse kopieren und dem anderen schicken

**Der andere** trägt sie einmal im eigenen Panel bei **Server** ein und speichert. Fertig – die Adresse bleibt, solange der Tunnel läuft.

> **Im selben WLAN** braucht ihr keinen Tunnel: Im Panel steht dann „Relay läuft auf `http://192.168.…`“ – den Link könnt ihr direkt benutzen, null Konfiguration.

## 3. Deinen Bildschirm mit Ton streamen

1. Panel → **Stream starten** (Ziel steht auf Hauptbildschirm) – oder Tray → „Bildschirm streamen“
2. Im Panel beim Link **Kopieren** drücken und dem Kumpel schicken
3. Er öffnet den Link im Browser, tippt seinen Namen ein und drückt **Beitreten**
4. Bei dir kommt eine Meldung → im Panel unter **Anfragen** auf **Annehmen** klicken

Läuft. Beim Zuschauer einmal auf den Lautsprecher-Knopf klicken, sonst bleibt der Browser stumm (Browser blockieren Ton bis zum ersten Klick). Wenn dich niemand nerven soll: Haken bei **„Zuschauer automatisch freigeben“**, dann fällt Schritt 4 weg.

## 4. Dein Kumpel streamt (gleiches Spiel rückwärts)

1. Er drückt in seinem Panel **Stream starten** – unter Omarchy/Hyprland nimmt er den Monitor auf, der gerade fokussiert ist
2. Er kopiert seinen Link (gleiche Adresse wie oben, nur seine Sitzungs-ID) und schickt ihn dir
3. Du öffnest den Link, tippst deinen Namen ein, er nimmt an – fertig

## Ton ausnehmen und fein einstellen

Während eines Streams steht im Panel die Karte **Ton-Quellen**: Dort tauchen alle Programme auf, die gerade Ton spielen. Ein Haken schaltet das Programm **während des Streams bei dir stumm** (praktisch für Discord & Co.) – beim Stoppen wird alles automatisch zurückgestellt.

**Wenn ein Programm bei dir weiter hörbar sein, aber nicht im Stream auftauchen soll** (z. B. Discord-Benachrichtigungen): Windows-Einstellungen → System → Sound → **Lautstärkemixer** → beim Programm **„Von Aufnahme ausschließen“** wählen. Der Knopf „Lautstärkemixer öffnen“ im Panel bringt dich direkt hin. Die Einstellung bleibt gespeichert.

## Klar und flüssig: realistische Einstellungen

- **Im WLAN**: 1920er Breite, 24–30 fps, Qualität „Hoch“ – hier geht es über die volle Leitung und sieht top aus.
- **Über den Tunnel** (trycloudflare): 1280–1600 Breite, 10–15 fps, Qualität „Mittel“. Der Tunnel ist der Flaschenhals, nicht euer Internet – echte 1080p bei 30 fps bräuchten dort stabil 20+ Mbit/s. Der Client bremst sich bei Engpässen inzwischen selbst runter und lässt dabei **Video fallen, nie Ton**.
- **Ein Monitor statt zweier**: Im Panel unter Ziel **Monitor** den gewünschten auswählen – „Hauptbildschirm“ nimmt unter Hyprland den fokussierten. „Alle Monitore“ nimmt den kompletten Desktop auf, das ist deutlich langsamer (und über einen Tunnel nie flüssig).

## Updates

Einfach denselben Installer noch mal laufen lassen – läuft die App dabei, wird sie beendet, aktualisiert und **automatisch neu gestartet**:

```powershell
irm https://raw.githubusercontent.com/JoshuaGerke/minimalistic-screen-share/main/install.ps1 | iex
```

bzw.

```bash
curl -fsSL https://raw.githubusercontent.com/JoshuaGerke/minimalistic-screen-share/main/install.sh | bash
```

## Wenn was klemmt

| Problem | Lösung |
|---|---|
| „Warte auf Freigabe…“ hängt | Der Streamer muss im Panel unter **Anfragen** auf **Annehmen** klicken |
| Kein Ton | Im Viewer erscheint unten ein **gelber Hinweis** – einmal irgendwo auf die Seite klicken, dann läuft der Ton (Browser-Regel). Beim Streamer muss „Systemton streamen“ angehakt sein |
| Ton knistert oder rauscht | Seite im Browser neu laden (F5) – seit v0.4.0 nimmt der Viewer die Tonrate des Geräts selbst, das behebt Knistern auf PipeWire-Systemen |
| „Ton –“ in der Statuszeile | Der Streamer sendet gar keinen Ton. Auf Linux fehlt dann meist parec: `sudo pacman -S libpulse` |
| Zwei Bildschirme im Stream | Im Panel unter Ziel **Monitor** den einzelnen auswählen (nicht „Alle Monitore“) |
| „Stream nicht gefunden“ | Der andere hat noch nicht auf **Stream starten** gedrückt (die Sitzungs-ID allein reicht nicht) |
| Omarchy meldet „grim fehlt“ | `sudo pacman -S grim` |
| Bild ruckelig oder viel Traffic | Im Panel „Bilder pro Sekunde“ auf 10 und „Maximale Breite“ auf 1280 |
| Tunnel-Adresse ändert sich | Sie bleibt, solange der Tunnel läuft. Nach einem Neustart einfach die neue Adresse einmal neu eintragen |
| Update gemacht, App läuft noch alt | Einfach den Installer noch mal laufen lassen – er beendet die App, aktualisiert und startet sie neu |

## Firewall

Ihr braucht **keine eingehenden Ports** – beide Seiten bauen nur Verbindungen nach außen auf (zum Relay bzw. Tunnel). Das gilt auch für Omarchy/Arch mit ufw oder nftables: Standard-Regeln blocken eingehend, ausgehend ist alles frei. Der Ton läuft über dieselbe Verbindung, dafür ist nie ein Port nötig.

Zwei Ausnahmen, beide betreffen nur den **Direktbetrieb im WLAN ohne Tunnel**:

- **Windows**: Beim ersten Start fragt die Firewall, ob `mss-share` ins Netzwerk darf → **Erlauben**. Wurde die Meldung weggeklickt: Windows-Sicherheit → Firewall und Netzwerkschutz → „Eine App durch die Firewall lassen“ → bei `mss-share` beide Haken setzen.
- **Linux mit aktiver Firewall**: Dort muss Port **8080 eingehend** offen sein, wenn Freunde direkt (ohne Tunnel) auf den eingebauten Relay zugreifen sollen – z. B. `sudo ufw allow 8080/tcp`.

## Was macht der Tunnel?

Er lädt einmalig [cloudflared](https://github.com/cloudflare/cloudflared) von Cloudflare und lässt es den lokalen Relay unter einer zufälligen `trycloudflare.com`-Adresse veröffentlichen – ohne Konto, ohne Portfreigabe am Router. Der Tunnel läuft nur, solange der Haken gesetzt ist. Wenn dir fremde Infrastruktur nicht passt: eigener Relay auf einem VPS oder LAN-only, beides steht im [README](README.md#relay-aufsetzen).
