package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Field is a single offset entry inside a class.
type Field struct {
	Name   string `json:"name"`
	Offset uint64 `json:"offset"`
	Hex    string `json:"hex"`
	Type   string `json:"type"`
	Line   int    `json:"line"`
}

// Class is a namespace block from the SDK dump.
type Class struct {
	Name    string  `json:"name"`
	Line    int     `json:"line"`
	EndLine int     `json:"end_line"`
	Fields  []Field `json:"fields"`
}

// ClassSummary is the lightweight entry used for the sidebar list.
type ClassSummary struct {
	Name   string `json:"n"`
	Fields int    `json:"f"`
	Line   int    `json:"l"`
}

// Export is the JSON document written by -export and served at /api/export.json.
type Export struct {
	Source    string        `json:"source"`
	Generated string        `json:"generated"`
	Classes   []*Class      `json:"classes"`
	Offsets   []OffsetGroup `json:"important_offsets,omitempty"`
}

type fieldRef struct {
	class int32
	field int32
}

// DB is the fully parsed, indexed dataset. It is immutable after build.
type DB struct {
	Source       string
	LoadedAt     time.Time
	ParseTime    time.Duration
	Classes      []*Class
	ByName       map[string]*Class
	Lines        []string // raw source lines (1-indexed via Lines[i-1])
	FieldCount   int
	SkippedEmpty int // namespaces with no fields (not shown anywhere)

	classLower []string
	fieldLower []string
	typeLower  []string
	refs       []fieldRef

	summaryJSON   []byte
	summaryJSONGz []byte
}

// Load reads either a .txt SDK dump or a .json export produced by this tool.
func Load(path string) (*DB, error) {
	start := time.Now()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var db *DB
	if strings.EqualFold(filepath.Ext(path), ".json") {
		db, err = parseJSON(data)
	} else {
		db, err = parseText(data)
	}
	if err != nil {
		return nil, err
	}
	db.Source = path
	db.LoadedAt = time.Now()
	db.buildIndex()
	db.ParseTime = time.Since(start)
	return db, nil
}

func parseText(data []byte) (*DB, error) {
	db := &DB{ByName: make(map[string]*Class, 32768)}
	rawLines := bytes.Split(data, []byte("\n"))
	db.Lines = make([]string, len(rawLines))

	type frame struct {
		class       *Class
		hadChildren bool
	}
	var stack []*frame

	for i, raw := range rawLines {
		raw = bytes.TrimRight(raw, "\r")
		db.Lines[i] = string(raw)
		line := strings.TrimSpace(db.Lines[i])
		lineNo := i + 1

		switch {
		case strings.HasPrefix(line, "namespace ") && strings.HasSuffix(line, "{"):
			name := strings.TrimSpace(line[len("namespace ") : len(line)-1])
			if len(stack) > 0 {
				stack[len(stack)-1].hadChildren = true
			}
			stack = append(stack, &frame{class: &Class{Name: name, Line: lineNo, Fields: []Field{}}})

		case strings.HasPrefix(line, "inline uint64_t "):
			if len(stack) == 0 {
				continue
			}
			f, ok := parseFieldLine(line[len("inline uint64_t "):], lineNo)
			if !ok {
				continue
			}
			top := stack[len(stack)-1].class
			top.Fields = append(top.Fields, f)

		case strings.HasPrefix(line, "}"):
			if len(stack) == 0 {
				continue
			}
			fr := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			fr.class.EndLine = lineNo
			// Skip containers (namespaces that only wrap other namespaces) and empty
			// classes such as `namespace ability_removed_from_scene { }`.
			if len(fr.class.Fields) == 0 {
				db.SkippedEmpty++
				continue
			}
			if _, dup := db.ByName[fr.class.Name]; dup {
				// Keep both but disambiguate so deep links stay unique.
				fr.class.Name = fmt.Sprintf("%s@%d", fr.class.Name, fr.class.Line)
			}
			db.ByName[fr.class.Name] = fr.class
			db.Classes = append(db.Classes, fr.class)
		}
	}
	if len(db.Classes) == 0 {
		return nil, fmt.Errorf("no namespaces found; is this an SDK dump?")
	}
	return db, nil
}

