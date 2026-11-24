package main

// Editing an entry in the web UI (plan step 22.3c, Erik 2026-11-24): the
// entry's pop-up has an Edit button, which opens an edit tab (its own colour)
// with the entry type, every field (the output value shown where a mapping
// changes it), the authors and editors ("A and B"), and the groups. Fields can
// be added (from those allowed for the type) and removed.
//
// Save runs the changes as one job, -edit_entry (one write session): per field
// as -set_field (an empty value removes it), per group as -add_to_group and
// -remove_from_group; so a value goes through the same normalisation, mappings
// and winner handling as bib.field. Save is refused when the entry changed
// since the form was read (another job, another tab): read it again then.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"sort"
	"strings"
)

// TEditState is an entry as the edit form shows it: stored values.
type TEditState struct {
	Type   string            `json:"type"`
	Fields map[string]string `json:"fields"` // incl. author and editor ("A and B")
	Groups []string          `json:"groups"`
}

// readEditState reads an entry's type, fields (authors and editors from the
// contributors) and groups; false when there is no such entry.
func readEditState(ro *sql.DB, key string) (TEditState, bool) {
	st := TEditState{Fields: map[string]string{}, Groups: []string{}}
	rows, err := ro.Query(`SELECT field, value FROM bib_entries WHERE entry_key = ?`, key)
	if err != nil {
		return st, false
	}
	for rows.Next() {
		var f, v string
		if rows.Scan(&f, &v) != nil {
			continue
		}
		switch f {
		case "entrytype":
			st.Type = v
		case "author", "editor":
			// from the contributors, below
		default:
			st.Fields[f] = v
		}
	}
	rows.Close()
	if st.Type == "" {
		return st, false
	}
	for _, role := range []string{"author", "editor"} {
		var names []string
		for _, p := range webContributors(ro, key, role) {
			names = append(names, p.Name)
		}
		if len(names) > 0 {
			st.Fields[role] = strings.Join(names, " and ")
		}
	}
	if gRows, err := ro.Query(`SELECT group_name FROM bib_groups WHERE entry_key = ? ORDER BY group_name`, key); err == nil {
		for gRows.Next() {
			var g string
			if gRows.Scan(&g) == nil {
				st.Groups = append(st.Groups, g)
			}
		}
		gRows.Close()
	}
	return st, true
}

// webEditField is a row of the edit form.
type webEditField struct {
	Name, Value, Output string
	Long                bool // shown as a larger box
	Required            bool
}

// editFieldOrder puts the fields in the order of a reference: people, title,
// venue, then the rest by name.
var editFieldOrder = []string{"author", "editor", "title", "booktitle", "journal", "crossref", "year", "volume", "number", "pages"}

func editFieldRank(f string) int {
	for i, g := range editFieldOrder {
		if g == f {
			return i
		}
	}
	return len(editFieldOrder)
}

// handleEditFrag serves the edit form of an entry (GET /frag/edit/<key>).
func (w *TWebUI) handleEditFrag(rw http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/"+w.token+"/frag/edit/")
	ro, err := openReadOnly()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	defer ro.Close()
	st, ok := readEditState(ro, key)
	if !ok {
		http.NotFound(rw, r)
		return
	}
	// The output values (mappings), from the loaded library when it is free.
	outputs := map[string]string{}
	webRenderMu.Lock()
	if libraryLock.TryRLock() {
		if serviceLibraryLevel != libraryNotLoaded && entryCache != nil {
			for f, v := range st.Fields {
				if out := Library.MapEntryFieldValue(key, f, v); out != v {
					outputs[f] = out
				}
			}
		}
		libraryLock.RUnlock()
	}
	webRenderMu.Unlock()

	var fields []webEditField
	for f, v := range st.Fields {
		fields = append(fields, webEditField{Name: f, Value: v, Output: outputs[f],
			Long:     len(v) > 70 || f == "author" || f == "editor" || f == "title" || f == "abstract" || f == "note",
			Required: FieldIsRequiredForEntry(st.Type, f)})
	}
	sort.Slice(fields, func(i, j int) bool {
		ri, rj := editFieldRank(fields[i].Name), editFieldRank(fields[j].Name)
		if ri != rj {
			return ri < rj
		}
		return fields[i].Name < fields[j].Name
	})
	var addable []string
	if allowed, ok := BibTeXAllowedEntryFields[st.Type]; ok {
		for _, f := range allowed.Set().ElementsSorted() {
			if _, has := st.Fields[f]; !has && f != "entrytype" {
				addable = append(addable, f)
			}
		}
	}
	types := BibTeXAllowedEntries.Set().ElementsSorted()
	var allGroups []string
	if gRows, err := ro.Query(`SELECT DISTINCT group_name FROM bib_groups ORDER BY group_name`); err == nil {
		for gRows.Next() {
			var g string
			if gRows.Scan(&g) == nil {
				allGroups = append(allGroups, g)
			}
		}
		gRows.Close()
	}
	state, _ := json.Marshal(st)
	var reference template.HTML
	if refs, ok := renderReferences([]string{key}); ok {
		reference = refs[key]
	}
	w.render(rw, "edit", map[string]any{
		"Key": key, "Type": st.Type, "Types": types, "Fields": fields, "Addable": addable,
		"Groups": st.Groups, "AllGroups": allGroups, "State": string(state), "Reference": reference,
		"Alias": st.Fields["preferredalias"],
	})
}

