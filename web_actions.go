package main

// The Actions tab (plan step 22.3d, Erik 2026-11-24; first named Dialogue): jobs started from the
// web UI (action buttons: check, homework, ...) and the service's own load of
// the library run here, their output shown as on a terminal, their questions
// answered in the page (a button per option, or typed). A job from the command
// line keeps asking on its terminal.
//
// One job at a time (the service runs no more): the last one
// stays shown until the next starts.

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// TActionsJob is the job the Actions tab shows, and the questions answered in it. Its
// status, open question and results are the job's (service_dialogue.go); its log
// text goes to the service log.
type TActionsJob struct {
	mu      sync.Mutex
	title   string
	running bool
	code    int
	job     *TJob
	started time.Time
	history []TActionsReply // the questions answered in this job
}

// TActionsReply is a question answered in the Actions tab.
type TActionsReply struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	At       string `json:"at"`
}

var actionsTab = &TActionsJob{}

func (d *TActionsJob) start(title string, job *TJob) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.title, d.job, d.running, d.code, d.started, d.history = title, job, true, 0, time.Now(), nil
}

// show shows a command line's job that asks something; it tells whether it was not
// shown yet.
func (d *TActionsJob) show(job *TJob) bool {
	d.mu.Lock()
	shown := d.job == job
	d.mu.Unlock()
	if !shown {
		d.start(job.title, job)
	}
	return !shown
}

// shows tells whether a job is shown.
func (d *TActionsJob) shows() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.job != nil
}

// finishIf is finish, when job is the one shown.
func (d *TActionsJob) finishIf(job *TJob, code int) {
	d.mu.Lock()
	shown := d.job == job
	d.mu.Unlock()
	if shown {
		d.finish(code)
	}
}

func (d *TActionsJob) finish(code int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running, d.code = false, code
}

// Asking tells whether the Actions tab's job waits for an answer.
func (d *TActionsJob) Asking() bool {
	d.mu.Lock()
	job := d.job
	d.mu.Unlock()
	return job.isAsking()
}

// answered notes that the job shown got its answer a to q: the log gets the issue
// and the action taken (the question itself is no log line).
func (d *TActionsJob) answered(q *TQuestion, a TAnswer) {
	shown := a.line()
	if v := a.value(); v != "" {
		shown += " " + v
	}
	d.mu.Lock()
	d.history = append(d.history, TActionsReply{Question: q.Text, Answer: shown, At: time.Now().Format("15:04:05")})
	d.mu.Unlock()
	for _, line := range summariseAnswer(q, a) {
		fmt.Fprintln(os.Stderr, line)
	}
}

// actionsSink is the frame sink of the Actions tab's job: its log text (and
// output) goes to the service log. Its dialogue frames are taken by its TJob.
type actionsSink struct{}

func (actionsSink) send(f serviceFrame) error {
	if f.T == "out" || f.T == "err" || f.T == "info" {
		os.Stderr.WriteString(f.D) //nolint:errcheck
	}
	return nil
}

