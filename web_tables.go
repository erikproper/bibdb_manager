package main

// Table tabs in the web UI (Erik, 2026-11-24): the mapping, alias and list
// tables, each in a tab of its own: listed (filter, sort, paged), a "+" to add a
// row, a delete per row, a click on a row for its edit pop-up. The Tables tab
// lists them all; a table's tab is shown in the tab bar when chosen there (Erik,
// 2026-11-25: in principle all content is editable). Reads go through a read-only
// connection; writes are jobs (-table_edit). The tables with a memory of their own
// (field_mappings, name_forms, key_oldies, key_hints) change it with the table (no
// load afresh); a change to another table has the library loaded afresh.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
)

// TWebTable describes a table tab: its rows (a query), the columns, which of them
// identify a row, which can be edited.
type TWebTable struct {
	Name     string   `json:"name"`
	Title    string   `json:"title"`
	Columns  []string `json:"columns"`
	Keys     []string `json:"keys"` // the columns that identify a row
	Help     string   `json:"help"`
	Group    string   `json:"group"`              // the Tables tab's group
	ReadOnly bool     `json:"readonly,omitempty"` // derived data: shown, not edited
	from     string   // the SQL table, or a subquery, the rows are read from
	table    string   // the SQL table written, when not from
}

// inMemory tells whether a change to the table is made in the library's memory
// too (doTableEdit); for the other tables the library is loaded afresh.
func (t TWebTable) inMemory() bool {
	switch t.Name {
	case "field_mappings", "name_forms", "key_oldies", "key_hints":
		return true
	}
	return false
}

// written is the SQL table a change writes.
func (t TWebTable) written() string {
	if t.table != "" {
		return t.table
	}
	return t.from
}

// webTables are the tables with a tab.
var webTables = map[string]TWebTable{
	"field_mappings": {
		Name: "field_mappings", Title: "Field mappings",
		Columns: []string{"source_field", "source_value", "target_field", "target_value"},
		Keys:    []string{"source_field", "source_value", "target_field"},
		Help: "A value of source_field is replaced by target_value in target_field. The same field on both " +
			"sides: a correction (stored values are corrected too); different fields: a value moved or added " +
			"elsewhere (source_field may carry an entry type, e.g. author:techreport).",
		Group: "Mappings", from: "field_mappings",
	},
	"name_forms": {
		Name: "name_forms", Title: "Name mappings",
		Columns: []string{"form", "canonical", "id"},
		Keys:    []string{"form", "id"},
		Help: "The other forms of an author's name, mapped to the author (their stored name and id): a form found " +
			"in an entry is taken as that author. When adding, canonical may be the stored name or the id (EP-…).",
		Group: "Mappings",
		from:  "(SELECT n.name AS form, c.name AS canonical, n.id AS id FROM contributor_names n JOIN contributors c ON c.id = n.id WHERE n.name != c.name)",
		table: "contributor_names",
	},
	"key_oldies": {
		Name: "key_oldies", Title: "Old keys",
		Columns: []string{"alias", "key"},
		Keys:    []string{"alias"},
		Help:    "Former keys (EP-…) of entries (e.g. of the entry merged away), mapped to the key used: an old citation still finds its entry.",
		Group:   "Keys and aliases", from: "key_oldies",
	},
	"key_hints": {
		Name: "key_hints", Title: "Key aliases",
		Columns: []string{"hint", "key"},
		Keys:    []string{"hint"},
		Help:    "Other keys an entry is known by (former preferred aliases, keys used in harvested bib files), mapped to its key: a citation with such a key finds the entry.",
		Group:   "Keys and aliases", from: "key_hints",
	},
}

