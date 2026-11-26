package main

// The entry index behind the web UI's Search tab (22.3b): every entry, with the
// few values needed to filter and order them, kept in the service. The library
// is too large to send to the browser (Erik's: 254k entries), so filtering,
// sorting and paging happen here, and the browser fetches the rows it shows.
//
// The index is read from the database through a read-only connection (as all
// pages are), not from the loaded library, and rebuilt when the database files
// change (databaseFingerprint). While a job runs, a stale index is used rather
// than rebuilding it under the job's feet. A column is read when it is first
// shown or sorted on.
//
// Crossref: most entries take year, venue and more from their parent. A
// missing value of an inheritable field is the parent's, shown as inherited.

import (
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// TWebIndex is the Search tab's index of all entries.
type TWebIndex struct {
	mu          sync.Mutex
	fingerprint string
	keys        []string
	pos         map[string]int32
	parent      []int32                   // the crossref parent's position; -1: none
	cols        map[string][]string       // a column's stored values, by position
	hay         []string                  // what a query is matched against (folded)
	base        []int32                   // the default order: newest first, then title
	fields      []string                  // the columns that can be chosen
	groups      map[string]map[int32]bool // the entries of each group
	groupList   []TWebGroup               // the groups, by name
	last        webIndexResult            // the last query's result, for paging
}

type webIndexResult struct {
	query, sortOn, groups string
	desc                  bool
	order                 []int32
	groupCounts           []TWebGroup // the groups in use by the listed entries, with their numbers there
}

// The current index, swapped for a new one once that is built: a search uses
// the current one meanwhile, so it never waits for a rebuild (11 s for 254k
// entries) except for the very first.
var (
	webIndexCurrent atomic.Pointer[TWebIndex]
	webIndexBuild   sync.Mutex
)

// webIndexFor returns the index: the current one (onlyIfMissing), or one
// rebuilt when the database changed since the current one was built.
func webIndexFor(onlyIfMissing bool) (*TWebIndex, error) {
	webIndexBuild.Lock()
	defer webIndexBuild.Unlock()
	fp := databaseFingerprint()
	cur := webIndexCurrent.Load()
	if cur != nil && (onlyIfMissing || cur.fingerprint == fp) {
		return cur, nil
	}
	webIndexBuilding.Store(true)
	defer webIndexBuilding.Store(false)
	x := &TWebIndex{}
	if err := x.build(fp); err != nil {
		return cur, err
	}
	if len(x.keys) == 0 { // the database not there yet
		return cur, nil
	}
	webIndexCurrent.Store(x)
	webIndexVersion.Add(1)
	return x, nil
}

// For the page's busy light: an index being built, and a count of the
// indexes swapped in (the page refreshes its tables when it changes).
var (
	webIndexBuilding atomic.Bool
	webIndexVersion  atomic.Int64
)

// webIndexStale tells whether the database changed since the current index
// was built (its lists do not show that yet).
func webIndexStale() bool {
	cur := webIndexCurrent.Load()
	return cur != nil && cur.fingerprint != databaseFingerprint()
}

// refreshWebIndex rebuilds the index when it is out of date and no job runs
// (called regularly by the web UI).
func refreshWebIndex(busy bool) {
	if !busy {
		webIndexFor(false) //nolint:errcheck
	}
}

// webPseudoFields are columns that are not bib_entries fields.
var webPseudoFields = []string{"citekey", "author", "editor", "groups"}

// webNotInherited are the fields an entry never takes from its crossref parent.
var webNotInherited = map[string]bool{
	"citekey": true, "entrytype": true, "crossref": true, "preferredalias": true, "dblp": true,
	"doi": true, "url": true, "urldate": true, "title": true, "author": true, "groups": true,
	"pages": true, "eprint": true, "eprinttype": true, "chapter": true,
}

// foldText is the form text is matched in: TeX markup resolved, lower case,
// accents dropped ("M{\"u}ller", "Müller" and "muller" all match "muller").
func foldText(s string) string {
	if strings.ContainsAny(s, `\{}~`) {
		s = texToText(s)
	}
	s = strings.ToLower(s)
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			var b strings.Builder
			for _, r := range norm.NFD.String(s) {
				if !unicode.Is(unicode.Mn, r) {
					b.WriteRune(r)
				}
			}
			return b.String()
		}
	}
	return s
}

