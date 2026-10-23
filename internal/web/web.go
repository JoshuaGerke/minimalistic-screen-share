// Paket web liefert die eingebetteten Weboberflächen aus.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var embedded embed.FS

// FS liefert die Weboberflächen: room.html ist das Raum-Fenster.
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