// parseFieldLine parses "Name = 0x298; // FName".
func parseFieldLine(s string, lineNo int) (Field, bool) {
	eq := strings.Index(s, " = ")
	if eq < 0 {
		return Field{}, false
	}
	name := s[:eq]
	rest := s[eq+3:]
	semi := strings.IndexByte(rest, ';')
	if semi < 0 {
		return Field{}, false
	}
	hex := strings.TrimSpace(rest[:semi])
	typ := ""
	if c := strings.Index(rest, "//"); c >= 0 {
		typ = strings.TrimSpace(rest[c+2:])
	}
	val, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(hex), "0x"), 16, 64)
	if err != nil {
		return Field{}, false
	}
	return Field{Name: name, Offset: val, Hex: hex, Type: typ, Line: lineNo}, true
}

func parseJSON(data []byte) (*DB, error) {
	var ex Export
	if err := json.Unmarshal(data, &ex); err != nil {
		return nil, err
	}
	db := &DB{ByName: make(map[string]*Class, len(ex.Classes))}
	// Re-synthesise source text so the source view still works for JSON inputs.
	db.Lines = append(db.Lines, "// regenerated from JSON export", "")
	for _, c := range ex.Classes {
		if len(c.Fields) == 0 {
			db.SkippedEmpty++
			continue
		}
		c.Line = len(db.Lines) + 1
		db.Lines = append(db.Lines, fmt.Sprintf("namespace %s {", c.Name))
		for i := range c.Fields {
			f := &c.Fields[i]
			if f.Hex == "" {
				f.Hex = fmt.Sprintf("0x%x", f.Offset)
			}
			f.Line = len(db.Lines) + 1
			db.Lines = append(db.Lines, fmt.Sprintf("    inline uint64_t %s = %s; // %s", f.Name, f.Hex, f.Type))
		}
		c.EndLine = len(db.Lines) + 1
		db.Lines = append(db.Lines, "}", "")
		db.ByName[c.Name] = c
		db.Classes = append(db.Classes, c)
	}
	return db, nil
}

func (db *DB) buildIndex() {
	n := 0
	for _, c := range db.Classes {
		n += len(c.Fields)
	}
	db.FieldCount = n
	db.classLower = make([]string, len(db.Classes))
	db.fieldLower = make([]string, 0, n)
	db.typeLower = make([]string, 0, n)
	db.refs = make([]fieldRef, 0, n)

	summaries := make([]ClassSummary, len(db.Classes))
	for ci, c := range db.Classes {
		db.classLower[ci] = strings.ToLower(c.Name)
		summaries[ci] = ClassSummary{Name: c.Name, Fields: len(c.Fields), Line: c.Line}
		for fi, f := range c.Fields {
			db.fieldLower = append(db.fieldLower, strings.ToLower(f.Name))
			db.typeLower = append(db.typeLower, strings.ToLower(f.Type))
			db.refs = append(db.refs, fieldRef{class: int32(ci), field: int32(fi)})
		}
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		return strings.ToLower(summaries[i].Name) < strings.ToLower(summaries[j].Name)
	})
	db.summaryJSON, _ = json.Marshal(summaries)
	db.summaryJSONGz = gzipBytes(db.summaryJSON)
}

// ExportDoc returns the JSON-serialisable document for the whole dataset.
func (db *DB) ExportDoc() Export {
	return Export{
		Source:    db.Source,
		Generated: time.Now().UTC().Format(time.RFC3339),
		Classes:   db.Classes,
	}
}

// TypeStats returns the global type distribution, most common first.
func (db *DB) TypeStats() []struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
} {
	counts := map[string]int{}
	for _, c := range db.Classes {
		for _, f := range c.Fields {
			counts[f.Type]++
		}
	}
	out := make([]struct {
		Type  string `json:"type"`
		Count int    `json:"count"`
	}, 0, len(counts))
	for t, n := range counts {
		out = append(out, struct {
			Type  string `json:"type"`
			Count int    `json:"count"`
		}{t, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Type < out[j].Type
	})
	return out
}
