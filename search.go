package main

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// ClassHit is a class-name search match.
type ClassHit struct {
	Name   string `json:"name"`
	Fields int    `json:"fields"`
	Line   int    `json:"line"`
}

// FieldHit is a field search match, including the raw source line for previews.
type FieldHit struct {
	Class  string `json:"class"`
	Name   string `json:"name"`
	Hex    string `json:"hex"`
	Offset uint64 `json:"offset"`
	Type   string `json:"type"`
	Line   int    `json:"line"`
	Src    string `json:"src"`
}

// SearchResult is the response body for /api/search.
type SearchResult struct {
	Query      string        `json:"query"`
	Terms      []string      `json:"terms"`
	TookMS     float64       `json:"took_ms"`
	Classes    []ClassHit    `json:"classes"`
	ClassTotal int           `json:"class_total"`
	Fields     []FieldHit    `json:"fields"`
	FieldTotal int           `json:"field_total"`
	Offsets    []OffsetEntry `json:"offsets"` // Important Offsets (offsets.json) matches
}

type query struct {
	terms      []string // free text, lowercased, all must match
	classTerms []string // class:/in: filters
	fieldTerms []string // field: filters
	typeTerms  []string // type: filters
	offset     *uint64  // exact offset (0x..., offset:, at:)
}

func parseHexOffset(s string) (uint64, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !strings.HasPrefix(s, "0x") || len(s) <= 2 {
		return 0, false
	}
	v, err := strconv.ParseUint(s[2:], 16, 64)
	return v, err == nil
}

func parseQuery(raw string) query {
	var q query
	for _, tok := range strings.Fields(strings.ToLower(raw)) {
		switch {
		case strings.HasPrefix(tok, "type:"):
			if v := tok[5:]; v != "" {
				q.typeTerms = append(q.typeTerms, v)
			}
		case strings.HasPrefix(tok, "class:"):
			if v := tok[6:]; v != "" {
				q.classTerms = append(q.classTerms, v)
			}
		case strings.HasPrefix(tok, "in:"):
			if v := tok[3:]; v != "" {
				q.classTerms = append(q.classTerms, v)
			}
		case strings.HasPrefix(tok, "field:"):
			if v := tok[6:]; v != "" {
				q.fieldTerms = append(q.fieldTerms, v)
			}
		case strings.HasPrefix(tok, "offset:"), strings.HasPrefix(tok, "at:"):
			v := tok[strings.IndexByte(tok, ':')+1:]
			if !strings.HasPrefix(v, "0x") {
				v = "0x" + v
			}
			if off, ok := parseHexOffset(v); ok {
				q.offset = &off
			}
		default:
			if off, ok := parseHexOffset(tok); ok {
				q.offset = &off
				continue
			}
			// Class::Field or Class.Field shorthand.
			if sep := strings.Index(tok, "::"); sep > 0 && sep < len(tok)-2 {
				q.classTerms = append(q.classTerms, tok[:sep])
				q.fieldTerms = append(q.fieldTerms, tok[sep+2:])
				continue
			}
			if sep := strings.IndexByte(tok, '.'); sep > 0 && sep < len(tok)-1 {
				q.classTerms = append(q.classTerms, tok[:sep])
				q.fieldTerms = append(q.fieldTerms, tok[sep+1:])
				continue
			}
			q.terms = append(q.terms, tok)
		}
	}
	return q
}

func (q query) empty() bool {
	return len(q.terms) == 0 && len(q.classTerms) == 0 && len(q.fieldTerms) == 0 &&
		len(q.typeTerms) == 0 && q.offset == nil
}

func containsAll(s string, terms []string) bool {
	for _, t := range terms {
		if !strings.Contains(s, t) {
			return false
		}
	}
	return true
}

