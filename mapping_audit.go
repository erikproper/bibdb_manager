package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// Plan step 24, slice 3: an audit of the field_mappings table. Read-only: it
// lists rows that are dead, undone or redundant, and Erik decides what goes.

// TMappingRow is one row of field_mappings. The source field may carry an
// entry type ("author:book").
type TMappingRow struct {
	sourceField, sourceValue, targetField, targetValue string
}

// TScriptFieldRule is an entry_actions rule of the form
// "if the F field matches "re" then <remove, move or set F>".
type TScriptFieldRule struct {
	field  string
	re     *regexp.Regexp
	action string
}

// TMappingAudit holds the findings, one line per row, per group.
type TMappingAudit struct {
	unknownField []string // a field or entry type that does not exist: never applies
	unstable     []string // the field's normaliser changes the target: the mapping is undone
	chained      []string // the target is mapped again: harmless (same-field mappings apply on output), but could name the final value
	scriptUndone []string // a script rule changes the target again
	emptyTarget  []string // an empty target value removes nothing (use a script "remove" rule)
}

// problems is the number of findings that point at an error, chains left out.
func (a TMappingAudit) problems() int {
	return len(a.unknownField) + len(a.unstable) + len(a.scriptUndone) + len(a.emptyTarget)
}

// scriptFieldRules collects the rules of prog that test a field with "matches"
// and then change that same field.
func scriptFieldRules(prog *scriptProgram) []TScriptFieldRule {
	var rules []TScriptFieldRule
	for _, rule := range prog.rules {
		cond, ok := rule.cond.(*sCondFieldMatches)
		if !ok {
			continue
		}
		if action := scriptActionOnField(rule.then, cond.field); action != "" {
			rules = append(rules, TScriptFieldRule{cond.field, cond.re, action})
		}
	}
	return rules
}

// scriptActionOnField describes what stmt does to field, or "" when it leaves it.
func scriptActionOnField(stmt scriptStmt, field string) string {
	switch s := stmt.(type) {
	case *sBlock:
		for _, inner := range s.stmts {
			if action := scriptActionOnField(inner, field); action != "" {
				return action
			}
		}
	case *sRemoveField:
		if s.field == field {
			return "removed"
		}
	case *sMoveField:
		if s.from == field {
			return "moved to " + s.to
		}
	case *sSetField:
		if s.field == field {
			return fmt.Sprintf("set to %q", s.value)
		}
	}
	return ""
}

// auditMappings checks rows. normalise returns a field's normalised value (nil:
// no check).
//
// A same-field source value still in bib_entries is not a finding: same-field
// mappings are applied when the bib file is written (MapEntryFieldValue), and
// the DB keeps the raw value.
func auditMappings(rows []TMappingRow, normalise func(field, value string) string,
	rules []TScriptFieldRule) TMappingAudit {
	var audit TMappingAudit

	sameFieldSources := map[string]bool{}
	for _, r := range rows {
		if r.sourceField == r.targetField {
			sameFieldSources[r.sourceField+"\x00"+r.sourceValue] = true
		}
	}

	for _, r := range rows {
		line := fmt.Sprintf("%s = %q → %s = %q", r.sourceField, r.sourceValue, r.targetField, r.targetValue)

		baseField, entryType, _ := strings.Cut(r.sourceField, ":")
		switch {
		case !BibTeXAllowedFields.Contains(baseField):
			audit.unknownField = append(audit.unknownField, line+fmt.Sprintf("   (no field %q)", baseField))
			continue
		case entryType != "" && !BibTeXAllowedEntries.Contains(entryType):
			audit.unknownField = append(audit.unknownField, line+fmt.Sprintf("   (no entry type %q)", entryType))
			continue
		case !BibTeXAllowedFields.Contains(r.targetField):
			audit.unknownField = append(audit.unknownField, line+fmt.Sprintf("   (no field %q)", r.targetField))
			continue
		case entryType != "" && !entryTypeAllowsField(entryType, r.targetField):
			audit.unknownField = append(audit.unknownField, line+fmt.Sprintf("   (a %s has no %s field)", entryType, r.targetField))
			continue
		}

		if strings.TrimSpace(r.targetValue) == "" {
			audit.emptyTarget = append(audit.emptyTarget, line)
			continue
		}
		if normalise != nil {
			if n := normalise(r.targetField, r.targetValue); n != r.targetValue {
				audit.unstable = append(audit.unstable, line+fmt.Sprintf("   (normalised to %q)", n))
			}
		}
		if r.targetValue != r.sourceValue && sameFieldSources[r.targetField+"\x00"+r.targetValue] {
			audit.chained = append(audit.chained, line)
		}
		for _, rule := range rules {
			if rule.field == r.targetField && r.targetValue != "" && rule.re.MatchString(r.targetValue) {
				audit.scriptUndone = append(audit.scriptUndone, line+"   (then "+rule.action+")")
				break
			}
		}
	}
	return audit
}

