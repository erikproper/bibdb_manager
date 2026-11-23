package main

import "testing"

func TestAuditMappings(t *testing.T) {
	src := `if the edition field matches "(?i)^(1|1st)$" then
  remove the edition field;
if the edition field matches "(?i)lecture notes" then
  move the edition field to the series field;
if the year field equals "1900" then
  remove the year field;
`
	parser := &scriptParser{lx: newScriptLexer(src)}
	prog := parser.parseProgram()
	if len(parser.errors) > 0 {
		t.Fatalf("parse: %v", parser.errors)
	}
	rules := scriptFieldRules(prog)
	if len(rules) != 2 {
		t.Fatalf("rules: got %d, want 2 (the equals rule is not a matches rule)", len(rules))
	}

	rows := []TMappingRow{
		{"joutnal", "a", "joutnal", "b"},                             // unknown field
		{"author:manua", "x", "institution", "y"},                    // unknown entry type
		{"edition", "First edition", "edition", "1st"},               // removed by the script
		{"edition", "2", "edition", "2nd"},                           // fine
		{"institution", "{IEEE}", "address", "Washington {DC}, USA"}, // chain
		{"address", "Washington {DC}, USA", "address", "Washington DC, USA"},
		{"publisher", "Springer-Verlag", "publisher", "springer"}, // normaliser changes it
		{"edition", "1st", "edition", ""},                         // empty target
		{"author:manual", "x", "institution", "y"},                // a manual has no institution
	}
	normalise := func(field, value string) string {
		if value == "springer" {
			return "Springer"
		}
		return value
	}
	a := auditMappings(rows, normalise, rules)
	if len(a.emptyTarget) != 1 || a.problems() != 6 {
		t.Errorf("got empty %d, problems %d; want 1, 6", len(a.emptyTarget), a.problems())
	}
	if len(a.unknownField) != 3 || len(a.scriptUndone) != 1 || len(a.chained) != 2 || len(a.unstable) != 1 {
		t.Errorf("got unknown %d, script %d, chained %d, unstable %d; want 3, 1, 2, 1 (\"1st\" → \"\" makes \"First edition\" a chain)",
			len(a.unknownField), len(a.scriptUndone), len(a.chained), len(a.unstable))
	}
}
