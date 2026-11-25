package main

// Select tabs in the web UI (Erik, 2026-11-24): `-select [<file>]`, run in a
// project folder, opens for each of its bib files (file_names in ./bib.config,
// with a .keys file: all modes but full and harvest), or for <file> only, a tab
// "Select: <file>" in the web UI. The tab is the Search table with a tick box
// before each entry: ticking an entry adds it to the file's .keys, unticking
// removes it.
//
// Ticks: yellow = selected. Unticking an entry whose local key (in .keys)
// differs from its preferred alias turns the tick blue: the old local key is
// kept until the next save, so ticking it again restores it ("oops"). Ticking
// an unselected entry adds it under its preferred alias (the canonical key
// when key_mapping is off).
//
// The selection lives in the service (a page reload keeps it); Save writes the
// .keys file (the old one kept as .keys.old, as a sync does); Sync saves, then
// runs -sync <file> in the project folder as a job (output in the Log tab;
// without a person there, so a sync asking questions stops at the first).
// Lines of the .keys file whose entry the library does not know are kept as
// they are.

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var cmdSelect bool

// TSelectFile is one bib file of a project, as -select sends it to the service.
type TSelectFile struct {
	Dir        string `json:"dir"`
	Name       string `json:"name"`
	KeyMapping bool   `json:"key_mapping"`
}

// Tick states of an entry in a Select tab.
const (
	selectOff  = 0
	selectOn   = 1 // yellow
	selectKept = 2 // blue: unticked, its old local key kept until the next save
)

// TSelectEntry is an entry of a selection: its local key (On), or the old one
// kept (Kept).
type TSelectEntry struct {
	Local string
	State int
}

// TSelectSession is one Select tab.
type TSelectSession struct {
	ID      string
	File    TSelectFile
	mu      sync.Mutex
	entries map[string]*TSelectEntry // by canonical key
	order   []string                 // canonical keys: the .keys file's order, added ones after
	unknown []string                 // .keys lines whose entry the library does not know (kept as they are)
	changes int                      // changes since the last save
	edits   int                      // changes ever (the "only selected" list is redone after one)
	message string                   // the last save's or sync's outcome
	opened  time.Time
}

var (
	selectSessions   = map[string]*TSelectSession{}
	selectSessionsMu sync.Mutex
)

func (f TSelectFile) keysPath() string { return filepath.Join(f.Dir, f.Name+KeysFileExtension) }

// ---- the command line side ----

// selectFiles reads ./bib.config and returns the files a Select tab is for:
// those with a .keys file in use (all modes but full and harvest), or only only.
func selectFiles(only string) ([]TSelectFile, error) {
	if !FileExists("bib.config") {
		return nil, fmt.Errorf("there is no bib.config in this folder")
	}
	base, ok := readBibGetConfig()
	if !ok {
		return nil, fmt.Errorf("bib.config could not be read")
	}
	dir, _ := os.Getwd()
	names := base.FileNames
	if names == "" {
		names = base.FileName
	}
	var files []TSelectFile
	found := false
	for _, name := range strings.Split(names, ";") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if only != "" && name != only && name != strings.TrimSuffix(only, BibFileExtension) {
			continue
		}
		found = true
		cfg, ok := readFileConfig(base, name, ".")
		if !ok {
			continue
		}
		if cfg.Mode == "full" || cfg.Mode == "harvest" { // the modes without a .keys file
			if only != "" {
				return nil, fmt.Errorf("%s is synced in mode %q, which has no .keys file", name, cfg.Mode)
			}
			continue
		}
		files = append(files, TSelectFile{Dir: dir, Name: name, KeyMapping: cfg.KeyMapping})
	}
	if only != "" && !found {
		return nil, fmt.Errorf("%s is not among the file_names in bib.config (%s)", only, names)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("bib.config names no file with a .keys file (modes pull, follow and subset)")
	}
	return files, nil
}

// doSelect is -select [<file>]: the service opens a Select tab per file, and
// the web UI is brought up on the first.
func doSelect() {
	only := ""
	if len(flag.Args()) > 0 {
		only = flag.Args()[0]
	}
	files, err := selectFiles(only)
	if err != nil {
		messagef("-select: %s.\n", err)
		endRun(1)
	}
	conn := dialService()
	if conn == nil {
		messagef("No service runs for %s; start it with -start.\n", bibTeXBaseName)
		endRun(1)
	}
	defer conn.Close()
	data, _ := json.Marshal(files)
	newFrameSender(conn).send(serviceFrame{T: "select", D: string(data)}) //nolint:errcheck
	var f serviceFrame
	if json.NewDecoder(bufio.NewReader(conn)).Decode(&f) != nil || f.T != "out" {
		messageln("The service did not open the Select tabs.")
		endRun(1)
	}
	address, hash, _ := strings.Cut(strings.TrimSpace(f.D), "#")
	for _, file := range files {
		messagef("Select tab for %s.\n", file.Name)
	}
	if !reuseBrowserTabAt(address, "#"+hash) {
		if err := openInBrowser(address + "#" + hash); err != nil {
			messagef("Open %s#%s in a browser (%s).\n", address, hash, err)
		}
	}
}