func init() {
	// The other tables, each with its primary key (Erik, 2026-11-25).
	for _, t := range []TWebTable{
		{Name: "shorten_mappings", Title: "Shortened values", Group: "Mappings", Columns: []string{"field", "original", "shortened"}, Keys: []string{"field", "original"},
			Help: "Short forms used when writing bib files with shortening on (the sync config's \"shorten\"): in that field, the original value is written as the shortened one (e.g. a long booktitle as its acronym). The library itself keeps the original."},
		{Name: "country_names", Title: "Country names (address)", Group: "Mappings", Columns: []string{"alias", "canonical"}, Keys: []string{"alias"},
			Help: "Every known form of a country name (also in other languages, and ISO codes such as US), mapped to the one used. Used to normalise the address field."},
		{Name: "state_names", Title: "State names", Group: "Mappings", Columns: []string{"alias", "canonical"}, Keys: []string{"alias"},
			Help: "Other forms of a state's name (e.g. abbreviations), mapped to the one used. Used to normalise the address field."},
		{Name: "state_countries", Title: "State countries", Group: "Mappings", Columns: []string{"state", "country"}, Keys: []string{"state"},
			Help: "The country each state lies in: an address with only a state gets its country (e.g. \"Texas\" → \"Texas, USA\")."},
		{Name: "booktitle_country_names", Title: "Country names in titles", Group: "Mappings", Columns: []string{"alias", "canonical"}, Keys: []string{"alias"},
			Help: "Country names as they may appear inside a booktitle (or a title equal to it), e.g. \"…, Toronto, Canada\", mapped to the form used there. English only, without ISO codes: a non-English country name suggests the title is in that language, and two-letter codes are ambiguous in a title. Used by the title alignment check."},
		{Name: "entry_doi_aliases", Title: "DOI aliases", Group: "Keys and aliases", Columns: []string{"doi", "entry_key"}, Keys: []string{"doi"},
			Help: "Other DOIs an entry is known by (e.g. the DOI of a merged duplicate): a bib file or DBLP record with such a DOI is taken as that entry."},
		{Name: "contributor_id_oldies", Title: "Old author ids", Group: "Keys and aliases", Columns: []string{"absorbed_id", "canonical_id"}, Keys: []string{"absorbed_id"},
			Help: "Former ids (EP-…) of authors that were merged into another author, mapped to the id used, so that old references still find the author."},
		{Name: "deleted_entries", Title: "Deleted entries", Group: "Keys and aliases", Columns: []string{"key"}, Keys: []string{"key"},
			Help: "Keys of entries deleted from the library: a sync does not offer to add them again from older bib files. Delete a row to let an entry come back."},
		{Name: "urls_ignore", Title: "Bad URLs", Group: "Lists", Columns: []string{"url"}, Keys: []string{"url"},
			Help: "URLs not to download PDFs from: Get PDFs (Actions, PDF) skips them. PDF → \"Add failed\" adds the URLs of the last failed downloads here."},
		{Name: "ignore_titles", Title: "Ignored titles", Group: "Lists", Columns: []string{"title"}, Keys: []string{"title"},
			Help: "Titles too general to tell entries apart (e.g. \"Preface\", \"Introduction\"): entries with such a title are not taken as duplicates of one another because of it."},
		{Name: "dblp_waived", Title: "DBLP waived", Group: "Lists", Columns: []string{"key"}, Keys: []string{"key"},
			Help: "Entries that have no DBLP record on purpose: the checks no longer warn that they lack a DBLP key."},
		{Name: "non_double_entries", Title: "Not double: entries", Group: "Not doubles", Columns: []string{"key1", "key2"}, Keys: []string{"key1", "key2"},
			Help: "Pairs of entries that look alike but are different publications (answered \"Keep\" when asked to merge): never offered for merging again."},
		{Name: "non_double_contributors", Title: "Not double: authors", Group: "Not doubles", Columns: []string{"contributor_id_a", "contributor_id_b"}, Keys: []string{"contributor_id_a", "contributor_id_b"},
			Help: "Pairs of authors (by id, the smaller first) that are different people: never offered for merging again."},
		{Name: "non_double_contributor_names", Title: "Not double: names", Group: "Not doubles", Columns: []string{"name1", "name2"}, Keys: []string{"name1", "name2"},
			Help: "Pairs of names (the smaller first) of different people (answered \"Different people\" in a name question): never asked about again."},
		{Name: "contributor_orcids", Title: "ORCIDs", Group: "Authors", Columns: []string{"orcid", "contributor_id", "is_canonical"}, Keys: []string{"orcid"},
			Help: "The ORCIDs of authors; is_canonical is 1 for the one used when an author has several."},
		{Name: "bib_groups", Title: "Group members", Group: "Entries", Columns: []string{"group_name", "entry_key"}, Keys: []string{"group_name", "entry_key"},
			Help: "Which entries are in which group (the groups of BibDesk; also set by Entry actions)."},
		{Name: "entry_metadata", Title: "Entry flags", Group: "Entries", Columns: []string{"entry_key", "property", "value"}, Keys: []string{"entry_key", "property"},
			Help: "Properties of entries kept outside their BibTeX fields (flags such as waivers), as property and value."},
		{Name: "superseded_field_values", Title: "Superseded values", Group: "Entries", Columns: []string{"entry_key", "field", "value", "triage_status", "winner"}, Keys: []string{"entry_key", "field", "value"},
			Help: "Values of fields that lost a challenge (to a newer or preferred value), kept so they are recognised when they come back; triage_status and winner record what Triage author mappings decided."},
		{Name: "config", Title: "Config", Group: "Settings", Columns: []string{"key", "value"}, Keys: []string{"key"},
			Help: "The library's stored configuration (key and value)."},
		{Name: "map_files", Title: "Map files", Group: "Settings", Columns: []string{"name", "content"}, Keys: []string{"name"},
			Help: "The hand-maintained maps kept in the database as whole files (unicode, HTML and LaTeX character maps, hyphenations, harvest keys to ignore). Click a row to edit a file's content."},
		{Name: "dblp_parent", Title: "DBLP parents", Group: "Derived (read-only)", Columns: []string{"child_key", "parent_key"}, Keys: []string{"child_key"}, ReadOnly: true,
			Help: "The crossref parent of each DBLP entry, derived from the DBLP data (read-only)."},
		{Name: "entry_warnings", Title: "Entry warnings", Group: "Derived (read-only)", Columns: []string{"key", "warning"}, Keys: []string{"key", "warning"}, ReadOnly: true,
			Help: "The warnings of the last check, per entry, derived by the checks (read-only)."},
	} {
		t.from = t.Name
		webTables[t.Name] = t
	}
}

