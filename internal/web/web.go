// Paket web liefert die eingebetteten Weboberflächen aus.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var embedded embed.FS

// FS liefert die Weboberflächen: index.html ist der Viewer, host.html das Host-Panel.
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
