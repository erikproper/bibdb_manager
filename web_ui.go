package main

// The web UI (plan steps 22.3a–b), served by the service on localhost. One
// page with virtual tabs (Status, Search, Statistics, Log) that keep their state
// when switching between them, and pop-ups for an entry and an author. The page
// fetches its parts: HTML fragments (status, statistics, an entry, an author)
// and JSON (the Search table's rows, the log's new lines).
//
// Safety: the server listens on 127.0.0.1 only; every path starts with a random
// token made when the service starts (any web page in the browser can send
// requests to localhost, but cannot know the token); the Host header must be a
// localhost name (against DNS rebinding).
//
// The pages read the database through a read-only connection of their own, per
// request, never the loaded library or a job's connection: they work while a
// job runs or waits for an answer. Entries are shown with their stored values
// (without the output mappings).

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultServicePort is used when the settings name no service_port.
const defaultServicePort = 8401

// TWebUI is the web part of the service.
type TWebUI struct {
	service *TService
	token   string
	port    int
	pages   *template.Template
	server  *http.Server
}

func newWebUI(s *TService) *TWebUI {
	b := make([]byte, 16)
	rand.Read(b) //nolint:errcheck
	port := servicePort
	if port == 0 {
		port = defaultServicePort
	}
	return &TWebUI{service: s, token: hex.EncodeToString(b), port: port,
		pages: template.Must(template.New("").Funcs(template.FuncMap{
			"esc": url.PathEscape,
			"tex": texToText, // TeX markup to plain text ({\"o} → ö), for text outside the BibTeX layout
		}).Parse(webPages))}
}

// URL is the address of the main page.
func (w *TWebUI) URL() string { return fmt.Sprintf("http://localhost:%d/%s/", w.port, w.token) }

// start listens in the background; a port in use is reported in the log.
func (w *TWebUI) start() {
	mux := http.NewServeMux()
	prefix := "/" + w.token
	mux.HandleFunc(prefix+"/", w.handleShell)
	mux.HandleFunc(prefix+"/frag/status", w.handleStatus)
	mux.HandleFunc(prefix+"/frag/stats", w.handleStats)
	mux.HandleFunc(prefix+"/frag/entry/", w.handleEntry)
	mux.HandleFunc(prefix+"/frag/author/", w.handleAuthor)
	mux.HandleFunc(prefix+"/api/entries", w.handleEntries)
	mux.HandleFunc(prefix+"/api/log", w.handleLog)
	mux.HandleFunc(prefix+"/api/state", w.handleState)
	mux.HandleFunc(prefix+"/api/table/", w.handleTable)
	mux.HandleFunc(prefix+"/api/dialogue", w.handleDialogue)
	mux.HandleFunc(prefix+"/api/dialogue/", w.handleDialogue)
	mux.HandleFunc(prefix+"/api/dblpsearch", w.handleDBLPSearch)
	mux.HandleFunc(prefix+"/api/stop", w.handleStop)
	mux.HandleFunc(prefix+"/api/prefs", w.handlePrefs)
	mux.HandleFunc(prefix+"/frag/edit/", w.handleEditFrag)
	mux.HandleFunc(prefix+"/api/edit/", w.handleEditSave)
	mux.HandleFunc(prefix+"/frag/editauthor/", w.handleEditAuthorFrag)
	mux.HandleFunc(prefix+"/api/editauthor/", w.handleEditAuthorSave)
	mux.HandleFunc(prefix+"/api/selects", w.handleSelects)
	mux.HandleFunc(prefix+"/api/selstate", w.handleSelState)
	mux.HandleFunc(prefix+"/api/selstates", w.handleSelStates)
	mux.HandleFunc(prefix+"/api/select/", w.handleSelect)
	mux.HandleFunc(prefix+"/pdf/", w.handlePDF)
	mux.HandleFunc(prefix+"/api/pdfup/", w.handlePDFUpload)
	mux.HandleFunc(prefix+"/api/pdfremove/", w.handlePDFRemove)
	mux.HandleFunc(prefix+"/api/pdfundo/", w.handlePDFUndo)
	// Any other address (e.g. the bare http://localhost:<port>/): say how to get
	// in, without giving the token away.
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(rw, `<!doctype html><meta charset="utf-8"><title>%s</title>
<body style="font:16px -apple-system,sans-serif;margin:40px;max-width:640px">
<h1 style="font-weight:400">%s</h1>
<p>This address needs the key the service made when it started. Open the web UI from the
command line with <code>bib.check -ui</code>; <code>bib.check -status</code> shows the full address.</p>`,
			template.HTMLEscapeString(bibTeXBaseName), template.HTMLEscapeString(bibTeXBaseName))
	})
	server := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", w.port),
		Handler:           w.guard(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	// A service that has just stopped may hold the port a moment longer.
	listener, err := net.Listen("tcp", server.Addr)
	for try := 0; err != nil && try < 10; try++ {
		time.Sleep(500 * time.Millisecond)
		listener, err = net.Listen("tcp", server.Addr)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s  web UI not started: %s (set another service_port in the settings)\n",
			time.Now().Format(time.DateTime), err)
		return
	}
	fmt.Fprintf(os.Stderr, "%s  web UI on port %d\n", time.Now().Format(time.DateTime), w.port)
	w.server = server
	go server.Serve(listener) //nolint:errcheck
	// Keep the Search index up to date between jobs, so a search seldom waits.
	go func() {
		for !w.service.stopping.Load() {
			refreshWebIndex(w.busy())
			refreshActionCounts(w.busy())
			time.Sleep(10 * time.Second)
		}
	}()
}