// Search runs a case-insensitive substring search over classes and fields.
func (db *DB) Search(raw string, classLimit, fieldLimit int) SearchResult {
	start := time.Now()
	q := parseQuery(raw)
	res := SearchResult{Query: raw, Terms: append(append(append([]string{}, q.terms...), q.fieldTerms...), q.classTerms...), Classes: []ClassHit{}, Fields: []FieldHit{}}
	if q.empty() {
		return res
	}
	joined := strings.Join(q.terms, " ")

	// --- classes: only when the query is text-only (no field/type/offset filters)
	if len(q.terms) > 0 && len(q.fieldTerms) == 0 && len(q.typeTerms) == 0 && q.offset == nil {
		var buckets [3][]int32
		for ci, name := range db.classLower {
			if !containsAll(name, q.terms) || !containsAll(name, q.classTerms) {
				continue
			}
			rank := 2
			if name == joined {
				rank = 0
			} else if strings.HasPrefix(name, joined) {
				rank = 1
			}
			buckets[rank] = append(buckets[rank], int32(ci))
			res.ClassTotal++
		}
		less := func(a, b int32) bool {
			na, nb := db.classLower[a], db.classLower[b]
			if la, lb := len(na), len(nb); la != lb {
				return la < lb
			}
			return na < nb
		}
		for _, b := range buckets {
			need := classLimit - len(res.Classes)
			if need <= 0 {
				break
			}
			if len(b) == 0 {
				continue
			}
			if len(b) > need {
				partialSort(b, need, less)
				b = b[:need]
			} else {
				sort.Slice(b, func(i, j int) bool { return less(b[i], b[j]) })
			}
			for _, ci := range b {
				c := db.Classes[ci]
				res.Classes = append(res.Classes, ClassHit{Name: c.Name, Fields: len(c.Fields), Line: c.Line})
			}
		}
	}

	// --- fields: bucket matches by rank so we only sort what we need to return.
	var buckets [4][]int32
	total := 0
	for i, ref := range db.refs {
		fl := db.fieldLower[i]
		cl := db.classLower[ref.class]
		c := db.Classes[ref.class]
		f := &c.Fields[ref.field]

		if q.offset != nil && f.Offset != *q.offset {
			continue
		}
		if !containsAll(db.typeLower[i], q.typeTerms) {
			continue
		}
		if !containsAll(cl, q.classTerms) || !containsAll(fl, q.fieldTerms) {
			continue
		}
		rank := 3
		if len(q.terms) > 0 {
			// Each free term may match either the field name or the class name.
			ok := true
			inField := true
			for _, t := range q.terms {
				fm := strings.Contains(fl, t)
				if !fm && !strings.Contains(cl, t) {
					ok = false
					break
				}
				inField = inField && fm
			}
			if !ok {
				continue
			}
			switch {
			case fl == joined:
				rank = 0
			case strings.HasPrefix(fl, joined):
				rank = 1
			case inField:
				rank = 2
			}
		} else if len(q.fieldTerms) > 0 {
			fj := strings.Join(q.fieldTerms, " ")
			switch {
			case fl == fj:
				rank = 0
			case strings.HasPrefix(fl, fj):
				rank = 1
			default:
				rank = 2
			}
		}
		buckets[rank] = append(buckets[rank], int32(i))
		total++
	}
	res.FieldTotal = total

	less := func(a, b int32) bool {
		fa, fb := db.fieldLower[a], db.fieldLower[b]
		if la, lb := len(fa), len(fb); la != lb {
			return la < lb
		}
		if fa != fb {
			return fa < fb
		}
		ca, cb := db.classLower[db.refs[a].class], db.classLower[db.refs[b].class]
		if ca != cb {
			return ca < cb
		}
		return db.Classes[db.refs[a].class].Fields[db.refs[a].field].Offset <
			db.Classes[db.refs[b].class].Fields[db.refs[b].field].Offset
	}

	hits := make([]FieldHit, 0, min(total, fieldLimit))
	for _, b := range buckets {
		if len(hits) >= fieldLimit {
			break
		}
		if len(b) == 0 {
			continue
		}
		need := fieldLimit - len(hits)
		if len(b) > need {
			partialSort(b, need, less)
			b = b[:need]
		} else {
			sort.Slice(b, func(i, j int) bool { return less(b[i], b[j]) })
		}
		for _, i := range b {
			ref := db.refs[i]
			c := db.Classes[ref.class]
			f := &c.Fields[ref.field]
			h := FieldHit{Class: c.Name, Name: f.Name, Hex: f.Hex, Offset: f.Offset, Type: f.Type, Line: f.Line}
			if f.Line > 0 && f.Line <= len(db.Lines) {
				h.Src = strings.TrimSpace(db.Lines[f.Line-1])
			}
			hits = append(hits, h)
		}
	}
	res.Fields = hits
	res.TookMS = float64(time.Since(start).Microseconds()) / 1000
	return res
}

// partialSort rearranges b so that b[:k] holds the k smallest elements in sorted
// order (quickselect + sort of the prefix). Much cheaper than sorting everything
// when k is small relative to len(b).
func partialSort(b []int32, k int, less func(a, b int32) bool) {
	if k <= 0 {
		return
	}
	if k >= len(b) {
		sort.Slice(b, func(i, j int) bool { return less(b[i], b[j]) })
		return
	}
	lo, hi := 0, len(b)-1
	for lo < hi {
		p := partition(b, lo, hi, less)
		switch {
		case p == k:
			lo = hi
		case p < k:
			lo = p + 1
		default:
			hi = p - 1
		}
	}
	s := b[:k]
	sort.Slice(s, func(i, j int) bool { return less(s[i], s[j]) })
}

func partition(b []int32, lo, hi int, less func(a, b int32) bool) int {
	mid := lo + (hi-lo)/2
	if less(b[mid], b[lo]) {
		b[mid], b[lo] = b[lo], b[mid]
	}
	if less(b[hi], b[lo]) {
		b[hi], b[lo] = b[lo], b[hi]
	}
	if less(b[hi], b[mid]) {
		b[hi], b[mid] = b[mid], b[hi]
	}
	pivot := b[mid]
	b[mid], b[hi] = b[hi], b[mid]
	i := lo
	for j := lo; j < hi; j++ {
		if less(b[j], pivot) {
			b[i], b[j] = b[j], b[i]
			i++
		}
	}
	b[i], b[hi] = b[hi], b[i]
	return i
}
