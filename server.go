package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Server holds the current DB (swapped atomically on reload) and the HTTP mux.
type Server struct {
	db      atomic.Pointer[DB]
	path    string
	mux     *http.ServeMux
	offsets *Offsets
	uiBuild string
}

func NewServer(path string, db *DB, static fs.FS, offsets *Offsets, indexHTML []byte, uiBuild string) (*Server, error) {
	s := &Server{path: path, mux: http.NewServeMux(), offsets: offsets, uiBuild: uiBuild}
	s.db.Store(db)

	// Important Offsets are read-only: they come from offsets.json on the server's disk.
	s.mux.HandleFunc("GET /api/offsets", s.handleOffsets)

	s.mux.HandleFunc("GET /api/stats", s.handleStats)
	s.mux.HandleFunc("GET /api/classes", s.handleClasses)
	s.mux.HandleFunc("GET /api/class/{name}", s.handleClass)
	s.mux.HandleFunc("GET /api/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/source", s.handleSource)
	s.mux.HandleFunc("GET /api/types", s.handleTypes)
	s.mux.HandleFunc("GET /api/export.json", s.handleExport)
	s.mux.HandleFunc("POST /api/reload", s.handleReload)
	s.mux.Handle("/", newStaticHandler(static, indexHTML))
	return s, nil
}

func (s *Server) DB() *DB { return s.db.Load() }

// Reload re-parses the source file and swaps the dataset.
func (s *Server) Reload() (*DB, error) {
	db, err := Load(s.path)
	if err != nil {
		return nil, err
	}
	s.db.Store(db)
	return db, nil
}

// Watch polls the source file and reloads when it changes.
func (s *Server) Watch(interval time.Duration, stat func() (int64, time.Time, bool)) {
	lastSize, lastMod, _ := stat()
	for {
		time.Sleep(interval)
		size, mod, ok := stat()
		if !ok || (size == lastSize && mod.Equal(lastMod)) {
			continue
		}
		// Debounce: wait for the writer to finish.
		time.Sleep(500 * time.Millisecond)
		size, mod, _ = stat()
		lastSize, lastMod = size, mod
		db, err := s.Reload()
		if err != nil {
			log.Printf("reload failed: %v", err)
			continue
		}
		log.Printf("reloaded %s: %d classes, %d fields in %s", s.path, len(db.Classes), db.FieldCount, db.ParseTime.Round(time.Millisecond))
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Vary", "Accept-Encoding")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "no-cache")
	}
	s.mux.ServeHTTP(w, r)
}

func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if len(b) > 1024 && acceptsGzip(r) {
		b = gzipBytes(b)
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	w.Write(b)
}

func writeErr(w http.ResponseWriter, r *http.Request, status int, msg string) {
	writeJSON(w, r, status, map[string]string{"error": msg})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	db := s.DB()
	writeJSON(w, r, 200, map[string]any{
		"source":        db.Source,
		"loaded_at":     db.LoadedAt,
		"parse_ms":      float64(db.ParseTime.Microseconds()) / 1000,
		"classes":       len(db.Classes),
		"fields":        db.FieldCount,
		"skipped_empty": db.SkippedEmpty,
		"lines":         len(db.Lines),
		"etag":          db.LoadedAt.UnixNano(),
		"ui_build":      s.uiBuild,
	})
}

func (s *Server) handleClasses(w http.ResponseWriter, r *http.Request) {
	db := s.DB()
	etag := `"` + strconv.FormatInt(db.LoadedAt.UnixNano(), 36) + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	b := db.summaryJSON
	if acceptsGzip(r) {
		b = db.summaryJSONGz
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Write(b)
}

func (s *Server) handleClass(w http.ResponseWriter, r *http.Request) {
	db := s.DB()
	c, ok := db.ByName[r.PathValue("name")]
	if !ok {
		writeErr(w, r, 404, "class not found")
		return
	}
	writeJSON(w, r, 200, c)
}

func clampInt(s string, def, min, max int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	raw := strings.TrimSpace(q.Get("q"))
	if len(raw) > 200 {
		raw = raw[:200]
	}
	classLimit := clampInt(q.Get("classes"), 40, 0, 500)
	fieldLimit := clampInt(q.Get("limit"), 150, 0, 5000)
	res := s.DB().Search(raw, classLimit, fieldLimit)
	res.Offsets = s.offsets.Search(raw)
	if res.Offsets == nil {
		res.Offsets = []OffsetEntry{}
	}
	writeJSON(w, r, 200, res)
}

// handleOffsets serves the read-only Important Offsets document (from offsets.json).
func (s *Server) handleOffsets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, 200, s.offsets.Doc())
}

// handleSource returns raw lines [from, to] of the source file (1-indexed, inclusive).
func (s *Server) handleSource(w http.ResponseWriter, r *http.Request) {
	db := s.DB()
	q := r.URL.Query()
	from := clampInt(q.Get("from"), 1, 1, len(db.Lines))
	to := clampInt(q.Get("to"), from, from, len(db.Lines))
	if to-from > 5000 {
		to = from + 5000
	}
	writeJSON(w, r, 200, map[string]any{
		"from":  from,
		"to":    to,
		"total": len(db.Lines),
		"lines": db.Lines[from-1 : to],
	})
}

func (s *Server) handleTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, 200, s.DB().TypeStats())
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	db := s.DB()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="sdk.json"`)
	var out = any(w)
	if acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		zw, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		defer zw.Close()
		out = zw
	}
	enc := json.NewEncoder(out.(interface{ Write([]byte) (int, error) }))
	if q := r.URL.Query(); q.Get("pretty") == "1" {
		enc.SetIndent("", "  ")
	}
	doc := db.ExportDoc()
	doc.Offsets = s.offsets.Doc().Groups
	if err := enc.Encode(doc); err != nil {
		log.Printf("export: %v", err)
	}
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	db, err := s.Reload()
	if err != nil {
		writeErr(w, r, 500, err.Error())
		return
	}
	log.Printf("manual reload: %d classes, %d fields in %s", len(db.Classes), db.FieldCount, db.ParseTime.Round(time.Millisecond))
	s.handleStats(w, r)
}
