/*
 *
 * Module:    bibdb_manager
 * Component:
 * - commands
 *   - moved_options
 *
 * The options whose function is in the web UI (Erik, 2026-11-24; CLI_OPTIONS.md,
 * group B). The web UI's jobs, and scripts (no person there), use them; typed at a
 * terminal they are refused, saying where the function is now.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 25.11.2026
 *
 */

package main

import (
	"flag"
	"fmt"
	"strings"
)

// movedOptions are the options in the web UI, with where.
var movedOptions = map[string]string{
	"add_dblp_entry":             "Actions, DBLP: Lookup",
	"add_doi_alias":              "Tables, DOI aliases",
	"add_failed_urls":            "Actions, PDF: Add failed",
	"add_group":                  "an entry's Edit tab, Groups",
	"add_key_mapping":            "Tables, Key aliases",
	"add_name_form":              "Tables, Name forms (or a contributor's Edit tab)",
	"add_name_mapping":           "Tables, Name forms (or a contributor's Edit tab)",
	"add_to_group":               "an entry's Edit tab, Groups",
	"align_booktitle_countries":  "Actions, Checks: Country names in titles",
	"assign_orcid":               "a contributor's Edit tab",
	"audit_mappings":             "Actions, Other: Audit mappings",
	"check_pdfs":                 "Actions, PDF: Check",
	"correct_name":               "a contributor's Edit tab, misspelt",
	"delete_entry":               "an entry's pop-up, Delete",
	"disambiguate_contributors":  "Actions, Contributors: Disambiguate contributors",
	"do_entry_actions":           "Actions, Other: Entry actions",
	"enrich_contributor_data":    "Actions, Contributors: Enrich contributors",
	"fix_candidates":             "Actions, DBLP: Candidates",
	"fix_duplicates":             "Actions, Checks: Fix duplicates",
	"fix_entries":                "an entry's pop-up, Check",
	"get_pdfs":                   "Actions, PDF: Get",
	"matched_orcid_data_only":    "Actions, Contributors: Enrich, matched ORCIDs only",
	"merge_contributors":         "a contributor's pop-up, Mark for merge",
	"merge_orcid_duplicates":     "Actions, Contributors: Merge ORCID duplicates",
	"remove_from_group":          "an entry's Edit tab, Groups",
	"set_groups":                 "an entry's Edit tab, Groups",
	"set_preferred_alias":        "an entry's Edit tab, preferredalias",
	"show_entry":                 "Search, an entry's pop-up",
	"split_position":             "an entry's pop-up, One of the authors or editors is someone else",
	"triage_author_mappings":     "Actions, Contributors: Triage contributor mappings",
	"triage_contributor_aliases": "Actions, Contributors: Triage contributor aliases",
	"update_all_dblp_entries":    "Actions, DBLP: Update entries",
	"update_dblp":                "Actions, DBLP: Update cache",
	"update_orcid":               "Actions, Contributors: Refresh ORCID",
	"watch":                      "Actions, DBLP: Watch",
}

// movedOptionsGiven are the options on this command line that are in the web UI now,
// each with where.
func movedOptionsGiven() []string {
	var given []string
	flag.Visit(func(f *flag.Flag) {
		if where, moved := movedOptions[f.Name]; moved && f.Value.String() != f.DefValue {
			given = append(given, fmt.Sprintf("-%s is in the web UI now: %s", f.Name, where))
		}
	})
	return given
}

// refuseMovedOptions ends a run typed at a terminal with an option that is in the
// web UI now.
func refuseMovedOptions() {
	if !userIO.IsInteractive() {
		return
	}
	if f := flag.Lookup("jobs_file"); f != nil && f.Value.String() != "" {
		return
	}
	if given := movedOptionsGiven(); len(given) > 0 {
		messageln(strings.Join(given, "\n"))
		messageln("Open it with -ui (or -start, when no service runs).")
		endRun(1)
	}
}
