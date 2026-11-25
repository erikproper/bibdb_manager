/*
 *
 * Module:    bibtex_check
 * Component:
 * - interaction
 *   - messages
 *
 * What a function tells its user, split over the two streams of ARCHITECTURE.md
 * §15: the log (plain text, one way) and the dialogue (its status, its results;
 * questions are in question.go). A function emits a TMessage; emit sends its text
 * to the log, and what a UI acts on as a "status" or "result" frame. A command run
 * on its own writes the text to the terminal.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 25.11.2026
 *
 */

package main

import (
	"fmt"
	"strings"
	"sync"
)

// TMessage is something a function tells its user; a "result" frame carries one.
type TMessage struct {
	// Kind is what it is:
	//   - "step": the step the job begins (Text: "Opening the library");
	//   - "item": the item a step works on (Text, N of Of: "Entry 3 of 12");
	//   - "progress": where the job is (Text; Live: a counter that replaces itself);
	//   - "warning", "error": an issue (Text: its first line the issue, the lines
	//     after it the details; Tight: right under the line before it);
	//   - "list": a heading with items (Text, Items: "Warnings:" and the warnings);
	//   - "note": a line of information (Text);
	//   - "outcome": what was done with an item (Key, Action, Target, Text);
	//   - "table": rows of label, value, note under a heading (Text, Rows);
	//   - "entry": an entry shown (Text: its heading, Entry).
	Kind   string          `json:"kind"`
	Text   string          `json:"text,omitempty"`
	Indent string          `json:"indent,omitempty"` // progress, note: its indentation in a terminal
	Tight  bool            `json:"tight,omitempty"`
	Live   bool            `json:"live,omitempty"`
	N      int             `json:"n,omitempty"`
	Of     int             `json:"of,omitempty"`
	Items  []string        `json:"items,omitempty"`
	Key    string          `json:"key,omitempty"`    // outcome: the item (an entry, a source entry's key)
	Action string          `json:"action,omitempty"` // outcome: "matched", "merged", "added", "skipped", "ignored", ...
	Target string          `json:"target,omitempty"` // outcome: the library entry it went to
	Rows   []TMessageRow   `json:"rows,omitempty"`
	Entry  *TQuestionEntry `json:"entry,omitempty"`
}

// TStatus is where a job is; the latest replaces the one before. The service adds
// the state (running, asking, ...).
type TStatus struct {
	State    string `json:"state"`
	Step     string `json:"step"`
	Item     string `json:"item"`
	N        int    `json:"n"`
	Of       int    `json:"of"`
	Progress string `json:"progress"`
	Live     bool   `json:"live,omitempty"` // a counter: it changes in place, and is no log line
}

// status is the status of this process's job.
var (
	statusMu sync.Mutex
	status   TStatus
)

// resetStatus starts a job's status afresh.
func resetStatus() {
	statusMu.Lock()
	status = TStatus{}
	statusMu.Unlock()
}

// nextStatus is the status after m.
func nextStatus(m TMessage) TStatus {
	statusMu.Lock()
	defer statusMu.Unlock()
	status.Live = false
	switch m.Kind {
	case "step":
		status = TStatus{Step: m.Text}
	case "item":
		status.Item, status.N, status.Of, status.Progress = m.Text, m.N, m.Of, ""
	case "progress":
		status.Progress, _, _ = strings.Cut(strings.TrimSpace(m.Text), "\n")
		status.Live = m.Live
	}
	return status
}

// TMessageRow is a row of a table message.
type TMessageRow struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Note  string `json:"note,omitempty"`
	// Action is the action of the Actions tab that the note names (a button there).
	Action string `json:"action,omitempty"`
}

// messageSender is a TUserIO that sends dialogue frames (a job of the service, in
// it or a child process); the terminal has none. They are "status" (S), "result"
// (R: a part of the result) and "question" (Q: the question it asks next, before
// "ask"). Beside them it sends its log text ("err") and its output ("out").
type messageSender interface {
	SendMessage(f serviceFrame)
}

// dialogueClient is where the job's dialogue frames go, if anywhere.
func dialogueClient() (messageSender, bool) {
	u := userIO
	if l, ok := u.(*TLoggingIO); ok {
		u = l.inner // -log keeps the log text; the dialogue goes on as it is
	}
	s, ok := u.(messageSender)
	return s, ok
}

