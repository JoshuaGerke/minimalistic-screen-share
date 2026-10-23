# In zwei Minuten loslegen

## 1. Installieren

**Windows:** [`MinimalisticScreenShare.exe`](https://github.com/JoshuaGerke/minimalistic-screen-share/releases/latest/download/MinimalisticScreenShare.exe) herunterladen und doppelklicken. Fertig – die App installiert sich selbst (ohne Admin), legt einen Startmenü-Eintrag an und öffnet ihr Fenster. Das Symbol sitzt danach im Infobereich (Windows 11: hinter dem Pfeil `^`).

> Windows zeigt beim ersten Start evtl. „Der Computer wurde durch Windows geschützt“ – auf **Weitere Informationen → Trotzdem ausführen** klicken. Das kommt bei jeder Software ohne kostenpflichtiges Signatur-Zertifikat.

**Arch/Omarchy:**

```bash
curl -fsSL https://raw.githubusercontent.com/JoshuaGerke/minimalistic-screen-share/main/install.sh | bash
sudo pacman -S wf-recorder grim slurp libpulse wl-clipboard   # Video, Bild, Ton, Link kopieren
mss-share --settings
```

**Updates:** Die App schaut selbst nach neuen Versionen. Gibt es eine, steht oben im Fenster **„Version … ist da“** – auf **Jetzt aktualisieren** klicken, ein paar Sekunden warten, fertig. Alternativ im Startmenü bzw. Anwendungsmenü **„Minimalistic Screen Share aktualisieren“** starten, oder im Terminal `mss-share --update`.

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

## Sprachkanal

Reden wie bei Discord – direkt im Stream, ohne Extra-Programm:

- **Wer streamt:** Im Fenster der App unter **Sprachkanal** auf **Beitreten** (geht, solange du teilst).
- **Wer zuschaut:** Im Viewer oben rechts auf das **Headset-Symbol** → **Beitreten**.

Der Browser fragt einmal nach dem Mikrofon. Wer spricht, bekommt einen grünen Ring. Stumm (`Strg+Umschalt+M`) und taub (`Strg+Umschalt+D`) gibt es neben dem Knopf, die Lautstärke jeder Person lässt sich einzeln regeln.

Unter dem Regler-Symbol stecken die Einstellungen: Mikrofon und Ausgabe wählen, **Sprachaktivierung** mit Empfindlichkeit (der Balken zeigt live deinen Pegel, der gelbe Strich die Schwelle) oder **Push-to-Talk**, Mikrofon-Verstärkung, **Echo-Unterdrückung**, **Rauschunterdrückung**, **automatische Lautstärke**, **Stereo** und die **Bitrate** (Standard 128 kbit/s, bis 510 kbit/s).

> Push-to-Talk wirkt, solange das Fenster der App bzw. der Viewer aktiv ist – im Spiel lieber Sprachaktivierung nehmen.

### Overlay

**Overlay** neben dem Beitreten-Knopf blendet die Teilnehmerliste über allen Fenstern ein – auch über Spielen, ohne ihnen den Fokus zu nehmen. Mit der Maus irgendwo anfassen und verschieben, die Position bleibt gespeichert. In den Spracheinstellungen: **Deckkraft**, **Größe**, **nur wer gerade spricht** und **Klicks durchlassen** (dann liegt es wie ein Aufkleber über dem Spiel und lässt sich nicht mehr verschieben).

Unter Linux öffnet das Overlay ein schwebendes Browserfenster (Chrome, Brave). Unter Hyprland macht diese Zeile in `hyprland.conf` es durchsichtig, angepinnt und rahmenlos:

```
windowrule = float, pin, opacity 0.85, noborder, title:^(Sprachkanal)$
```

## Diagnosemodus (wenn etwas ruckelt)

Im Fenster ganz unten unter **System** → **Diagnosemodus** einschalten (oder die App mit `mss-share --diag` starten). Dann schreibt die App alle 2 Sekunden eine Zeile ins Log: CPU-Last von App, Encoder, Ton, Tunnel, Compositor und Browser, die Auslastung jeder Grafikkarte, Systemlast, freien Speicher, Bilder pro Sekunde, Datenrate, Warteschlangen und Meldungen des Encoders. Beim Einschalten kommen einmal Kernel, Grafikkarten und Monitore (mit Hz) dazu.

So schickst du es weiter: Diagnosemodus an, ein paar Minuten wie gewohnt teilen, bis es ruckelt, dann

```bash
tail -150 ~/.config/minimalistic-screen-share/share.log
```

(Windows: **Log öffnen** im Fenster) und die Ausgabe schicken.

## Bildeinstellungen

Im Fenster unter **Einstellungen → Bild** gibt es vier Voreinstellungen:

| Voreinstellung | Wofür |
|---|---|
| Sparsam | schwache Leitung, nur „mitgucken“ |
| Ausgewogen | Standard |
| Scharf (Text, Code) | Programmieren, Dokumente – kleiner Text bleibt lesbar |
| Flüssig (Videos) | Videos, Spiele, viel Bewegung |

Das Bild läuft als H.264-Video über die Grafikkarte – wie bei Discord oder OBS: 60 fps in voller Auflösung bei fast null CPU-Last. Die **Datenrate** bestimmt die Schärfe; 6 Mbit/s reichen für einen 1440p-Bildschirm. Wird die Leitung eng, springt das Bild zum nächsten Keyframe, **der Ton bleibt**.

Windows nimmt automatisch den passenden Encoder (NVIDIA, AMD, Intel, sonst Windows/CPU) und lädt dafür beim ersten Start einmalig ffmpeg (~60 MB). Unter Hyprland/Omarchy läuft das Video über `wf-recorder` (VAAPI). Fenster und „Alle Monitore“ laufen noch im alten Einzelbild-Verfahren.

**Kompatibilitätsmodus:** Bekommt ein Zuschauer kein Bild (sehr alter Browser, oder Link über `http://` im WLAN), schaltet den Modus ein – dann wieder Einzelbilder wie früher.

## Ton

- **Systemton mitschicken** schickt alles mit, was du hörst.
- **Windows:** Unter Einstellungen → Ton ein Programm anhaken (z. B. Discord) – es landet nicht mehr im Stream, **du hörst es aber ganz normal weiter**. Wirkt sofort, auch mitten im Stream. Geht für ein Programm gleichzeitig und braucht Windows 10 (2004) oder neuer.
- **Linux:** Angehakte Programme werden während des Teilens auch bei dir stummgeschaltet und beim Beenden wieder laut.
- Schaust du **auf demselben PC** zu, der gerade streamt, bleibt der Viewer automatisch stumm – sonst würde der Ton sich selbst wieder aufnehmen und extrem laut rückkoppeln.

## Wenn was klemmt

| Problem | Lösung |
|---|---|
| „Warte auf Freigabe…“ hängt | Der Streamer muss **Reinlassen** klicken (Meldung am Symbol oder im Fenster) |
| „Gerade wird nichts geteilt“ | Der andere hat noch nicht auf **Teilen starten** gedrückt – offen lassen, es geht von selbst los |
| Kein Ton | Unten erscheint ein gelber Knopf „Ton ist aus“ – einmal draufklicken |
| Link funktioniert nicht mehr | Der Link gilt, solange die App beim Streamer läuft. Nach einem Neustart der App gibt es einen neuen Link |
| Bild ruckelt | Unter Linux `wf-recorder` installiert? Sonst Voreinstellung **Sparsam** nehmen. In den Verbindungsdaten (Taste `I`) muss „H.264“ stehen |
| Omarchy: „grim fehlt“ | `sudo pacman -S grim` |

## Wie kommt der Link ins Internet?

Die App bringt ihren eigenen kleinen Server mit und stellt ihn über einen kostenlosen [Cloudflare-Tunnel](https://github.com/cloudflare/cloudflared) ins Netz – ohne Konto, ohne Router-Einstellungen, ohne offene Ports. Beim ersten Start lädt sie dafür einmalig ca. 40 MB. Wer das nicht möchte: **Einstellungen → Verbindung → Nur im selben Netzwerk** oder einen eigenen Server eintragen (siehe [README](README.md#eigener-server)).