// handleTables lists the tables with a tab (GET /api/tables), for the Tables tab.
func (w *TWebUI) handleTables(rw http.ResponseWriter, r *http.Request) {
	list := make([]TWebTable, 0, len(webTables))
	for _, t := range webTables {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Group != list[j].Group {
			return tableGroupRank(list[i].Group) < tableGroupRank(list[j].Group)
		}
		return list[i].Title < list[j].Title
	})
	writeJSON(rw, list)
}

// tableGroupRank orders the Tables tab's groups.
func tableGroupRank(g string) int {
	for i, name := range []string{"Mappings", "Keys and aliases", "Lists", "Not doubles", "Authors", "Entries", "Settings", "Derived (read-only)"} {
		if g == name {
			return i
		}
	}
	return 99
}

// handleTable serves a table tab: GET /api/table/<name>?q=…&sort=…&desc=1&from=…&n=…
// (its rows, filtered on any column containing every word of q); POST
// /api/table/<name> {op: add|delete|update, row, old} (a change, run as a job).
func (w *TWebUI) handleTable(rw http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/"+w.token+"/api/table/")
	t, ok := webTables[name]
	if !ok {
		http.NotFound(rw, r)
		return
	}
	if r.Method == http.MethodPost {
		if t.ReadOnly {
			http.Error(rw, t.Title+" is derived data: not edited here", http.StatusForbidden)
			return
		}
		w.editTable(rw, r, t)
		return
	}
	q := r.URL.Query()
	from, _ := strconv.Atoi(q.Get("from"))
	n, _ := strconv.Atoi(q.Get("n"))
	if n <= 0 || n > 2000 {
		n = 200
	}
	var where []string
	var args []any
	for _, word := range strings.Fields(q.Get("q")) {
		var ors []string
		for _, c := range t.Columns {
			ors = append(ors, c+" LIKE ?")
			args = append(args, "%"+word+"%")
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	order := strings.Join(t.Columns, ", ")
	for _, c := range t.Columns {
		if c == q.Get("sort") {
			dir := ""
			if q.Get("desc") == "1" {
				dir = " DESC"
			}
			order = c + " COLLATE NOCASE" + dir + ", " + order
		}
	}
	ro, err := openReadOnly()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	defer ro.Close()
	var total int
	ro.QueryRow(`SELECT COUNT(*) FROM `+t.from+cond, args...).Scan(&total) //nolint:errcheck
	rows, err := ro.Query(`SELECT `+strings.Join(t.Columns, ", ")+` FROM `+t.from+cond+` ORDER BY `+order+` LIMIT ? OFFSET ?`,
		append(args, n, from)...)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		vals := make([]sql.NullString, len(t.Columns))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if rows.Scan(ptrs...) != nil {
			continue
		}
		row := make([]string, len(vals))
		for i, v := range vals {
			row[i] = v.String
		}
		out = append(out, row)
	}
	writeJSON(rw, map[string]any{"table": t, "total": total, "from": from, "rows": out})
}

// TTableEdit is a change to a table tab's table: add a row, delete one (by its
// key columns), or update one (old → row).
type TTableEdit struct {
	Op  string            `json:"op"`
	Row map[string]string `json:"row"`
	Old map[string]string `json:"old"`
}

// editTable runs a change as a job (-table_edit) and answers with its messages.
func (w *TWebUI) editTable(rw http.ResponseWriter, r *http.Request, t TWebTable) {
	var e TTableEdit
	if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 1<<16)).Decode(&e); err != nil || (e.Op != "add" && e.Op != "delete" && e.Op != "update") {
		http.Error(rw, "not a valid change", http.StatusBadRequest)
		return
	}
	data, _ := json.Marshal(e)
	out := &collectSink{}
	done := make(chan []int, 1)
	cwd, _ := os.Getwd()
	if err := w.service.runWebJobs([][]string{{"-table_edit", t.Name, string(data)}}, cwd, out, func(codes []int) { done <- codes }); err != nil {
		http.Error(rw, err.Error(), http.StatusConflict)
		return
	}
	codes := <-done
	ok := len(codes) == 1 && codes[0] == 0
	msg := strings.TrimSpace(out.text())
	if msg == "" {
		msg = map[bool]string{true: "Saved.", false: "Not saved."}[ok]
	}
	writeJSON(rw, map[string]any{"ok": ok, "message": msg})
}