// ---- the service side ----

// openSelectSessions opens (or reuses) a session per file; it returns the id
// of the first.
func openSelectSessions(files []TSelectFile) string {
	selectSessionsMu.Lock()
	defer selectSessionsMu.Unlock()
	first := ""
	for _, f := range files {
		var session *TSelectSession
		for _, s := range selectSessions {
			if s.File.Dir == f.Dir && s.File.Name == f.Name {
				session = s // already open: keep its state
			}
		}
		if session == nil {
			b := make([]byte, 4)
			rand.Read(b) //nolint:errcheck
			session = &TSelectSession{ID: hex.EncodeToString(b), File: f, opened: time.Now()}
			session.read()
			selectSessions[session.ID] = session
			fmt.Fprintf(os.Stderr, "%s  select tab for %s (%d entries)\n", time.Now().Format(time.DateTime),
				f.keysPath(), len(session.entries))
		}
		if first == "" {
			first = session.ID
		}
	}
	return first
}

// read loads the session's .keys file: "local;canonical" lines (a bare key is
// a canonical one, its local key the preferred alias). Old keys are mapped to
// their entry.
func (s *TSelectSession) read() {
	s.entries = map[string]*TSelectEntry{}
	s.order, s.unknown, s.changes = nil, nil, 0
	data, err := os.ReadFile(s.File.keysPath())
	if err != nil {
		return
	}
	ro, err := openReadOnly()
	if err != nil {
		return
	}
	defer ro.Close()
	resolve := func(key string) string {
		var n int
		if ro.QueryRow(`SELECT COUNT(*) FROM bib_entries WHERE entry_key = ? AND field = 'entrytype'`, key).Scan(&n) == nil && n > 0 {
			return key
		}
		var canonical string
		if ro.QueryRow(`SELECT key FROM key_oldies WHERE alias = ?`, key).Scan(&canonical) == nil {
			return canonical
		}
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		local, canonical, paired := strings.Cut(line, csvDelimiter)
		local, canonical = strings.TrimSpace(local), strings.TrimSpace(canonical)
		if !paired {
			canonical, local = local, ""
		}
		key := resolve(canonical)
		if key == "" || s.entries[key] != nil {
			s.unknown = append(s.unknown, line)
			continue
		}
		if local == "" {
			local = s.newLocal(key)
		}
		s.entries[key] = &TSelectEntry{Local: local, State: selectOn}
		s.order = append(s.order, key)
	}
}

// preferredAlias is an entry's preferred alias ("" when it has none).
func preferredAlias(key string) string {
	ro, err := openReadOnly()
	if err != nil {
		return ""
	}
	defer ro.Close()
	var alias string
	ro.QueryRow(`SELECT value FROM bib_entries WHERE entry_key = ? AND field = 'preferredalias'`, key).Scan(&alias) //nolint:errcheck
	return alias
}

// newLocal is the local key an entry gets when it is added: its preferred
// alias, or its own key when there is none or key_mapping is off.
func (s *TSelectSession) newLocal(key string) string {
	if !s.File.KeyMapping {
		return key
	}
	if alias := preferredAlias(key); alias != "" {
		return alias
	}
	return key
}

