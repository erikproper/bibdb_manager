/*
 *
 * Module:    bibtex_check
 * Component:
 * - interaction
 *   - question
 *
 * A question a job puts to its user, as a message of the service's protocol
 * (Erik, 2026-11-25: "basically a question/answer protocol between a client and
 * server app"). The job sends it as a "question" frame right before it asks for
 * the answer ("ask"); a client shows it its own way: the terminal client by the
 * text the job prints, the web UI (the Actions tab) from the question's fields,
 * never by reading that text. The answer is a line, as from a terminal.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 25.11.2026
 *
 */

package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// TQuestion is a question put to the user.
type TQuestion struct {
	// Kind is "choice" (one of Options; "q" stops the job) or "text" (a line typed;
	// Options, when given, are answers offered besides).
	Kind string `json:"kind"`
	// Topic tells what the question is about, for a client that lays it out:
	// "dblp_candidate" (which DBLP entry matches a library entry: Options carry the
	// candidates), "library_candidate" (which library entry matches a harvested
	// one), "field_challenge" (Field, Values current/challenger), "name_pair"
	// (Field, Values current/challenger), or "" (a plain question).
	Topic   string            `json:"topic,omitempty"`
	Warning string            `json:"warning,omitempty"` // the issue, as a sentence
	Text    string            `json:"text"`              // the question itself
	Options []TQuestionOption `json:"options,omitempty"` // in the order offered
	Entries []TQuestionEntry  `json:"entries,omitempty"` // the entries it is about
	Key     string            `json:"key,omitempty"`     // the library entry it is about
	Field   string            `json:"field,omitempty"`   // the field it is about
	Values  []TQuestionValue  `json:"values,omitempty"`  // the values to choose between
}

// TQuestionOption is an answer the question offers.
type TQuestionOption struct {
	Key   string          `json:"key"`             // what is answered ("y", "1", ...)
	Label string          `json:"label,omitempty"` // what it means ("none", "enter key manually")
	Entry *TQuestionEntry `json:"entry,omitempty"` // a candidate: the entry it stands for
}

// TQuestionEntry is an entry shown with a question.
type TQuestionEntry struct {
	Role   string          `json:"role,omitempty"` // "Library entry", "Current entry", "Challenging entry", ...
	Type   string          `json:"type"`
	Key    string          `json:"key"`
	Fields []TQuestionPair `json:"fields"`
	Notes  []string        `json:"notes,omitempty"`  // e.g. how many children it has
	Parent *TQuestionEntry `json:"parent,omitempty"` // its crossref parent
}

// TQuestionPair is a field of an entry shown with a question.
type TQuestionPair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// TQuestionValue is a value to choose between ("current", "challenger").
type TQuestionValue struct {
	Role  string `json:"role"`
	Value string `json:"value"`
	Note  string `json:"note,omitempty"` // e.g. its lineage
}

// messageSender is a TUserIO that sends the job's messages as frames (a job of
// the service, in it or a child process); the terminal has none. Besides its
// text ("out", "err"), a job sends what a client shows in its own way:
//
//   - "question" (Q): the question it asks next, before "ask";
//   - "warning", "error" (D): a warning or error, its first line the issue, the
//     lines after it the details;
//   - "step" (D): the step the job begins ("Opening the library");
//   - "progress" (D): where it is within the step (a progress line, a counter).
type messageSender interface {
	SendMessage(f serviceFrame)
}

// sendMessage sends f to the job's client, when it takes messages as frames.
func sendMessage(f serviceFrame) {
	if s, ok := userIO.(messageSender); ok {
		s.SendMessage(f)
	}
}

// sendQuestion sends q to the job's client (see messageSender).
func sendQuestion(q *TQuestion) {
	if userIO.IsInteractive() {
		sendMessage(serviceFrame{T: "question", Q: q})
	}
}

func (s *TSocketIO) SendMessage(f serviceFrame) { s.out.send(f) } //nolint:errcheck
func (t *TFrameIO) SendMessage(f serviceFrame)  { t.out.send(f) } //nolint:errcheck

// announceStep shows the step a job begins: a heading in the text, a "step"
// message to a client that takes them.
func announceStep(heading string) {
	if !isTTY {
		return
	}
	messagef("\n%s:\n", heading)
	sendMessage(serviceFrame{T: "step", D: heading})
}

// reOptionLabels finds the option meanings the question constants carry in their
// text, "(0 = none, k = enter key manually)", "(Y/N = for all entries, ...)".
// These are this programme's own strings: read once here, where the question is
// made, so that a client gets the meanings as fields.
var reOptionLabels = regexp.MustCompile(`\s*\(([^()]*=[^()]*)\)`)

