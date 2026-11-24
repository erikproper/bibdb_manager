package main

// The web UI (plan step 22.3a): read-only pages served by the service on
// localhost — status, statistics, search, an entry, its PDF opened with macOS.
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
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
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
		}).Parse(webPages))}
}

// URL is the address of the main page.
func (w *TWebUI) URL() string { return fmt.Sprintf("http://localhost:%d/%s/", w.port, w.token) }

// start listens in the background; a port in use is reported in the log.
func (w *TWebUI) start() {
	mux := http.NewServeMux()
	prefix := "/" + w.token
	mux.HandleFunc(prefix+"/", w.handleStatus)
	mux.HandleFunc(prefix+"/stats", w.handleStats)
	mux.HandleFunc(prefix+"/search", w.handleSearch)
	mux.HandleFunc(prefix+"/entry/", w.handleEntry)
	mux.HandleFunc(prefix+"/pdf/", w.handlePDF)
	mux.HandleFunc(prefix+"/author/", w.handleAuthor)
	mux.HandleFunc(prefix+"/log", w.handleLog)
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
}

// stop closes the web server (and so its port).
func (w *TWebUI) stop() {
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

// render fills the page layout with one page's content.
func (w *TWebUI) render(rw http.ResponseWriter, page string, data map[string]any) {
	data["Base"] = "/" + w.token
	data["Library"] = bibTeXBaseName
	data["Page"] = page
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := w.pages.ExecuteTemplate(rw, "layout", data); err != nil {
		fmt.Fprintf(os.Stderr, "web UI: %s\n", err)
	}
}

// ---- status ----

func (w *TWebUI) handleStatus(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/"+w.token+"/" {
		http.NotFound(rw, r)
		return
	}
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
}

func (w *TWebUI) handleSearch(rw http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	data := map[string]any{"Query": q}
	if q == "" {
		w.render(rw, "search", data)
		return
	}
	ro, err := openReadOnly()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	defer ro.Close()

	// A key, an old key or a preferred alias goes straight to the entry.
	key := q
	var alias string
	if ro.QueryRow(`SELECT key FROM key_oldies WHERE alias = ?`, q).Scan(&alias) == nil {
		key = alias
	} else if ro.QueryRow(`SELECT entry_key FROM bib_entries WHERE field = 'preferredalias' AND value = ?`, q).Scan(&alias) == nil {
		key = alias
	}
	var found string
	if ro.QueryRow(`SELECT entry_key FROM bib_entries WHERE entry_key = ? LIMIT 1`, key).Scan(&found) == nil {
		http.Redirect(rw, r, "/"+w.token+"/entry/"+url.PathEscape(found), http.StatusSeeOther)
		return
	}

	// Otherwise: title, DOI or author name containing the text.
	like := "%" + q + "%"
	rows, err := ro.Query(`
		SELECT DISTINCT entry_key FROM bib_entries WHERE field IN ('title', 'doi') AND value LIKE ?
		UNION
		SELECT DISTINCT r.entry_key FROM contributor_roles r JOIN contributors c ON c.id = r.contributor_id
		 WHERE c.name LIKE ?
		LIMIT 201`, like, like)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	var keys []string
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			keys = append(keys, k)
		}
	}
	rows.Close()
	if len(keys) > 200 {
		data["More"] = true
		keys = keys[:200]
	}
	list := make([]TWebEntryRow, 0, len(keys))
	for _, k := range keys {
		list = append(list, webEntryRow(ro, k))
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Year != list[j].Year {
			return list[i].Year > list[j].Year
		}
		return list[i].Title < list[j].Title
	})
	addReferences(list)
	data["Results"] = list
	w.render(rw, "search", data)
}

// webEntryRow reads the few fields a list shows.
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

type webField struct{ Name, Value, Link string }

func (w *TWebUI) handleEntry(rw http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/"+w.token+"/entry/")
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
		switch f {
		case "crossref":
			field.Link = "/" + w.token + "/entry/" + url.PathEscape(v)
		case "doi":
			field.Link = "https://doi.org/" + v
		case "url":
			if strings.HasPrefix(v, "http") {
				field.Link = v
			}
		case "dblp":
			field.Link = "https://dblp.org/rec/" + v
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
		reference = refs[key]
	}
	w.render(rw, "entry", map[string]any{
		"Reference": reference,
		"Key":       key,
		"Type":      entryType,
		"Authors":   webContributors(ro, key, "author"),
		"Editors":   webContributors(ro, key, "editor"),
		"Fields":    fields,
		"Groups":    groups,
		"HasPDF":    pdfErr == nil,
		"Opened":    r.URL.Query().Get("opened") != "",
	})
}

// entryPDFPath is where an entry's PDF lives: <base>.files/<key>.pdf.
func entryPDFPath(key string) string {
	return bibTeXFolder + bibTeXBaseName + ".files/" + key + ".pdf"
}

// handlePDF opens an entry's PDF with macOS (POST only: a page cannot open it
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
	if err := exec.Command("open", path).Run(); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(rw, r, "/"+w.token+"/entry/"+url.PathEscape(key)+"?opened=1", http.StatusSeeOther)
}

// ---- an author ----

// handleAuthor shows what the library knows about a contributor: name, ORCIDs,
// DBLP person key, the name forms, and the entries (newest first).
func (w *TWebUI) handleAuthor(rw http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/"+w.token+"/author/")
	ro, err := openReadOnly()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	defer ro.Close()
	var canonical string
	if ro.QueryRow(`SELECT canonical_id FROM contributor_id_oldies WHERE absorbed_id = ?`, id).Scan(&canonical) == nil {
		http.Redirect(rw, r, "/"+w.token+"/author/"+url.PathEscape(canonical), http.StatusSeeOther)
		return
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
	list := make([]TWebEntryRow, 0, len(keys))
	for _, k := range keys {
		list = append(list, webEntryRow(ro, k))
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Year != list[j].Year {
			return list[i].Year > list[j].Year
		}
		return list[i].Title < list[j].Title
	})
	addReferences(list)
	data := map[string]any{"ID": id, "Name": name, "ORCIDs": orcids, "Forms": forms, "Results": list}
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
			l.current.Reset()
			if len(l.lines) > webLogLines {
				l.lines = l.lines[len(l.lines)-webLogLines:]
			}
		default:
			l.current.WriteRune(r)
		}
	}
}

func (l *TWebLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(append(append([]string{}, l.lines...), l.current.String()), "\n")
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

func (w *TWebUI) handleLog(rw http.ResponseWriter, r *http.Request) {
	w.render(rw, "log", map[string]any{"Log": webLog.text()})
}
