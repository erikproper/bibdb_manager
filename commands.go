/*
 *
 * Module:    bibdb_manager
 * Component:
 * - commands
 *
 * The command table (plan step 22.1): every command the programme runs, with its
 * kind (does it change the library?) and its function. main selects the first
 * command whose flag is set, as its switch did; "check" (the default run) when none
 * is. The kind is what the service (step 22.2) needs to allow one writing job at a
 * time while reads go on.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 18.11.2026
 *
 */

package main

import (
	"fmt"
	"strings"
)

// TCommandKind says whether a command changes the library.
type TCommandKind int

const (
	commandRead  TCommandKind = iota // only reads the library
	commandWrite                     // may change the library: needs the single writer
	commandMixed                     // -sync: per file, reading or writing
)

// TCommand is one entry of the command table.
type TCommand struct {
	name     string
	kind     TCommandKind
	selected func() bool         // its flag is set; nil for the default run
	run      func(args []string) // validates its arguments and does the work
}

// commandTable lists the commands in the order main's switch tested them: the
// first selected one runs. The last entry is the default run.
var commandTable []TCommand

func init() {
	// Filled in init: the functions refer (indirectly) to main's state, which a
	// package-level initialiser would turn into an initialisation cycle.
	commandTable = []TCommand{
		{
			name:     "sync",
			kind:     commandMixed,
			selected: func() bool { return cmdSync },
			run: func(args []string) {
				filter := ""
				if len(args) > 0 {
					filter = args[0]
				}
				endRunOnError(doSync(filter))
			},
		},
		{
			name:     "add_failed_urls",
			kind:     commandWrite,
			selected: func() bool { return cmdAddFailedURLs },
			run:      func(args []string) { doAddFailedURLs() },
		},
		{
			name:     "get_pdfs",
			kind:     commandWrite,
			selected: func() bool { return cmdGetPdfs },
			run: func(args []string) {
				doGetPdfs()
			},
		},
		{
			name:     "check_pdf_title",
			kind:     commandWrite,
			selected: func() bool { return cmdCheckPdfTitle },
			run: func(args []string) {
				if len(args) != 1 {
					messageln("Usage: -check_pdf_title <key>")
					endRun(1)
				}
				doCheckPdfTitle(args[0])
			},
		},
		{
			name:     "get_pdf",
			kind:     commandWrite,
			selected: func() bool { return cmdGetPdf },
			run: func(args []string) {
				if len(args) != 1 {
					messageln("Usage: -get_pdf <key>")
					endRun(1)
				}
				doGetPdf(args[0])
			},
		},
		{
			name:     "get_eprint_pdfs",
			kind:     commandWrite,
			selected: func() bool { return cmdGetEprintPdfs },
			run: func(args []string) {
				if len(args) != 1 {
					messageln("Usage: -get_eprint_pdfs <eprinttype>")
					endRun(1)
				}
				doGetEprintPdfs(args[0])
			},
		},
		{
			name:     "audit_mappings",
			kind:     commandRead,
			selected: func() bool { return cmdAuditMappings },
			run:      func(args []string) { doAuditMappings() },
		},
		{
			name:     "find_entries",
			kind:     commandRead,
			selected: func() bool { return cmdFindEntries },
			run: func(args []string) {
				if len(args) == 0 || len(args) > 2 {
					messageln("Usage: -find_entries <field> [<value>]")
					endRun(1)
				}
				doFindEntries(args)
			},
		},
		{
			name:     "entry_key",
			kind:     commandRead,
			selected: func() bool { return cmdEntryKey },
			run: func(args []string) {
				if len(args) == 0 {
					messageln("Usage: -entry_key <alias>")
					endRun(1)
				}
				doEntryKey(args)
			},
		},
		{
			name:     "entry_key_alias",
			kind:     commandWrite,
			selected: func() bool { return cmdEntryKeyAlias },
			run: func(args []string) {
				if len(args) == 0 {
					messageln("Usage: -entry_key_alias <key>")
					endRun(1)
				}
				doEntryKeyAlias(args, cmdMap)
			},
		},
		{
			name:     "map",
			kind:     commandWrite,
			selected: func() bool { return cmdMap },
			run: func(args []string) {
				if len(args) < 2 {
					messageln("Usage: -map <alias>... <canonical>")
					endRun(1)
				}
				doAddKeyMapping(args)
			},
		},
		{
			name:     "show_entry",
			kind:     commandRead,
			selected: func() bool { return cmdShowEntry },
			run: func(args []string) {
				if len(args) == 0 {
					messageln("Usage: -show_entry <key>")
					endRun(1)
				}
				doShowEntry(args)
			},
		},
		{
			name:     "fix_entries",
			kind:     commandWrite,
			selected: func() bool { return cmdFixEntries },
			run: func(args []string) {
				if len(args) == 0 {
					messageln("Usage: -fix_entries <key>...")
					endRun(1)
				}
				doFixEntries(args)
			},
		},
		{
			name:     "fix_duplicates",
			kind:     commandWrite,
			selected: func() bool { return cmdFixDuplicates },
			run: func(args []string) {
				doFixDuplicates()
			},
		},
		{
			name:     "triage_author_mappings",
			kind:     commandWrite,
			selected: func() bool { return cmdTriageAuthorMappings },
			run: func(args []string) {
				doTriageAuthorMappings()
			},
		},
		{
			name:     "triage_contributor_aliases",
			kind:     commandWrite,
			selected: func() bool { return cmdTriageContributorAliases },
			run: func(args []string) {
				doTriageContributorAliases()
			},
		},
		{
			name:     "disambiguate_contributors",
			kind:     commandWrite,
			selected: func() bool { return cmdDisambiguateContributors },
			run: func(args []string) {
				doDisambiguateContributors()
			},
		},
		{
			name:     "assign_orcid",
			kind:     commandWrite,
			selected: func() bool { return cmdAssignOrcid },
			run: func(args []string) {
				if len(args) != 2 {
					messageln("Usage: -assign_orcid <name-or-EP-id> <orcid>")
					endRun(1)
				}
				doAssignOrcid(args)
			},
		},
		{
			name:     "update_orcid",
			kind:     commandRead,
			selected: func() bool { return cmdUpdateOrcidCache },
			run: func(args []string) {
				doUpdateOrcidCache()
			},
		},
		{
			name:     "enrich_contributor_data",
			kind:     commandWrite,
			selected: func() bool { return cmdEnrichContributorData },
			run: func(args []string) {
				doEnrichContributorData()
			},
		},
		{
			name:     "merge_orcid_duplicates",
			kind:     commandWrite,
			selected: func() bool { return cmdMergeOrcidDuplicates },
			run: func(args []string) {
				doMergeOrcidDuplicates()
			},
		},
		{
			name:     "merge_contributors",
			kind:     commandWrite,
			selected: func() bool { return cmdMergeContributors },
			run: func(args []string) {
				if len(args) < 2 {
					messageln("Usage: -merge_contributors <from...> <into>")
					endRun(1)
				}
				doMergeContributors(args)
			},
		},
		{
			name:     "restore_key_aliases",
			kind:     commandWrite,
			selected: func() bool { return cmdRestoreKeyAliases },
			run: func(args []string) {
				doRestoreKeyAliases(restoreKeyAliasesPath)
			},
		},
		{
			name:     "update_dblp",
			kind:     commandWrite,
			selected: func() bool { return cmdUpdateDblp },
			run: func(args []string) {
				doUpsertDblpEntries()
				if dbWriteSessionActive {
					runWatchAndScript()
				}
			},
		},
		{
			name:     "fix_candidates",
			kind:     commandWrite,
			selected: func() bool { return cmdFixCandidates },
			run: func(args []string) {
				doFixCandidates()
			},
		},
		{
			name:     "update_all_dblp_entries",
			kind:     commandWrite,
			selected: func() bool { return cmdAddDblpEntries },
			run: func(args []string) {
				requireNoDblpImport()
				doUpsertDblpEntries()
			},
		},
		{
			name:     "add_dblp_entry",
			kind:     commandWrite,
			selected: func() bool { return cmdAddDblpEntry },
			run: func(args []string) {
				requireNoDblpImport()
				if len(args) == 0 {
					messageln("Usage: -add_dblp_entry <key>...")
					endRun(1)
				}
				// Route each argument: DBLP keys (containing "/") go to doAddDblpEntry;
				// library keys go to doFixDblpFor.
				var libKeys, dblpKeys []string
				for _, arg := range args {
					if strings.Contains(arg, "/") {
						dblpKeys = append(dblpKeys, arg)
					} else {
						libKeys = append(libKeys, arg)
					}
				}
				if len(libKeys) > 0 {
					doUpsertDblpFor(libKeys)
				}
				if len(dblpKeys) > 0 {
					doUpsertDblpEntryFromDblpKeys(dblpKeys)
				}
			},
		},
		{
			name:     "watch",
			kind:     commandWrite,
			selected: func() bool { return cmdWatch },
			run: func(args []string) {
				requireNoDblpImport()
				doWatch()
			},
		},
		{
			name:     "add_group",
			kind:     commandWrite,
			selected: func() bool { return cmdAddGroup },
			run: func(args []string) {
				if len(args) < 1 {
					messageln("Usage: -add_group <group_name> [key...]")
					endRun(1)
				}
				doCreateGroup(args)
			},
		},
		{
			name:     "add_key_mapping",
			kind:     commandWrite,
			selected: func() bool { return cmdAddKeyMapping },
			run: func(args []string) {
				if len(args) < 2 {
					messageln("Usage: -add_key_mapping <alias>... <canonical>")
					endRun(1)
				}
				doAddKeyMapping(args)
			},
		},
		{
			name:     "merge_entries",
			kind:     commandWrite,
			selected: func() bool { return cmdMergeEntries },
			run: func(args []string) {
				if len(args) < 2 {
					messageln("Usage: -merge_entries <key>... <target>")
					endRun(1)
				}
				doMergeEntries(args)
			},
		},
		{
			name:     "add_name_form",
			kind:     commandWrite,
			selected: func() bool { return cmdAddNameForm },
			run: func(args []string) {
				if len(args) != 2 {
					messageln("Usage: -add_name_form <form> <canonical>")
					endRun(1)
				}
				doAddNameForm(args)
			},
		},
		{
			name:     "correct_name",
			kind:     commandWrite,
			selected: func() bool { return cmdCorrectName },
			run: func(args []string) {
				if len(args) != 2 {
					messageln("Usage: -correct_name <bad_name> <correct_name>")
					endRun(1)
				}
				doCorrectName(args)
			},
		},
		{
			name:     "add_doi_alias",
			kind:     commandWrite,
			selected: func() bool { return cmdAddDoiAlias },
			run: func(args []string) {
				if len(args) != 2 {
					messageln("Usage: -add_doi_alias <doi> <entry_key>")
					endRun(1)
				}
				if !openLibraryToUpdate() {
					endRun(1)
				}
				addEntryDoiAlias(args[1], args[0])
			},
		},
		{
			name:     "delete_entry",
			kind:     commandWrite,
			selected: func() bool { return cmdDeleteEntry },
			run: func(args []string) {
				if len(args) == 0 {
					messageln("Usage: -delete_entry <key>...")
					endRun(1)
				}
				if !openLibraryToUpdate() {
					endRun(1)
				}
				for _, rawKey := range args {
					key := Library.MapEntryKey(rawKey)
					if key == "" {
						key = rawKey
					}
					if !Library.EntryExists(key) {
						messagef("Entry not found: %s\n", rawKey)
						continue
					}
					about := TQuestion{Key: key, Entries: []TQuestionEntry{Library.questionEntryWithParent("Entry to delete", key)}}
					if !Library.ConfirmActionAbout(about, fmt.Sprintf("Delete %s?", key)) {
						continue
					}
					Library.DeleteEntry(key)
					Library.Progress("Deleted: %s", key)
				}
			},
		},
		{
			name:     "set_preferred_alias",
			kind:     commandWrite,
			selected: func() bool { return cmdSetPreferredAlias },
			run: func(args []string) {
				if len(args) != 2 {
					messageln("Usage: -set_preferred_alias <alias> <key>")
					endRun(1)
				}
				doSetPreferredAlias(args)
			},
		},
		{
			name:     "load_library",
			kind:     commandWrite,
			selected: func() bool { return cmdLoadLibrary },
			run:      func(args []string) { openLibraryToUpdate() },
		},
		{
			name:     "push",
			kind:     commandWrite,
			selected: func() bool { return cmdPush },
			run:      func(args []string) { doPush() },
		},
		{
			name:     "split_position",
			kind:     commandWrite,
			selected: func() bool { return cmdSplitPosition },
			run: func(args []string) {
				if len(args) != 3 {
					messageln("Usage: -split_position <key> <author|editor> <position>")
					endRun(1)
				}
				doSplitPosition(args)
			},
		},
		{
			name:     "set_field",
			kind:     commandWrite,
			selected: func() bool { return cmdSetField },
			run: func(args []string) {
				if len(args) < 2 || len(args) > 3 {
					messageln("Usage: -set_field <key> <field> [<value>]")
					endRun(1)
				}
				doSetField(args)
			},
		},
		{
			name:     "table_edit",
			kind:     commandWrite,
			selected: func() bool { return cmdTableEdit },
			run: func(args []string) {
				if len(args) != 2 {
					messageln("Usage: -table_edit <table> <json>")
					endRun(1)
				}
				doTableEdit(args)
			},
		},
		{
			name:     "rename_contributor",
			kind:     commandWrite,
			selected: func() bool { return cmdRenameContributor },
			run: func(args []string) {
				if len(args) != 2 {
					messageln("Usage: -rename_contributor <EP-id> <name>")
					endRun(1)
				}
				doRenameContributor(args)
			},
		},
		{
			name:     "edit_entry",
			kind:     commandWrite,
			selected: func() bool { return cmdEditEntry },
			run: func(args []string) {
				if len(args) != 2 {
					messageln("Usage: -edit_entry <key> <json>")
					endRun(1)
				}
				doEditEntry(args)
			},
		},
		{
			name:     "add_to_group",
			kind:     commandWrite,
			selected: func() bool { return cmdAddToGroup },
			run: func(args []string) {
				if len(args) != 2 {
					messageln("Usage: -add_to_group <key> <group>")
					endRun(1)
				}
				doAddToGroup(args)
			},
		},
		{
			name:     "remove_from_group",
			kind:     commandWrite,
			selected: func() bool { return cmdRemoveFromGroup },
			run: func(args []string) {
				if len(args) != 2 {
					messageln("Usage: -remove_from_group <key> <group>")
					endRun(1)
				}
				doRemoveFromGroup(args)
			},
		},
		{
			name:     "set_groups",
			kind:     commandWrite,
			selected: func() bool { return cmdSetGroups },
			run: func(args []string) {
				if len(args) < 2 {
					messageln("Usage: -set_groups <key> [+] <group>...")
					endRun(1)
				}
				doSetGroups(args)
			},
		},
		{
			name:     "render_group",
			kind:     commandRead,
			selected: func() bool { return cmdRenderGroup },
			run: func(args []string) {
				if len(args) != 3 {
					messageln("Usage: -render_group [-use_aliases] <group> <pubs_folder> <citations_folder>")
					endRun(1)
				}
				doRenderGroup(args, cmdUseAliases)
			},
		},
		{
			name:     "list_group_aliases",
			kind:     commandRead,
			selected: func() bool { return cmdListGroupAliases },
			run: func(args []string) {
				if len(args) != 1 {
					messageln("Usage: -list_group_aliases <group>")
					endRun(1)
				}
				doListGroupAliases(args)
			},
		},
		{
			name:     "render_as_bibtex",
			kind:     commandRead,
			selected: func() bool { return cmdRenderAsBibTeX },
			run: func(args []string) {
				if len(args) == 0 {
					messageln("Usage: -render_as_bibtex <key>")
					endRun(1)
				}
				doRenderAsBibTeX(args)
			},
		},
		{
			name:     "render_as_tex",
			kind:     commandRead,
			selected: func() bool { return cmdRenderAsTex },
			run: func(args []string) {
				if len(args) == 0 {
					messageln("Usage: -render_as_tex <key>")
					endRun(1)
				}
				doRenderAsTex(args)
			},
		},
		{
			name:     "render_as_html",
			kind:     commandRead,
			selected: func() bool { return cmdRenderAsHTML },
			run: func(args []string) {
				if len(args) == 0 {
					messageln("Usage: -render_as_html <key>")
					endRun(1)
				}
				doRenderAsHTML(args)
			},
		},
		{
			name:     "render_as_text",
			kind:     commandRead,
			selected: func() bool { return cmdRenderAsText },
			run: func(args []string) {
				if len(args) == 0 {
					messageln("Usage: -render_as_text <key>")
					endRun(1)
				}
				doRenderAsText(args)
			},
		},
		{
			name:     "check_pdfs",
			kind:     commandWrite,
			selected: func() bool { return cmdCheckPdfs },
			run: func(args []string) {
				doCheckPDFs()
			},
		},
		{
			name:     "align_booktitle_countries",
			kind:     commandWrite,
			selected: func() bool { return cmdAlignBooktitleCountries },
			run: func(args []string) {
				if openLibraryToUpdate() {
					Library.CheckAlignBooktitleCountries()
				}
			},
		},
		{
			name:     "do_entry_actions",
			kind:     commandWrite,
			selected: func() bool { return cmdApplyScript },
			run: func(args []string) {
				doApplyScript()
			},
		},
		{
			name:     "export",
			kind:     commandRead,
			selected: func() bool { return cmdExport != "" },
			run: func(args []string) {
				// IsBoolFlag=true means "-export table" parses as bare "-export" with "table" in args.
				// When the spec is the default "all" and positional args are present, treat the first
				// arg as the table spec so "-export bib_entries" works alongside "-export=bib_entries".
				exportSpec := string(cmdExport)
				if exportSpec == "all" && len(args) > 0 {
					exportSpec = strings.Join(args, ",")
				}
				if openLibraryToReport() {
					ExportTables(exportSpec)
				}
			},
		},
		{
			name:     "import",
			kind:     commandWrite,
			selected: func() bool { return cmdImport != "" },
			run: func(args []string) {
				importSpec := string(cmdImport)
				if importSpec == "all" && len(args) > 0 {
					importSpec = strings.Join(args, ",")
				}
				skipStartupChecks = true
				if openLibraryToUpdate() {
					ImportTables(importSpec, &Library)
				}
			},
		},
		{
			name:     "import_bib",
			kind:     commandWrite,
			selected: func() bool { return cmdImportBib },
			run: func(args []string) {
				if len(args) == 0 {
					messageln("Usage: -import_bib <file.bib>")
					endRun(1)
				}
				doImportBib(args[0])
			},
		},
		{
			name:     "harvest",
			kind:     commandWrite,
			selected: func() bool { return cmdHarvest },
			run: func(args []string) {
				path := ""
				if len(args) > 0 {
					path = args[0]
				}
				doHarvest(path)
			},
		},
		{
			name:     "check",
			kind:     commandWrite,
			selected: nil,
			run: func(args []string) {
				if len(args) > 0 {
					messageln("Unexpected arguments (did you forget a command flag?):", args)
					endRun(1)
				}
				doDefaultRun()
			},
		},
	}
}

// selectCommand returns the command to run: the first whose flag is set, or the
// default run.
func selectCommand() *TCommand {
	for i := range commandTable {
		c := &commandTable[i]
		if c.selected == nil || c.selected() {
			return c
		}
	}
	return nil // not reached: the last entry is the default run
}
