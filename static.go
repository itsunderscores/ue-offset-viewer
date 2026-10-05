package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
)

// uiBuildID hashes the embedded UI entrypoints so each binary gets a unique ?v= for JS/CSS.
func uiBuildID(static fs.FS) string {
	h := sha256.New()
	for _, name := range []string{"index.html", "style.css", "app.js"} {
		b, err := fs.ReadFile(static, name)
		if err != nil {
			continue
		}
		h.Write([]byte(name))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)[:6])
}

func prepareIndex(static fs.FS, build string) ([]byte, error) {
	raw, err := fs.ReadFile(static, "index.html")
	if err != nil {
		return nil, err
	}
	return bytes.ReplaceAll(raw, []byte("__UI_BUILD__"), []byte(build)), nil
}

type staticHandler struct {
	files http.Handler
	index []byte
}

func newStaticHandler(static fs.FS, index []byte) http.Handler {
	return &staticHandler{
		files: http.FileServer(http.FS(static)),
		index: index,
	}
}

func (h *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" || path == "/index.html" {
		setNoCacheUI(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(h.index)
		return
	}
	if isUIAsset(path) {
		setNoCacheUI(w)
	}
	h.files.ServeHTTP(w, r)
}

func isUIAsset(path string) bool {
	if strings.HasPrefix(path, "/fonts/") {
		return false
	}
	switch {
	case strings.HasSuffix(path, ".js"), strings.HasSuffix(path, ".css"), strings.HasSuffix(path, ".html"):
		return true
	default:
		return false
	}
}

func setNoCacheUI(w http.ResponseWriter) {
	// Origin + Cloudflare: do not keep old JS/CSS/HTML after a deploy.
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("CDN-Cache-Control", "no-store")
}
