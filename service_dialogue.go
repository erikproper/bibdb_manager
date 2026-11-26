/*
 *
 * Module:    bibdb_manager
 * Component:
 * - service
 *   - dialogue
 *
 * The jobs' side of the dialogue (ARCHITECTURE.md §15): each job has an id, and
 * the service retains, per job, its status, its open question and its results so
 * far, as an MQTT broker retains messages. An answer names the question it
 * answers, and is checked against the open one; the first valid answer counts.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 25.11.2026
 *
 */

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// TAnswer is the answer to a question: a choice (an option's key), a text, or
// both (a choice that takes a value: "edit" and the value).
type TAnswer struct {
	Question string `json:"question"`
	Choice   string `json:"choice"`
	Text     string `json:"text"`
}

// line is the answer as the line a job reads.
func (a TAnswer) line() string {
	if a.Choice != "" {
		return a.Choice
	}
	return a.Text
}

// value is the text that comes with a choice.
func (a TAnswer) value() string {
	if a.Choice != "" {
		return a.Text
	}
	return ""
}

// answerValue is the text that came with the last answer (a choice with a value):
// the job's next AskForInput takes it instead of asking.
var answerValue atomic.Pointer[string]

func setAnswerValue(text string) {
	if text == "" {
		answerValue.Store(nil)
		return
	}
	answerValue.Store(&text)
}

func takeAnswerValue() (string, bool) {
	if p := answerValue.Swap(nil); p != nil {
		return *p, true
	}
	return "", false
}

const (
	jobRunning = "running"
	jobAsking  = "asking"
	jobDone    = "done"
	jobStopped = "stopped"
	jobFailed  = "failed"

	jobsRetained       = 20   // jobs whose state is kept
	jobResultsRetained = 2000 // result parts kept per job
)

var (
	errNoSuchQuestion = errors.New("That question is not open (any more)")
)

// TJob is a job of the service and what is retained of its dialogue.
type TJob struct {
	mu       sync.Mutex
	id       string
	title    string
	from     string // "web UI", "command line", "service"
	started  time.Time
	ended    time.Time
	state    string
	stopping bool
	status   TStatus    // as the job sent it last
	question *TQuestion // the open question (asked: waiting for its answer)
	asked    bool
	results  []TMessage
	dropped  int // result parts beyond jobResultsRetained
	exit     int
	reply    func(TAnswer) // gives an answer to the job
	onAsk    func()        // the job asks something (a command line's job: bring it up)
}

var (
	jobsMu        sync.Mutex
	jobs          []*TJob // the last jobsRetained, oldest first
	jobCount      int
	questionCount atomic.Int64
)

// newJob registers a job.
func newJob(title, from string) *TJob {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	jobCount++
	j := &TJob{id: fmt.Sprintf("j%d", jobCount), title: title, from: from, started: time.Now(), state: jobRunning}
	jobs = append(jobs, j)
	if len(jobs) > jobsRetained {
		jobs = jobs[len(jobs)-jobsRetained:]
	}
	return j
}

// findJob is the retained job with that id, or nil.
func findJob(id string) *TJob {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	for _, j := range jobs {
		if j.id == id {
			return j
		}
	}
	return nil
}

// take notes a dialogue frame of the job: its status, a part of its result, a
// question, the wait for its answer.
func (j *TJob) take(f serviceFrame) {
	if j == nil {
		return
	}
	if f.T == "ask" && j.onAsk != nil {
		defer j.onAsk() // once noted
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	switch f.T {
	case "question":
		if f.Q != nil {
			q := *f.Q // the job may ask it again (Again): a copy, with an id of its own
			q.ID = fmt.Sprintf("q%d", questionCount.Add(1))
			j.question, j.asked = &q, false
		}
	case "ask":
		if j.question == nil { // a line asked for without a question message
			j.question = &TQuestion{ID: fmt.Sprintf("q%d", questionCount.Add(1)), Kind: "text", Text: "Your answer?"}
		}
		j.asked, j.state = true, jobAsking
	case "status":
		if f.S != nil {
			j.status = *f.S
		}
	case "result":
		if f.R != nil {
			if len(j.results) >= jobResultsRetained {
				j.dropped++
			} else {
				j.results = append(j.results, *f.R)
			}
		}
	}
}

// currentStatus is the job's status, with its state.
func (j *TJob) currentStatus() TStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := j.status
	st.State = j.state
	return st
}