// doTableEdit is -table_edit <table> <json {op, row, old}> (internal: the table
// tabs of the web UI). Rows are added through the library (normalised, checked, in
// memory too) and deleted from memory and the table alike, so the service keeps
// the library loaded.
func doTableEdit(args []string) {
	t, ok := webTables[args[0]]
	if !ok {
		messagef("table_edit: no such table %q\n", args[0])
		endRun(1)
	}
	var e TTableEdit
	if err := json.Unmarshal([]byte(args[1]), &e); err != nil {
		messagef("table_edit: not a valid change: %s\n", err)
		endRun(1)
	}
	if !openLibraryToUpdate() {
		return
	}
	if t.ReadOnly {
		messagef("%s is derived data: not edited here.\n", t.Title)
		endRun(1)
	}
	isKey := map[string]bool{}
	for _, k := range t.Keys {
		isKey[k] = true
	}
	for _, c := range t.Columns {
		if t.Name == "name_forms" && (c == "id" || c == "canonical") {
			continue // one of the two names the author
		}
		if !t.inMemory() && !isKey[c] {
			continue // a value may be empty (stored as NULL where the column allows it)
		}
		if e.Op != "delete" && strings.TrimSpace(e.Row[c]) == "" {
			messagef("%s is empty.\n", c)
			endRun(1) // refused: the page keeps its pop-up open
		}
	}
	// A deleted row leaves memory too (the library's own structures), so the change
	// needs no load afresh (Erik, 2026-11-24): the table and memory stay in step.
	forget := func(row map[string]string) {
		switch t.Name {
		case "field_mappings":
			sf, sv, tf := row["source_field"], row["source_value"], row["target_field"]
			if sf == tf {
				if target, ok := Library.GenericFieldSourceToTarget[sf][sv]; ok {
					delete(Library.GenericFieldSourceToTarget[sf], sv)
					if inv := Library.GenericFieldTargetToSource[sf]; inv != nil {
						inv.DeleteValueFromStringSetMap(target, sv)
					}
				}
			} else if m := Library.FieldMappings[sf][sv]; m != nil {
				delete(m, tf)
			}
		case "key_oldies":
			Library.KeyOldies.Delete(row["alias"])
		case "key_hints":
			Library.HintToKey.Delete(row["hint"])
		case "name_forms":
			if Library.NameToContributorID[row["form"]] == row["id"] {
				delete(Library.NameToContributorID, row["form"])
			}
		}
	}
	del := func(row map[string]string) bool {
		if t.Name == "name_forms" {
			var canonical string
			bibQueryRow(`SELECT name FROM contributors WHERE id = ?`, row["id"]).Scan(&canonical) //nolint:errcheck
			if canonical == row["form"] {
				messagef("%q is the author's stored name: rename the author instead.\n", row["form"])
				return false
			}
			forget(row)
			if err := bibExec(`DELETE FROM contributor_names WHERE id = ? AND name = ?`, row["id"], row["form"]); err != nil {
				messagef("Could not delete: %s\n", err)
				return false
			}
			return true
		}
		forget(row)
		var conds []string
		var vals []any
		for _, k := range t.Keys {
			conds = append(conds, k+" = ?")
			vals = append(vals, row[k])
		}
		if err := bibExec(`DELETE FROM `+t.written()+` WHERE `+strings.Join(conds, " AND "), vals...); err != nil {
			messagef("Could not delete: %s\n", err)
			return false
		}
		return true
	}
	add := func(row map[string]string) bool {
		get := func(c string) string { return strings.TrimSpace(row[c]) }
		switch t.Name {
		case "field_mappings":
			sf, tf := get("source_field"), get("target_field")
			if sf == tf {
				Library.AddGenericFieldAlias(sf, Library.MapFieldValue(sf, get("source_value")), Library.MapFieldValue(tf, get("target_value")), true)
			} else {
				field, _, _ := strings.Cut(sf, ":")
				Library.AddFieldMapping(sf, Library.MapFieldValue(field, get("source_value")), tf, Library.NormaliseFieldValue(tf, get("target_value")))
			}
		case "key_oldies":
			Library.AddKeyAlias(get("alias"), get("key"))
		case "key_hints":
			Library.AddKeyHint(get("hint"), get("key"))
		case "name_forms":
			id := get("id")
			if id == "" {
				id = get("canonical")
			}
			cid, _, ok := resolveContributorArg(id)
			if !ok {
				messagef("No such author: %q\n", id)
				return false
			}
			form := get("form")
			var other string
			bibQueryRow(`SELECT id FROM contributor_names WHERE name = ? AND id != ?`, form, cid).Scan(&other) //nolint:errcheck
			if other == "" {
				bibQueryRow(`SELECT id FROM contributors WHERE name = ? AND id != ?`, form, cid).Scan(&other) //nolint:errcheck
			}
			if other != "" {
				messagef("%q is a name of another author (%s); to make them one: Mark for merge, %s and %s\n", form, other, other, cid)
				return false
			}
			upsertContributorNameToDB(cid, form)
			Library.NameToContributorID[form] = cid
		default:
			nullable := tableNullableColumns(t.written())
			var vals []any
			for _, c := range t.Columns {
				if v := get(c); v == "" && nullable[c] {
					vals = append(vals, nil)
				} else {
					vals = append(vals, v)
				}
			}
			marks := strings.TrimSuffix(strings.Repeat("?, ", len(t.Columns)), ", ")
			if err := bibExec(`INSERT INTO `+t.written()+` (`+strings.Join(t.Columns, ", ")+`) VALUES (`+marks+`)`, vals...); err != nil {
				messagef("Could not add: %s\n", err)
				return false
			}
		}
		return true
	}
	done := false
	switch e.Op {
	case "delete":
		if done = del(e.Row); done {
			messagef("Deleted from %s.\n", t.Title)
		}
	case "add":
		if done = add(e.Row); done {
			messagef("Added to %s.\n", t.Title)
		}
	case "update":
		if del(e.Old) {
			if done = add(e.Row); done {
				messagef("Changed in %s.\n", t.Title)
			} else {
				add(e.Old) // the new row was refused: the old one back
			}
		}
	}
	if t.inMemory() {
		writesKeptInMemory([]string{t.written()}, nil)
	} // else: the service loads the library afresh (its write hook sees the table changed)
	bibEntriesModified = true
	if !done {
		endRun(1) // refused: the page keeps its pop-up open
	}
}

// tableNullableColumns are the columns of table that may hold NULL.
func tableNullableColumns(table string) map[string]bool {
	out := map[string]bool{}
	rows, err := bibQuery(`SELECT name, "notnull", pk FROM pragma_table_info(?)`, table)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var notNull, pk int
		if rows.Scan(&name, &notNull, &pk) == nil && notNull == 0 && pk == 0 {
			out[name] = true
		}
	}
	return out
}
