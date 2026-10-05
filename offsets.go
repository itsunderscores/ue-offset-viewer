package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// OffsetEntry is one hand-maintained offset from offsets.json.
type OffsetEntry struct {
	Group  string `json:"group"`
	Name   string `json:"name"`
	Offset uint64 `json:"offset"`
	Hex    string `json:"hex"`
}

// OffsetGroup is a top-level key in offsets.json ("core", "player", ...), in file order.
type OffsetGroup struct {
	Name  string        `json:"name"`
	Items []OffsetEntry `json:"items"`
}

// OffsetsDoc is the parsed, read-only contents of offsets.json.
type OffsetsDoc struct {
	Path     string        `json:"-"` // server-side only; never sent to the browser
	LoadedAt time.Time     `json:"loaded_at"`
	Groups   []OffsetGroup `json:"groups"`
	Total    int           `json:"total"`
	Meta     *OffsetsMeta  `json:"meta,omitempty"`     // from the reserved "_meta" section
	Snippets []Snippet     `json:"snippets,omitempty"` // from the reserved "_code" section
	Error    string        `json:"error,omitempty"`    // set when the file exists but could not be parsed
}

// Snippet is a code block attached to the Important Offsets page, optionally tied
// to one entry by name ("for"). In offsets.json:
//
//	"_code": {
//	  "decrypt_uworld": { "for": "UWORLD", "lang": "cpp", "note": "...", "code": ["line 1", "line 2"] },
//	  "quick_note":     "a plain string (or array of lines) is also accepted"
//	}
type Snippet struct {
	Title string `json:"title"`
	For   string `json:"for,omitempty"`
	Lang  string `json:"lang,omitempty"`
	Note  string `json:"note,omitempty"`
	Code  string `json:"code"`
}

// codeValue decodes "..." | ["line", ...] into a single string.
func codeValue(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var lines []string
	if json.Unmarshal(raw, &lines) == nil {
		return strings.Join(lines, "\n"), true
	}
	return "", false
}