// runActionsJob runs commands one after the other as the Actions tab's job, in
// the background, with questions answered in the page. own: the service's own
// job (a command from the command line waits for it rather than being
// refused). done (when not nil) gets the last exit code.
func (s *TService) runActionsJob(title string, cmds [][]string, own bool, done func(int), dir ...string) error {
	s.mu.Lock()
	if s.busy || s.stopping.Load() {
		running := s.runningJob()
		s.mu.Unlock()
		return fmt.Errorf("The service is busy (%s); try again when it is done", running)
	}
	s.busy, s.ownJob, s.jobStop, s.jobArgs, s.jobFrom = true, own, nil, []string{"(" + title + ")"}, time.Now()
	s.ownWhat = strings.ToLower(title[:1]) + title[1:]
	from := "web UI"
	if own {
		from = "service"
	} else if len(dir) > 0 {
		from = "command line"
	}
	job := newJob(title, from)
	s.job = job
	s.mu.Unlock()
	run := func() {
		code := 0
		for _, args := range cmds {
			sio := &TSocketIO{out: actionsSink{}, interactive: true, lines: make(chan string, 16), job: job}
			setAnswerValue("")
			job.setReply(sio.answer)
			if own && actionsTab.shows() {
				job.onAsk = func() { actionsTab.show(job) } // a reload leaves the last job's result, until it asks
			} else {
				actionsTab.start(title, job)
			}
			fmt.Fprintf(os.Stderr, "%s  job (Actions tab): %s\n", time.Now().Format(time.DateTime), strings.Join(append([]string{title + ":"}, args...), " "))
			req := serviceFrame{T: "run", Args: append([]string{"-base", libraryBase()}, args...), Version: AppVersion, Base: libraryBase()}
			flag.CommandLine.SetOutput(flagOutput{sio})
			code = 2
			positional, ok := parseJobArguments(req.Args)
			if ok && !runsInService() {
				// A command that runs as a process of its own (-update_dblp, ...): its output
				// and questions relayed (runActionsChild).
				childDir := ""
				if len(dir) > 0 {
					childDir = dir[0]
				}
				code = s.runActionsChild(req.Args, childDir, job)
				ok = false
			}
			if ok {
				req.Cwd, _ = os.Getwd()
				s.mu.Lock()
				s.jobStop = func() { job.withdraw(); sio.interrupt() }
				s.mu.Unlock()
				deferServicePush = len(args) == 0 || args[0] != "-push" // -push pushes now
				if len(dir) > 0 && dir[0] != "" {                       // a job from a command line: its folder
					cwd, _ := os.Getwd()
					if os.Chdir(dir[0]) == nil {
						req.Cwd = dir[0]
						code = s.runJobInService(req, positional, sio)
						os.Chdir(cwd) //nolint:errcheck
					}
				} else {
					code = s.runJobInService(req, positional, sio)
				}
			}
			sio.finished.Store(true)
			sio.endInput()
			fmt.Fprintf(os.Stderr, "\n[ended with code %d]\n", code)
			actionsTab.finishIf(job, code)
			if code != 0 {
				break
			}
		}
		if readLibraryUnpushed() != nil {
			s.schedulePush()
		}
		s.mu.Lock()
		s.busy, s.ownJob, s.jobStop, s.job, s.lastActive = false, false, nil, nil, time.Now()
		s.mu.Unlock()
		job.finish(code)
		if done != nil {
			done(code)
		}
	}
	if own {
		run() // the caller (loadAtStart) waits for it
	} else {
		go run()
	}
	return nil
}

// actionFromCommandLine: the commands that, asked for at a terminal, run in the
// Actions tab (command name → title).
var actionFromCommandLine = map[string]string{
	"check": "Check", "fix_candidates": "Fix DBLP candidates",
	"fix_duplicates": "Fix duplicates", "enrich_contributor_data": "Enrich contributor data",
	"merge_entries": "Merge entries", "sync": "Sync", "harvest": "Harvest",
}

// TWebAction is an action the web UI can start: its title (the button's name),
// its command, and what it is given (the names of its arguments, for a refusal).
type TWebAction struct {
	Title string
	Args  []string
	Given []string
}

// webActions are the actions: name → command.
var webActions = map[string]TWebAction{
	"check":           {"Check", []string{}, nil},
	"fix_candidates":  {"DBLP candidates", []string{"-fix_candidates"}, nil},
	"fix_duplicates":  {"Fix duplicates", []string{"-fix_duplicates"}, nil},
	"align_countries": {"Country names in titles", []string{"-align_booktitle_countries"}, nil},
	"enrich":          {"Enrich contributors", []string{"-enrich_contributor_data"}, nil},
	"enrich_matched":  {"Enrich, matched ORCIDs only", []string{"-matched_orcid_data_only", "-enrich_contributor_data"}, nil},
	"refresh_orcid":   {"Refresh ORCID", []string{"-update_orcid"}, nil},
	"merge_orcid":     {"Merge ORCID duplicates", []string{"-merge_orcid_duplicates"}, nil},
	"disambiguate":    {"Disambiguate contributors", []string{"-disambiguate_contributors"}, nil},
	"triage":          {"Triage contributor mappings", []string{"-triage_author_mappings"}, nil},
	"triage_aliases":  {"Triage contributor aliases", []string{"-triage_contributor_aliases"}, nil},
	"check_pdfs":      {"Check PDFs", []string{"-check_pdfs"}, nil},
	"get_pdfs":        {"Get PDFs from URLs", []string{"-get_pdfs"}, nil},
	"get_arxiv":       {"Get PDFs from arXiv", []string{"-get_eprint_pdfs", "arXiv"}, nil},
	"get_pdf":         {"Download PDF", []string{"-get_pdf"}, []string{"an entry's key"}},
	"add_failed":      {"Add failed URLs to Bad URLs", []string{"-add_failed_urls"}, nil},
	"update_dblp":     {"Update DBLP", []string{"-update_dblp"}, nil},
	"update_entries":  {"Update all DBLP entries", []string{"-update_all_dblp_entries"}, nil},
	"watch":           {"Watch", []string{"-watch"}, nil},
	"entry_actions":   {"Entry actions", []string{"-do_entry_actions"}, nil},
	"audit":           {"Audit mappings", []string{"-audit_mappings"}, nil},
	"push":            {"Push", []string{"-push"}, nil},
	"add_dblp":        {"Add DBLP entry", []string{"-add_dblp_entry"}, []string{"a DBLP key"}},
	"check_entry":     {"Check entry", []string{"-fix_entries"}, []string{"an entry's key"}},
	"delete_entry":    {"Delete entry", []string{"-delete_entry"}, []string{"an entry's key"}},
	"split_position":  {"Someone else", []string{"-split_position"}, []string{"an entry's key", "author or editor", "a position"}},
	"correct_name":    {"Correct name", []string{"-correct_name"}, []string{"the wrong name", "the correct name"}},
}