// stop closes the web server (and so its port).
func (w *TWebUI) stop() {
	flushPDFUndos()
	if w != nil && w.server != nil {
		w.server.Close() //nolint:errcheck
	}
}

// guard refuses requests not addressed to localhost by name.
func (w *TWebUI) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "localhost" && host != "127.0.0.1" {
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// openReadOnly opens the working database read-only, for one request.
func openReadOnly() (*sql.DB, error) {
	return sql.Open(sqliteDatabaseDriver, "file:"+dbPath()+"?mode=ro")
}

// render writes a template: the page itself ("shell"), or a fragment of it.
func (w *TWebUI) render(rw http.ResponseWriter, name string, data map[string]any) {
	data["Base"] = "/" + w.token
	data["Library"] = bibTeXBaseName
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	if err := w.pages.ExecuteTemplate(rw, name, data); err != nil {
		fmt.Fprintf(os.Stderr, "web UI: %s\n", err)
	}
}

// writeJSON answers with v as JSON.
func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(rw).Encode(v) //nolint:errcheck
}

// busy tells whether a job runs (the Search index is then not rebuilt).
func (w *TWebUI) busy() bool {
	w.service.mu.Lock()
	defer w.service.mu.Unlock()
	return w.service.busy
}

// handleState answers what the busy light in the top bar shows (JSON): a job
// running (which), the library or the search index being brought up to date,
// changes not pushed yet; and the index version (the page refreshes its tables
// when it changes).
func (w *TWebUI) handleState(rw http.ResponseWriter, r *http.Request) {
	s := w.service
	s.mu.Lock()
	busy, job := s.busy, strings.Join(s.jobArgs, " ")
	s.mu.Unlock()
	writeJSON(rw, map[string]any{
		"busy":     busy,
		"job":      strings.TrimPrefix(strings.TrimPrefix(job, "-base "+libraryBase()), " "),
		"indexing": webIndexBuilding.Load() || (!busy && webIndexStale()),
		"index":    webIndexVersion.Load(),
		"unloaded": serviceLibraryLevel == libraryNotLoaded,
		"unpushed": readLibraryUnpushed() != nil,
		"answer":   libraryNeedsAnswer.Load() || dialogue.Asking(),
		"asking":   dialogue.Asking(),
		"terminal": jobAsksOnTerminal(),
		"counts":   currentActionCounts(),
	})
}

// handleShell serves the page with the tabs; the rest is fetched by it.
func (w *TWebUI) handleShell(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/"+w.token+"/" {
		http.NotFound(rw, r)
		return
	}
	w.render(rw, "shell", map[string]any{"Version": AppVersion})
}

// ---- status ----

func (w *TWebUI) handleStatus(rw http.ResponseWriter, r *http.Request) {
	s := w.service
	s.mu.Lock()
	data := map[string]any{
		"Version": AppVersion,
		"Since":   s.started.Format("2006-01-02 15:04"),
		"Busy":    s.busy,
		"Job":     strings.Join(s.jobArgs, " "),
		"JobFrom": s.jobFrom.Format("15:04:05"),
	}
	if !s.pushDue.IsZero() {
		data["PushDue"] = s.pushDue.Format("15:04:05")
	}
	s.mu.Unlock()
	if stamp := readLibraryStamp(); stamp != nil {
		data["Pushed"] = fmt.Sprintf("%s, from %s (generation %d)", stamp.PushedAt, stamp.Machine, stamp.Generation)
	}
	data["Unpushed"] = readLibraryUnpushed() != nil
	w.render(rw, "status", data)
}