// handleEditSave saves an edit tab (POST /api/edit/<key>, JSON {orig, new}):
// refused (409) when the entry no longer has the values the form was read
// with; otherwise the changes run as jobs, and the answer (when they are done)
// gives their output.
func (w *TWebUI) handleEditSave(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/"+w.token+"/api/edit/")
	var req struct {
		Orig TEditState `json:"orig"`
		New  TEditState `json:"new"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(rw, "not a valid request", http.StatusBadRequest)
		return
	}
	ro, err := openReadOnly()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	current, ok := readEditState(ro, key)
	ro.Close()
	if !ok {
		http.NotFound(rw, r)
		return
	}
	if !sameEditState(current, req.Orig) {
		http.Error(rw, "The entry changed since this form was read (another job or tab). Reload the form; your changes are not saved.", http.StatusConflict)
		return
	}
	cmds := editCommands(key, req.Orig, req.New)
	if len(cmds) == 0 {
		writeJSON(rw, map[string]any{"ok": true, "message": "Nothing changed."})
		return
	}
	out := &collectSink{}
	done := make(chan []int, 1)
	cwd, _ := os.Getwd()
	if err := w.service.runWebJobs(cmds, cwd, out, func(codes []int) { done <- codes }); err != nil {
		http.Error(rw, err.Error(), http.StatusConflict)
		return
	}
	codes := <-done
	failed := len(codes) < len(cmds) || (len(codes) > 0 && codes[len(codes)-1] != 0)
	n := editChanges(req.Orig, req.New)
	msg := fmt.Sprintf("Saved: %d change%s.", n, map[bool]string{true: "", false: "s"}[n == 1])
	if failed {
		msg = fmt.Sprintf("Not all saved: %s ended with code %d.", strings.Join(cmds[len(codes)-1], " "), codes[len(codes)-1])
	}
	writeJSON(rw, map[string]any{"ok": !failed, "message": msg, "output": strings.TrimSpace(out.text())})
}

// sameEditState compares what the form was read with to what is stored now.
func sameEditState(a, b TEditState) bool {
	if a.Type != b.Type || len(a.Fields) != len(b.Fields) || strings.Join(a.Groups, "\n") != strings.Join(b.Groups, "\n") {
		return false
	}
	for f, v := range a.Fields {
		if b.Fields[f] != v {
			return false
		}
	}
	return true
}

// editCommands are the command lines that turn orig into new: one
// -edit_entry, all changes in one write session (none when nothing changed).
func editCommands(key string, orig, new TEditState) [][]string {
	edit := TEntryEdit{Set: map[string]string{}}
	if t := strings.TrimSpace(new.Type); t != "" && t != orig.Type {
		edit.Set["entrytype"] = t
	}
	for f, v := range orig.Fields {
		if _, kept := new.Fields[f]; !kept && v != "" {
			edit.Set[f] = ""
		}
	}
	for f, v := range new.Fields {
		v = strings.TrimSpace(v)
		if f == "" || strings.ContainsAny(f, " \t") || v == orig.Fields[f] {
			continue
		}
		edit.Set[f] = v
	}
	groups := map[string]bool{}
	for _, g := range new.Groups {
		if g = strings.TrimSpace(g); g != "" {
			groups[g] = true
		}
	}
	had := map[string]bool{}
	for _, g := range orig.Groups {
		had[g] = true
		if !groups[g] {
			edit.Leave = append(edit.Leave, g)
		}
	}
	for g := range groups {
		if !had[g] {
			edit.Join = append(edit.Join, g)
		}
	}
	sort.Strings(edit.Join)
	if len(edit.Set) == 0 && len(edit.Join) == 0 && len(edit.Leave) == 0 {
		return nil
	}
	data, _ := json.Marshal(edit)
	return [][]string{{"-edit_entry", key, string(data)}}
}

// editChanges counts the changes of an edit (for the message).
func editChanges(orig, new TEditState) int {
	cmds := editCommands("", orig, new)
	if len(cmds) == 0 {
		return 0
	}
	var edit TEntryEdit
	json.Unmarshal([]byte(cmds[0][2]), &edit) //nolint:errcheck
	return len(edit.Set) + len(edit.Join) + len(edit.Leave)
}