// toggle turns an entry's tick: on → off (or kept, when its local key is not
// its preferred alias), kept → on (with its old local key), off → on (with its
// preferred alias). It returns the new state, or an error (a local key in use).
func (s *TSelectSession) toggle(key string) (int, error) {
	if ro, err := openReadOnly(); err == nil { // an old key: its entry
		var canonical string
		if ro.QueryRow(`SELECT key FROM key_oldies WHERE alias = ?`, key).Scan(&canonical) == nil {
			key = canonical
		}
		ro.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[key]
	switch {
	case e != nil && e.State == selectOn:
		if s.File.KeyMapping && e.Local != preferredAlias(key) {
			e.State = selectKept
		} else {
			delete(s.entries, key)
			s.removeFromOrder(key)
		}
	case e != nil && e.State == selectKept:
		if other := s.localInUse(e.Local, key); other != "" {
			return e.State, fmt.Errorf("the local key %s is in use for %s", e.Local, other)
		}
		e.State = selectOn
	default:
		local := s.newLocal(key)
		if other := s.localInUse(local, key); other != "" {
			return selectOff, fmt.Errorf("the local key %s is in use for %s", local, other)
		}
		s.entries[key] = &TSelectEntry{Local: local, State: selectOn}
		s.order = append(s.order, key)
	}
	s.changes++
	s.edits++
	if e := s.entries[key]; e != nil {
		return e.State, nil
	}
	return selectOff, nil
}

// localInUse returns the entry (other than key) that has local as its local
// key in the selection, ticked or kept.
func (s *TSelectSession) localInUse(local, key string) string {
	for k, e := range s.entries {
		if k != key && e.Local == local {
			return k
		}
	}
	return ""
}

func (s *TSelectSession) removeFromOrder(key string) {
	for i, k := range s.order {
		if k == key {
			s.order = append(s.order[:i], s.order[i+1:]...)
			return
		}
	}
}

// state is an entry's tick state.
func (s *TSelectSession) state(key string) int {
	if e := s.entries[key]; e != nil {
		return e.State
	}
	return selectOff
}

// save writes the .keys file: the ticked entries in order, then the lines of
// unknown entries; the kept (blue) ones are dropped. The old file is kept as
// .keys.old.
func (s *TSelectSession) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	var order []string
	for _, key := range s.order {
		e := s.entries[key]
		if e == nil {
			continue
		}
		if e.State != selectOn {
			delete(s.entries, key)
			continue
		}
		order = append(order, key)
		if s.File.KeyMapping {
			b.WriteString(e.Local + csvDelimiter + key + "\n")
		} else {
			b.WriteString(key + "\n")
		}
	}
	for _, line := range s.unknown {
		b.WriteString(line + "\n")
	}
	path := s.File.keysPath()
	if FileExists(path) {
		if err := os.Rename(path, path+".old"); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	s.order, s.changes = order, 0
	s.edits++
	s.message = fmt.Sprintf("Saved %s (%d entries) at %s.", filepath.Base(path), len(order), time.Now().Format("15:04:05"))
	fmt.Fprintf(os.Stderr, "%s  select: saved %s (%d entries)\n", time.Now().Format(time.DateTime), path, len(order))
	return nil
}

// selectedPositions are the index positions of the entries ticked or kept.
func (s *TSelectSession) selectedPositions(x *TWebIndex) map[int32]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[int32]bool{}
	for key := range s.entries {
		if i, ok := x.pos[key]; ok {
			out[i] = true
		}
	}
	return out
}

// TSelectInfo is a Select tab as the page sees it.
type TSelectInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Dir      string `json:"dir"`
	Selected int    `json:"selected"`
	Kept     int    `json:"kept"`
	Unknown  int    `json:"unknown"`
	Changes  int    `json:"changes"`
	Message  string `json:"message"`
}

func (s *TSelectSession) info() TSelectInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := TSelectInfo{ID: s.ID, Name: s.File.Name, Dir: tildePath(s.File.Dir), Unknown: len(s.unknown),
		Changes: s.changes, Message: s.message}
	for _, e := range s.entries {
		if e.State == selectOn {
			info.Selected++
		} else {
			info.Kept++
		}
	}
	return info
}

func selectSession(id string) *TSelectSession {
	selectSessionsMu.Lock()
	defer selectSessionsMu.Unlock()
	return selectSessions[id]
}

// handleSelects answers the open Select tabs (GET /api/selects), in the order
// they were opened.
func (w *TWebUI) handleSelects(rw http.ResponseWriter, r *http.Request) {
	selectSessionsMu.Lock()
	list := make([]*TSelectSession, 0, len(selectSessions))
	for _, s := range selectSessions {
		list = append(list, s)
	}
	selectSessionsMu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].opened.Before(list[j].opened) })
	infos := make([]TSelectInfo, 0, len(list))
	for _, s := range list {
		infos = append(infos, s.info())
	}
	writeJSON(rw, infos)
}

