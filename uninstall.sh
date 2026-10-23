#!/usr/bin/env bash
# Minimalistic Screen Share – Deinstallation unter Linux
#
# Nutzung:
#   ./uninstall.sh           entfernt Programm und Verknüpfungen
#   ./uninstall.sh --purge   entfernt zusätzlich die Einstellungen

set -euo pipefail

BIN_DIR="${HOME}/.local/bin"
APP_DIR="${HOME}/.local/share/applications"
AUTO_DIR="${HOME}/.config/autostart"
UNIT_DIR="${HOME}/.config/systemd/user"
CONFIG_DIR="${HOME}/.config/minimalistic-screen-share"
PURGE=0

while [ $# -gt 0 ]; do
	case "$1" in
		--purge) PURGE=1; shift ;;
		--bin-dir) BIN_DIR="${2:?--bin-dir braucht einen Ordner}"; shift 2 ;;
		-h|--help)
			echo "uninstall.sh [--purge] [--bin-dir DIR]"
			exit 0 ;;
		*) echo "unbekannte Option: $1" >&2; exit 1 ;;
	esac
done

pkill -x mss-share 2>/dev/null || true

if [ -f "$UNIT_DIR/mss-relay.service" ] && command -v systemctl >/dev/null 2>&1; then
	systemctl --user disable --now mss-relay.service 2>/dev/null || true
	rm -f "$UNIT_DIR/mss-relay.service"
	systemctl --user daemon-reload 2>/dev/null || true
	echo "entfernt: Relay-Dienst"
fi

for b in mss-share mss-relay mss-viewer; do
	if [ -e "$BIN_DIR/$b" ]; then
		rm -f "$BIN_DIR/$b"
		echo "entfernt: $BIN_DIR/$b"
	fi
done

for f in "$APP_DIR/minimalistic-screen-share.desktop" \
         "$APP_DIR/minimalistic-screen-share-update.desktop" \
         "$AUTO_DIR/minimalistic-screen-share.desktop"; do
	if [ -e "$f" ]; then
		rm -f "$f"
		echo "entfernt: $f"
	fi
done

if [ "$PURGE" -eq 1 ]; then
	rm -rf "$CONFIG_DIR"
	echo "entfernt: $CONFIG_DIR"
else
	echo "Einstellungen bleiben erhalten in $CONFIG_DIR (mit --purge löschen)."
fi

echo "Fertig."
