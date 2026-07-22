// Package web provides static embedded web assets.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:static
var staticFS embed.FS

func Handler() http.Handler {

	fsys, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}

	fileServer := http.FileServer(http.FS(fsys))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		f, err := fsys.Open(strings.TrimPrefix(path, "/"))
		if err == nil {
			stat, _ := f.Stat()
			f.Close()

			if !stat.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}

		}

		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
