package main

// The Actions tab (plan step 22.3d, Erik 2026-11-24; first named Dialogue): jobs started from the
// web UI (action buttons: check, homework, ...) and the service's own load of
// the library run here, their output shown as on a terminal, their questions
// answered in the page (a button per option, or typed). A job from the command
// line keeps asking on its terminal.
//
// One dialogue at a time (the service runs one job at a time): the last one
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

// TDialogue is the job of the Actions tab: its output, and whether it waits
// for an answer.
type TDialogue struct {
	mu       sync.Mutex
	title    string
	lines    []string
	ended    int // lines ended so far (the kept ones are the last)
	current  strings.Builder
	running  bool
	code     int
	asking   bool
	sio      *TSocketIO
	reply    func(string) // gives an answer to the job: a line for its questions (in the service, or a child's stdin)
	started  time.Time
	since    int              // the line number after the last answer: a question's context starts there
	notes    []TDialogueNote  // the warnings of this job (shown in the tab)
	pending  []string         // finished lines not in the log yet: a question's context stays out of it
	settled  int              // pending lines up to the job's last message (step, progress, warning): not a question's context
	step     string           // the current step ("Opening the library:", "Step 3: …")
	substep  string           // the last sub-step line (indented) after it
	history  []TDialogueReply // the questions answered in this job
	question *TQuestion       // the question the job asks (its "question" frame), until answered
}

// flushLog writes the pending lines to the log: those up to the job's last
// message (all: force; the job ended). The lines after it may still be the
// context of a question (the entries, the candidates), printed right before it
// asks: the log then gets the issue and the action taken instead (Erik, 2026-11-24).
func (d *TDialogue) flushLog(force bool) {
	d.mu.Lock()
	n := d.settled
	if force {
		n = len(d.pending)
	}
	lines := d.logLines(n)
	d.mu.Unlock()
	for _, l := range lines {
		fmt.Fprintln(os.Stderr, l)
	}
}

// logLines takes the first n pending lines off for the log (called with d.mu held).
func (d *TDialogue) logLines(n int) []string {
	lines := append([]string{}, d.pending[:n]...)
	d.pending = append([]string{}, d.pending[n:]...)
	d.settled -= n
	if d.settled < 0 {
		d.settled = 0
	}
	return lines
}

// TDialogueNote is a warning: its first line and the lines that belong to it
// ("- Current : …", "needs to be resolved").
type TDialogueNote struct {
	Head  string   `json:"head"`
	Lines []string `json:"lines"`
}

// TDialogueReply is a question answered in the Actions tab.
type TDialogueReply struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	At       string `json:"at"`
}

const dialogueLines = 5000

var dialogue = &TDialogue{}

func (d *TDialogue) start(title string, sio *TSocketIO) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.title, d.sio, d.running, d.asking, d.code, d.started = title, sio, true, false, 0, time.Now()
	d.reply = nil
	if sio != nil {
		d.reply = func(text string) {
			sio.asked.Store(false)
			select {
			case sio.lines <- text:
			default:
			}
		}
	}
	d.lines, d.ended, d.since, d.notes, d.history, d.step, d.substep = nil, 0, 0, nil, nil, "", ""
	d.pending, d.settled = nil, 0
	d.question = nil
	d.current.Reset()
}

func (d *TDialogue) finish(code int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running, d.asking, d.code, d.sio, d.reply, d.question = false, false, code, nil, nil, nil
}

// write adds output: "\r" restarts the current line (a progress line shows its
// latest state), "\n" ends it.
func (d *TDialogue) write(s string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range reANSIEscape.ReplaceAllString(s, "") {
		switch r {
		case '\r':
			d.current.Reset()
		case '\n':
			line := d.current.String()
			d.pending = append(d.pending, line)
			d.lines = append(d.lines, line)
			d.ended++
			d.current.Reset()
			if len(d.lines) > dialogueLines {
				d.lines = d.lines[len(d.lines)-dialogueLines:]
			}
		default:
			d.current.WriteRune(r)
		}
	}
}

func (d *TDialogue) setAsking(asking bool) {
	d.mu.Lock()
	d.asking = asking
	d.mu.Unlock()
}

// Asking tells whether the dialogue's job waits for an answer.
func (d *TDialogue) Asking() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.asking
}

// dialogueSink is the frame sink of a dialogue's job.
type dialogueSink struct{}

func (dialogueSink) send(f serviceFrame) error {
	switch f.T {
	case "out", "err", "info":
		dialogue.write(f.D)
	case "question":
		dialogue.mu.Lock()
		dialogue.question = f.Q
		dialogue.mu.Unlock()
	case "warning", "error", "step", "progress":
		dialogue.message(f)
	case "ask":
		dialogue.mu.Lock()
		dialogueSink{}.markAsking()
		dialogue.mu.Unlock()
	}
	return nil
}

