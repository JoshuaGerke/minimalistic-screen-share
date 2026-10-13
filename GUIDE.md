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

1. Er drückt in seinem Panel **Stream starten** – unter Omarchy/Hyprland nimmt er den fokussierten Monitor und den Systemton auf
2. Er kopiert seinen Link (gleiche Adresse wie oben, nur seine Sitzungs-ID) und schickt ihn dir
3. Du öffnest den Link, tippst deinen Namen ein, er nimmt an – fertig

## Wenn was klemmt

| Problem | Lösung |
|---|---|
| „Warte auf Freigabe…“ hängt | Der Streamer muss im Panel unter **Anfragen** auf **Annehmen** klicken |
| Kein Ton | Im Viewer auf den Lautsprecher-Knopf klicken; beim Streamer muss „Systemton streamen“ angehakt sein |
| „Stream nicht gefunden“ | Der andere hat noch nicht auf **Stream starten** gedrückt (die Sitzungs-ID allein reicht nicht) |
| Omarchy meldet „grim fehlt“ | `sudo pacman -S grim` |
| Bild ruckelig oder viel Traffic | Im Panel „Bilder pro Sekunde“ auf 10 und „Maximale Breite“ auf 1280 |
| Tunnel-Adresse ändert sich | Sie bleibt, solange der Tunnel läuft. Nach einem Neustart einfach die neue Adresse einmal neu eintragen |

## Was macht der Tunnel?

Er lädt einmalig [cloudflared](https://github.com/cloudflare/cloudflared) von Cloudflare und lässt es den lokalen Relay unter einer zufälligen `trycloudflare.com`-Adresse veröffentlichen – ohne Konto, ohne Portfreigabe am Router. Der Tunnel läuft nur, solange der Haken gesetzt ist. Wenn dir fremde Infrastruktur nicht passt: eigener Relay auf einem VPS oder LAN-only, beides steht im [README](README.md#relay-aufsetzen).