// build reads a new index; fp is the database's fingerprint before reading.
func (x *TWebIndex) build(fp string) error {
	ro, err := openReadOnly()
	if err != nil {
		return err
	}
	defer ro.Close()
	started := time.Now()
	defer func() {
		fmt.Fprintf(os.Stderr, "%s  search index: %d entries in %.1fs\n", time.Now().Format(time.DateTime), len(x.keys), time.Since(started).Seconds())
	}()

	n := map[string]int32{}
	var keys []string
	need := []string{"title", "year", "crossref", "preferredalias", "doi", "booktitle", "journal"}
	rows, err := ro.Query(`SELECT entry_key, field, value FROM bib_entries
		WHERE field IN ('entrytype', 'title', 'year', 'crossref', 'preferredalias', 'doi', 'booktitle', 'journal')
		ORDER BY entry_key`)
	if err != nil {
		return err
	}
	type kv struct{ key, field, value string }
	var all []kv
	for rows.Next() {
		var r kv
		if rows.Scan(&r.key, &r.field, &r.value) == nil {
			all = append(all, r)
			if _, ok := n[r.key]; !ok {
				n[r.key] = int32(len(keys))
				keys = append(keys, r.key)
			}
		}
	}
	rows.Close()
	cols := map[string][]string{"entrytype": make([]string, len(keys))}
	for _, f := range need {
		cols[f] = make([]string, len(keys))
	}
	for _, r := range all {
		cols[r.field][n[r.key]] = r.value
	}
	all = nil

	x.keys, x.pos, x.cols = keys, n, cols
	x.parent = make([]int32, len(keys))
	for i := range keys {
		x.parent[i] = -1
		if p, ok := n[cols["crossref"][i]]; ok && cols["crossref"][i] != "" {
			x.parent[i] = p
		}
	}
	for _, role := range []string{"author", "editor"} {
		if err := x.loadColumn(ro, role); err != nil {
			return err
		}
	}

	x.hay = make([]string, len(keys))
	for i, key := range keys {
		var b strings.Builder
		for _, part := range []string{key, cols["preferredalias"][i], cols["title"][i], x.cols["author"][i],
			x.cols["editor"][i], x.value("year", int32(i)), x.value("booktitle", int32(i)),
			x.value("journal", int32(i)), cols["doi"][i]} {
			b.WriteString(part)
			b.WriteByte('\n')
		}
		x.hay[i] = foldText(b.String())
	}

	x.base = make([]int32, len(keys))
	sortKeys := make([]string, len(keys))
	for i := range keys {
		x.base[i] = int32(i)
		sortKeys[i] = foldText(cols["title"][i])
	}
	years := make([]string, len(keys))
	for i := range keys {
		years[i] = x.value("year", int32(i))
	}
	sort.SliceStable(x.base, func(a, b int) bool {
		i, j := x.base[a], x.base[b]
		if years[i] != years[j] {
			return years[i] > years[j]
		}
		return sortKeys[i] < sortKeys[j]
	})

	x.fields = append([]string{}, webPseudoFields...)
	if fRows, err := ro.Query(`SELECT DISTINCT field FROM bib_entries WHERE field NOT IN ('author', 'editor', 'groups', 'citekey')`); err == nil {
		for fRows.Next() {
			var f string
			if fRows.Scan(&f) == nil {
				x.fields = append(x.fields, f)
			}
		}
		fRows.Close()
	}
	sort.Strings(x.fields)
	x.groups = map[string]map[int32]bool{}
	if gRows, err := ro.Query(`SELECT group_name, entry_key FROM bib_groups`); err == nil {
		for gRows.Next() {
			var g, k string
			if gRows.Scan(&g, &k) != nil {
				continue
			}
			if i, ok := n[k]; ok {
				if x.groups[g] == nil {
					x.groups[g] = map[int32]bool{}
				}
				x.groups[g][i] = true
			}
		}
		gRows.Close()
	}
	for g, members := range x.groups {
		x.groupList = append(x.groupList, TWebGroup{g, len(members)})
	}
	sort.Slice(x.groupList, func(a, b int) bool {
		return strings.ToLower(x.groupList[a].Name) < strings.ToLower(x.groupList[b].Name)
	})
	x.fingerprint = fp
	return nil
}