// handleStop stops the service (POST only), as -stop does: a running job is
// stopped, what is not pushed yet is pushed. The answer comes before that.
func (w *TWebUI) handleStop(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "not allowed", http.StatusMethodNotAllowed)
		return
	}
	fmt.Fprintf(os.Stderr, "%s  stop asked in the web UI\n", time.Now().Format(time.DateTime))
	rw.WriteHeader(http.StatusAccepted)
	go w.service.stop(logSink{})
}

// ---- preferences ----

// webPrefsPath is the web UI's preferences file (Erik, 2026-11-24): the Search
// columns, their widths and order, the sorting. One file in the local DB folder,
// for every library there, as the maps (*.csv) there are.
func webPrefsPath() string { return localDBFolder + "web_ui.json" }

var webPrefsMu sync.Mutex

// handlePrefs answers the preferences (GET; {} when there are none) or replaces
// them (POST, a JSON object of at most 64 KB). The page owns their content.
func (w *TWebUI) handlePrefs(rw http.ResponseWriter, r *http.Request) {
	webPrefsMu.Lock()
	defer webPrefsMu.Unlock()
	switch r.Method {
	case http.MethodGet:
		data, err := os.ReadFile(webPrefsPath())
		if err != nil || !json.Valid(data) {
			data = []byte("{}")
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.Header().Set("Cache-Control", "no-store")
		rw.Write(data) //nolint:errcheck
	case http.MethodPost:
		var prefs map[string]json.RawMessage
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 64<<10)).Decode(&prefs); err != nil {
			http.Error(rw, "not a JSON object", http.StatusBadRequest)
			return
		}
		data, _ := json.MarshalIndent(prefs, "", "  ")
		tmp := webPrefsPath() + ".tmp"
		if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, webPrefsPath()); err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	default:
		http.Error(rw, "not allowed", http.StatusMethodNotAllowed)
	}
}

// ---- statistics ----

func (w *TWebUI) handleStats(rw http.ResponseWriter, r *http.Request) {
	ro, err := openReadOnly()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	defer ro.Close()
	type stat struct {
		Label string
		Value int
	}
	var stats []stat
	for _, q := range []struct{ label, query string }{
		{"Entries", `SELECT COUNT(*) FROM bib_entries WHERE field = 'entrytype'`},
		{"Groups", `SELECT COUNT(DISTINCT group_name) FROM bib_groups`},
		{"Contributors", `SELECT COUNT(*) FROM contributors`},
		{"Contributor name forms", `SELECT COUNT(*) FROM contributor_names`},
		{"Entries with a DBLP key", `SELECT COUNT(*) FROM bib_entries WHERE field = 'dblp'`},
		{"Same-field mappings", `SELECT COUNT(*) FROM field_mappings WHERE source_field = target_field`},
		{"Cross-field mappings", `SELECT COUNT(*) FROM field_mappings WHERE source_field != target_field`},
		{"Superseded values", `SELECT COUNT(*) FROM superseded_field_values`},
		{"Key oldies", `SELECT COUNT(*) FROM key_oldies`},
		{"Key hints", `SELECT COUNT(*) FROM key_hints`},
		{"Non-double entry pairs", `SELECT COUNT(*) FROM non_double_entries`},
	} {
		var n int
		if ro.QueryRow(q.query).Scan(&n) == nil {
			stats = append(stats, stat{q.label, n})
		}
	}
	w.render(rw, "stats", map[string]any{"Stats": stats})
}

// ---- search ----

// TWebEntryRow is one entry in a list.
type TWebEntryRow struct {
	Key, Type, Year, Title string
	Authors                []TWebPerson
	HTML                   template.HTML // the reference as the homepage fragments render it ("": not available)
	Roles                  string        // on an author's list: "author", "editor" or both (space-separated)
	HasPDF                 bool          // on an author's list: the entry has a PDF
}

