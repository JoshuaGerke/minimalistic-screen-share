// Paket tray zeigt das Symbol samt Menü im Infobereich an (Windows) bzw.
// als StatusNotifier-Eintrag (Linux).
package tray

// Item ist ein Menüeintrag. Einträge mit Unterpunkten sind Untermenüs,
// Separator-Einträge sind Trennlinien.
type Item struct {
	ID        int
	Title     string
	Disabled  bool
	Checked   bool
	Separator bool
	Items     []*Item
	OnClick   func()
}

// indexItems sammelt alle Einträge samt Untermenüs nach ID ein.
func indexItems(items []*Item, out map[int]*Item) {
	for _, it := range items {
		if it.ID != 0 {
			out[it.ID] = it
		}
		indexItems(it.Items, out)
	}
}

// Tray steuert das laufende Symbol.
type Tray interface {
	SetTooltip(s string)
	SetItemTitle(id int, title string)
	SetItemEnabled(id int, enabled bool)
	SetItemChecked(id int, checked bool)
	SetIconState(state string) // idle | live | paused | error
	// SetActivate hinterlegt, was ein Klick auf das Symbol selbst tun soll
	// (unter Linux üblich, unter Windows zeigt der Klick das Menü).
	SetActivate(fn func())
	Quit()
}

// Run baut das Menü auf und kehrt erst zurück, wenn Quit gerufen wurde.
// Gibt es kein Tray (headless, kein StatusNotifier), kommt ein Fehler.
// Implementiert in tray_windows.go bzw. tray_linux.go.