func (s *TSocketIO) SendMessage(f serviceFrame) {
	if f.T != "question" { // answered in the web UI
		s.out.send(f) //nolint:errcheck
	}
	s.job.take(f)
}
func (t *TFrameIO) SendMessage(f serviceFrame) { t.out.send(f) } //nolint:errcheck

// emit tells the user m: its text to the log (but for a counter), and what a UI
// acts on to the dialogue.
func emit(m TMessage) {
	s, ok := dialogueClient()
	if !ok {
		userIO.WriteMessage(renderMessage(m))
		return
	}
	if !m.Live {
		userIO.WriteMessage(renderMessage(m))
	}
	switch m.Kind {
	case "step", "item", "progress":
		st := nextStatus(m)
		s.SendMessage(serviceFrame{T: "status", S: &st})
	case "warning", "error", "outcome", "table":
		s.SendMessage(serviceFrame{T: "result", R: &m})
	case "list":
		kind := map[string]string{"Warnings": "warning", "Errors": "error"}[m.Text]
		for _, it := range m.Items {
			if kind != "" {
				s.SendMessage(serviceFrame{T: "result", R: &TMessage{Kind: kind, Text: it}})
			}
		}
	}
}

// clearCounter clears the counter's line on the terminal of a command run on its
// own; a client of the service does that itself.
func clearCounter() {
	if _, ok := dialogueClient(); !ok {
		message("\r\033[K")
	}
}

// announceStep shows the step a job begins.
func announceStep(heading string) {
	if isTTY {
		emit(TMessage{Kind: "step", Text: heading})
	}
}

// endStep ends the step a job announced, for the status (no log line).
func endStep() {
	if s, ok := dialogueClient(); ok && isTTY {
		resetStatus()
		s.SendMessage(serviceFrame{T: "status", S: &TStatus{}})
	}
}

// renderMessage is m as text, for the log and a terminal.
func renderMessage(m TMessage) string {
	switch m.Kind {
	case "step":
		return "\n" + m.Text + ":\n"
	case "item":
		if m.Of > 0 {
			return fmt.Sprintf("\n[%d/%d] %s\n", m.N, m.Of, m.Text)
		}
		return "\n" + m.Text + "\n"
	case "progress":
		if m.Live {
			return "\r\033[K" + m.Indent + m.Text
		}
		return m.Indent + m.Text + "\n"
	case "warning", "error":
		label := map[string]string{"warning": "WARNING:  ", "error": "ERROR:    "}[m.Kind]
		if m.Tight {
			return label + m.Text + "\n"
		}
		return "\n" + label + m.Text + "\n"
	case "list":
		var b strings.Builder
		b.WriteString("\n" + m.Text + ":\n")
		for _, it := range m.Items {
			b.WriteString("  - " + it + "\n")
		}
		return b.String()
	case "outcome":
		s := m.Indent + m.Key + ": " + m.Action
		if m.Target != "" {
			s += " " + m.Target
		}
		if m.Text != "" {
			s += " (" + m.Text + ")"
		}
		return s + "\n"
	case "table":
		return renderTable(m)
	case "entry":
		s := ""
		if m.Text != "" {
			s = m.Text + ":\n"
		}
		return s + renderEntry(m.Entry, "  ")
	}
	return m.Indent + m.Text + "\n" // "note"
}

// renderTable lines the rows up: labels left, values right, notes after them.
func renderTable(m TMessage) string {
	lw, vw := 0, 0
	for _, r := range m.Rows {
		lw = max(lw, len(r.Label)+1)
		vw = max(vw, len(r.Value))
	}
	var b strings.Builder
	b.WriteString("\n" + m.Text + ":\n")
	for _, r := range m.Rows {
		line := fmt.Sprintf("  %-*s  %*s", lw, r.Label+":", vw, r.Value)
		if r.Action != "" {
			line += "  [Actions: " + r.Note + "]"
		} else if r.Note != "" {
			line += "  " + r.Note
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// renderEntry is an entry in BibTeX, each line after prefix; its notes and its
// crossref parent under it.
func renderEntry(e *TQuestionEntry, prefix string) string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s@%s{%s,\n", prefix, e.Type, e.Key)
	for _, f := range e.Fields {
		b.WriteString(FormatBibTeXFieldAssignment(prefix, f.Name, f.Value))
	}
	b.WriteString(prefix + "}\n")
	for _, n := range e.Notes {
		b.WriteString(prefix + "  " + n + "\n")
	}
	if e.Parent != nil {
		b.WriteString(prefix + "Crossref parent:\n" + renderEntry(e.Parent, prefix+"  "))
	}
	return b.String()
}