// handleSelect acts on a Select tab (POST /api/select/<id>/<action>): toggle
// (key=…), save, sync, close (discard=1 closes with unsaved changes).
func (w *TWebUI) handleSelect(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"+w.token+"/api/select/"), "/")
	s := selectSession(id)
	if s == nil {
		http.Error(rw, "this Select tab is closed", http.StatusNotFound)
		return
	}
	reply := func(extra map[string]any) {
		out := map[string]any{"info": s.info()}
		for k, v := range extra {
			out[k] = v
		}
		writeJSON(rw, out)
	}
	switch action {
	case "toggle":
		state, err := s.toggle(r.FormValue("key"))
		extra := map[string]any{"state": state}
		if err != nil {
			extra["error"] = err.Error()
		}
		s.mu.Lock()
		if e := s.entries[r.FormValue("key")]; e != nil {
			extra["local"] = e.Local
		}
		s.mu.Unlock()
		reply(extra)
	case "save":
		if err := s.save(); err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		reply(nil)
	case "sync":
		if err := s.save(); err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		bibPath := filepath.Join(s.File.Dir, s.File.Name+BibFileExtension)
		before, _ := os.Stat(bibPath)
		selected := s.info().Selected
		s.mu.Lock()
		s.message = "Saved; syncing " + s.File.Name + " (see the Log tab)…" // before the job: it may end at once
		s.mu.Unlock()
		if err := w.service.runWebJob([]string{"-sync", s.File.Name}, s.File.Dir, func(code int) {
			msg := syncOutcome(code, s.File.Name, bibPath, before, selected)
			fmt.Fprintf(os.Stderr, "%s  select: %s\n", time.Now().Format(time.DateTime), msg)
			s.mu.Lock()
			s.message = msg
			s.mu.Unlock()
		}); err != nil {
			http.Error(rw, err.Error(), http.StatusConflict)
			return
		}
		reply(nil)
	case "close":
		if s.info().Changes > 0 && r.FormValue("discard") != "1" {
			http.Error(rw, "unsaved changes", http.StatusConflict)
			return
		}
		selectSessionsMu.Lock()
		delete(selectSessions, id)
		selectSessionsMu.Unlock()
		fmt.Fprintf(os.Stderr, "%s  select tab for %s closed\n", time.Now().Format(time.DateTime), s.File.keysPath())
		rw.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(rw, r)
	}
}

// runWebJob runs a command as a job started from the web UI, in dir, in the
// background (output to the service log and the Log tab; no person to answer
// questions). done gets its exit code. An error: another job runs.
func (s *TService) runWebJob(args []string, dir string, done func(int)) error {
	return s.runWebJobs([][]string{args}, dir, nil, func(codes []int) { done(codes[0]) })
}

// runWebJobs runs commands one after the other as jobs in one go (no other job
// in between), in dir, in the background; their output also goes to out (when
// not nil). done gets their exit codes; a command that fails ends the run (the
// codes stop there).
func (s *TService) runWebJobs(cmds [][]string, dir string, out frameSink, done func([]int)) error {
	if len(cmds) == 0 {
		done(nil)
		return nil
	}
	s.mu.Lock()
	if s.busy || s.stopping.Load() {
		running := s.runningJob()
		s.mu.Unlock()
		return fmt.Errorf("The service is busy (%s); try again when it is done", running)
	}
	job := newJob(jobTitle(cmds[0]), "web UI")
	s.busy, s.ownJob, s.jobStop, s.jobArgs, s.jobFrom = true, false, nil, cmds[0], time.Now()
	s.job = job
	s.mu.Unlock()
	var sink frameSink = logSink{}
	if out != nil {
		sink = teeSink{logSink{}, out}
	}
	go func() {
		var codes []int
		for _, args := range cmds {
			s.mu.Lock()
			s.jobArgs = args
			s.mu.Unlock()
			fmt.Fprintf(os.Stderr, "%s  job (web UI): %s (in %s)\n", time.Now().Format(time.DateTime), strings.Join(args, " "), dir)
			req := serviceFrame{T: "run", Args: append([]string{"-base", libraryBase()}, args...), Version: AppVersion,
				Base: libraryBase(), Cwd: dir}
			sio := &TSocketIO{out: sink, lines: make(chan string), job: job}
			sio.endInput()
			code := 2
			if positional, ok := parseJobArguments(req.Args); ok {
				s.mu.Lock()
				s.jobStop = func() { job.withdraw(); sio.interrupt() }
				s.mu.Unlock()
				deferServicePush = true
				cwd, _ := os.Getwd()
				if err := os.Chdir(dir); err == nil {
					code = s.runJobInService(req, positional, sio)
					os.Chdir(cwd) //nolint:errcheck
				} else {
					fmt.Fprintf(os.Stderr, "%s  cannot use the folder %s: %s\n", time.Now().Format(time.DateTime), dir, err)
				}
			}
			sio.finished.Store(true)
			fmt.Fprintf(os.Stderr, "%s  job (web UI) ended with code %d\n", time.Now().Format(time.DateTime), code)
			codes = append(codes, code)
			if code != 0 {
				break
			}
		}
		if readLibraryUnpushed() != nil {
			s.schedulePush()
		}
		s.mu.Lock()
		s.busy, s.jobStop, s.job, s.lastActive = false, nil, nil, time.Now()
		s.mu.Unlock()
		job.finish(codes[len(codes)-1])
		done(codes)
	}()
	return nil
}

