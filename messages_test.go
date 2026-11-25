package main

import (
	"strings"
	"testing"
)

type fakeIO struct {
	log    strings.Builder
	frames []serviceFrame
}

func (f *fakeIO) IsInteractive() bool        { return true }
func (f *fakeIO) WriteMessage(s string)      { f.log.WriteString(s) }
func (f *fakeIO) WriteResult(string)         {}
func (f *fakeIO) Input() <-chan string       { return nil }
func (f *fakeIO) RequestInput()              {}
func (f *fakeIO) SendMessage(x serviceFrame) { f.frames = append(f.frames, x) }

// The streams: every message but a counter is log text; a status, a result part
// and a question are dialogue frames, and a question is no log text.
func TestEmitSplitsStreams(t *testing.T) {
	old := userIO
	defer func() { userIO = old; resetStatus() }()
	f := &fakeIO{}
	userIO = f
	resetStatus()
	j := newJob("test", "web UI")
	emit(TMessage{Kind: "step", Text: "Harvest"})
	emit(TMessage{Kind: "item", Text: "x2021", N: 2, Of: 5})
	emit(TMessage{Kind: "progress", Text: "Counting: 3/9", Live: true})
	emit(TMessage{Kind: "warning", Text: "Odd"})
	emit(TMessage{Kind: "outcome", Key: "x2021", Action: "added as", Target: "EP-1"})
	emit(TMessage{Kind: "list", Text: "Errors", Items: []string{"Bad"}})
	emit(TMessage{Kind: "note", Text: "Just a line"})
	sendQuestion(&TQuestion{Kind: "text", Text: "Which key?"})
	for _, x := range f.frames {
		j.take(x)
	}
	log := f.log.String()
	for _, want := range []string{"Harvest:", "[2/5] x2021", "WARNING:  Odd", "x2021: added as EP-1", "- Bad", "Just a line"} {
		if !strings.Contains(log, want) {
			t.Errorf("not in the log: %q", want)
		}
	}
	for _, not := range []string{"Counting", "Which key", "\x1b"} {
		if strings.Contains(log, not) {
			t.Errorf("in the log: %q", not)
		}
	}
	st := j.currentStatus()
	if st.Step != "Harvest" || st.Item != "x2021" || st.N != 2 || st.Of != 5 || st.Progress != "Counting: 3/9" {
		t.Errorf("status: %+v", st)
	}
	if r := j.lastResults(10, "warning", "error", "outcome"); len(r) != 3 || r[2].Kind != "error" || r[2].Text != "Bad" {
		t.Errorf("results: %+v", r)
	}
	if j.question == nil || j.question.Text != "Which key?" {
		t.Error("question")
	}
	if got := f.frames[len(f.frames)-1].T; got != "question" {
		t.Errorf("last frame: %s", got)
	}
}