// nameValuedFields are not normalised by the audit: their normaliser looks up
// and may record contributors.
var nameValuedFields = map[string]bool{"author": true, "editor": true}

// collectMappingAudit reads field_mappings and the entry_actions script of the
// open library and audits the mappings. scriptOK is false when the script does
// not parse (its rules are then not checked).
func collectMappingAudit() (audit TMappingAudit, rowCount, ruleCount int, scriptOK bool, err error) {
	var rows []TMappingRow
	dbRows, err := bibQuery(`SELECT source_field, source_value, target_field, target_value FROM field_mappings
		ORDER BY source_field, source_value, target_field`)
	if err != nil {
		return audit, 0, 0, false, err
	}
	for dbRows.Next() {
		var r TMappingRow
		if dbRows.Scan(&r.sourceField, &r.sourceValue, &r.targetField, &r.targetValue) == nil {
			rows = append(rows, r)
		}
	}
	dbRows.Close()

	var rules []TScriptFieldRule
	scriptOK = true
	if src, readErr := os.ReadFile(libraryBase() + ScriptFilePath); readErr == nil {
		parser := &scriptParser{lx: newScriptLexer(string(src))}
		prog := parser.parseProgram()
		if len(parser.errors) == 0 {
			rules = scriptFieldRules(prog)
		} else {
			scriptOK = false
		}
	}

	normalise := func(field, value string) string {
		if nameValuedFields[field] {
			return value
		}
		return Library.NormaliseFieldValue(field, value)
	}
	return auditMappings(rows, normalise, rules), len(rows), len(rules), scriptOK, nil
}

// warnMappingProblems runs the audit at every library opening (from
// CheckFieldMappings), so a typo in an imported table or a clash with a new
// script rule shows up at once. Chains are left out: they are harmless.
func (l *TBibTeXLibrary) warnMappingProblems() {
	audit, _, _, _, err := collectMappingAudit()
	if err != nil || audit.problems() == 0 {
		return
	}
	var parts []string
	for _, p := range []struct {
		n    int
		what string
	}{
		{len(audit.unknownField), "unknown field or entry type"},
		{len(audit.unstable), "undone by the normaliser"},
		{len(audit.scriptUndone), "undone by an entry_actions rule"},
		{len(audit.emptyTarget), "empty target"},
	} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.what))
		}
	}
	l.Warning("field_mappings: %d row(s) never apply or are undone (%s); details with -audit_mappings",
		audit.problems(), strings.Join(parts, ", "))
}

// doAuditMappings implements -audit_mappings.
func doAuditMappings() {
	if !openLibraryToReport() {
		return
	}
	audit, rowCount, ruleCount, scriptOK, err := collectMappingAudit()
	if err != nil {
		messagef("Cannot read field_mappings: %s\n", err)
		return
	}
	if !scriptOK {
		messagef("Script %s does not parse; its rules are not checked.\n", libraryBase()+ScriptFilePath)
	}

	groups := []struct {
		title string
		lines []string
	}{
		{"Unknown field or entry type (the row never applies)", audit.unknownField},
		{"Target changed by the field's normaliser (the mapping is undone)", audit.unstable},
		{"Target changed by an entry_actions rule", audit.scriptUndone},
		{"Empty target (removes nothing; use a script remove rule)", audit.emptyTarget},
		{"Target mapped again (a chain; harmless, applied on output)", audit.chained},
	}

	logPath := libraryBase() + tablesFolderSuffix + "/mapping_audit.log"
	os.MkdirAll(libraryBase()+tablesFolderSuffix, 0o755) //nolint:errcheck
	var out strings.Builder
	fmt.Fprintf(&out, "# %s  bibtex_check %s  field_mappings: %d rows, %d script rule(s) checked\n",
		time.Now().Format("2006-01-02 15:04:05"), AppVersion, rowCount, ruleCount)
	for _, g := range groups {
		fmt.Fprintf(&out, "\n## %s: %d\n", g.title, len(g.lines))
		for _, l := range g.lines {
			out.WriteString(l + "\n")
		}
	}
	if err := os.WriteFile(logPath, []byte(out.String()), 0o644); err != nil {
		messagef("Cannot write %s: %s\n", logPath, err)
	}

	messagef("field_mappings: %d rows, %d script rule(s) checked\n", rowCount, ruleCount)
	for _, g := range groups {
		messagef("  %-66s %4d\n", g.title+":", len(g.lines))
	}
	messagef("Details: %s\n", logPath)
}

// entryTypeAllowsField reports whether entries of entryType may have field.
func entryTypeAllowsField(entryType, field string) bool {
	allowed, ok := BibTeXAllowedEntryFields[entryType]
	return ok && allowed.Contains(field)
}
