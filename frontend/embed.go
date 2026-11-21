// Package frontend embeds the web build of the user interface, which the
// daemon serves when ui.enabled is set (ADR 0012). Run `make frontend`
// before building a release binary; a fresh checkout holds only a
// placeholder, which is enough to compile and test the Go code.
package frontend

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Web returns the web build, rooted at its index.html (absent in a
// checkout where the frontend was not built).
func Web() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	return sub
}
