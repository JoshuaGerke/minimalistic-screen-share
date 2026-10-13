//go:build linux

package tray

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"

	"github.com/JoshuaGerke/minimalistic-screen-share/internal/icon"
)

const (
	sniPath  = dbus.ObjectPath("/StatusNotifierItem")
	menuPath = dbus.ObjectPath("/MenuBar")
)

// themeIcon liefert je Zustand ein Symbol aus dem Icon-Theme – das passt
// sich der Oberfläche an, ohne dass wir Bilder mitliefern.
var themeIcon = map[string]string{
	"idle":   "video-display",
	"live":   "media-record",
	"paused": "media-playback-pause",
	"error":  "dialog-warning",
}

type linuxTray struct {
	mu       sync.Mutex
	items    []*Item
	byID     map[int]*Item
	state    string
	tooltip  string
	activate func()

	conn     *dbus.Conn
	busName  string
	props    *prop.Properties
	revision uint32
	quitOnce sync.Once
	quitCh   chan struct{}
}

func Run(items []*Item, onReady func(Tray)) error {
	conn, err := dbus.SessionBus()
	if err != nil {
		return fmt.Errorf("keine Session-Bus-Verbindung: %w", err)
	}

	t := &linuxTray{
		items:  items,
		byID:   map[int]*Item{},
		state:  "idle",
		conn:   conn,
		quitCh: make(chan struct{}),
	}
	indexItems(items, t.byID)

	t.busName = fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	reply, err := conn.RequestName(t.busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("Bus-Name: %w", err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return errors.New("Bus-Name ist schon vergeben")
	}

	menu := &dbusMenu{tray: t}
	if err := conn.Export(sniItem{t}, sniPath, "org.kde.StatusNotifierItem"); err != nil {
		return err
	}
	if err := conn.Export(menu, menuPath, "com.canonical.dbusmenu"); err != nil {
		return err
	}

	propsSpec := map[string]map[string]*prop.Prop{
		"org.kde.StatusNotifierItem": {
			"Category":      {Value: "ApplicationStatus", Writable: false, Emit: prop.EmitFalse},
			"Id":            {Value: "minimalistic-screen-share", Writable: false, Emit: prop.EmitFalse},
			"Title":         {Value: "minimalistic screen share", Writable: false, Emit: prop.EmitTrue},
			"Status":        {Value: "Active", Writable: false, Emit: prop.EmitTrue},
			"IconName":      {Value: themeIcon["idle"], Writable: false, Emit: prop.EmitTrue},
			"IconThemePath": {Value: "", Writable: false, Emit: prop.EmitFalse},
			"Menu":          {Value: menuPath, Writable: false, Emit: prop.EmitFalse},
			"ItemIsMenu":    {Value: false, Writable: false, Emit: prop.EmitFalse},
			"WindowId":      {Value: uint32(0), Writable: false, Emit: prop.EmitFalse},
			"IconPixmap":    {Value: pixmapValue("idle"), Writable: false, Emit: prop.EmitTrue},
		},
	}
	props, err := prop.Export(conn, sniPath, propsSpec)
	if err != nil {
		return err
	}
	t.props = props

	conn.Export(introspect.NewIntrospectable(&introspect.Node{
		Name: string(sniPath),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{
				Name: "org.kde.StatusNotifierItem",
				Methods: []introspect.Method{
					{Name: "Activate"},
					{Name: "SecondaryActivate"},
					{Name: "ContextMenu"},
					{Name: "Scroll"},
				},
				Properties: []introspect.Property{
					{Name: "Category"}, {Name: "Id"}, {Name: "Title"},
					{Name: "Status"}, {Name: "IconName"}, {Name: "Menu"},
				},
				Signals: []introspect.Signal{
					{Name: "NewIcon"}, {Name: "NewStatus"}, {Name: "NewTitle"},
				},
			},
		},
	}), sniPath, "org.freedesktop.DBus.Introspectable")

	// Beim StatusNotifierWatcher anmelden (waybar, gnome-shell, …).
	watcher := conn.Object("org.kde.StatusNotifierWatcher", dbus.ObjectPath("/StatusNotifierWatcher"))
	if err := watcher.Call("org.kde.StatusNotifierWatcher.RegisterStatusNotifierItem", 0, t.busName).Err; err != nil {
		conn.ReleaseName(t.busName)
		return errors.New("StatusNotifierWatcher nicht erreichbar – läuft die Leiste?")
	}

	if onReady != nil {
		go onReady(t)
	}
	<-t.quitCh
	conn.ReleaseName(t.busName)
	return nil
}