// lastResults are the last n result parts of these kinds.
func (j *TJob) lastResults(n int, kinds ...string) []TMessage {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := []TMessage{}
	for _, r := range j.results {
		for _, k := range kinds {
			if r.Kind == k {
				out = append(out, r)
			}
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// isAsking tells whether the job waits for an answer.
func (j *TJob) isAsking() bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.asked
}

// openQuestion is the question waiting for its answer, or nil.
func (j *TJob) openQuestion() *TQuestion {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.asked {
		return nil
	}
	return j.question
}

// setReply sets how an answer reaches the job (nil: no more).
func (j *TJob) setReply(reply func(TAnswer)) {
	j.mu.Lock()
	j.reply = reply
	j.mu.Unlock()
}

// takeAnswer checks a against the open question and closes it: the question
// answered and how to reply, or why the answer is refused (a question of another
// id, or none open; an answer the question does not take: it stays open).
func (j *TJob) takeAnswer(a TAnswer) (*TQuestion, func(TAnswer), error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.asked || j.question == nil || a.Question != j.question.ID || j.reply == nil {
		return nil, nil, errNoSuchQuestion
	}
	if err := checkAnswer(j.question, a); err != nil {
		return nil, nil, err
	}
	q, reply := j.question, j.reply
	if a.line() == "q" {
		j.stopping = true
	}
	j.question, j.asked, j.state = nil, false, jobRunning
	return q, reply, nil
}

// checkAnswer tells why q does not take a (nil: it does).
func checkAnswer(q *TQuestion, a TAnswer) error {
	if strings.ContainsAny(a.Text, "\r\n") {
		return errors.New("An answer is one line")
	}
	if a.Choice == "" {
		if q.Kind == "text" {
			return nil
		}
		return errors.New("Choose one of the answers")
	}
	for _, o := range q.Options {
		if o.Key == a.Choice {
			if a.Text != "" && o.Value == "" {
				return fmt.Errorf("The answer %q takes no text", a.Choice)
			}
			return nil
		}
	}
	return fmt.Errorf("%q is not one of the answers", a.Choice)
}

// withdraw takes the open question back (the job is being stopped), and tells
// whether there was one.
func (j *TJob) withdraw() bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.stopping = true
	open := j.asked
	if open {
		j.question, j.asked, j.state = nil, false, jobRunning
	}
	return open
}

// finish notes the job's end.
func (j *TJob) finish(code int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.question, j.asked, j.reply = nil, false, nil
	j.ended, j.exit = time.Now(), code
	switch {
	case j.stopping:
		j.state = jobStopped
	case code != 0:
		j.state = jobFailed
	default:
		j.state = jobDone
	}
}

// snapshot is the job's retained state, as its topics (JSON).
func (j *TJob) snapshot(results bool) map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := j.status
	st.State = j.state
	out := map[string]any{
		"job": j.id, "title": j.title, "from": j.from, "started": j.started.Format(time.RFC3339),
		"status": st,
	}
	if j.asked {
		out["question"] = j.question
	}
	if !j.ended.IsZero() {
		out["ended"], out["exit"] = j.ended.Format(time.RFC3339), j.exit
	}
	if results {
		out["result"] = append([]TMessage{}, j.results...)
		out["dropped"] = j.dropped
	}
	return out
}

// handleJobs lists the retained jobs, the latest first (GET /api/jobs).
func (w *TWebUI) handleJobs(rw http.ResponseWriter, r *http.Request) {
	jobsMu.Lock()
	list := append([]*TJob{}, jobs...)
	jobsMu.Unlock()
	out := []map[string]any{}
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i].snapshot(false))
	}
	writeJSON(rw, out)
}

// handleJob serves one job: GET /api/job/<id> (its retained state), POST
// /api/job/<id>/answer (a TAnswer), POST /api/job/<id>/stop.
func (w *TWebUI) handleJob(rw http.ResponseWriter, r *http.Request) {
	id, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"+w.token+"/api/job/"), "/")
	j := findJob(id)
	if j == nil {
		http.NotFound(rw, r)
		return
	}
	switch {
	case r.Method == http.MethodGet && action == "":
		writeJSON(rw, j.snapshot(true))
	case r.Method == http.MethodPost && action == "answer":
		var a TAnswer
		if json.NewDecoder(http.MaxBytesReader(rw, r.Body, 1<<16)).Decode(&a) != nil {
			http.Error(rw, "not a valid answer", http.StatusBadRequest)
			return
		}
		a.Text = strings.TrimSpace(a.Text)
		q, reply, err := j.takeAnswer(a)
		if errors.Is(err, errNoSuchQuestion) {
			http.Error(rw, err.Error(), http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(rw, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		actionsTab.answered(q, a)
		reply(a)
		rw.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && action == "stop":
		s := w.service
		s.mu.Lock()
		stop := s.jobStop
		if s.job != j {
			stop = nil
		}
		s.mu.Unlock()
		if stop == nil {
			http.Error(rw, "That job is not running", http.StatusConflict)
			return
		}
		stop()
		rw.WriteHeader(http.StatusNoContent)
	default:
		http.Error(rw, "not allowed", http.StatusMethodNotAllowed)
	}
}
