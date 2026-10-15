# In zwei Minuten loslegen

## 1. Installieren

**Windows:** [`MinimalisticScreenShare.exe`](https://github.com/JoshuaGerke/minimalistic-screen-share/releases/latest/download/MinimalisticScreenShare.exe) herunterladen und doppelklicken. Fertig – die App installiert sich selbst (ohne Admin), legt einen Startmenü-Eintrag an und öffnet ihr Fenster. Das Symbol sitzt danach im Infobereich (Windows 11: hinter dem Pfeil `^`).

> Windows zeigt beim ersten Start evtl. „Der Computer wurde durch Windows geschützt“ – auf **Weitere Informationen → Trotzdem ausführen** klicken. Das kommt bei jeder Software ohne kostenpflichtiges Signatur-Zertifikat.

**Arch/Omarchy:**

```bash
curl -fsSL https://raw.githubusercontent.com/JoshuaGerke/minimalistic-screen-share/main/install.sh | bash
sudo pacman -S grim slurp libpulse wl-clipboard   # Aufnahme, Ton, Link kopieren
mss-share --settings
```

**Update:** Neue exe herunterladen und doppelklicken – die alte Version wird beendet, ersetzt und neu gestartet.

## 2. Teilen

1. Im Fenster auswählen, was du teilen willst (Bildschirm, Monitor, Fenster) – oder Rechtsklick aufs Symbol → **Bildschirm teilen**
2. **Teilen starten** – der Link ist jetzt in deiner Zwischenablage
3. Im Chat einfügen (Strg+V) und abschicken

Wer den Link öffnet, tippt seinen Namen ein und klopft an. Du bekommst eine Meldung und klickst **Reinlassen**. Wer einmal drin war, kommt den ganzen Tag ohne neues Anklopfen wieder rein – auch wenn du zwischendurch aufhörst und neu startest.

Im festen Team: **Alle sofort reinlassen** einschalten, dann entfällt das Anklopfen.

## 3. Zuschauen

Link öffnen, Namen eingeben, **Zuschauen**. Mehr braucht es nicht – kein Programm, kein Konto, nur der Browser.

- **Bild-in-Bild** (Taste `P`): Der Stream schwebt klein über deinen anderen Fenstern – ideal fürs Co-Working
- **Lautstärke** oben rechts, `M` schaltet stumm, `↑`/`↓` regeln
- `F` Vollbild, `O` Originalgröße (1:1, für kleinen Text), `I` Verbindungsdaten
- Hört der andere auf zu teilen, einfach offen lassen: Sobald er wieder startet, bist du automatisch wieder dabei

## Für Co-Working

- **Pausieren** (im Fenster oder Tray) hält das Bild sofort an, wenn du kurz etwas Privates machst. Die Zuschauer sehen „Pausiert“, die Verbindung bleibt stehen.
- Die **Quelle wechseln** geht jederzeit während des Teilens – die Zuschauer sehen einfach das neue Bild.
- Alle Einstellungen gelten sofort, auch mitten im Stream – kein Neustart nötig.
- Beide teilen gleichzeitig? Kein Problem: Jeder startet sein eigenes Teilen und schickt seinen Link.

## Bildeinstellungen

Im Fenster unter **Einstellungen → Bild** gibt es vier Voreinstellungen:

| Voreinstellung | Wofür |
|---|---|
| Sparsam | schwache Leitung, nur „mitgucken“ |
| Ausgewogen | Standard |
| Scharf (Text, Code) | Programmieren, Dokumente – kleiner Text bleibt lesbar |
| Flüssig (Videos) | Videos, Spiele, viel Bewegung |

Der Upload ist der Flaschenhals. Wird es eng, lässt die App Bilder weg, **nie den Ton**.

## Ton

- **Systemton mitschicken** schickt alles mit, was du hörst.
- Programme, die nicht im Stream landen sollen (Discord & Co.), lassen sich während des Teilens im Fenster stummschalten – beim Beenden wird alles zurückgestellt.
- Soll ein Programm für dich hörbar bleiben, aber nicht im Stream landen: Windows-Lautstärkemixer → beim Programm **„Von Aufnahme ausschließen“** (Knopf „Lautstärkemixer öffnen“ im Fenster).
- Schaust du **auf demselben PC** zu, der gerade streamt, bleibt der Viewer automatisch stumm – sonst würde der Ton sich selbst wieder aufnehmen und extrem laut rückkoppeln.

## Wenn was klemmt

| Problem | Lösung |
|---|---|
| „Warte auf Freigabe…“ hängt | Der Streamer muss **Reinlassen** klicken (Meldung am Symbol oder im Fenster) |
| „Gerade wird nichts geteilt“ | Der andere hat noch nicht auf **Teilen starten** gedrückt – offen lassen, es geht von selbst los |
| Kein Ton | Unten erscheint ein gelber Knopf „Ton ist aus“ – einmal draufklicken |
| Link funktioniert nicht mehr | Der Link gilt, solange die App beim Streamer läuft. Nach einem Neustart der App gibt es einen neuen Link |
| Bild ruckelt | Voreinstellung **Sparsam** oder **Ausgewogen** nehmen |
| Omarchy: „grim fehlt“ | `sudo pacman -S grim` |

## Wie kommt der Link ins Internet?

Die App bringt ihren eigenen kleinen Server mit und stellt ihn über einen kostenlosen [Cloudflare-Tunnel](https://github.com/cloudflare/cloudflared) ins Netz – ohne Konto, ohne Router-Einstellungen, ohne offene Ports. Beim ersten Start lädt sie dafür einmalig ca. 40 MB. Wer das nicht möchte: **Einstellungen → Verbindung → Nur im selben Netzwerk** oder einen eigenen Server eintragen (siehe [README](README.md#eigener-server)).