// handleEntries answers a page of the Search table (JSON): q filters (every
// word must occur), sort names the column ordered on ("": newest first), desc
// reverses it, cols names the columns, from and n the rows. fields=1 adds the
// columns that can be chosen.
func (w *TWebUI) handleEntries(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, _ := strconv.Atoi(q.Get("from"))
	n, _ := strconv.Atoi(q.Get("n"))
	if n <= 0 || n > 5000 {
		n = 200
	}
	var cols []string
	if c := q.Get("cols"); c != "" {
		cols = strings.Split(c, ",")
	}
	var groups []string
	if g := q.Get("groups"); g != "" {
		groups = strings.Split(g, "\n")
	}
	var sel *TSelectSession
	if id := q.Get("sel"); id != "" {
		if sel = selectSession(id); sel == nil {
			http.Error(rw, "this Select tab is closed", http.StatusNotFound)
			return
		}
	}
	page, err := webIndexPage(q.Get("q"), groups, sel, q.Get("only") == "1", q.Get("sort"), q.Get("desc") == "1", cols, from, n, q.Get("fields") == "1")
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(rw, page)
}

// webEntryRow reads the few fields a list shows; the year is the crossref
// parent's when the entry has none.
func webEntryRow(ro *sql.DB, key string) TWebEntryRow {
	row := TWebEntryRow{Key: key}
	rows, err := ro.Query(`SELECT field, value FROM bib_entries WHERE entry_key = ? AND field IN ('entrytype', 'year', 'title')`, key)
	if err == nil {
		for rows.Next() {
			var f, v string
			if rows.Scan(&f, &v) == nil {
				switch f {
				case "entrytype":
					row.Type = v
				case "year":
					row.Year = v
				case "title":
					row.Title = v
				}
			}
		}
		rows.Close()
	}
	if row.Year == "" { // the crossref parent's
		ro.QueryRow(`SELECT p.value FROM bib_entries c JOIN bib_entries p ON p.entry_key = c.value AND p.field = 'year'
			WHERE c.entry_key = ? AND c.field = 'crossref'`, key).Scan(&row.Year) //nolint:errcheck
	}
	row.Authors = webContributors(ro, key, "author")
	if len(row.Authors) == 0 {
		row.Authors = webContributors(ro, key, "editor")
	}
	return row
}

// TWebPerson is a contributor as a list shows it: name and id (for the link).
type TWebPerson struct{ ID, Name string }

// webContributors returns the contributors in a role of an entry, in order.
func webContributors(ro *sql.DB, key, role string) []TWebPerson {
	rows, err := ro.Query(`SELECT c.id, c.name FROM contributor_roles r JOIN contributors c ON c.id = r.contributor_id
		WHERE r.entry_key = ? AND r.role = ? ORDER BY r.position`, key, role)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var people []TWebPerson
	for rows.Next() {
		var p TWebPerson
		if rows.Scan(&p.ID, &p.Name) == nil {
			people = append(people, p)
		}
	}
	return people
}

// ---- an entry ----

// webField is a field on the entry pop-up: a link out (Link), or to another
// entry's pop-up (Entry).
type webField struct{ Name, Value, Link, Entry string }

func (w *TWebUI) handleEntry(rw http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/"+w.token+"/frag/entry/")
	ro, err := openReadOnly()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	defer ro.Close()
	rows, err := ro.Query(`SELECT field, value FROM bib_entries WHERE entry_key = ? ORDER BY field`, key)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	var fields []webField
	entryType := ""
	for rows.Next() {
		var f, v string
		if rows.Scan(&f, &v) != nil {
			continue
		}
		switch f {
		case "entrytype":
			entryType = v
			continue
		case "author", "editor":
			continue // from the contributors, below
		}
		field := webField{Name: f, Value: v}
		if f == "crossref" {
			field.Entry = v
		} else {
			field.Link = webFieldLink(f, v)
		}
		fields = append(fields, field)
	}
	rows.Close()
	if entryType == "" && len(fields) == 0 {
		http.NotFound(rw, r)
		return
	}
	var groups []string
	if gRows, err := ro.Query(`SELECT group_name FROM bib_groups WHERE entry_key = ? ORDER BY group_name`, key); err == nil {
		for gRows.Next() {
			var g string
			if gRows.Scan(&g) == nil {
				groups = append(groups, g)
			}
		}
		gRows.Close()
	}
	_, pdfErr := os.Stat(entryPDFPath(key))
	var reference template.HTML
	if refs, ok := renderReferences([]string{key}); ok {
		reference = linkReferenceNames(ro, key, refs[key])
	}
	w.render(rw, "entry", map[string]any{
		"GroupsField": strings.Join(groups, ", "),
		"Reference":   reference,
		"Key":         key,
		"Type":        entryType,
		"Authors":     webContributors(ro, key, "author"),
		"Editors":     webContributors(ro, key, "editor"),
		"Fields":      fields,
		"Groups":      groups,
		"HasPDF":      pdfErr == nil,
	})
}