// givenArguments are an action's arguments, as a page gives them (JSON).
func givenArguments(args ...any) string {
	given := make([]string, len(args))
	for i, a := range args {
		given[i] = fmt.Sprint(a)
	}
	out, _ := json.Marshal(given)
	return string(out)
}

// command is the action's command line with what it is given.
func (a TWebAction) command(given []string) ([]string, error) {
	if len(given) != len(a.Given) {
		return nil, fmt.Errorf("%s: give %s", a.Title, strings.Join(a.Given, ", "))
	}
	args := append([]string{}, a.Args...)
	for i, g := range given {
		g = strings.TrimSpace(g)
		if g == "" || strings.HasPrefix(g, "-") || strings.ContainsAny(g, "\r\n") {
			return nil, fmt.Errorf("%s: %q is not %s", a.Title, g, a.Given[i])
		}
		args = append(args, g)
	}
	return args, nil
}

// handleActions answers what the Actions tab shows (GET /api/actions: the job, its
// status, question, results), or acts on it (POST
// /api/actions/start {action, given}). Answers and stop: /api/job/<id>/… (service_dialogue.go).
func (w *TWebUI) handleActions(rw http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, "/"+w.token+"/api/actions")
	action = strings.TrimPrefix(action, "/")
	if r.Method == http.MethodGet && action == "" {
		d := actionsTab
		d.mu.Lock()
		out := map[string]any{
			"title": d.title, "running": d.running, "asking": false, "code": d.code,
			"since": d.started.Format("15:04:05"), "history": append([]TActionsReply{}, d.history...),
		}
		if d.job != nil {
			out["job"], out["status"] = d.job.id, d.job.currentStatus()
			out["warnings"] = d.job.lastResults(200, "warning", "error")
			out["outcomes"] = d.job.lastResults(200, "outcome")
			out["tables"] = d.job.lastResults(20, "table")
			if q := d.job.openQuestion(); q != nil {
				out["asking"], out["q"] = true, q
			}
		}
		d.mu.Unlock()
		writeJSON(rw, out)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(rw, "not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Action string   `json:"action"`
		Given  []string `json:"given"` // the action's arguments
	}
	json.NewDecoder(http.MaxBytesReader(rw, r.Body, 1<<16)).Decode(&req) //nolint:errcheck
	switch action {
	case "start":
		a, ok := webActions[req.Action]
		if !ok {
			http.Error(rw, "no such action", http.StatusBadRequest)
			return
		}
		args, err := a.command(req.Given)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		title := a.Title
		if len(req.Given) > 0 {
			title += " " + strings.Join(req.Given, " ")
		}
		if err := w.service.runActionsJob(title, [][]string{args}, false, nil); err != nil {
			http.Error(rw, err.Error(), http.StatusConflict)
			return
		}
		rw.WriteHeader(http.StatusAccepted)
	default:
		http.NotFound(rw, r)
	}
}