// message takes a message of the job (see messageSender): a warning or error
// (the Warnings card), the step it begins, its progress. The output so far is
// then no question's context: it goes to the log.
func (d *TDialogue) message(f serviceFrame) {
	d.mu.Lock()
	switch f.T {
	case "warning", "error":
		head, rest, _ := strings.Cut(strings.TrimSpace(f.D), "\n")
		if f.T == "error" {
			head = "Error: " + head
		}
		note := TDialogueNote{Head: head}
		if rest != "" {
			note.Lines = strings.Split(rest, "\n")
		}
		d.notes = append(d.notes, note)
	case "step":
		d.step, d.substep = f.D, ""
	case "progress":
		d.substep, _, _ = strings.Cut(f.D, "\n")
	}
	d.settled = len(d.pending)
	lines := d.logLines(d.settled)
	d.mu.Unlock()
	for _, l := range lines {
		fmt.Fprintln(os.Stderr, l)
	}
}

// markAsking marks the dialogue as asking (called with dialogue.mu held): the
// output since the job's last message is the question's context (shown in its
// pop-up from the question message) and stays out of the log; what came before
// goes there.
func (dialogueSink) markAsking() {
	for _, l := range dialogue.logLines(dialogue.settled) {
		fmt.Fprintln(os.Stderr, l)
	}
	dialogue.pending, dialogue.settled = nil, 0
	dialogue.asking = true
}

