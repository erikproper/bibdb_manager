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
	server := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", w.port),
		Handler:           w.guard(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s  web UI not started: %s (set another service_port in the settings)\n",
			time.Now().Format(time.DateTime), err)
		return
	}
	fmt.Fprintf(os.Stderr, "%s  web UI on port %d\n", time.Now().Format(time.DateTime), w.port)
	go server.Serve(listener) //nolint:errcheck
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
	Key, Type, Year, Title, Authors string
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
	row.Authors = strings.Join(webContributors(ro, key, "author"), "; ")
	if row.Authors == "" {
		row.Authors = strings.Join(webContributors(ro, key, "editor"), "; ")
	}
	return row
}

// webContributors returns the names in a role of an entry, in order.
func webContributors(ro *sql.DB, key, role string) []string {
	rows, err := ro.Query(`SELECT c.name FROM contributor_roles r JOIN contributors c ON c.id = r.contributor_id
		WHERE r.entry_key = ? AND r.role = ? ORDER BY r.position`, key, role)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	return names
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
	w.render(rw, "entry", map[string]any{
		"Key":     key,
		"Type":    entryType,
		"Authors": webContributors(ro, key, "author"),
		"Editors": webContributors(ro, key, "editor"),
		"Fields":  fields,
		"Groups":  groups,
		"HasPDF":  pdfErr == nil,
		"Opened":  r.URL.Query().Get("opened") != "",
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
