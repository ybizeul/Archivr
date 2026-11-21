package main

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist holds the built web app. `mise run build-frontend` copies frontend/dist here.
// The .gitkeep file keeps the directory present for a fresh checkout, so the build works before the first web build.
//
//go:embed all:dist
var dist embed.FS

// webHandler serves the single-page app. Real files are served as they are, and every
// other path gets index.html, so client-side routes survive a reload.
func webHandler() http.Handler {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		return notBuilt()
	}
	index, err := fs.ReadFile(root, "index.html")
	if err != nil {
		return notBuilt()
	}
	files := http.FileServerFS(root)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if info, err := fs.Stat(root, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Asset names carry a content hash, so they can be cached forever.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}

func notBuilt() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "The web app is not built into this binary. Run: mise run build-frontend", http.StatusNotFound)
	})
}