// runDialogueJob runs commands one after the other as the dialogue's job, in
// the background, with questions answered in the page. own: the service's own
// job (a command from the command line waits for it rather than being
// refused). done (when not nil) gets the last exit code.
func (s *TService) runDialogueJob(title string, cmds [][]string, own bool, done func(int), dir ...string) error {
	s.mu.Lock()
	if s.busy || s.stopping.Load() {
		running := s.runningJob()
		s.mu.Unlock()
		return fmt.Errorf("The service is busy (%s); try again when it is done", running)
	}
	s.busy, s.ownJob, s.jobStop, s.jobArgs, s.jobFrom = true, own, nil, []string{"(" + title + ")"}, time.Now()
	s.ownWhat = strings.ToLower(title[:1]) + title[1:]
	s.mu.Unlock()
	run := func() {
		code := 0
		for _, args := range cmds {
			sio := &TSocketIO{out: dialogueSink{}, interactive: true, lines: make(chan string, 16)}
			dialogue.start(title, sio)
			flushDone := make(chan struct{})
			go func() {
				for {
					select {
					case <-flushDone:
						return
					case <-time.After(150 * time.Millisecond):
						dialogue.flushLog(false)
					}
				}
			}()
			fmt.Fprintf(os.Stderr, "%s  job (Actions tab): %s\n", time.Now().Format(time.DateTime), strings.Join(append([]string{title + ":"}, args...), " "))
			req := serviceFrame{T: "run", Args: append([]string{"-base", libraryBase()}, args...), Version: AppVersion, Base: libraryBase()}
			flag.CommandLine.SetOutput(flagOutput{sio})
			code = 2
			positional, ok := parseJobArguments(req.Args)
			if ok && !runsInService() {
				// A command that runs as a process of its own (-update_dblp, ...): its output
				// and questions relayed (runDialogueChild).
				childDir := ""
				if len(dir) > 0 {
					childDir = dir[0]
				}
				code = s.runDialogueChild(req.Args, childDir)
				ok = false
			}
			if ok {
				req.Cwd, _ = os.Getwd()
				s.mu.Lock()
				s.jobStop = sio.interrupt
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
			close(flushDone)
			dialogue.write(fmt.Sprintf("\n[ended with code %d]\n", code))
			dialogue.flushLog(true)
			dialogue.finish(code)
			if code != 0 {
				break
			}
		}
		if readLibraryUnpushed() != nil {
			s.schedulePush()
		}
		s.mu.Lock()
		s.busy, s.ownJob, s.jobStop, s.lastActive = false, false, nil, time.Now()
		s.mu.Unlock()
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

// dialogueActions are the action buttons: name → command.
var dialogueActions = map[string]struct {
	Title string
	Args  []string
}{
	"check":          {"Check", []string{}},
	"fix_candidates": {"Fix DBLP candidates", []string{"-fix_candidates"}},
	"fix_duplicates": {"Fix duplicates", []string{"-fix_duplicates"}},
	"enrich":         {"Enrich contributor data", []string{"-enrich_contributor_data"}},
	"triage":         {"Triage author mappings", []string{"-triage_author_mappings"}},
	"check_pdfs":     {"Check PDFs", []string{"-check_pdfs"}},
	"get_pdfs":       {"Get PDFs", []string{"-get_pdfs"}},
	"add_failed":     {"Add failed URLs to Bad URLs", []string{"-add_failed_urls"}},
	"update_dblp":    {"Update DBLP", []string{"-update_dblp"}},
	"update_entries": {"Update all DBLP entries", []string{"-update_all_dblp_entries"}},
	"watch":          {"Watch", []string{"-watch"}},
	"entry_actions":  {"Entry actions", []string{"-do_entry_actions"}},
	"audit":          {"Audit mappings", []string{"-audit_mappings"}},
	"push":           {"Push", []string{"-push"}},
}

// handleDialogue answers the dialogue (GET /api/dialogue?from=N: its lines
// after N, whether it runs, asks, the options), or acts on it (POST
// /api/dialogue/start {action}, /answer {text}, /stop).
func (w *TWebUI) handleDialogue(rw http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, "/"+w.token+"/api/dialogue")
	action = strings.TrimPrefix(action, "/")
	if r.Method == http.MethodGet && action == "" {
		from := 0
		fmt.Sscan(r.URL.Query().Get("from"), &from) //nolint:errcheck
		d := dialogue
		d.mu.Lock()
		first := d.ended - len(d.lines)
		if from < first || from > d.ended {
			from = first
		}
		out := map[string]any{
			"title": d.title, "running": d.running, "asking": d.asking, "code": d.code,
			"lines": append([]string{}, d.lines[from-first:]...), "next": d.ended, "current": d.current.String(),
			"since": d.started.Format("15:04:05"),
			"notes": append([]TDialogueNote{}, d.notes...), "history": append([]TDialogueReply{}, d.history...),
			"step": d.step, "substep": d.substep,
		}
		if d.asking {
			out["q"] = d.question // nil: a line is asked for without a question message
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
		Action string `json:"action"`
		Text   string `json:"text"`
		Arg    string `json:"arg"` // add_dblp: the DBLP key
	}
	json.NewDecoder(http.MaxBytesReader(rw, r.Body, 1<<16)).Decode(&req) //nolint:errcheck
	switch action {
	case "start":
		if req.Action == "add_dblp" { // the Add section: a DBLP entry by its key
			key := strings.TrimSpace(req.Arg)
			if key == "" || strings.ContainsAny(key, " \t") {
				http.Error(rw, "give a DBLP key, e.g. conf/er/SmithJ21", http.StatusBadRequest)
				return
			}
			if err := w.service.runDialogueJob("Add DBLP entry "+key, [][]string{{"-add_dblp_entry", key}}, false, nil); err != nil {
				http.Error(rw, err.Error(), http.StatusConflict)
				return
			}
			rw.WriteHeader(http.StatusAccepted)
			return
		}
		a, ok := dialogueActions[req.Action]
		if !ok {
			http.Error(rw, "no such action", http.StatusBadRequest)
			return
		}
		if err := w.service.runDialogueJob(a.Title, [][]string{a.Args}, false, nil); err != nil {
			http.Error(rw, err.Error(), http.StatusConflict)
			return
		}
		rw.WriteHeader(http.StatusAccepted)
	case "answer":
		d := dialogue
		d.mu.Lock()
		reply, asking := d.reply, d.asking
		d.asking = false
		d.mu.Unlock()
		if reply == nil || !asking {
			http.Error(rw, "no question is waiting", http.StatusConflict)
			return
		}
		d.mu.Lock()
		q := d.question
		d.question = nil
		asked := strings.TrimSpace(d.current.String())
		if q != nil {
			asked = q.Text
		}
		d.history = append(d.history, TDialogueReply{Question: asked, Answer: req.Text, At: time.Now().Format("15:04:05")})
		d.mu.Unlock()
		d.write(req.Text + "\n") // as a terminal echoes it
		d.mu.Lock()
		d.since = d.ended
		d.pending, d.settled = nil, 0 // the question line and the echoed answer: the log gets the summary below
		d.mu.Unlock()
		for _, line := range summariseAnswer(q, req.Text) {
			fmt.Fprintln(os.Stderr, line)
		}
		reply(req.Text)
		rw.WriteHeader(http.StatusNoContent)
	case "stop":
		w.service.mu.Lock()
		stop := w.service.jobStop
		w.service.mu.Unlock()
		if stop != nil {
			stop()
		}
		rw.WriteHeader(http.StatusNoContent)
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
// question message; q is nil for a line asked for without one.
func summariseAnswer(q *TQuestion, answer string) []string {
	if q == nil {
		if answer == "q" {
			return []string{"ACTION: Stopped"}
		}
		return []string{"ACTION: Answered " + answer}
	}
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

// runDialogueChild runs a command that does not run in the service (a process of
// its own: -update_dblp, -harvest, ...) as the Actions tab's job. It speaks the
// same frames as a job in the service (TFrameIO): its output goes through the
// dialogue, its questions are put to the page, the answers go back as frames.
func (s *TService) runDialogueChild(args []string, dir string) int {
	c, err := startFrameChild(args, dir, true, func(_, d string) { dialogue.write(d) })
	if err != nil {
		dialogue.write("The service cannot run the job: " + err.Error() + "\n")
		return 1
	}
	s.mu.Lock()
	s.jobStop = func() {
		c.interrupt()
		dialogue.mu.Lock()
		asking := dialogue.asking
		dialogue.mu.Unlock()
		if asking { // a question waiting: answered "q", as a Ctrl-C would
			c.answer("q\n")
		}
	}
	s.mu.Unlock()
	dialogue.mu.Lock()
	dialogue.reply = func(text string) { c.answer(text + "\n") }
	dialogue.mu.Unlock()
	return c.run(func(f serviceFrame) { dialogueSink{}.send(f) }) //nolint:errcheck
}