// handleDBLPSearch looks entries up in the local DBLP store, for a question's
// "enter key manually" (GET /api/dblpsearch?q=…&author=…): q a DBLP key (or part
// of one), else a title (as the store indexes titles: the whole title,
// normalised); author an author's name as DBLP writes it ("Judi Romijn"), the
// words of q then filtering their entries. At most 40 results.
func (w *TWebUI) handleDBLPSearch(rw http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	author := strings.TrimSpace(r.URL.Query().Get("author"))
	var keys []string
	seen := map[string]bool{}
	add := func(rows *sql.Rows, err error) {
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if rows.Scan(&k) == nil && !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	stmt := func(name string) *sql.Stmt { return dblpStmt(name) }
	if author != "" {
		if s := stmt("person"); s != nil {
			add(s.Query(author))
		}
	} else if strings.Contains(q, "/") {
		if s := stmt("search"); s != nil {
			add(s.Query(q, 40))
		}
	} else if q != "" {
		if s := stmt("title"); s != nil {
			add(s.Query(libraryTitleHash(q)))
		}
	}
	words := strings.Fields(foldText(q))
	type result struct {
		Key    string            `json:"key"`
		Fields map[string]string `json:"fields"`
	}
	out := []result{}
	for _, k := range keys {
		e := dblpEntryFromStore(k)
		if e == nil {
			continue
		}
		if author != "" && len(words) > 0 {
			hay := foldText(e.Fields["title"])
			match := true
			for _, w := range words {
				if !strings.Contains(hay, w) {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		}
		fields := map[string]string{}
		for _, f := range []string{"entrytype", "title", "author", "editor", "booktitle", "journal", "year", "volume", "number", "pages", "doi", "crossref"} {
			if v := e.Fields[f]; v != "" {
				fields[f] = texToText(v)
			}
		}
		out = append(out, result{k, fields})
		if len(out) >= 40 {
			break
		}
	}
	writeJSON(rw, out)
}

// ---- the numbers on the action buttons ----

// actionCounts are the cases waiting for an action (Erik, 2026-11-24), as the
// homework summary of a check counts them: entries with unresolved DBLP
// candidates, title groups with unresolved duplicates, contributors with an ORCID
// not yet enriched, author/editor values awaiting triage. Worked out in the
// background when the library is loaded and no job runs, again after a change.
var (
	actionCountsMu      sync.Mutex
	actionCountsValue   map[string]int
	actionCountsVersion int64 = -1
)

func currentActionCounts() map[string]int {
	actionCountsMu.Lock()
	defer actionCountsMu.Unlock()
	return actionCountsValue
}

// refreshActionCounts works the numbers out when the search index (that is, the
// database) changed since they were; a job arriving meanwhile waits for it.
func refreshActionCounts(busy bool) {
	version := webIndexVersion.Load()
	actionCountsMu.Lock()
	stale := version != actionCountsVersion
	actionCountsMu.Unlock()
	if busy || !stale || serviceLibraryLevel == libraryNotLoaded {
		return
	}
	if !libraryLock.TryRLock() {
		return
	}
	counts := map[string]int{}
	func() {
		defer libraryLock.RUnlock()
		defer func() { recover() }() //nolint:errcheck // a count that needs more than the loaded library is left out
		if entryCache == nil {
			return
		}
		counts["fix_candidates"] = countDblpCandidates()
		counts["fix_duplicates"] = countUnresolvedGroups()
	}()
	if ro, err := openReadOnly(); err == nil {
		var n int
		if ro.QueryRow(`SELECT COUNT(*) FROM superseded_field_values WHERE field IN ('author', 'editor') AND triage_status IS NULL`).Scan(&n) == nil {
			counts["triage"] = n
		}
		var seen int
		ro.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='contributor_orcid_seen'`).Scan(&seen) //nolint:errcheck
		if seen > 0 && ro.QueryRow(`SELECT COUNT(*) FROM contributors WHERE orcid != '' AND NOT EXISTS
			(SELECT 1 FROM contributor_orcid_seen s WHERE s.contributor_id = contributors.id AND s.canonical != '')`).Scan(&n) == nil {
			counts["enrich"] = n
		}
		ro.Close()
	}
	actionCountsMu.Lock()
	actionCountsValue, actionCountsVersion = counts, version
	actionCountsMu.Unlock()
}

// summariseAnswer is what the log says about a question answered in the Actions
// tab (Erik, 2026-11-24), instead of the question's context (the entries, the
// candidates): "ISSUE: …" (its warning) or "CHALLENGE: …" (with the current and
// challenging values), then "ACTION: …" (what the answer does). Made from the
// question message.
func summariseAnswer(q *TQuestion, a TAnswer) []string {
	answer := a.line()
	var out []string
	value := func(role string) string {
		for _, v := range q.Values {
			if v.Role == role {
				return v.Value
			}
		}
		return ""
	}
	switch {
	case q.Topic == "field_challenge" || q.Topic == "name_pair":
		head := "For entry " + q.Key + " and field " + q.Field
		if q.Topic == "name_pair" && q.Warning != "" {
			head = strings.TrimSuffix(strings.SplitN(q.Warning, "\n", 2)[0], ":")
		}
		out = append(out, "CHALLENGE: "+head, "- Current   : "+value("current"), "- Challenger: "+value("challenger"))
	case q.Warning != "":
		head := strings.SplitN(q.Warning, "\n", 2)[0]
		if i := strings.Index(head, " — "); i > 0 {
			head = head[:i]
		}
		out = append(out, "ISSUE: "+head)
	}
	var chosen *TQuestionOption
	for i := range q.Options {
		if q.Options[i].Key == answer {
			chosen = &q.Options[i]
		}
	}
	action := ""
	f := q.Field
	if f == "" {
		f = "value"
	}
	switch {
	case answer == "q":
		action = "Stopped"
	case q.Kind == "text" && strings.EqualFold(q.Text, "DBLP key"):
		action = "Entered DBLP key " + answer
	case q.Kind == "text":
		action = "Entered: " + answer
	case a.value() != "":
		action = "Entered " + chosen.Value + ": " + a.value()
	case chosen != nil && chosen.Entry != nil:
		action = "Selected " + chosen.Entry.Key
	case q.Topic == "dblp_candidate" && answer == "0":
		action = "No DBLP entry matches"
	case q.Topic == "library_candidate" && answer == "0":
		action = "No library entry matches"
	case q.Topic == "dblp_candidate" && answer == "k":
		action = "Looking up the DBLP key"
	case q.Topic == "merge":
		action = map[string]string{"y": "Merged", "n": "Kept apart"}[answer]
	case q.Topic == "name_pair":
		action = map[string]string{"Y": "Kept the current name, everywhere", "y": "Kept the current name, in this entry",
			"N": "Took the challenger's name, everywhere", "n": "Took the challenger's name, in this entry",
			"e": "Editing the canonical name", "d": "Different people"}[answer]
	case q.Topic == "field_challenge":
		action = map[string]string{"y": "Kept the current " + f, "Y": "Kept the current " + f + ", for all entries",
			"n": "Took the challenger's " + f, "N": "Took the challenger's " + f + ", for all entries",
			"e": "Editing the " + f, "b": "Breaking it down by name"}[answer]
	case chosen != nil && chosen.Label != "":
		action = "Answered " + answer + " (" + chosen.Label + ")"
	}
	if action == "" {
		action = "Answered " + answer
	}
	return append(out, "ACTION: "+action)
}

// runActionsChild runs a command that does not run in the service (a process of
// its own: -update_dblp, -harvest, ...) as the Actions tab's job. It speaks the
// same frames as a job in the service (TFrameIO): its output goes through the
// dialogue, its questions are put to the page, the answers go back as frames.
func (s *TService) runActionsChild(args []string, dir string, job *TJob) int {
	c, err := startFrameChild(args, dir, true, func(_, d string) { os.Stderr.WriteString(d) }) //nolint:errcheck
	if err != nil {
		fmt.Fprintf(os.Stderr, "The service cannot run the job: %s\n", err)
		return 1
	}
	s.mu.Lock()
	s.jobStop = func() {
		c.interrupt()
		if job.withdraw() { // a question waiting: answered "q", as a Ctrl-C would
			c.answer("q\n", "")
		}
	}
	s.mu.Unlock()
	job.setReply(func(a TAnswer) { c.answer(a.line()+"\n", a.value()) })
	return c.run(func(f serviceFrame) {
		actionsSink{}.send(f) //nolint:errcheck
		job.take(f)
	})
}