func (t *linuxTray) SetActivate(fn func()) {
	t.mu.Lock()
	t.activate = fn
	t.mu.Unlock()
}

func (t *linuxTray) SetTooltip(s string) {
	t.mu.Lock()
	t.tooltip = s
	t.mu.Unlock()
	t.props.SetMust("org.kde.StatusNotifierItem", "Title", s)
}

func (t *linuxTray) SetItemTitle(id int, title string) {
	t.mu.Lock()
	if it := t.byID[id]; it != nil {
		it.Title = title
	}
	t.mu.Unlock()
	t.layoutChanged()
}

func (t *linuxTray) SetItemEnabled(id int, enabled bool) {
	t.mu.Lock()
	if it := t.byID[id]; it != nil {
		it.Disabled = !enabled
	}
	t.mu.Unlock()
	t.layoutChanged()
}

func (t *linuxTray) SetItemChecked(id int, checked bool) {
	t.mu.Lock()
	if it := t.byID[id]; it != nil {
		it.Checked = checked
	}
	t.mu.Unlock()
	t.layoutChanged()
}

func (t *linuxTray) SetIconState(state string) {
	name, ok := themeIcon[state]
	if !ok {
		return
	}
	t.mu.Lock()
	t.state = state
	t.mu.Unlock()
	t.props.SetMust("org.kde.StatusNotifierItem", "IconName", name)
	t.props.SetMust("org.kde.StatusNotifierItem", "IconPixmap", pixmapValue(state))
	status := "Active"
	if state == "error" {
		status = "NeedsAttention"
	}
	t.props.SetMust("org.kde.StatusNotifierItem", "Status", status)
}

func (t *linuxTray) Quit() {
	t.quitOnce.Do(func() { close(t.quitCh) })
}

func (t *linuxTray) layoutChanged() {
	t.mu.Lock()
	t.revision++
	rev := t.revision
	t.mu.Unlock()
	t.conn.Emit(menuPath, "com.canonical.dbusmenu.LayoutUpdated", rev)
}

// pixmapValue baut eine IconPixmap für Systeme ohne passendes Theme-Symbol.
func pixmapValue(state string) []interface{} {
	const size = 22
	img := icon.Pixmap(state, size)
	b := img.Bounds()
	raw := make([]byte, 0, b.Dx()*b.Dy()*4)
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := img.RGBAAt(x, y)
			raw = append(raw, c.A, c.R, c.G, c.B) // ARGB, Netzwerkreihenfolge
		}
	}
	return []interface{}{
		[]interface{}{int32(b.Dx()), int32(b.Dy()), raw},
	}
}

/* ---------- StatusNotifierItem-Methoden ---------- */

type sniItem struct{ tray *linuxTray }

func (s sniItem) Activate(x, y int32) *dbus.Error {
	s.tray.mu.Lock()
	fn := s.tray.activate
	s.tray.mu.Unlock()
	if fn != nil {
		go fn()
	}
	return nil
}

func (s sniItem) SecondaryActivate(x, y int32) *dbus.Error { return nil }
func (s sniItem) ContextMenu(x, y int32) *dbus.Error       { return nil }
func (s sniItem) Scroll(delta int32, orientation string) *dbus.Error {
	return nil
}

/* ---------- DBusMenu ---------- */

// layoutItem ist ein Menüpunkt im DBusMenu-Format (ia{sv}av).
type layoutItem struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

type propertyGroup struct {
	ID    int32
	Props map[string]dbus.Variant
}

