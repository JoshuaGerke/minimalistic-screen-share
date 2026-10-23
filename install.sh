#!/usr/bin/env bash
# Minimalistic Screen Share – Installer für Linux
#
# Nutzung:
#   ./install.sh                  installiert aus diesem Ordner (baut zur Not
#                                 aus dem Quelltext, Go vorausgesetzt)
#   ./install.sh --from-github    lädt die neueste Version von GitHub
#   ./install.sh --autostart      zusätzlich beim Anmelden starten
#
# Deinstallieren: uninstall.sh

set -euo pipefail

REPO="JoshuaGerke/minimalistic-screen-share"
BIN_DIR="${HOME}/.local/bin"
APP_DIR="${HOME}/.local/share/applications"
AUTO_DIR="${HOME}/.config/autostart"

FROM_GITHUB=0
AUTOSTART=0
SOURCE=""

usage() {
	cat <<EOF
install.sh – Minimalistic Screen Share installieren

Optionen:
  --from-github   neueste Version von GitHub laden statt lokal zu installieren
  --autostart     beim Anmelden automatisch starten
  --source DIR    Binaries aus DIR nehmen
  --bin-dir DIR   Zielordner (Standard: ~/.local/bin)
  -h, --help      diese Hilfe
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
		--from-github) FROM_GITHUB=1; shift ;;
		--autostart)   AUTOSTART=1; shift ;;
		--source)      SOURCE="${2:?--source braucht einen Ordner}"; shift 2 ;;
		--bin-dir)     BIN_DIR="${2:?--bin-dir braucht einen Ordner}"; shift 2 ;;
		-h|--help)     usage; exit 0 ;;
		*) echo "unbekannte Option: $1" >&2; usage >&2; exit 1 ;;
	esac
done

schritt() { printf '==> %s\n' "$1"; }

# Ordnername dieses Skripts – beim Aufruf über "curl | bash" gibt es keinen.
SCRIPT_DIR=""
if [ -n "${BASH_SOURCE[0]:-}" ]; then
	SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd || true)"
fi
[ -n "$SCRIPT_DIR" ] || SCRIPT_DIR="$PWD"

# --- Quelle bestimmen -------------------------------------------------------

if [ -z "$SOURCE" ]; then
	for cand in "$SCRIPT_DIR" "$SCRIPT_DIR/bin"; do
		if [ -f "$cand/mss-share" ]; then
			SOURCE="$cand"
			break
		fi
	done
fi

if [ -z "$SOURCE" ] && [ "$FROM_GITHUB" -eq 0 ] && [ -f "$SCRIPT_DIR/go.mod" ]; then
	if ! command -v go >/dev/null 2>&1; then
		echo "Go fehlt und es liegen keine fertigen Binaries herum." >&2
		echo "Entweder Go installieren oder --from-github verwenden." >&2
		exit 1
	fi
	schritt "Baue aus dem Quelltext"
	mkdir -p "$SCRIPT_DIR/bin"
	(cd "$SCRIPT_DIR" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/mss-share ./cmd/share)
	SOURCE="$SCRIPT_DIR/bin"
fi

if [ -z "$SOURCE" ]; then
	FROM_GITHUB=1
fi

if [ "$FROM_GITHUB" -eq 1 ]; then
	case "$(uname -m)" in
		x86_64|amd64) ARCH=amd64 ;;
		aarch64|arm64) ARCH=arm64 ;;
		*) echo "unbekannte Architektur: $(uname -m)" >&2; exit 1 ;;
	esac
	schritt "Lade die neueste Version von GitHub (linux_${ARCH})"
	url="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
		| grep -o "https://[^\"]*linux_${ARCH}\.tar\.gz" | head -n1)"
	if [ -z "$url" ]; then
		echo "Kein Release gefunden. Alternativ das Repo klonen und install.sh dort ausführen." >&2
		exit 1
	fi
	SOURCE="$(mktemp -d)"
	trap 'rm -rf "$SOURCE"' EXIT
	curl -fsSL "$url" | tar -xz -C "$SOURCE"
fi

# --- installieren -----------------------------------------------------------

# Läuft die App gerade, wird sie beendet und nach dem Update neu gestartet.
was_running=0
if pgrep -x mss-share >/dev/null 2>&1; then
	was_running=1
	pkill -x mss-share 2>/dev/null || true
	sleep 0.5
fi

schritt "Installiere nach $BIN_DIR"
mkdir -p "$BIN_DIR" "$APP_DIR"
for b in mss-share; do
	if [ -f "$SOURCE/$b" ]; then
		install -m755 "$SOURCE/$b" "$BIN_DIR/$b"
	fi
done
if [ ! -x "$BIN_DIR/mss-share" ]; then
	echo "mss-share nicht gefunden – bitte im Release-Ordner oder im Repo ausführen." >&2
	exit 1
fi

# --- Menü- und Autostart-Eintrag --------------------------------------------

schritt "Lege .desktop-Eintrag an"
cat > "$APP_DIR/minimalistic-screen-share.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Minimalistic Screen Share
GenericName=Screensharing
Comment=Bildschirm und Ton teilen wie in Discord
Exec=$BIN_DIR/mss-share
Icon=video-display
Terminal=false
Categories=Network;AudioVideo;Utility;
Keywords=screenshare;streaming;screen;discord;
StartupNotify=false
EOF
cat > "$APP_DIR/minimalistic-screen-share-update.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Minimalistic Screen Share aktualisieren
Comment=Holt die neueste Version und startet die App neu
Exec=$BIN_DIR/mss-share --update
Icon=system-software-update
Terminal=true
Categories=Network;AudioVideo;Utility;
Keywords=update;aktualisieren;screenshare;
EOF

if [ "$AUTOSTART" -eq 1 ]; then
	schritt "Richte Autostart ein"
	mkdir -p "$AUTO_DIR"
	cat > "$AUTO_DIR/minimalistic-screen-share.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Minimalistic Screen Share
Exec=$BIN_DIR/mss-share
Icon=video-display
Terminal=false
X-GNOME-Autostart-enabled=true
EOF
fi

# --- Hinweise ---------------------------------------------------------------

case ":${PATH}:" in
	*":$BIN_DIR:"*) ;;
	*) echo "Hinweis: $BIN_DIR ist nicht im PATH. Für zsh in ~/.zshrc ergänzen:"
	   echo "        export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
esac

if [ -n "${WAYLAND_DISPLAY:-}" ] && ! command -v grim >/dev/null 2>&1; then
	echo "Hinweis: grim fehlt für die Aufnahme unter Wayland. Auf Arch: sudo pacman -S grim"
fi
if [ -n "${WAYLAND_DISPLAY:-}" ] && ! command -v wf-recorder >/dev/null 2>&1; then
	echo "Hinweis: wf-recorder fehlt – ohne ruckelt der Stream (Einzelbilder statt Video). Auf Arch: sudo pacman -S wf-recorder"
fi
if ! command -v parec >/dev/null 2>&1; then
	echo "Hinweis: parec fehlt für den Systemton. Auf Arch: sudo pacman -S libpulse"
fi

echo ""
echo "Fertig. Start: mss-share (oder aus dem Anwendungsmenü)."
echo "Deinstallieren: uninstall.sh"

if [ "$was_running" -eq 1 ]; then
	if command -v setsid >/dev/null 2>&1; then
		(setsid "$BIN_DIR/mss-share" >/dev/null 2>&1 &)
	else
		("$BIN_DIR/mss-share" >/dev/null 2>&1 &)
	fi
	echo "Laufende Instanz wurde neu gestartet."
fi
