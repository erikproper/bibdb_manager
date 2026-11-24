package main

// Table tabs in the web UI (Erik, 2026-11-24): the larger mapping and alias
// tables, each in a tab of its own: listed (filter, sort, paged), a "+" to add a
// row, a delete per row, a click on a row for its edit pop-up. Reads go through
// a read-only connection; writes are jobs (-table_edit), which change the
// library's memory and the table together (no load afresh).

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// TWebTable describes a table tab: its rows (a query), the columns, which of them
// identify a row, which can be edited.
type TWebTable struct {
	Name    string   `json:"name"`
	Title   string   `json:"title"`
	Columns []string `json:"columns"`
	Keys    []string `json:"keys"` // the columns that identify a row
	Help    string   `json:"help"`
	from    string   // the SQL table, or a subquery, the rows are read from
	table   string   // the SQL table written, when not from
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
		from: "field_mappings",
	},
	"name_forms": {
		Name: "name_forms", Title: "Name mappings",
		Columns: []string{"form", "canonical", "id"},
		Keys:    []string{"form", "id"},
		Help: "The other forms of an author's name, mapped to the author (their stored name and id): a form found " +
			"in an entry is taken as that author. When adding, canonical may be the stored name or the id (EP-…).",
		from:  "(SELECT n.name AS form, c.name AS canonical, n.id AS id FROM contributor_names n JOIN contributors c ON c.id = n.id WHERE n.name != c.name)",
		table: "contributor_names",
	},
	"key_oldies": {
		Name: "key_oldies", Title: "Old keys",
		Columns: []string{"alias", "key"},
		Keys:    []string{"alias"},
		Help:    "Former keys (EP-…) of entries, kept so that old citations still find their entry (e.g. after a merge).",
		from:    "key_oldies",
	},
	"key_hints": {
		Name: "key_hints", Title: "Key aliases",
		Columns: []string{"hint", "key"},
		Keys:    []string{"hint"},
		Help:    "Other keys an entry is known by (former preferred aliases, keys in harvested files), mapped to its key.",
		from:    "key_hints",
	},
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
	for _, c := range t.Columns {
		if t.Name == "name_forms" && (c == "id" || c == "canonical") {
			continue // one of the two names the author
		}
		if e.Op != "delete" && strings.TrimSpace(e.Row[c]) == "" {
			messagef("%s is empty.\n", c)
			return
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
				messagef("%q is a name of another author (%s); to make them one: -merge_contributors %s %s\n", form, other, other, cid)
				return false
			}
			upsertContributorNameToDB(cid, form)
			Library.NameToContributorID[form] = cid
		}
		return true
	}
	switch e.Op {
	case "delete":
		if del(e.Row) {
			messagef("Deleted from %s.\n", t.Title)
		}
	case "add":
		if add(e.Row) {
			messagef("Added to %s.\n", t.Title)
		}
	case "update":
		if del(e.Old) && add(e.Row) {
			messagef("Changed in %s.\n", t.Title)
		}
	}
	writesKeptInMemory([]string{t.written()}, nil)
	bibEntriesModified = true
}