type dbusMenu struct {
	tray *linuxTray
}

func (m *dbusMenu) GetLayout(parentID int32, recursionDepth int32, propertyNames []string) (uint32, layoutItem, *dbus.Error) {
	m.tray.mu.Lock()
	defer m.tray.mu.Unlock()
	root := layoutItem{ID: parentID, Props: map[string]dbus.Variant{}}
	if parentID == 0 {
		root.Children = m.children(m.tray.items, 0)
	}
	return m.tray.revision, root, nil
}

func (m *dbusMenu) children(items []*Item, base int32) []dbus.Variant {
	out := make([]dbus.Variant, 0, len(items))
	for i, it := range items {
		id := int32(it.ID)
		if id == 0 {
			id = base + int32(i) + 9000
		}
		props := map[string]dbus.Variant{"visible": dbus.MakeVariant(true)}
		node := layoutItem{ID: id, Props: props}
		if it.Separator {
			props["type"] = dbus.MakeVariant("separator")
		} else {
			props["label"] = dbus.MakeVariant(escapeLabel(it.Title))
			props["enabled"] = dbus.MakeVariant(!it.Disabled)
			if len(it.Items) > 0 {
				node.Children = m.children(it.Items, id*100)
			}
			if it.Checked {
				props["toggle-type"] = dbus.MakeVariant("checkmark")
				props["toggle-state"] = dbus.MakeVariant(int32(1))
			}
		}
		out = append(out, dbus.MakeVariant(node))
	}
	return out
}

// escapeLabel schützt Unterstriche, die in DBusMenu sonst als
// Tastenkürzel-Marker gelten.
func escapeLabel(s string) string {
	return strings.ReplaceAll(s, "_", "__")
}

func (m *dbusMenu) GetGroupProperties(ids []int32, propertyNames []string) ([]propertyGroup, *dbus.Error) {
	m.tray.mu.Lock()
	defer m.tray.mu.Unlock()
	var out []propertyGroup
	var walk func(items []*Item, base int32)
	walk = func(items []*Item, base int32) {
		for i, it := range items {
			id := int32(it.ID)
			if id == 0 {
				id = base + int32(i) + 9000
			}
			if len(ids) == 0 || contains(ids, id) {
				props := map[string]dbus.Variant{"visible": dbus.MakeVariant(true)}
				if it.Separator {
					props["type"] = dbus.MakeVariant("separator")
				} else {
					props["label"] = dbus.MakeVariant(escapeLabel(it.Title))
					props["enabled"] = dbus.MakeVariant(!it.Disabled)
					if it.Checked {
						props["toggle-type"] = dbus.MakeVariant("checkmark")
						props["toggle-state"] = dbus.MakeVariant(int32(1))
					}
				}
				out = append(out, propertyGroup{ID: id, Props: props})
			}
			if len(it.Items) > 0 {
				walk(it.Items, id*100)
			}
		}
	}
	walk(m.tray.items, 0)
	return out, nil
}

func (m *dbusMenu) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	groups, err := m.GetGroupProperties([]int32{id}, nil)
	if err != nil || len(groups) == 0 {
		return dbus.MakeVariant(""), err
	}
	if v, ok := groups[0].Props[name]; ok {
		return v, nil
	}
	return dbus.MakeVariant(""), nil
}

func (m *dbusMenu) Event(id int32, eventID string, data dbus.Variant, timestamp uint32) *dbus.Error {
	if eventID != "clicked" {
		return nil
	}
	m.tray.mu.Lock()
	it := m.tray.byID[int(id)]
	m.tray.mu.Unlock()
	if it != nil && it.OnClick != nil {
		go it.OnClick()
	}
	return nil
}

func (m *dbusMenu) AboutToShow(id int32) (bool, *dbus.Error) {
	return false, nil
}

func (m *dbusMenu) AboutToShowGroup(ids []int32) ([]int32, []int32, *dbus.Error) {
	return nil, nil, nil
}

func contains(list []int32, v int32) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