func parseSnippet(title string, raw json.RawMessage) (Snippet, error) {
	sn := Snippet{Title: title}
	if code, ok := codeValue(raw); ok {
		sn.Code = code
		return sn, nil
	}
	var obj struct {
		For  string          `json:"for"`
		Lang string          `json:"lang"`
		Note string          `json:"note"`
		Code json.RawMessage `json:"code"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return sn, fmt.Errorf("must be a string, an array of lines, or an object with \"code\"")
	}
	code, ok := codeValue(obj.Code)
	if !ok || strings.TrimSpace(code) == "" {
		return sn, fmt.Errorf("\"code\" must be a string or an array of lines")
	}
	sn.For, sn.Lang, sn.Note, sn.Code = obj.For, obj.Lang, obj.Note, code
	return sn, nil
}

// OffsetsMeta describes the game build the offsets belong to. Comes from a
// "_meta" object in offsets.json, e.g. {"version": "42.30-CL-58557680", "date": "10/01/2026"}.
type OffsetsMeta struct {
	Version string            `json:"version,omitempty"`
	Date    string            `json:"date,omitempty"`     // normalised to YYYY-MM-DD when parseable
	DateRaw string            `json:"date_raw,omitempty"` // exactly as written in the file
	Extra   map[string]string `json:"extra,omitempty"`    // any other keys
}

var dateLayouts = []string{"2006-01-02", "01/02/2006", "1/2/2006", "01-02-2006", "2006/01/02", "Jan 2, 2006", "2 Jan 2006", "January 2, 2006"}

func normalizeDate(raw string) string {
	raw = strings.TrimSpace(raw)
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, raw); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

// Offsets holds the current document and reloads it when the file changes.
// It is strictly read-only from the web: there is no API that writes to it.
type Offsets struct {
	path string
	doc  atomic.Pointer[OffsetsDoc]
}

func OpenOffsets(path string) *Offsets {
	o := &Offsets{path: path}
	doc, err := loadOffsetsFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Printf("important offsets: %s not found (create it to populate the Important Offsets view)", path)
			doc = &OffsetsDoc{Path: path, LoadedAt: time.Now(), Groups: []OffsetGroup{}}
		} else {
			log.Printf("important offsets: %v", err)
			doc = &OffsetsDoc{Path: path, LoadedAt: time.Now(), Groups: []OffsetGroup{}, Error: publicErr(err, path)}
		}
	} else {
		log.Printf("important offsets: %d entries in %d groups from %s", doc.Total, len(doc.Groups), path)
	}
	o.doc.Store(doc)
	return o
}

func (o *Offsets) Doc() *OffsetsDoc { return o.doc.Load() }
func (o *Offsets) Path() string     { return o.path }

// Reload re-reads the file. On a parse error the previous data is kept and the
// error is surfaced in the document so the UI can show it.
func (o *Offsets) Reload() {
	doc, err := loadOffsetsFile(o.path)
	if err != nil {
		prev := o.doc.Load()
		if errors.Is(err, os.ErrNotExist) {
			o.doc.Store(&OffsetsDoc{Path: o.path, LoadedAt: time.Now(), Groups: []OffsetGroup{}})
			log.Printf("important offsets: %s removed", o.path)
			return
		}
		log.Printf("important offsets: reload failed, keeping previous data: %v", err)
		cp := *prev
		cp.Error = publicErr(err, o.path)
		o.doc.Store(&cp)
		return
	}
	o.doc.Store(doc)
	log.Printf("important offsets: reloaded %d entries in %d groups", doc.Total, len(doc.Groups))
}

// publicErr strips the server-side file path from an error before it is shown in the UI.
func publicErr(err error, path string) string {
	return strings.TrimPrefix(err.Error(), path+": ")
}

// Watch polls the file and reloads it when its size or mtime changes.
func (o *Offsets) Watch(interval time.Duration) {
	stat := func() (int64, time.Time, bool) {
		st, err := os.Stat(o.path)
		if err != nil {
			return 0, time.Time{}, false
		}
		return st.Size(), st.ModTime(), true
	}
	lastSize, lastMod, lastOK := stat()
	for {
		time.Sleep(interval)
		size, mod, ok := stat()
		if ok == lastOK && size == lastSize && mod.Equal(lastMod) {
			continue
		}
		time.Sleep(300 * time.Millisecond) // let the editor finish writing
		lastSize, lastMod, lastOK = stat()
		o.Reload()
	}
}

// loadOffsetsFile parses the file while preserving key order, which encoding/json
// maps would lose. Expected shape:
//
//	{ "core": { "UWORLD": "0x1AC816F8", ... }, "player": { ... } }
//
// Values may be hex strings ("0x1AC", "1AC"), decimal strings ("416d") or JSON numbers.
func loadOffsetsFile(path string) (*OffsetsDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	expect := func(want json.Delim) error {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); !ok || d != want {
			return fmt.Errorf("expected %q, got %v", want, t)
		}
		return nil
	}

	doc := &OffsetsDoc{Path: path, LoadedAt: time.Now(), Groups: []OffsetGroup{}}
	if err := expect('{'); err != nil {
		return nil, fmt.Errorf("%s: top level must be an object: %w", path, err)
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		group := t.(string)
		if err := expect('{'); err != nil {
			return nil, fmt.Errorf("%s: group %q must be an object of name -> offset: %w", path, group, err)
		}
		if group == "_code" || group == "_snippets" {
			for dec.More() {
				t, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("%s: %w", path, err)
				}
				title := t.(string)
				var raw json.RawMessage
				if err := dec.Decode(&raw); err != nil {
					return nil, fmt.Errorf("%s: %s.%s: %w", path, group, title, err)
				}
				sn, err := parseSnippet(title, raw)
				if err != nil {
					return nil, fmt.Errorf("%s: %s.%s: %w", path, group, title, err)
				}
				doc.Snippets = append(doc.Snippets, sn)
			}
			if err := expect('}'); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			continue
		}
		if strings.HasPrefix(group, "_") {
			// Reserved metadata section: plain string values, not offsets.
			meta := doc.Meta
			if meta == nil {
				meta = &OffsetsMeta{}
				doc.Meta = meta
			}
			for dec.More() {
				t, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("%s: %w", path, err)
				}
				key := strings.ToLower(t.(string))
				v, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("%s: %w", path, err)
				}
				val := fmt.Sprint(v)
				switch key {
				case "version", "build", "cl":
					meta.Version = val
				case "date", "updated", "dumped":
					meta.DateRaw = val
					meta.Date = normalizeDate(val)
				default:
					if meta.Extra == nil {
						meta.Extra = map[string]string{}
					}
					meta.Extra[t.(string)] = val
				}
			}
			if err := expect('}'); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			continue
		}
		g := OffsetGroup{Name: group, Items: []OffsetEntry{}}
		for dec.More() {
			t, err := dec.Token()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			name := t.(string)
			v, err := dec.Token()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			off, err := offsetFromToken(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %s.%s: %w", path, group, name, err)
			}
			g.Items = append(g.Items, OffsetEntry{Group: group, Name: name, Offset: off, Hex: displayHex(v, off)})
		}
		if err := expect('}'); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		doc.Total += len(g.Items)
		doc.Groups = append(doc.Groups, g)
	}
	if err := expect('}'); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%s: unexpected content after the closing brace", path)
	}
	return doc, nil
}

// displayHex keeps the hex exactly as written in the file (e.g. "0x1AC816F8"),
// falling back to a canonical lower-case form for decimal/numeric values.
func displayHex(v json.Token, off uint64) string {
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		l := strings.ToLower(s)
		if strings.HasPrefix(l, "0x") {
			return s
		}
		if !strings.HasSuffix(l, "d") {
			return "0x" + s
		}
	}
	return fmt.Sprintf("0x%x", off)
}

func offsetFromToken(v json.Token) (uint64, error) {
	switch x := v.(type) {
	case string:
		return ParseOffset(x)
	case json.Number:
		n, err := strconv.ParseUint(x.String(), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid number %q", x.String())
		}
		return n, nil
	default:
		return 0, fmt.Errorf("value must be a hex string like \"0x1A0\", got %v", v)
	}
}

// ParseOffset accepts "0x1a0", "1a0" (hex) or "416d" (explicit decimal).
func ParseOffset(raw string) (uint64, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	v = strings.ReplaceAll(v, "_", "")
	if v == "" {
		return 0, errors.New("offset is empty")
	}
	if strings.HasSuffix(v, "d") && !strings.HasPrefix(v, "0x") {
		n, err := strconv.ParseUint(strings.TrimSuffix(v, "d"), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid decimal offset %q", raw)
		}
		return n, nil
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(v, "0x"), 16, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid hex offset %q (use 0x1A0, or 416d for decimal)", raw)
	}
	return n, nil
}

// Search returns entries whose group or name matches the free-text terms, or whose
// offset equals an "0x..." term. type: filters never match (entries are untyped).
func (o *Offsets) Search(raw string) []OffsetEntry {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	q := parseQuery(raw)
	if len(q.typeTerms) > 0 {
		return nil
	}
	terms := append(append(append([]string{}, q.terms...), q.fieldTerms...), q.classTerms...)
	if q.offset == nil && len(terms) == 0 {
		return nil
	}
	var out []OffsetEntry
	for _, g := range o.Doc().Groups {
		for _, it := range g.Items {
			if q.offset != nil && it.Offset != *q.offset {
				continue
			}
			hay := strings.ToLower(g.Name + " " + it.Name + " " + it.Hex)
			if !containsAll(hay, terms) {
				continue
			}
			out = append(out, it)
		}
	}
	return out
}
