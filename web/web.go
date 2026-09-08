// Package web embeds the single-page console.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var static embed.FS

// Handler serves the embedded console. index.html is always revalidated; every
// other asset gets a content ETag so browsers can send If-None-Match and receive
// 304 on reload, plus a short max-age so page-to-page navigation hits memory.
func Handler() http.Handler {
	sub, _ := fs.Sub(static, "static")
	fsrv := http.FileServer(http.FS(sub))
	etags := map[string]string{}
	_ = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := fs.ReadFile(sub, p)
		sum := sha256.Sum256(b)
		etags["/"+p] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/" || p == "/index.html" {
			w.Header().Set("Cache-Control", "no-cache")
			fsrv.ServeHTTP(w, r)
			return
		}
		if tag, ok := etags[p]; ok {
			w.Header().Set("ETag", tag)
			w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
			if strings.Contains(r.Header.Get("If-None-Match"), tag) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		fsrv.ServeHTTP(w, r)
	})
}
