# In zwei Minuten loslegen

## 1. Installieren

**Windows:** [`MinimalisticScreenShare.exe`](https://github.com/JoshuaGerke/minimalistic-screen-share/releases/latest/download/MinimalisticScreenShare.exe) herunterladen und doppelklicken. Die App installiert sich selbst ohne Admin-Rechte und öffnet ihr Fenster. Das Symbol sitzt danach im Infobereich (Windows 11: hinter dem Pfeil `^`).

> Zeigt Windows beim ersten Start „Der Computer wurde durch Windows geschützt“, auf **Weitere Informationen → Trotzdem ausführen** klicken.

**Arch/Omarchy:**

```bash
curl -fsSL https://raw.githubusercontent.com/JoshuaGerke/minimalistic-screen-share/main/install.sh | bash
sudo pacman -S wf-recorder grim libpulse
mss-share
```

**Updates:** In den Einstellungen erscheint „Auf … aktualisieren“, sobald es eine neue Version gibt. Ein Klick genügt. Alternativ im Terminal: `mss-share --update`.

## 2. Raum betreten

1. Namen eingeben.
2. **Raum-ID** ausdenken (oder auf „zufällig“ klicken) und ein **Passwort** mit mindestens 6 Zeichen.
3. **Betreten.** Gibt es den Raum noch nicht, hast du ihn damit angelegt.

Den anderen sagst du die Raum-ID und das Passwort, das Passwort am besten mündlich oder über einen anderen Weg als die ID. Mit **Raum merken** steht er nächstes Mal mit einem Klick bereit. Gespeichert wird dabei nur der abgeleitete Schlüssel, nicht das Passwort.

Das Schloss mit **E2E** oben zeigt: Alles im Raum ist Ende-zu-Ende verschlüsselt.

## 3. Im Raum

- **Sprechen:** rechts „Sprachkanal beitreten“. Mit `Strg+Umschalt+M` schaltest du stumm, mit `Strg+Umschalt+D` taub. Ein Klick auf eine Person öffnet ihren Lautstärkeregler.
- **Bildschirm teilen:** unten links, dann Hauptbildschirm, Monitor oder Fenster wählen, wahlweise mit Systemton. „Quelle wechseln“ und „Pausieren“ gehen jederzeit.
- **Zuschauen:** Teilt jemand, erscheint sein Bildschirm groß. Teilen mehrere, liegen die anderen als Kacheln darunter, ein Klick macht sie groß. Doppelklick oder `F11` schaltet auf Vollbild.
- **Zeigen:** 5× schnell auf dieselbe Stelle im Bild klicken, dann sieht der andere dort einen Ring.
- **Chat:** unten rechts. Dateien schickst du über die Büroklammer oder ziehst sie einfach ins Fenster. Bilder aus der Zwischenablage gehen mit `Strg+V`.
- **Overlay:** „Overlay“ im Sprachkanal blendet eine schwebende Liste ein, die zeigt, wer gerade spricht. Deckkraft und Größe stellst du unter Einstellungen → Sprache ein.

## 4. Immer verbunden (Co-Working)

Unter **Einstellungen → Allgemein**:

- **Beim Anmelden starten** einschalten.
- **Beim Start automatisch betreten:** den gemerkten Raum wählen, dazu „Sprachkanal beitreten“, „…mit stummem Mikrofon“ und/oder „Bildschirm teilen“.

Ab dann seid ihr nach dem Hochfahren automatisch im Raum und hört euch, sobald jemand das Mikrofon anmacht.

## Wenn etwas hakt

- **„Falsches Passwort“:** Raum-ID und Passwort genau vergleichen. Groß- und Kleinschreibung zählt beim Passwort.
- **„Suche den Raum…“ bleibt stehen:** Dann betreibst du den Raum bald selbst. Die anderen finden dich, sobald der Tunnel steht (erstes Mal: cloudflared wird geladen, das dauert etwas).
- **Jemand hört dich nicht:** Steht in der Liste bei dir ein rotes Mikrofon? Unter Einstellungen → Sprache prüfen, ob das richtige Mikrofon gewählt ist und der Pegel über die gelbe Marke geht.
- **Ruckeln:** Unter Einstellungen → Teilen die Bildrate oder Datenrate senken. Unter Einstellungen → Allgemein → **Diagnosemodus** einschalten und die Logdatei („Log öffnen“) schicken.