// loadColumn reads a column's stored values (once per index).
func (x *TWebIndex) loadColumn(ro *sql.DB, field string) error {
	if _, ok := x.cols[field]; ok {
		return nil
	}
	col := make([]string, len(x.keys))
	var rows *sql.Rows
	var err error
	switch field {
	case "citekey":
		copy(col, x.keys)
		x.cols[field] = col
		return nil
	case "author", "editor":
		rows, err = ro.Query(`SELECT r.entry_key, c.name FROM contributor_roles r JOIN contributors c ON c.id = r.contributor_id
			WHERE r.role = ? ORDER BY r.entry_key, r.position`, field)
	case "groups":
		rows, err = ro.Query(`SELECT entry_key, group_name FROM bib_groups ORDER BY entry_key, group_name`)
	default:
		rows, err = ro.Query(`SELECT entry_key, value FROM bib_entries WHERE field = ?`, field)
	}
	if err != nil {
		return err
	}
	defer rows.Close()
	sep := " and "
	if field == "groups" {
		sep = ", "
	}
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) != nil {
			continue
		}
		if i, ok := x.pos[k]; ok {
			if col[i] != "" {
				col[i] += sep + v
			} else {
				col[i] = v
			}
		}
	}
	x.cols[field] = col
	return nil
}

// value is an entry's value in a loaded column, its parent's when it has none.
func (x *TWebIndex) value(field string, i int32) string {
	v, _ := x.valueInherited(field, i)
	return v
}

func (x *TWebIndex) valueInherited(field string, i int32) (string, bool) {
	col := x.cols[field]
	if v := col[i]; v != "" || webNotInherited[field] {
		return v, false
	}
	if p := x.parent[i]; p >= 0 && col[p] != "" {
		return col[p], true
	}
	return "", false
}

// TWebGroup is a group with its number of entries.
type TWebGroup struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

// TWebRow is one row of the Search table: the key, the cells (as text), and
// which of them come from the crossref parent.
type TWebRow struct {
	Key       string   `json:"k"`
	Cells     []string `json:"c"`
	Inherited []int    `json:"i,omitempty"`
	PDF       bool     `json:"p,omitempty"` // the entry has a PDF (checked on the fly)
	Tick      int      `json:"t,omitempty"` // in a Select tab: selectOn, selectKept
	Local     string   `json:"l,omitempty"` // in a Select tab: the entry's local key in the .keys file
}

// TWebPage is a page of the Search table.
type TWebPage struct {
	Total  int         `json:"total"`
	Offset int         `json:"offset"`
	Rows   []TWebRow   `json:"rows"`
	Fields []string    `json:"fields,omitempty"`
	Groups []TWebGroup `json:"groups"` // the groups in use by the listed entries (first page only)
}