// teeSink sends a job's output to two sinks.
type teeSink struct{ a, b frameSink }

func (t teeSink) send(f serviceFrame) error {
	t.a.send(f) //nolint:errcheck
	return t.b.send(f)
}

// collectSink keeps a job's messages (for a reply to the page).
type collectSink struct {
	mu sync.Mutex
	b  strings.Builder
}

func (c *collectSink) send(f serviceFrame) error {
	if f.T != "out" && f.T != "err" && f.T != "info" {
		return nil
	}
	c.mu.Lock()
	c.b.WriteString(f.D)
	c.mu.Unlock()
	return nil
}

func (c *collectSink) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return reANSIEscape.ReplaceAllString(c.b.String(), "")
}

// version counts the session's changes (and saves, which drop the kept ones).
func (s *TSelectSession) version() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.edits
}

// syncOutcome says what a sync from a Select tab did: the .bib written (its
// entries: the selected ones and the crossref parents they bring along), left
// as it was, or the exit code. before is the .bib's state before the sync.
func syncOutcome(code int, name, bibPath string, before os.FileInfo, selected int) string {
	at := time.Now().Format("15:04:05")
	if code != 0 {
		return fmt.Sprintf("The sync of %s ended with code %d at %s; see the Log tab.", name, code, at)
	}
	after, err := os.Stat(bibPath)
	if err != nil {
		return fmt.Sprintf("Synced %s at %s.", name, at)
	}
	data, _ := os.ReadFile(bibPath)
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "@") && !strings.HasPrefix(strings.ToLower(line), "@comment") &&
			!strings.HasPrefix(strings.ToLower(line), "@string") && !strings.HasPrefix(strings.ToLower(line), "@preamble") {
			n++
		}
	}
	what := "written"
	if before != nil && after.ModTime().Equal(before.ModTime()) && after.Size() == before.Size() {
		what = "unchanged"
	}
	detail := fmt.Sprintf("%d entries", n)
	if parents := n - selected; parents > 0 {
		plural := "s"
		if parents == 1 {
			plural = ""
		}
		detail = fmt.Sprintf("%d entries: %d selected, %d crossref parent%s", n, selected, parents, plural)
	}
	return fmt.Sprintf("%s %s at %s (%s).", filepath.Base(bibPath), what, at, detail)
}

// handleSelState answers an entry's tick in each open Select tab (GET
// /api/selstate?key=…; an old key counts as its entry), for its pop-up.
func (w *TWebUI) handleSelState(rw http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if ro, err := openReadOnly(); err == nil {
		var canonical string
		if ro.QueryRow(`SELECT key FROM key_oldies WHERE alias = ?`, key).Scan(&canonical) == nil {
			key = canonical
		}
		ro.Close()
	}
	selectSessionsMu.Lock()
	list := make([]*TSelectSession, 0, len(selectSessions))
	for _, s := range selectSessions {
		list = append(list, s)
	}
	selectSessionsMu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].opened.Before(list[j].opened) })
	type state struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Dir   string `json:"dir"`
		Key   string `json:"key"`
		State int    `json:"state"`
	}
	out := []state{}
	for _, s := range list {
		s.mu.Lock()
		out = append(out, state{s.ID, s.File.Name, tildePath(s.File.Dir), key, s.state(key)})
		s.mu.Unlock()
	}
	writeJSON(rw, out)
}

// handleSelStates answers the ticks of entries in one Select tab (GET
// /api/selstates?sel=<id>&keys=k1,k2,…), for an author's pop-up.
func (w *TWebUI) handleSelStates(rw http.ResponseWriter, r *http.Request) {
	s := selectSession(r.URL.Query().Get("sel"))
	if s == nil {
		http.Error(rw, "this Select tab is closed", http.StatusNotFound)
		return
	}
	out := map[string]int{}
	s.mu.Lock()
	for _, key := range strings.Split(r.URL.Query().Get("keys"), ",") {
		if key != "" {
			out[key] = s.state(key)
		}
	}
	s.mu.Unlock()
	writeJSON(rw, out)
}
