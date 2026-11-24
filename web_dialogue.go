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
	"regexp"
	"strings"
	"sync"
	"time"
)

// TDialogue is the job of the Actions tab: its output, and whether it waits
// for an answer.
type TDialogue struct {
	mu      sync.Mutex
	title   string
	lines   []string
	ended   int // lines ended so far (the kept ones are the last)
	current strings.Builder
	running bool
	code    int
	asking  bool
	sio     *TSocketIO
	started time.Time
	since   int              // the line number after the last answer: a question's context starts there
	notes   []TDialogueNote  // the warnings of this job (shown in the tab)
	inNote  bool             // the last warning still takes the lines that follow it
	pending []dialogueLine   // finished lines not in the log yet: a question's context stays out of it
	step    string           // the current step ("Opening the library:", "Step 3: …")
	substep string           // the last sub-step line (indented) after it
	history []TDialogueReply // the questions answered in this job
}

// dialogueLine is an output line waiting to go to the log.
type dialogueLine struct {
	text string
	at   time.Time
}

// dialogueLogDelay: a finished line goes to the log after this, unless a question
// comes first (its context is printed right before it asks): the log then gets the
// issue and the action taken instead (Erik, 2026-11-24).
const dialogueLogDelay = 400 * time.Millisecond

// flushLog writes the pending lines older than the delay (all: force) to the log.
func (d *TDialogue) flushLog(force bool) {
	d.mu.Lock()
	n := 0
	for n < len(d.pending) && (force || time.Since(d.pending[n].at) >= dialogueLogDelay) {
		n++
	}
	lines := d.pending[:n]
	d.pending = append([]dialogueLine{}, d.pending[n:]...)
	d.mu.Unlock()
	for _, l := range lines {
		fmt.Fprintln(os.Stderr, l.text)
	}
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
	d.lines, d.ended, d.since, d.notes, d.history, d.inNote, d.step, d.substep = nil, 0, 0, nil, nil, false, "", ""
	d.current.Reset()
}

func (d *TDialogue) finish(code int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running, d.asking, d.code, d.sio = false, false, code, nil
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
			d.track(line)
			d.pending = append(d.pending, dialogueLine{line, time.Now()})
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

// track notes a finished output line: a warning (with the lines that belong to
// it), a step (a heading line), a sub-step (an indented line). Called with d.mu held.
func (d *TDialogue) track(line string) {
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(trimmed, "WARNING:") && reHeadingWarning.MatchString(strings.TrimSpace(strings.TrimPrefix(trimmed, "WARNING:"))):
		d.inNote = false // a heading printed as a warning ("WARNING:  First entry:")
		return
	case strings.HasPrefix(trimmed, "WARNING:"):
		d.notes = append(d.notes, TDialogueNote{Head: strings.TrimSpace(strings.TrimPrefix(trimmed, "WARNING:"))})
		d.inNote = true
		return
	case d.inNote && trimmed != "" && !strings.HasPrefix(trimmed, "QUESTION:") && !strings.HasPrefix(trimmed, "@") &&
		!(strings.HasSuffix(trimmed, ":") && !strings.HasPrefix(trimmed, "-")):
		n := &d.notes[len(d.notes)-1]
		n.Lines = append(n.Lines, line)
		return
	}
	d.inNote = false
	if trimmed == "" || strings.HasPrefix(trimmed, "QUESTION:") || strings.HasPrefix(trimmed, "@") || strings.Contains(trimmed, " = {") {
		return
	}
	if line == trimmed && (strings.HasSuffix(trimmed, ":") || strings.HasPrefix(strings.ToLower(trimmed), "step ")) && len(trimmed) < 100 {
		d.step, d.substep = strings.TrimSuffix(trimmed, ":"), ""
	} else if line != trimmed && len(trimmed) < 160 {
		d.substep = trimmed
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
	case "ask", "askall":
		dialogue.mu.Lock()
		// The question's context (the run of entry, option and warning lines right before
		// it) stays out of the log; what came before it (progress) goes there.
		cut := len(dialogue.pending)
		for cut > 0 && isQuestionContext(dialogue.pending[cut-1].text) {
			cut--
		}
		keep := dialogue.pending[:cut]
		dialogue.pending = nil
		dialogue.asking = true
		dialogue.mu.Unlock()
		for _, l := range keep {
			fmt.Fprintln(os.Stderr, l.text)
		}
	}
	return nil
}

// reHeadingWarning: a warning that is a heading of up to three words ("First entry:").
var reHeadingWarning = regexp.MustCompile(`^[A-Z][\w-]*(?: [\w-]+){0,2}:$`)

