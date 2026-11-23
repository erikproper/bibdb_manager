package main

import (
	"os"
	"strings"
	"testing"
)

// scriptFixture puts entries in the entry cache and stubs the field writes, so the
// script evaluator runs without a database.
func scriptFixture(t *testing.T, entries map[string]map[string]string) {
	t.Helper()
	saved, savedSet, savedDel := entryCache, scriptSetField, scriptDeleteField
	t.Cleanup(func() { entryCache, scriptSetField, scriptDeleteField = saved, savedSet, savedDel })
	entryCache = map[string]*TBibTeXEntry{}
	for key, fields := range entries {
		entryCache[key] = &TBibTeXEntry{Key: key, Fields: fields}
	}
	scriptSetField = func(_ *TBibTeXLibrary, e *TBibTeXEntry, field, value string) { e.Fields[field] = value }
	scriptDeleteField = func(_ *TBibTeXLibrary, e *TBibTeXEntry, field string) { delete(e.Fields, field) }
}

func parseTestScript(t *testing.T, src string) *scriptProgram {
	t.Helper()
	p := &scriptParser{lx: newScriptLexer(src)}
	prog := p.parseProgram()
	if len(p.errors) > 0 {
		t.Fatalf("parse errors: %s", strings.Join(p.errors, "; "))
	}
	return prog
}

// TestScriptFieldActions: the sanity-rule forms of step 24 — a pattern test, and
// removing, moving and setting a field.
func TestScriptFieldActions(t *testing.T) {
	scriptFixture(t, map[string]map[string]string{
		"first":  {EntryTypeField: "book", "edition": "1st"},
		"second": {EntryTypeField: "book", "edition": "2nd"},
		"series": {EntryTypeField: "book", "edition": "Lecture Notes in Computer Science"},
		"taken":  {EntryTypeField: "book", "edition": "Lecture Notes in Computer Science", "series": "LNCS"},
		"misc":   {EntryTypeField: "misc", "howpublished": "online"},
		"braced": {EntryTypeField: "book", "edition": "NATO Advanced Science Institute Series", "series": "{NATO} Advanced Science Institute Series"},
	})
	prog := parseTestScript(t, `
if the edition field matches "(?i)^\s*(1|1st|first)\s*(ed\.?|edition)?\s*$" then
  remove the edition field;
if the edition field matches "(?i)(lecture notes|series)" then
  move the edition field to the series field;
if the entry is of type misc and the howpublished field equals "online" then
  set the howpublished field to "Online";
`)
	for _, key := range []string{"first", "second", "series", "taken", "misc", "braced"} {
		for _, rule := range prog.rules {
			scriptEvalStmt(&Library, key, prog, rule)
		}
	}
	check := func(key, field, want string) {
		t.Helper()
		if got := entryCache[key].Fields[field]; got != want {
			t.Errorf("%s.%s = %q, want %q", key, field, got, want)
		}
	}
	check("first", "edition", "")
	check("second", "edition", "2nd")
	check("series", "edition", "")
	check("series", "series", "Lecture Notes in Computer Science")
	check("taken", "edition", "Lecture Notes in Computer Science") // series taken: left, with a warning
	check("taken", "series", "LNCS")
	check("misc", "howpublished", "Online")
	check("braced", "edition", "")
	check("braced", "series", "{NATO} Advanced Science Institute Series")
	if prog.fieldChanges != 4 {
		t.Errorf("field changes = %d, want 4", prog.fieldChanges)
	}
}

// TestScriptParseErrors: a bad pattern and a field action without "field" are reported.
func TestScriptParseErrors(t *testing.T) {
	for _, src := range []string{
		`if the edition field matches "(" then remove the edition field;`,
		`if the edition field is not empty then remove the edition;`,
		`if the edition field is not empty then move the edition field to series;`,
	} {
		p := &scriptParser{lx: newScriptLexer(src)}
		p.parseProgram()
		if len(p.errors) == 0 {
			t.Errorf("no error for %s", src)
		}
	}
}

// TestScriptGroupRemoveStillParses: "remove it from the group" is unchanged.
func TestScriptGroupRemoveStillParses(t *testing.T) {
	prog := parseTestScript(t, `if the repositum field is not empty then begin remove it from the group "repositum-add"; add it to the group "repositum-done"; end;`)
	if len(prog.rules) != 1 {
		t.Fatalf("rules = %d", len(prog.rules))
	}
}

// TestSanityRules runs sanity_rules.entry_actions (the rules for Erik's script) on
// the edition values of the library (test copy, 2026-11-23).
func TestSanityRules(t *testing.T) {
	src, err := os.ReadFile("sanity_rules.entry_actions")
	if err != nil {
		t.Fatal(err)
	}
	prog := parseTestScript(t, string(src))
	cases := map[string]struct{ edition, series string }{
		"1st": {"", ""}, "1": {"", ""}, "1.": {"", ""}, "1e": {"", ""}, "First": {"", ""},
		"First Edition": {"", ""}, "1st ed.": {"", ""}, "{1st}": {"", ""}, " first edition ": {"", ""},
		"1e dr": {"", ""}, "1. publ": {"", ""},
		"2nd": {"2nd", ""}, "3.": {"3.", ""}, "10th": {"10th", ""}, "11th": {"11th", ""},
		"Revised": {"Revised", ""}, "Sixth Edition": {"Sixth Edition", ""}, "1.5": {"1.5", ""},
		"version 1.0": {"version 1.0", ""}, "Second English": {"Second English", ""},
		"8th international edition":                        {"8th international edition", ""},
		"Lecture notes in computer science":                {"", "Lecture notes in computer science"},
		"NATO Advanced Science Institute Series":           {"", "NATO Advanced Science Institute Series"},
		"Mathematisch Centrum Syllabus":                    {"", "Mathematisch Centrum Syllabus"},
		"International Handbooks on Information Systems":   {"", "International Handbooks on Information Systems"},
		"Cambridge Tracts in Theoretical Computer Science": {"", "Cambridge Tracts in Theoretical Computer Science"},
		"ACM Press, Frontier Series":                       {"", "ACM Press, Frontier Series"},
	}
	entries := map[string]map[string]string{}
	for in := range cases {
		entries[in] = map[string]string{EntryTypeField: "book", "edition": in}
	}
	scriptFixture(t, entries)
	for in := range cases {
		for _, rule := range prog.rules {
			scriptEvalStmt(&Library, in, prog, rule)
		}
	}
	for in, want := range cases {
		f := entryCache[in].Fields
		if f["edition"] != want.edition || f["series"] != want.series {
			t.Errorf("%q: edition %q series %q, want %q %q", in, f["edition"], f["series"], want.edition, want.series)
		}
	}
}
