package main

// The Dialogue tab (plan step 22.3d, Erik 2026-11-24): jobs started from the
// web UI (action buttons: check, homework, ...) and the service's own load of
// the library run here, their output shown as on a terminal, their questions
// answered in the page (a button per option, or typed). A job from the command
// line keeps asking on its terminal.
//
// One dialogue at a time (the service runs one job at a time): the last one
// stays shown until the next starts.

import (
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

// TDialogue is the job of the Dialogue tab: its output, and whether it waits
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
}

const dialogueLines = 5000

var dialogue = &TDialogue{}

func (d *TDialogue) start(title string, sio *TSocketIO) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.title, d.sio, d.running, d.asking, d.code, d.started = title, sio, true, false, 0, time.Now()
	d.lines, d.ended = nil, 0
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
			d.lines = append(d.lines, d.current.String())
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
	case "ask", "askall":
		dialogue.setAsking(true)
	}
	return nil
}

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
func (s *TService) runDialogueJob(title string, cmds [][]string, own bool, done func(int)) error {
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
			sio := &TSocketIO{out: teeSink{logSink{}, dialogueSink{}}, interactive: true, lines: make(chan string, 16)}
			dialogue.start(title, sio)
			dialogue.write("$ bib.check " + strings.Join(args, " ") + "\n")
			fmt.Fprintf(os.Stderr, "%s  job (Dialogue tab): %s\n", time.Now().Format(time.DateTime), strings.Join(append([]string{title + ":"}, args...), " "))
			req := serviceFrame{T: "run", Args: append([]string{"-base", libraryBase()}, args...), Version: AppVersion, Base: libraryBase()}
			flag.CommandLine.SetOutput(flagOutput{sio})
			code = 2
			if positional, ok := parseJobArguments(req.Args); ok {
				req.Cwd, _ = os.Getwd()
				s.mu.Lock()
				s.jobStop = sio.interrupt
				s.mu.Unlock()
				deferServicePush = len(args) == 0 || args[0] != "-push" // -push pushes now
				code = s.runJobInService(req, positional, sio)
			}
			sio.finished.Store(true)
			sio.endInput()
			dialogue.write(fmt.Sprintf("\n[ended with code %d]\n", code))
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

// dialogueActions are the action buttons: name → command.
var dialogueActions = map[string]struct {
	Title string
	Args  []string
}{
	"check":          {"Check", []string{}},
	"homework":       {"Homework", []string{"-homework"}},
	"fix_candidates": {"Fix DBLP candidates", []string{"-fix_candidates"}},
	"fix_duplicates": {"Fix duplicates", []string{"-fix_duplicates"}},
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
		d.write(req.Text + "\n") // as a terminal echoes it
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