// page filters (every word of query must occur), sorts (on sortOn; "": the
// default order) and returns the rows from offset, with the given columns.
func webIndexPage(query string, groups []string, sel *TSelectSession, onlySelected bool, sortOn string, desc bool, columns []string, offset, limit int, withFields bool) (TWebPage, error) {
	x, err := webIndexFor(true)
	if x == nil {
		if err == nil {
			err = fmt.Errorf("the library is not loaded yet")
		}
		return TWebPage{}, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	var ro *sql.DB
	load := func(field string) error {
		if _, ok := x.cols[field]; ok {
			return nil
		}
		if ro == nil {
			var err error
			if ro, err = openReadOnly(); err != nil {
				return err
			}
		}
		return x.loadColumn(ro, field)
	}
	defer func() {
		if ro != nil {
			ro.Close()
		}
	}()
	known := map[string]bool{}
	for _, f := range x.fields {
		known[f] = true
	}
	var cols []string
	for _, c := range columns {
		if known[c] {
			cols = append(cols, c)
		}
	}
	if !known[sortOn] && sortOn != "pdf" && !(sortOn == "sel" && sel != nil) {
		sortOn = ""
	}

	query = strings.TrimSpace(query)
	groupKey := strings.Join(groups, "\x00")
	if sel != nil && (onlySelected || sortOn == "sel") {
		groupKey += "\x00sel:" + sel.ID + ":" + fmt.Sprint(onlySelected) + ":" + fmt.Sprint(sel.version())
	}
	if r := x.last; r.order == nil || r.query != query || r.groups != groupKey || r.sortOn != sortOn || r.desc != desc {
		order := x.filter(query)
		if sel != nil && onlySelected {
			chosen := sel.selectedPositions(x)
			kept := order[:0]
			for _, i := range order {
				if chosen[i] {
					kept = append(kept, i)
				}
			}
			order = kept
		}
		if len(groups) > 0 { // in every selected group
			kept := order[:0]
			for _, i := range order {
				in := true
				for _, g := range groups {
					if !x.groups[g][i] {
						in = false
						break
					}
				}
				if in {
					kept = append(kept, i)
				}
			}
			order = kept
		}
		if sortOn != "" {
			sortKeys := make([]string, len(x.keys))
			if sortOn == "sel" { // selected first, then kept (blue), then the rest (ascending)
				sel.mu.Lock()
				for _, i := range order {
					sortKeys[i] = string(rune('a' + []int{2, 0, 1}[sel.state(x.keys[i])]))
				}
				sel.mu.Unlock()
			} else if sortOn == "pdf" { // entries with a PDF first (ascending)
				withPDF := entriesWithPDF()
				for _, i := range order {
					sortKeys[i] = "b"
					if withPDF[x.keys[i]] {
						sortKeys[i] = "a"
					}
				}
			} else {
				if err := load(sortOn); err != nil {
					return TWebPage{}, err
				}
				for _, i := range order {
					sortKeys[i] = foldText(x.value(sortOn, i))
				}
			}
			sort.SliceStable(order, func(a, b int) bool {
				ka, kb := sortKeys[order[a]], sortKeys[order[b]]
				if ka == "" || kb == "" { // empty values last, either way
					return ka != "" && kb == ""
				}
				if desc {
					return ka > kb
				}
				return ka < kb
			})
		}
		x.last = webIndexResult{query: query, sortOn: sortOn, groups: groupKey, desc: desc, order: order,
			groupCounts: x.groupsIn(order, groups)}
	}
	order := x.last.order

	out := TWebPage{Total: len(order), Offset: offset}
	if offset == 0 {
		out.Groups = x.last.groupCounts
	}
	if withFields {
		out.Fields = x.fields
	}
	for _, c := range cols {
		if err := load(c); err != nil {
			return TWebPage{}, err
		}
	}
	for n := offset; n < len(order) && n < offset+limit; n++ {
		i := order[n]
		row := TWebRow{Key: x.keys[i], Cells: make([]string, len(cols))}
		for ci, c := range cols {
			v, inherited := x.valueInherited(c, i)
			if c != "citekey" && c != "groups" {
				v = texToText(v)
			}
			row.Cells[ci] = v
			if inherited {
				row.Inherited = append(row.Inherited, ci)
			}
		}
		if _, err := os.Stat(entryPDFPath(row.Key)); err == nil {
			row.PDF = true
		}
		if sel != nil {
			sel.mu.Lock()
			row.Tick = sel.state(row.Key)
			if e := sel.entries[row.Key]; e != nil {
				row.Local = e.Local
			}
			sel.mu.Unlock()
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}

// filter returns the positions (in the default order) of the entries holding
// every word of the query; an old key matches its entry.
func (x *TWebIndex) filter(query string) []int32 {
	words := strings.Fields(foldText(query))
	if len(words) == 0 {
		return append([]int32{}, x.base...)
	}
	var oldie int32 = -1
	if !strings.Contains(query, " ") {
		if ro, err := openReadOnly(); err == nil {
			var key string
			if ro.QueryRow(`SELECT key FROM old_keys WHERE alias = ?`, query).Scan(&key) == nil {
				if i, ok := x.pos[key]; ok {
					oldie = i
				}
			}
			ro.Close()
		}
	}
	var order []int32
	for _, i := range x.base {
		match := i == oldie
		if !match {
			match = true
			for _, w := range words {
				if !strings.Contains(x.hay[i], w) {
					match = false
					break
				}
			}
		}
		if match {
			order = append(order, i)
		}
	}
	return order
}

// entriesWithPDF lists the keys of the entries with a PDF in <base>.files/
// (read when sorting on the PDF column).
func entriesWithPDF() map[string]bool {
	out := map[string]bool{}
	files, _ := os.ReadDir(bibTeXFolder + bibTeXBaseName + ".files/")
	for _, f := range files {
		if name := f.Name(); strings.HasSuffix(name, ".pdf") {
			out[strings.TrimSuffix(name, ".pdf")] = true
		}
	}
	return out
}

// groupsIn returns the groups in use by the entries listed (order), with the
// number of them in each, by name; the chosen groups are always included.
func (x *TWebIndex) groupsIn(order []int32, chosen []string) []TWebGroup {
	listed := make([]bool, len(x.keys))
	for _, i := range order {
		listed[i] = true
	}
	isChosen := map[string]bool{}
	for _, g := range chosen {
		isChosen[g] = true
	}
	out := []TWebGroup{}
	for _, g := range x.groupList {
		n := 0
		for i := range x.groups[g.Name] {
			if listed[i] {
				n++
			}
		}
		if n > 0 || isChosen[g.Name] {
			out = append(out, TWebGroup{g.Name, n})
		}
	}
	return out
}