// webFieldURLs are the built-in field links; the settings' field_urls add to them.
var webFieldURLs = map[string]string{"doi": "https://doi.org/$", "dblp": "https://dblp.org/rec/$"}

// webFieldLink is the link a field's value stands for: the value itself for a
// URL field holding a web address, else the field's URL pattern ("$": the
// value, path-escaped but for its slashes) from the settings or built in; ""
// when there is none.
func webFieldLink(field, value string) string {
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		if field == "url" || strings.HasPrefix(field, "url") {
			return value
		}
	}
	pattern, ok := fieldURLs[field]
	if !ok {
		pattern = webFieldURLs[field]
	}
	if pattern == "" || value == "" {
		return ""
	}
	return strings.ReplaceAll(pattern, "$", strings.ReplaceAll(url.PathEscape(value), "%2F", "/"))
}

// entryPDFPath is where an entry's PDF lives: <base>.files/<key>.pdf.
func entryPDFPath(key string) string {
	return bibTeXFolder + bibTeXBaseName + ".files/" + key + ".pdf"
}

// handlePDF opens an entry's PDF with the OS's viewer (POST only: a page cannot open it
// by merely being loaded or prefetched).
func (w *TWebUI) handlePDF(rw http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/"+w.token+"/pdf/")
	if r.Method != http.MethodPost || strings.ContainsAny(key, "/\\") {
		http.Error(rw, "not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := entryPDFPath(key)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(rw, r)
		return
	}
	if err := openWithOS(path); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

// maxPDFUpload bounds a PDF dropped on an entry.
const maxPDFUpload = 300 << 20

// handlePDFUpload stores a PDF dropped on an entry in the web UI (Erik,
// 2026-11-24) as <base>.files/<key>.pdf (POST /api/pdfup/<key>, the file as the
// body). Only a PDF (it starts with %PDF-); an entry that has a PDF already is
// refused (409) unless replace=1, and then its old PDF is put aside (undo).
func (w *TWebUI) handlePDFUpload(rw http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/"+w.token+"/api/pdfup/")
	if r.Method != http.MethodPost || key == "" || strings.ContainsAny(key, "/\\") || strings.HasPrefix(key, ".") {
		http.Error(rw, "not allowed", http.StatusMethodNotAllowed)
		return
	}
	ro, err := openReadOnly()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	var canonical string
	if ro.QueryRow(`SELECT key FROM key_oldies WHERE alias = ?`, key).Scan(&canonical) == nil {
		key = canonical
	}
	var n int
	ro.QueryRow(`SELECT COUNT(*) FROM bib_entries WHERE entry_key = ? AND field = 'entrytype'`, key).Scan(&n) //nolint:errcheck
	ro.Close()
	if n == 0 {
		http.Error(rw, "no such entry: "+key, http.StatusNotFound)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, maxPDFUpload))
	if err != nil {
		http.Error(rw, "the file could not be read (larger than 300 MB?)", http.StatusRequestEntityTooLarge)
		return
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		http.Error(rw, "this is not a PDF file", http.StatusUnsupportedMediaType)
		return
	}
	path := entryPDFPath(key)
	_, existsErr := os.Stat(path)
	exists := existsErr == nil
	if exists && r.URL.Query().Get("replace") != "1" {
		http.Error(rw, key+" has a PDF already", http.StatusConflict)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	tmp := path + ".upload"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	old := ""
	if exists {
		if old, err = stashPDF(path); err != nil {
			os.Remove(tmp) //nolint:errcheck
			http.Error(rw, "the old PDF could not be put aside: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	setLibraryPDF(key, true)
	what := "stored"
	if exists {
		what = "replaced"
	}
	fmt.Fprintf(os.Stderr, "%s  PDF %s for %s (%d KB, dropped in the web UI)\n", time.Now().Format(time.DateTime), what, key, len(data)/1024)
	undo := newPDFUndo(key, old, true)
	writeJSON(rw, map[string]any{"ok": true, "undo": undo, "message": fmt.Sprintf("PDF %s for %s (%d KB).", what, key, len(data)/1024)})
}

// ---- undoing a PDF drop or removal ----
//
// A replaced or removed PDF is first put aside in the local DB folder (pdf_undo/),
// not in the Trash, so Undo can put it back; after pdfUndoKeep it goes to the
// Trash. Undo of a drop sends the dropped file to the Trash and puts a replaced
// one back; undo of a removal puts the PDF back.

const pdfUndoKeep = 5 * time.Minute

type tPDFUndo struct {
	key     string
	stashed string // the PDF put aside ("" when there was none)
	added   bool   // a drop (its file goes on undo), not a removal
}

var (
	pdfUndos   = map[string]*tPDFUndo{}
	pdfUndosMu sync.Mutex
)

// stashPDF puts a PDF aside for an undo and returns where.
func stashPDF(path string) (string, error) {
	dir := localDBFolder + "pdf_undo/"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := dir + time.Now().Format("20060102-150405.000") + "-" + filepath.Base(path)
	return dest, os.Rename(path, dest)
}

// newPDFUndo records what an undo needs and returns its id; after pdfUndoKeep
// the stashed PDF goes to the Trash and the undo is gone.
func newPDFUndo(key, stashed string, added bool) string {
	b := make([]byte, 6)
	rand.Read(b) //nolint:errcheck
	id := hex.EncodeToString(b)
	pdfUndosMu.Lock()
	pdfUndos[id] = &tPDFUndo{key: key, stashed: stashed, added: added}
	pdfUndosMu.Unlock()
	time.AfterFunc(pdfUndoKeep, func() {
		pdfUndosMu.Lock()
		u := pdfUndos[id]
		delete(pdfUndos, id)
		pdfUndosMu.Unlock()
		if u != nil && u.stashed != "" {
			moveFileToTrash(u.stashed)
		}
	})
	return id
}

// flushPDFUndos sends the PDFs still put aside for an undo to the Trash (the
// service stops: no undo after that).
func flushPDFUndos() {
	pdfUndosMu.Lock()
	defer pdfUndosMu.Unlock()
	for id, u := range pdfUndos {
		if u.stashed != "" {
			moveFileToTrash(u.stashed)
		}
		delete(pdfUndos, id)
	}
}

// setLibraryPDF updates the loaded library's list of PDFs, when it is free
// (else the next load reads the folder).
func setLibraryPDF(key string, has bool) {
	if libraryLock.TryLock() {
		if Library.PDFFiles != nil {
			if has {
				Library.PDFFiles[key] = true
			} else {
				delete(Library.PDFFiles, key)
			}
		}
		libraryLock.Unlock()
	}
}

// handlePDFRemove moves an entry's PDF aside, to the Trash after pdfUndoKeep
// (POST /api/pdfremove/<key>); the answer has the undo's id.
func (w *TWebUI) handlePDFRemove(rw http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/"+w.token+"/api/pdfremove/")
	if r.Method != http.MethodPost || key == "" || strings.ContainsAny(key, "/\\") || strings.HasPrefix(key, ".") {
		http.Error(rw, "not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := entryPDFPath(key)
	if _, err := os.Stat(path); err != nil {
		http.Error(rw, key+" has no PDF", http.StatusNotFound)
		return
	}
	stashed, err := stashPDF(path)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	setLibraryPDF(key, false)
	fmt.Fprintf(os.Stderr, "%s  PDF of %s removed in the web UI (to the Trash in %s)\n", time.Now().Format(time.DateTime), key, pdfUndoKeep)
	writeJSON(rw, map[string]any{"ok": true, "undo": newPDFUndo(key, stashed, false), "message": "PDF of " + key + " removed."})
}

// handlePDFUndo undoes a drop or a removal (POST /api/pdfundo/<id>).
func (w *TWebUI) handlePDFUndo(rw http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/"+w.token+"/api/pdfundo/")
	if r.Method != http.MethodPost {
		http.Error(rw, "not allowed", http.StatusMethodNotAllowed)
		return
	}
	pdfUndosMu.Lock()
	u := pdfUndos[id]
	delete(pdfUndos, id)
	pdfUndosMu.Unlock()
	if u == nil {
		http.Error(rw, "too late to undo", http.StatusGone)
		return
	}
	path := entryPDFPath(u.key)
	if u.added {
		if _, err := os.Stat(path); err == nil && !moveFileToTrash(path) {
			http.Error(rw, "the dropped PDF could not be moved to the Trash", http.StatusInternalServerError)
			return
		}
	}
	has := false
	if u.stashed != "" {
		if err := os.Rename(u.stashed, path); err != nil {
			http.Error(rw, "the old PDF could not be put back: "+err.Error(), http.StatusInternalServerError)
			return
		}
		has = true
	}
	setLibraryPDF(u.key, has)
	fmt.Fprintf(os.Stderr, "%s  PDF change of %s undone in the web UI\n", time.Now().Format(time.DateTime), u.key)
	writeJSON(rw, map[string]any{"ok": true, "key": u.key, "message": "Undone."})
}

// ---- an author ----

// handleAuthor shows what the library knows about a contributor: name, ORCIDs,
// DBLP person key, the name forms, and the entries (newest first).
func (w *TWebUI) handleAuthor(rw http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/"+w.token+"/frag/author/")
	ro, err := openReadOnly()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	defer ro.Close()
	var canonical string
	if ro.QueryRow(`SELECT canonical_id FROM contributor_id_oldies WHERE absorbed_id = ?`, id).Scan(&canonical) == nil {
		id = canonical
	}
	var name string
	var orcid, dblpKey sql.NullString
	if ro.QueryRow(`SELECT name, orcid, dblp_key FROM contributors WHERE id = ?`, id).Scan(&name, &orcid, &dblpKey) != nil {
		http.NotFound(rw, r)
		return
	}
	strings1 := func(query string, args ...any) []string {
		var out []string
		if rows, err := ro.Query(query, args...); err == nil {
			for rows.Next() {
				var v string
				if rows.Scan(&v) == nil {
					out = append(out, v)
				}
			}
			rows.Close()
		}
		return out
	}
	orcids := strings1(`SELECT orcid FROM contributor_orcids WHERE contributor_id = ? ORDER BY is_canonical DESC, orcid`, id)
	if len(orcids) == 0 && orcid.Valid && orcid.String != "" {
		orcids = []string{orcid.String}
	}
	forms := strings1(`SELECT name FROM contributor_names WHERE id = ? AND name != ? ORDER BY name`, id, name)
	keys := strings1(`SELECT DISTINCT entry_key FROM contributor_roles WHERE contributor_id = ?`, id)
	roles := map[string][]string{}
	if rRows, err := ro.Query(`SELECT DISTINCT entry_key, role FROM contributor_roles WHERE contributor_id = ? ORDER BY role`, id); err == nil {
		for rRows.Next() {
			var k, role string
			if rRows.Scan(&k, &role) == nil {
				roles[k] = append(roles[k], role)
			}
		}
		rRows.Close()
	}
	list := make([]TWebEntryRow, 0, len(keys))
	authored, edited := 0, 0
	for _, k := range keys {
		row := webEntryRow(ro, k)
		row.Roles = strings.Join(roles[k], " ")
		if _, err := os.Stat(entryPDFPath(k)); err == nil {
			row.HasPDF = true
		}
		if strings.Contains(row.Roles, "author") {
			authored++
		}
		if strings.Contains(row.Roles, "editor") {
			edited++
		}
		list = append(list, row)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Year != list[j].Year {
			return list[i].Year > list[j].Year
		}
		return list[i].Title < list[j].Title
	})
	addReferences(list)
	for i := range list {
		if list[i].HTML != "" {
			list[i].HTML = linkReferenceNames(ro, list[i].Key, list[i].HTML)
		}
	}
	data := map[string]any{"ID": id, "Name": name, "ORCIDs": orcids, "Forms": forms, "Results": list,
		"Authored": authored, "Edited": edited}
	if dblpKey.Valid && dblpKey.String != "" {
		data["DBLP"] = strings.TrimPrefix(dblpKey.String, "homepages/")
	}
	w.render(rw, "author", data)
}

// webRenderMu lets one page at a time render from the library (the rendering
// may fill caches of its own).
var webRenderMu sync.Mutex

// renderReferences renders entries as HTML references, as -render_as_html does
// for the homepage fragments (with the crossref parent and the output mappings),
// from the loaded library. It returns false, rendering nothing, when the library
// is not loaded or a job is busy with it: the page then shows the stored values.
func renderReferences(keys []string) (map[string]template.HTML, bool) {
	webRenderMu.Lock()
	defer webRenderMu.Unlock()
	if !libraryLock.TryRLock() {
		return nil, false
	}
	defer libraryLock.RUnlock()
	if serviceLibraryLevel == libraryNotLoaded || entryCache == nil {
		return nil, false
	}
	out := map[string]template.HTML{}
	for _, key := range keys {
		if html := Library.renderAsHTML(key); html != "" {
			out[key] = template.HTML(html) //nolint:gosec // our own rendering of our own data
		}
	}
	return out, true
}

// linkReferenceNames makes the authors' and editors' names in an entry's HTML
// reference links to their pop-ups (Erik, 2026-11-24), as in the BibTeX part:
// each contributor's name, in "First Last" order as the reference shows it,
// in role order; the crossref parent's editors ("In: …") likewise. A name not
// found as rendered stays plain.
func linkReferenceNames(ro *sql.DB, key string, ref template.HTML) template.HTML {
	html := string(ref)
	at := 0
	link := func(entry, role string) {
		for _, person := range webContributors(ro, entry, role) {
			shown := texToHTML(normalNameOrder(strings.TrimSpace(person.Name)))
			if shown == "" {
				continue
			}
			// The rendering may write a letter such as á as &#225;.
			var entities strings.Builder
			for _, r := range shown {
				if r < 128 {
					entities.WriteRune(r)
				} else {
					fmt.Fprintf(&entities, "&#%d;", r)
				}
			}
			for _, form := range []string{shown, entities.String()} {
				j := strings.Index(html[at:], form)
				if j < 0 {
					continue
				}
				j += at
				a := fmt.Sprintf(`<a href="#" data-author="%s" data-name="%s">%s</a>`,
					template.HTMLEscapeString(person.ID), template.HTMLEscapeString(texToText(person.Name)), form)
				html = html[:j] + a + html[j+len(form):]
				at = j + len(a)
				break
			}
		}
	}
	link(key, "author")
	link(key, "editor")
	var parent string
	if ro.QueryRow(`SELECT value FROM bib_entries WHERE entry_key = ? AND field = 'crossref'`, key).Scan(&parent) == nil {
		link(parent, "editor")
	}
	return template.HTML(html) //nolint:gosec // our own rendering, names escaped as rendered
}

// addReferences fills in the HTML references of a list, when available.
func addReferences(list []TWebEntryRow) {
	keys := make([]string, len(list))
	for i, row := range list {
		keys[i] = row.Key
	}
	if refs, ok := renderReferences(keys); ok {
		for i := range list {
			list[i].HTML = refs[list[i].Key]
		}
	}
}

// ---- the log ----

// TWebLog keeps the last lines of what the service and its jobs wrote, for the
// log page (Erik, 2026-11-24): the service log, and every job's output, also
// of jobs run from the command line.
type TWebLog struct {
	mu      sync.Mutex
	ended   int // the number of lines ended so far (the kept ones are the last)
	lines   []string
	current strings.Builder // the line being written ("\r" starts it afresh)
}

const webLogLines = 3000

var webLog = &TWebLog{}

// write adds text: ANSI codes dropped, "\r" restarts the current line (a
// progress line shows its latest state), "\n" ends it.
func (l *TWebLog) write(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range reANSIEscape.ReplaceAllString(s, "") {
		switch r {
		case '\r':
			l.current.Reset()
		case '\n':
			l.lines = append(l.lines, l.current.String())
			l.ended++
			l.current.Reset()
			if len(l.lines) > webLogLines {
				l.lines = l.lines[len(l.lines)-webLogLines:]
			}
		default:
			l.current.WriteRune(r)
		}
	}
}

// since returns the lines ended after the first from (as far as kept), the
// number ended so far, and the line being written.
func (l *TWebLog) since(from int) ([]string, int, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	first := l.ended - len(l.lines) // the number of the first kept line
	if from < first || from > l.ended {
		from = first
	}
	return append([]string{}, l.lines[from-first:]...), l.ended, l.current.String()
}

// teeServiceStderr sends what the service writes to stderr (its log file) to
// the log page as well.
func teeServiceStderr() {
	r, w, err := os.Pipe()
	if err != nil {
		return
	}
	orig := os.Stderr
	os.Stderr = w
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				orig.Write(buf[:n]) //nolint:errcheck
				webLog.write(string(buf[:n]))
			}
			if err != nil {
				return
			}
		}
	}()
}

// handleLog answers the log lines after from (JSON).
func (w *TWebUI) handleLog(rw http.ResponseWriter, r *http.Request) {
	from, _ := strconv.Atoi(r.URL.Query().Get("from"))
	lines, ended, current := webLog.since(from)
	writeJSON(rw, map[string]any{"lines": lines, "next": ended, "current": current})
}