// newChoiceQuestion makes the question for question (its last line: earlier lines
// are shown context), offering keys in that order, about the issue warning.
// about (when not nil) carries what the caller knows about it: topic, entries,
// values, labels of its own.
func newChoiceQuestion(about *TQuestion, question string, keys []string, warning string) *TQuestion {
	q := &TQuestion{}
	if about != nil {
		*q = *about
	}
	q.Kind = "choice"
	if q.Warning == "" {
		q.Warning = strings.TrimSpace(warning)
	}
	lines := strings.Split(strings.TrimSpace(question), "\n")
	last := lines[len(lines)-1]
	labels := map[string]string{"y": "yes", "n": "no", "q": "stop"}
	for _, m := range reOptionLabels.FindAllStringSubmatch(last, -1) {
		for _, part := range strings.Split(m[1], ",") {
			ks, label, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			for _, k := range strings.Split(ks, "/") {
				labels[strings.TrimSpace(k)] = strings.TrimSpace(label)
			}
		}
	}
	if q.Text == "" {
		q.Text = strings.TrimSpace(reOptionLabels.ReplaceAllString(last, ""))
	}
	given := map[string]TQuestionOption{}
	for _, o := range q.Options {
		given[o.Key] = o
	}
	q.Options = nil
	for _, k := range keys {
		o, ok := given[k]
		if !ok {
			o = TQuestionOption{Key: k}
		}
		if o.Label == "" {
			o.Label = labels[k]
		}
		q.Options = append(q.Options, o)
	}
	return q
}

// questionEntryOf is an entry as shown with a question: its fields in the order
// EntryString shows them, with their values as mapped.
func (l *TBibTeXLibrary) questionEntryOf(role, key string) TQuestionEntry {
	entry := loadEntryFromDb(key)
	if !entry.Exists() {
		if direct := loadEntryFromDbDirect(key); direct.Exists() {
			entry = direct
		}
	}
	q := TQuestionEntry{Role: role, Key: key}
	if !entry.Exists() {
		return q
	}
	q.Type = entry.EntryType()
	for _, field := range BibTeXAllowedEntryFields[q.Type].Set().ElementsSorted() {
		if field == EntryTypeField {
			continue
		}
		if field == LocalURLField {
			if l.PDFFiles[entry.Key] {
				q.Fields = append(q.Fields, TQuestionPair{field, l.FilesRoot + l.FilesFolder + entry.Key + ".pdf"})
			}
			continue
		}
		if value := entry.FieldValue(field); value != "" {
			q.Fields = append(q.Fields, TQuestionPair{field, l.MapEntryFieldValue(entry.Key, field, value)})
		}
	}
	return q
}

// questionEntryOfFields is an entry given by its fields (a harvested entry, a DBLP
// entry from the store), fields in alphabetical order.
func questionEntryOfFields(role, key string, fields map[string]string) TQuestionEntry {
	q := TQuestionEntry{Role: role, Key: key, Type: fields[EntryTypeField]}
	names := make([]string, 0, len(fields))
	for f, v := range fields {
		if f != EntryTypeField && v != "" {
			names = append(names, f)
		}
	}
	sort.Strings(names)
	for _, f := range names {
		q.Fields = append(q.Fields, TQuestionPair{f, fields[f]})
	}
	return q
}

// questionDblpCandidate is a DBLP entry from the store offered as a candidate:
// its fields, its crossref parent, and how many children it (a book, proceedings)
// or its parent has (the work that taking it brings).
func (l *TBibTeXLibrary) questionDblpCandidate(dblpKey string) *TQuestionEntry {
	entry := dblpEntryFromStore(dblpKey)
	if entry == nil {
		return &TQuestionEntry{Key: dblpKey}
	}
	children := func(key string) []string {
		kids := readDblpCrossrefChildren(key)
		if len(kids) == 0 {
			return nil
		}
		in := 0
		for _, child := range kids {
			if l.LookupDBLPKey(child) != "" {
				in++
			}
		}
		return []string{fmt.Sprintf("children: %d total, %d already in library, %d new", len(kids), in, len(kids)-in)}
	}
	q := questionEntryOfFields("", dblpKey, entry.Fields)
	q.Type = entry.EntryType()
	if BibTeXBookish.Contains(entry.EntryType()) {
		q.Notes = children(dblpKey)
	} else if crossref := entry.Fields["crossref"]; crossref != "" {
		if parent := dblpEntryFromStore(crossref); parent != nil {
			p := questionEntryOfFields("", crossref, parent.Fields)
			p.Type = parent.EntryType()
			p.Notes = children(crossref)
			q.Parent = &p
		}
	}
	return &q
}

// candidateOptions are the options of a candidate question: "0" (none), "k" (a key
// typed) when offered, and one per candidate ("1", "2", ...) with its entry.
func candidateOptions(candidates []*TQuestionEntry) []TQuestionOption {
	var options []TQuestionOption
	for i, c := range candidates {
		options = append(options, TQuestionOption{Key: fmt.Sprintf("%d", i+1), Entry: c})
	}
	return options
}