// reOptions finds a question's options at the end of its line: "(n/y/q)".
var reOptions = regexp.MustCompile(`\(([^()\s/]{1,12}(?:/[^()\s/]{1,12})+)\)\s*:?\s*$`)

// options are the answers the waiting question offers ("" when none found).
func (d *TDialogue) options() []string {
	for _, line := range []string{d.current.String(), lastLine(d.lines)} {
		if m := reOptions.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			return strings.Split(m[1], "/")
		}
	}
	return nil
}

func lastLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

// runDialogueJob runs commands one after the other as the dialogue's job, in
// the background, with questions answered in the page. own: the service's own
// job (a command from the command line waits for it rather than being
// refused). done (when not nil) gets the last exit code.
func (s *TService) runDialogueJob(title string, cmds [][]string, own bool, done func(int), dir ...string) error {
	s.mu.Lock()
	if s.busy || s.stopping.Load() {
		running := strings.Join(s.jobArgs, " ")
		s.mu.Unlock()
		return fmt.Errorf("the service is busy (%s); try again when it is done", running)
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
			if positional, ok := parseJobArguments(req.Args); ok {
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
	"check": "Check", "homework": "Homework", "fix_candidates": "Fix DBLP candidates",
	"fix_duplicates": "Fix duplicates", "enrich_contributor_data": "Enrich contributor data",
	"merge_entries": "Merge entries", "sync": "Sync",
}

// dialogueActions are the action buttons: name → command.
var dialogueActions = map[string]struct {
	Title string
	Args  []string
}{
	"check":          {"Check", []string{}},
	"homework":       {"Homework", []string{"-homework"}},
	"fix_candidates": {"Fix DBLP candidates", []string{"-fix_candidates"}},
	"fix_duplicates": {"Fix duplicates", []string{"-fix_duplicates"}},
	"enrich":         {"Enrich contributor data", []string{"-enrich_contributor_data"}},
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
			"options": d.options(), "since": d.started.Format("15:04:05"),
			"notes": append([]TDialogueNote{}, d.notes...), "history": append([]TDialogueReply{}, d.history...),
			"step": d.step, "substep": d.substep,
		}
		if d.asking {
			// The question (its line) and its context: what the job wrote since the last answer.
			ctxFrom := d.since
			if ctxFrom < first {
				ctxFrom = first
			}
			context := append([]string{}, d.lines[ctxFrom-first:]...)
			question := strings.TrimSpace(d.current.String())
			if question == "" && len(context) > 0 {
				question = strings.TrimSpace(context[len(context)-1])
				context = context[:len(context)-1]
			}
			out["question"] = question
			out["context"] = context
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
	}
	json.NewDecoder(http.MaxBytesReader(rw, r.Body, 1<<16)).Decode(&req) //nolint:errcheck
	switch action {
	case "start":
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
		sio, asking := d.sio, d.asking
		d.asking = false
		d.mu.Unlock()
		if sio == nil || !asking {
			http.Error(rw, "no question is waiting", http.StatusConflict)
			return
		}
		d.mu.Lock()
		q := strings.TrimSpace(d.current.String())
		if q == "" {
			q = strings.TrimSpace(lastLine(d.lines))
		}
		d.history = append(d.history, TDialogueReply{Question: q, Answer: req.Text, At: time.Now().Format("15:04:05")})
		first := d.ended - len(d.lines)
		ctxFrom := d.since
		if ctxFrom < first {
			ctxFrom = first
		}
		context := append([]string{}, d.lines[ctxFrom-first:]...)
		d.mu.Unlock()
		d.write(req.Text + "\n") // as a terminal echoes it
		d.mu.Lock()
		d.since = d.ended
		d.pending = nil // the question line and the echoed answer: the log gets the summary below
		d.mu.Unlock()
		for _, line := range summariseAnswer(q, context, req.Text) {
			fmt.Fprintln(os.Stderr, line)
		}
		sio.asked.Store(false)
		select {
		case sio.lines <- req.Text:
		default:
		}
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
	counts["homework"] = counts["fix_candidates"] + counts["fix_duplicates"] + counts["triage"] + counts["enrich"]
	actionCountsMu.Lock()
	actionCountsValue, actionCountsVersion = counts, version
	actionCountsMu.Unlock()
}

// summariseAnswer is what the log says about a question answered in the Actions
// tab (Erik, 2026-11-24), instead of the question's context (the entries, the
// candidates): "ISSUE: …" (its warning) or "CHALLENGE: …" (with the current and
// challenging values), then "ACTION: …" (what the answer does).
func summariseAnswer(question string, context []string, answer string) []string {
	question = strings.TrimSpace(strings.TrimPrefix(question, "QUESTION:"))
	// The warnings (with the lines that belong to them) and the numbered options.
	type warning struct {
		head  string
		lines []string
	}
	var warns []warning
	blocks := map[string]string{}
	in := false
	for _, l := range context {
		t := strings.TrimSpace(l)
		if h, ok := strings.CutPrefix(t, "WARNING:"); ok {
			h = strings.TrimSpace(h)
			if reHeadingWarning.MatchString(h) {
				in = false
				continue
			}
			warns = append(warns, warning{head: h})
			in = true
			continue
		}
		if in && t != "" && !strings.HasPrefix(t, "QUESTION:") && !strings.HasPrefix(t, "@") && !(strings.HasSuffix(t, ":") && !strings.HasPrefix(t, "-")) {
			warns[len(warns)-1].lines = append(warns[len(warns)-1].lines, l)
			continue
		}
		in = false
		if m := reOptionBlock.FindStringSubmatch(t); m != nil {
			blocks[m[1]] = m[2]
		}
	}
	var out []string
	field := ""
	for _, w := range warns {
		var values []string
		for _, l := range w.lines {
			if v := strings.TrimSpace(l); strings.HasPrefix(v, "- Current") || strings.HasPrefix(v, "- Challenger") {
				values = append(values, v)
			}
		}
		if len(values) == 2 {
			out = append(out, "CHALLENGE: "+w.head)
			out = append(out, values...)
			if m := reChallengeField.FindStringSubmatch(w.head); m != nil {
				field = m[1]
			}
		} else {
			head := w.head
			if i := strings.Index(head, " — "); i > 0 {
				head = head[:i]
			}
			out = append(out, "ISSUE: "+head)
		}
	}
	lower := strings.ToLower(question)
	action := "Answered " + answer
	switch {
	case answer == "q":
		action = "Stopped"
	case blocks[answer] != "":
		action = "Selected " + strings.Fields(blocks[answer])[0]
	case strings.Contains(lower, "dblp entry matches") && answer == "0":
		action = "No DBLP entry matches"
	case strings.Contains(lower, "dblp entry matches") && answer == "k":
		action = "Looking up the DBLP key"
	case strings.HasPrefix(lower, "dblp key"):
		action = "Entered DBLP key " + answer
	case strings.HasPrefix(lower, "merge these entries"):
		action = map[string]string{"y": "Merged", "n": "Kept apart"}[answer]
	case strings.HasPrefix(lower, "keep current name"):
		action = map[string]string{"Y": "Kept the current name, everywhere", "y": "Kept the current name, in this entry",
			"N": "Took the challenger's name, everywhere", "n": "Took the challenger's name, in this entry",
			"e": "Editing the canonical name", "d": "Different people"}[answer]
	case strings.HasPrefix(lower, "keep the value as is"):
		f := field
		if f == "" {
			f = "value"
		}
		action = map[string]string{"y": "Kept the current " + f, "Y": "Kept the current " + f + ", for all entries",
			"n": "Took the challenger's " + f, "N": "Took the challenger's " + f + ", for all entries",
			"e": "Editing the " + f, "b": "Breaking it down by name"}[answer]
	case strings.HasPrefix(lower, "enter"):
		action = "Entered: " + answer
	}
	if action == "" {
		action = "Answered " + answer
	}
	return append(out, "ACTION: "+action)
}

var (
	reOptionBlock    = regexp.MustCompile(`^\[(\w+)\]\s+(\S.*)$`)
	reChallengeField = regexp.MustCompile(`field\s+(\S+?):?$`)
)

// isQuestionContext tells whether an output line belongs to a question's context:
// an entry in BibTeX, a numbered option and its "key : value" lines, a warning and
// the lines that go with it, a heading, a blank line.
func isQuestionContext(line string) bool {
	t := strings.TrimSpace(line)
	switch {
	case t == "", strings.HasPrefix(t, "WARNING:"), strings.HasPrefix(t, "QUESTION:"), strings.HasPrefix(t, "@"),
		t == "}", strings.HasPrefix(t, "["), strings.HasPrefix(t, "- "), strings.HasPrefix(t, "("),
		strings.HasPrefix(t, "Parent:"), t == "needs to be resolved":
		return true
	case strings.Contains(t, " = {"), reContextField.MatchString(t):
		return true
	case strings.HasSuffix(t, ":") && len(t) < 60:
		return true
	}
	return false
}

var reContextField = regexp.MustCompile(`^[a-z][\w-]*\s+:\s`)
