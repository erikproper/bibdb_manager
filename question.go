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
 * the answer ("ask"); the web UI shows it (the Actions tab) and answers it. Only a
 * command that runs on its own asks at the terminal, yes/no questions.
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
	ID   string `json:"id,omitempty"` // given by the service; an answer names it
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
	Confirm bool              `json:"confirm,omitempty"` // a safety confirmation (asked even when silenced)
	Tight   bool              `json:"tight,omitempty"`   // shown right under the line before it
	Again   bool              `json:"again,omitempty"`   // asked again after an answer it does not take
}

// TQuestionOption is an answer the question offers.
type TQuestionOption struct {
	Key   string          `json:"key"`             // what is answered ("y", "1", ...)
	Label string          `json:"label,omitempty"` // what it means ("none", "enter key manually")
	Entry *TQuestionEntry `json:"entry,omitempty"` // a candidate: the entry it stands for
	Value string          `json:"value,omitempty"` // a value goes with it (what value: "DBLP key"): one answer, choice and text
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

// sendQuestion puts q to the user: as a message to the web UI, or (a yes/no
// question) rendered to the terminal when the command runs on its own.
func sendQuestion(q *TQuestion) {
	if !userIO.IsInteractive() {
		return
	}
	if s, ok := dialogueClient(); ok {
		if l, ok := userIO.(*TLoggingIO); ok {
			l.log(renderQuestion(q)) // -log keeps the questions too
		}
		s.SendMessage(serviceFrame{T: "question", Q: q})
		return
	}
	if !q.yesNo() {
		messagef("\nThis needs an answer in the web UI (%s). Start the service (-start) and run it again.\n", q.Text)
		quitNow()
		return
	}
	userIO.WriteMessage(renderQuestion(q))
}

// yesNo tells whether q is a yes/no question: the only kind asked at a terminal,
// by a command that runs on its own (ARCHITECTURE.md §15.6).
func (q *TQuestion) yesNo() bool {
	if q.Kind != "choice" {
		return false
	}
	for _, o := range q.Options {
		if o.Key != "y" && o.Key != "n" && o.Key != "q" {
			return false
		}
	}
	return true
}

// renderQuestion is q as text for a terminal: its warning, the entries and values
// it is about, the candidates, and the prompt with the answers it takes.
func renderQuestion(q *TQuestion) string {
	var b strings.Builder
	keys := make([]string, 0, len(q.Options))
	var meanings []string
	for _, o := range q.Options {
		keys = append(keys, o.Key)
		if o.Label != "" && o.Entry == nil && !(o.Key == "y" && o.Label == "yes") && !(o.Key == "n" && o.Label == "no") && o.Key != "q" {
			meanings = append(meanings, o.Key+" = "+o.Label)
		}
	}
	prompt := q.Text
	if len(meanings) > 0 {
		prompt += " (" + strings.Join(meanings, ", ") + ")"
	}
	label := "QUESTION: "
	if q.Confirm {
		label = "CONFIRM:  "
	}
	if q.Kind == "text" {
		prompt += " (q=quit)"
		if len(keys) > 0 {
			prompt += " (" + strings.Join(keys, "/") + ", or type)"
		}
	} else {
		prompt += " (" + strings.Join(keys, "/") + ")"
	}
	if q.Again {
		return "(" + strings.Join(keys, "/") + "): "
	}
	if !q.Tight {
		b.WriteString("\n")
	}
	if q.Warning != "" {
		b.WriteString("WARNING:  " + q.Warning + "\n")
	}
	for _, e := range q.Entries {
		if e.Role != "" {
			b.WriteString(e.Role + ":\n")
		}
		b.WriteString(renderEntry(&e, "  "))
	}
	for _, v := range q.Values {
		fmt.Fprintf(&b, "- %-10s: %s\n", cases(v.Role), v.Value)
		if v.Note != "" {
			b.WriteString("    (" + v.Note + ")\n")
		}
	}
	for _, o := range q.Options {
		if o.Entry != nil {
			b.WriteString("[" + o.Key + "]\n" + renderEntry(o.Entry, "  "))
		}
	}
	b.WriteString(label + prompt + ": ")
	return b.String()
}

// cases is s with its first letter in upper case.
func cases(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
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

// questionEntryWithParent is questionEntryOf with the entry's crossref parent.
func (l *TBibTeXLibrary) questionEntryWithParent(role, key string) TQuestionEntry {
	q := l.questionEntryOf(role, key)
	if crossref := l.EntryFieldValueity(key, "crossref"); crossref != "" {
		if p := l.questionEntryOf("", l.MapEntryKey(crossref)); p.Type != "" {
			q.Parent = &p
		}
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
