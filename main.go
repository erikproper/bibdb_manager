/*
 *
 * Module:    bibdb_manager
 * Component:
 * - main
 *
 * Command-line entry point: flag parsing, top-level command dispatch, and the
 * batch-processing driver loop over the library.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 13.11.2026
 *
 */

package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// tableListFlag implements flag.Value for -export/-import.
// Used bare (-export) it sets the value to "all" via IsBoolFlag.
// Used with a value (-export name_mappings,bib_entries) it stores that value.
type tableListFlag string

func (f *tableListFlag) String() string   { return string(*f) }
func (f *tableListFlag) IsBoolFlag() bool { return true }
func (f *tableListFlag) Set(s string) error {
	if s == "true" {
		*f = "all"
	} else {
		*f = tableListFlag(s)
	}
	return nil
}

var (
	Library   TBibTeXLibrary
	Reporting TInteraction
)

const AppVersion = "30.163"

// Run-state flags consumed by the write tail in main.
var (
	skipBibDbRefresh           bool
	skipBibValidation          bool
	skipStartupChecks          bool // set by -import so the import runs before consistency checks
	forceWrite                 bool
	cmdNoGarbageCleaning       bool
	cmdTrustHints              bool           // -trust_hints: auto-accept key-hint matches in harvest
	cmdCollectKeys             bool           // -collect_keys: add source keys to hints DB when unambiguous
	cmdHarvestTransferKeysPath string         // resolved .keys path for harvest_transfer target; "" = disabled
	cmdHarvestWeaveEntries     []TBibTeXEntry // ignored entries accumulated during this harvest run; flushed to follow .sync DB
	cmdFix                     bool           // -fix: apply full per-entry checks when combined with -sync or -harvest
	cmdPull                    bool           // -pull: with -sync, skip up-sync (phase 1); only write bib output from DB
	cmdMatchedOrcidDataOnly    bool           // -matched_orcid_data_only: skip ORCID challenges in step 3
)

// stderrPrintf writes to stderr only when running in a TTY session.
// Use for section headers and status lines that are noise in piped/scripted runs.
// errReported is returned by a command that failed and has already told the user
// why; the caller only ends the run with exit code 1.
var errReported = errors.New("reported")

// endRun ends the run with the given exit code. It is the one place where the
// programme ends (besides main returning); step 22.1 turns it into the end of a
// job, so that a service can go on.
func endRun(code int) {
	if inServiceJob {
		panic(jobEnded{code}) // ends the job, not the service (service_jobs.go)
	}
	if l, ok := userIO.(*TLoggingIO); ok {
		l.close()
	}
	os.Exit(code) //nolint:forbidigo // the only exit
}

// endRunOnError ends the run with exit code 1 when err is set, telling the user
// why unless the command already did.
func endRunOnError(err error) {
	if err == nil {
		return
	}
	if !errors.Is(err, errReported) {
		messageln(err)
	}
	endRun(1)
}

func stderrPrintf(format string, args ...any) {
	if isTTY {
		messagef(format, args...)
	}
}

func reportCacheMode() {
	if entryCache != nil {
		Library.Progress(ProgressEntryCacheLoaded, len(entryCache))
	} else {
		Library.Progress(ProgressEntryPerQuery)
	}
}

func initialiseLibrary() {
	Library = TBibTeXLibrary{}
	Library.Initialise(Reporting, bibTeXFolder, bibTeXBaseName)
	Library.PreMergeCheck = func(source, target string) {
		if Library.EntryFieldValueity(target, DBLPField) != "" {
			return
		}
		// Do NOT auto-associate/auto-merge here, even when source already has a
		// DBLP key. A shared DBLP key is a strong signal, not proof — this hook
		// fires from any MaybeMergeEntries call, including CheckNeedToMergeForEqualTitles'
		// loose title-hash bucket, which can and does pair up genuinely different
		// publications (e.g. two papers whose titles differ only in case/hyphenation).
		// AssociateDblpKey performs the full merge as a side effect and only ever
		// declines when there's already a conflicting doi/dblp value recorded — a
		// brand-new target never has one, so it always "succeeded", and afterward
		// source/target resolved to the same entry, silently skipping the explicit
		// "Merge these entries?" question below entirely. Confirmed 2026-07-23: this
		// merged two unrelated papers (Schmidt 2006 "Model-driven engineering" /
		// Kent 2002 "Model Driven Engineering") without ever asking. Always fall
		// through to the explicit confirmation instead; if source has no DBLP key,
		// still offer to find one for target first so both sides have DBLP context
		// when that confirmation is shown.
		if Library.EntryFieldValueity(source, DBLPField) == "" {
			maybeFindDBLPCandidates(target)
		}
	}

	// If bib_entries was dirty on the previous run (crash mid-write), advance its
	// timestamp now so that any repaired mapping files (whose timestamps are about
	// to be set to NOW) do not make ValidBibDb() return false and force a re-parse
	// from the stale bib file — which would lose in-DB changes.
	if isTableDirty("bib_entries") {
		skipBibValidation = true
		refreshBibDbTimestamp()
	}

	repairDirtyMappingTables()
}

func loadMappingFiles() {
	Library.ReadAddressMappings()
	Library.CheckAddressMappings()
	Library.ReadKeyHintsFile()
	Library.ReadKeyNonDoublesFile()
	Library.ReadIgnoreTitlesFile()
	Library.ReadNameMappingsFile()
	Library.ReadFieldMappingsFile()
	Library.ReadEntryFieldMappingsFile()
	Library.ReadDblpParentFile()
	Library.ReadDblpWaivedFile()
	Library.ReadMetadataFile()
	Library.ReadEntryFlagsFile()
	Library.CheckFieldMappings()
	Library.CheckNameMappingConsistency()
}

func loadBibFromDb() {
	loadGroupsFromDb(&Library)
	loadCommentsFromDb(&Library)
	buildKeyAliasesFromDb(&Library)
	resolveGroupEntriesKeys(&Library)
	initEntryCache()
	reportCacheMode()
	if contributorRolesActive {
		loadAuthorEditorFieldMappingsFromCache(&Library)
	}
}

// parseSyncBibFile clears the bib tables and re-parses a sync bib file (full mode).
// On success the working DB contains the re-imported entries and bibEntriesModified
// is set so the home DB is updated at close. On failure the working DB is restored
// from the home copy via the safe-parse rollback mechanism.
// doImportBib is the -import_bib entry point: clears all bib tables and re-imports
// from the given bib file (full replace — same semantics as -import_XXX for mapping
// tables). After this, normal runs load from DB only; the bib file is not touched
// again unless -import_bib or -sync is invoked explicitly.
//
// Companion: -upsert_bib upserts entries from a bib file without clearing existing
// ones (entries already in DB are updated; new ones are added; nothing is deleted).
// Unlike -import_bib, -upsert_bib also accepts stdin when no filename is given.
func doImportBib(path string) {
	if !prepareWorkingDatabase() {
		return
	}
	initialiseLibrary()
	Library.ReadKeyOldiesFile()
	loadMappingFiles()
	Library.Progress(ProgressImportingBibFile, path)
	if !parseSyncBibFile(path) {
		return
	}
	buildTitleIndexFromDb(&Library)
	Library.ReportLibrarySize()
}

func parseSyncBibFile(path string) bool {
	Library.NoDBUpdating = false // clear any flag left from a previous operation
	Library.Progress(ProgressReparsingBibFile)
	safeOk := beginSafeParse()
	if !safeOk {
		Library.Warning("Proceeding without safe-parse backup; database not protected during reparse.")
	}
	// Reset in-memory group and comment state before re-parsing so that the
	// additive BibDesk XML reader does not carry over stale memberships from
	// the previous in-memory state (clearBibTables only clears the DB tables).
	Library.GroupEntries = TStringSetMap{}
	Library.Comments = nil
	Library.Progress(ProgressClearingBibTables)
	clearBibTables()
	beginBibTransaction()
	parseCh := make(chan bool, 1)
	go func() { parseCh <- Library.ParseBibFile(path) }()
	parseTicker := Library.NewProgressTicker(ProgressParsingBibFile, 0)
	parseTimeTicker := time.NewTicker(200 * time.Millisecond)
	var parseOk bool
syncParseLoop:
	for {
		select {
		case ok := <-parseCh:
			parseTimeTicker.Stop()
			parseTicker.Done()
			parseOk = ok
			break syncParseLoop
		case <-parseTimeTicker.C:
			parseTicker.SetCount(int(atomic.LoadInt64(&bibParseCount)))
		}
	}
	if !parseOk {
		rollbackBibTransaction()
		if safeOk {
			rollbackSafeParse()
		}
		return false
	}
	saveBibGroupsToDb(&Library)
	saveBibCommentsToDb(&Library)
	commitBibTransaction()
	// Guard: if the parser set NoDBUpdating (unknown entry types, parse errors
	// that did not abort parsing, etc.) treat this as a failed import. Roll back the
	// safe-parse temp DB so the working DB reverts to the pre-parse state and the
	// home DB is not affected.
	if Library.NoDBUpdating {
		Library.Warning("Sync bib re-import aborted: parser reported errors — home database unchanged.")
		if safeOk {
			rollbackSafeParse()
		}
		return false
	}
	if safeOk {
		commitSafeParse()
	}
	bibEntriesModified = true
	setTableDirty("dblp_hierarchy")
	initEntryCache()
	reportCacheMode()
	refreshBibDbTimestamp()
	return true
}

func parseBibIntoDb() bool {
	Library.Progress(ProgressReparsingBibFile)
	safeOk := beginSafeParse()
	if !safeOk {
		Library.Warning("Proceeding without safe-parse backup; database not protected during reparse.")
	}
	Library.GroupEntries = TStringSetMap{}
	Library.Comments = nil
	Library.Progress(ProgressClearingBibTables)
	clearBibTables()
	beginBibTransaction()
	readCh := make(chan bool, 1)
	go func() { readCh <- Library.ReadBib(BibFile) }()
	readTicker := Library.NewProgressTicker(ProgressParsingBibFile, 0)
	readTimeTicker := time.NewTicker(200 * time.Millisecond)
	var readOk bool
bibParseLoop:
	for {
		select {
		case ok := <-readCh:
			readTimeTicker.Stop()
			readTicker.Done()
			readOk = ok
			break bibParseLoop
		case <-readTimeTicker.C:
			readTicker.SetCount(int(atomic.LoadInt64(&bibParseCount)))
		}
	}
	if !readOk {
		rollbackBibTransaction()
		if safeOk {
			rollbackSafeParse()
			endRun(1)
		}
		return false
	}
	saveBibGroupsToDb(&Library)
	saveBibCommentsToDb(&Library)
	commitBibTransaction()

	if safeOk {
		commitSafeParse()
	}

	bibEntriesModified = false // parse is a load, not a modification
	setTableDirty("dblp_hierarchy")
	initEntryCache()
	reportCacheMode()
	refreshBibDbTimestamp()
	return true
}

// sessionStart* capture library metrics at the beginning of a write session so
// that reportHomework() can print deltas at the end.
var (
	sessionStartEntryCount       int
	sessionStartDblpKeyCount     int
	sessionStartFieldMapCount    int
	sessionStartUnresolvedGroups int
	sessionStartDblpCandidates   int
	sessionManualDblpAssignments int // gross count of manually entered DBLP keys this session
)

func countDblpKeyedEntries() int {
	if entryCache != nil {
		n := 0
		for _, e := range entryCache {
			if e.Fields[DBLPField] != "" {
				n++
			}
		}
		return n
	}
	var n int
	bibQueryRow(`SELECT COUNT(*) FROM bib_entries WHERE field = ?`, DBLPField).Scan(&n)
	return n
}

// printSessionStats prints a library-state summary at the start of a write
// session and captures the current counts for the session-change report at end.
func printSessionStats() {
	total := countBibEntries()
	dblpKeys := countDblpKeyedEntries()

	var contributors, contributorForms, nameStrings, entryContribAliases int
	bibQueryRow(`SELECT COUNT(*) FROM contributors`).Scan(&contributors)
	bibQueryRow(`SELECT COUNT(*) FROM contributor_names`).Scan(&contributorForms)
	bibQueryRow(`SELECT COUNT(DISTINCT name_used) FROM entry_contributor_names`).Scan(&nameStrings)
	bibQueryRow(`SELECT COUNT(*) FROM entry_contributor_names ecn
	             JOIN contributors c ON c.id = ecn.contributor_id
	             WHERE ecn.name_used != c.name`).Scan(&entryContribAliases)

	var sameFieldMaps, crossFieldMaps int
	bibQueryRow(`SELECT COUNT(*) FROM field_mappings WHERE source_field = target_field`).Scan(&sameFieldMaps)
	bibQueryRow(`SELECT COUNT(*) FROM field_mappings WHERE source_field != target_field`).Scan(&crossFieldMaps)
	fieldMaps := sameFieldMaps + crossFieldMaps

	var losingValues, losingPending int
	bibQueryRow(`SELECT COUNT(*) FROM superseded_field_values`).Scan(&losingValues)
	// triage_status is only ever set by doTriageAuthorMappings, which only ever looks
	// at author/editor rows — a non-author/editor row is NULL forever, with no command
	// that will ever act on it. Counting those here under "awaiting triage" reads as an
	// actionable backlog that in fact never shrinks; scope to what -triage_author_mappings
	// (and its auto-run at the end of -update_all_dblp_entries) can actually resolve, same
	// as the homework line below.
	bibQueryRow(`SELECT COUNT(*) FROM superseded_field_values WHERE field IN ('author', 'editor') AND triage_status IS NULL`).Scan(&losingPending)

	sessionStartEntryCount = total
	sessionStartDblpKeyCount = dblpKeys
	sessionStartFieldMapCount = fieldMaps
	sessionStartUnresolvedGroups = countUnresolvedGroups()
	sessionStartDblpCandidates = countDblpCandidates()

	entryFlags := 0
	for _, s := range Library.EntryFlags {
		entryFlags += len(s.Elements())
	}

	var ndEntries, ndContributors, ndNames int
	bibQueryRow(`SELECT COUNT(*) FROM non_double_entries`).Scan(&ndEntries)
	bibQueryRow(`SELECT COUNT(*) FROM non_double_contributors`).Scan(&ndContributors)
	bibQueryRow(`SELECT COUNT(*) FROM non_double_contributor_names`).Scan(&ndNames)

	pct := 0.0
	if total > 0 {
		pct = float64(dblpKeys) * 100.0 / float64(total)
	}
	logStatBlock("Library statistics:", []statRow{
		{StatEntries, fmt.Sprintf("%d", total), ""},
		{StatGroups, fmt.Sprintf("%d", len(Library.GroupEntries)), ""},
		{StatPDFFiles, fmt.Sprintf("%d", len(Library.PDFFiles)), ""},
		{StatContributors, fmt.Sprintf("%d", contributors), ""},
		{StatContributorForms, fmt.Sprintf("%d", contributorForms), ""},
		{StatNameSpellings, fmt.Sprintf("%d", nameStrings), ""},
		{StatEntryContributorAliases, fmt.Sprintf("%d", entryContribAliases), ""},
		{StatFieldMappingsSameField, fmt.Sprintf("%d", sameFieldMaps), ""},
		{StatFieldMappingsCrossField, fmt.Sprintf("%d", crossFieldMaps), ""},
		{StatLosingValues, fmt.Sprintf("%d", losingValues), ""},
		{StatLosingValuesPending, fmt.Sprintf("%d", losingPending), ""},
		{StatDblpCoverage, fmt.Sprintf("%.0f%%", pct), ""},
		{StatDblpLinks, fmt.Sprintf("%d", dblpKeys), ""},
		{StatDblpCrossrefOverrides, fmt.Sprintf("%d", Library.DblpParent.Len()), ""},
		{StatDblpWaivedChildren, fmt.Sprintf("%d", Library.DblpWaived.Len()), ""},
		{StatKeyOldies, fmt.Sprintf("%d", Library.KeyOldies.Len()), ""},
		{StatKeyHints, fmt.Sprintf("%d", Library.HintToKey.Len()), ""},
		{StatNonDoubleEntryPairs, fmt.Sprintf("%d", ndEntries), ""},
		{StatNonDoubleContributorPairs, fmt.Sprintf("%d", ndContributors), ""},
		{StatNonDoubleNamePairs, fmt.Sprintf("%d", ndNames), ""},
		{StatEntryFlags, fmt.Sprintf("%d", entryFlags), ""},
	}, false)
}

func openLibraryToUpdate() bool {
	if dbWriteSessionActive {
		return true
	}
	announceStep("Opening the library")
	defer endStep()
	if !prepareWorkingDatabase() {
		return false
	}
	// The service keeps the library loaded between jobs (service_jobs.go). The
	// repairs and checks of the whole library then ran when it was loaded; a
	// job on the kept library does not repeat them (Erik, 2026-11-23).
	reuse := reuseLoadedLibrary(libraryLoadedUpdate)
	if reuse {
		resetLibraryRunState()
		Library.Progress("  Library kept loaded by the service")
	} else {
		preCheckRepair()
	}
	Library.BeginDeferringMessages()
	if !reuse {
		initialiseLibrary()
		Library.ReadKeyOldiesFile()
		loadMappingFiles()
		seedContributorsFromEntries(&Library)
		maybeCleanupOrphanedContributors(&Library)
	}

	if reuse {
		// loaded by an earlier job
	} else if skipBibValidation || Library.ValidBibDb() {
		buildTitleIndexFromDb(&Library)
		repairDblpData()
		loadBibFromDb()
		if skipBibValidation {
			// Crash recovery: bib_entries was dirty on the previous run.
			// DB is already the primary — no bib file write needed; just clear dirty state.
			clearTableDirty("bib_entries")
			refreshBibDbTimestamp()
			skipBibValidation = false
		}
	} else {
		// No bib_entries yet. Bootstrap: if the canonical .bib file is present, import it now.
		bibPath := libraryBase() + BibFileExtension
		if FileExists(bibPath) {
			Library.Progress(ProgressImportingBibFile, bibPath)
			if !parseSyncBibFile(bibPath) {
				return false
			}
			buildTitleIndexFromDb(&Library)
		} else {
			messageln("Database not initialised — run '-import_bib <file.bib>' to import the bib file first.")
			return false
		}
	}

	if !reuse {
		cleanupIgnoredTitleNonDoubles(&Library)
		Library.LoadPDFFiles()
		markLibraryLoaded(libraryLoadedUpdate)
	}
	if !skipStartupChecks && !reuse {
		Library.CheckDblpDuplicates()
		Library.CheckKeyOldiesConsistency()
		Library.CheckKeyHintsConsistency()
		Library.CheckKeyOldieHintOverlap()
		Library.CheckGarbledContributorFlags()
		Library.CheckDblpWaivedConsistency()
		Library.CheckEntryFieldMappingWinners()
	}
	if !reuse {
		normalizeAuthorEditorEntryFields()
		retireResolvedAuthorEditorSuperseded()
		cleanupRedundantSuperseded()
	}
	// The statistics and the homework count the whole library: on the kept
	// library only for the check itself.
	report := !reuse || serviceJobCommand == "check"
	if report {
		printSessionStats()
	}
	Library.FlushDeferredMessages()
	if !Online {
		Library.Progress("  Offline: actions requiring network connectivity will be skipped.")
	}
	if report {
		preCloseHook = reportHomework
	}
	return true
}

func openLibraryToReport() bool {
	// Read-only session: suppress all routine progress; show only warnings and questions.
	Reporting.progressSuppressed = true
	warnIfLibraryStampNewer()
	if reuseLoadedLibrary(libraryLoadedReport) {
		resetLibraryRunState() // the service keeps the library loaded (service_jobs.go)
		return true
	}
	initialiseLibrary() // propagates progressSuppressed via Library.Initialise(Reporting, ...)
	Library.ReadKeyOldiesFile()
	loadMappingFiles()

	if skipBibValidation || Library.ValidBibDb() {
		loadBibFromDb()
		if skipBibValidation {
			clearTableDirty("bib_entries")
			refreshBibDbTimestamp()
			skipBibValidation = false
		}
	} else {
		messageln("Database not initialised — run '-import_bib <file.bib>' to import the bib file first.")
		return false
	}

	Library.LoadPDFFiles()
	markLibraryLoaded(libraryLoadedReport)
	return true
}

func doC1Checks(key string) bool {
	merged := Library.CheckNeedToMergeForEqualTitles(key)
	Library.CheckNeedToSplitBookishEntry(key)
	return merged
}

// doC2Checks runs DBLP sync for key and returns whether it modified the entry,
// plus how many DBLP children were inspected along the way (see stopC2Tracking).
func doC2Checks(key string) (modified bool, childrenChecked int) {
	startC2Tracking()
	Library.CheckDBLP(key)
	return stopC2Tracking()
}

func doC3Checks(key string) {
	Library.CheckEntry(Library.buildEntry(key))
}

// normalizeDblpKey strips a leading "DBLP:" prefix so that callers can pass
// keys either way and the internal KeyForDBLP wrapper adds it exactly once.
func normalizeDblpKey(key string) string {
	return strings.TrimPrefix(key, "DBLP:")
}

// findLibraryEqualWithDblp looks for an existing library entry that shares the
// same title index as key and already has a dblp field.  When such a peer is
// found, MaybeMergeEntries is called interactively; if the merge is accepted
// the function returns true (key now has a dblp field via the merge).
func findLibraryEqualWithDblp(key string) bool {
	title := Library.EntryFieldValueity(Library.MapEntryKey(key), TitleField)
	if title == "" {
		return false
	}
	titleIdx := TeXStringIndexer(title)
	if titleIdx == "" {
		return false
	}
	peers := Library.TitleIndex[titleIdx]
	if peers.Size() <= 1 {
		return false
	}
	for _, peer := range peers.ElementsSorted() {
		peer = Library.MapEntryKey(peer)
		mappedKey := Library.MapEntryKey(key)
		if peer == mappedKey {
			continue
		}
		if Library.EntryFieldValueity(peer, DBLPField) == "" {
			continue
		}
		if Library.NonDoubleEntries[mappedKey].Set().Contains(peer) {
			continue
		}
		if Library.EvidenceForBeingDifferentEntries(mappedKey, peer) {
			continue
		}
		Library.MaybeMergeEntries(key, peer)
		if Library.EntryFieldValueity(Library.MapEntryKey(key), DBLPField) != "" {
			return true
		}
	}
	return false
}

// maybeFindDBLPCandidates searches the DBLP title index for a library entry
// that has no dblp field and interactively offers the user numbered candidates.
// Pick 0 → all candidates are recorded as non-doubles with the library entry.
// Pick N → AssociateDblpKey is called and the function returns true so the
// dblpFilterYear returns the year to use for DBLP candidate year-range filtering
// and whether it is valid. Falls back through: year field → crossref parent year →
// year embedded in preferredalias.
func dblpFilterYear(key string) (int, bool) {
	yearStr := Library.EntryFieldValueity(key, "year")
	if yearStr == "" {
		if crossref := Library.EntryFieldValueity(key, "crossref"); crossref != "" {
			yearStr = Library.EntryFieldValueity(crossref, "year")
		}
	}
	if yearStr == "" {
		alias := Library.EntryFieldValueity(key, PreferredAliasField)
		if m := reYearInAlias.FindString(alias); m != "" {
			yearStr = m
		}
	}
	y, err := strconv.Atoi(yearStr)
	return y, err == nil
}

// dblpCandidateInYearRange reports whether the DBLP candidate c falls within
// the ±3-year window of the library entry's year (from dblpFilterYear).
// When the library entry has no resolvable year, all candidates are accepted.
func dblpCandidateInYearRange(c string, entryYear int, hasYear bool) bool {
	if !hasYear {
		return true
	}
	ce := dblpEntryFromStore(c)
	if ce == nil {
		return true
	}
	candYear, err := strconv.Atoi(ce.Fields["year"])
	if err != nil {
		return true
	}
	diff := candYear - entryYear
	return diff > -3 && diff < 3
}

// maybeFindDBLPCandidatesExcluding searches the DBLP title index for a library
// entry that has no dblp field and interactively offers the user numbered
// candidates. extraExclusions contains DBLP path strings (e.g.
// "conf/foo/Bar2022") already claimed by sibling variations in the same title
// bucket; those are filtered out so the same DBLP record is never offered to
// two different variations. Pick 0 → all candidates are recorded as non-doubles
// with the library entry. Pick N → AssociateDblpKey is called and the function
// returns true so the caller can re-run DBLP checks for the now-associated entry.
func maybeFindDBLPCandidatesExcluding(key string, extraExclusions TStringSet) bool {
	if Library.EntryFieldValueity(key, DBLPField) != "" {
		return false
	}
	title := Library.EntryFieldValueity(key, TitleField)
	hash := libraryTitleHash(title)
	if hash == "" {
		return false
	}
	allCandidates := readDblpTitleLinks(hash)
	existing := Library.NonDoubleEntries[key]
	entryYear, hasYear := dblpFilterYear(key)
	var candidates []string
	var yearFiltered []string
	for _, c := range allCandidates {
		// AddNonDoubleEntries stores both sides through MapEntryKey, so when this
		// candidate's DBLP key is already claimed by another library entry (e.g. a
		// near-duplicate that already has dblp set), what's actually recorded is
		// (key, thatOtherEntry) — not (key, "DBLP:..."). Checking membership with
		// the same mapping keeps this consistent with what was stored; otherwise a
		// dismissal silently never matches and the candidate is offered again on
		// every later run.
		if existing.Contains(Library.MapEntryKey(KeyForDBLP(c))) {
			continue
		}
		if extraExclusions.Contains(c) {
			Library.AddNonDoubleEntries(key, KeyForDBLP(c))
			continue
		}
		if !dblpCandidateInYearRange(c, entryYear, hasYear) {
			yearFiltered = append(yearFiltered, c)
			continue
		}
		candidates = append(candidates, c)
	}
	if len(candidates) == 0 {
		// All remaining candidates were rejected by the year filter. Auto-dismiss
		// them into non-doubles so the homework count stops counting this entry.
		for _, c := range yearFiltered {
			Library.AddNonDoubleEntries(key, KeyForDBLP(c))
		}
		if len(yearFiltered) > 0 {
			flushWorkingDbToHome()
		}
		return false
	}

	// For bookish library entries, also offer the DBLP parent proceedings of each
	// candidate as a direct association target (prepended so they appear first).
	if BibTeXBookish.Contains(Library.EntryType(key)) {
		parentsSeen := map[string]bool{}
		var parentKeys []string
		for _, c := range candidates {
			if ce := dblpEntryFromStore(c); ce != nil {
				if crossref := ce.Fields["crossref"]; crossref != "" &&
					!existing.Contains(KeyForDBLP(crossref)) &&
					!extraExclusions.Contains(crossref) &&
					!parentsSeen[crossref] {
					parentsSeen[crossref] = true
					parentKeys = append(parentKeys, crossref)
				}
			}
		}
		candidates = append(parentKeys, candidates...)
	}

	if len(candidates) > 9 {
		candidates = candidates[:9]
	}
	chosen := Library.AskCandidateDblpKey(key, candidates)
	if chosen == "" {
		for _, c := range candidates {
			Library.AddNonDoubleEntries(key, KeyForDBLP(c))
			extraExclusions.Add(c)
		}
		flushWorkingDbToHome()
		return false
	}
	Library.AssociateDblpKey(key, chosen)
	return true
}

func maybeFindDBLPCandidates(key string) bool {
	return maybeFindDBLPCandidatesExcluding(key, TStringSetNew())
}

// doVariationSetDblpLinking links each variation in the list to DBLP, excluding
// DBLP keys already claimed by other variations in the same title bucket. After a
// successful link, the newly claimed key is added to the exclusion set so it cannot
// be offered to a later variation.
func doVariationSetDblpLinking(variations []string) {
	usedDblp := TStringSetNew()
	for _, v := range variations {
		if d := Library.EntryFieldValueity(Library.MapEntryKey(v), DBLPField); d != "" {
			usedDblp.Add(normalizeDblpKey(d))
		}
	}
	for _, v := range variations {
		v = Library.MapEntryKey(v)
		if !Library.EntryExists(v) {
			continue
		}
		if Library.EntryFieldValueity(v, DBLPField) != "" {
			continue
		}
		found := findLibraryEqualWithDblp(v)
		if !found {
			found = maybeFindDBLPCandidatesExcluding(v, usedDblp)
		}
		v = Library.MapEntryKey(v)
		if found {
			if d := Library.EntryFieldValueity(v, DBLPField); d != "" {
				usedDblp.Add(normalizeDblpKey(d))
			}
		}
	}
}

func doAllChecks(key string) {
	merged := doC1Checks(key)
	key = Library.MapEntryKey(key)
	if merged {
		// A merge group was resolved. Handle ALL surviving canonical entries in the
		// title bucket — there may be multiple distinct survivors (different publications
		// with the same title). Each one without a DBLP key gets a DBLP search and,
		// when that finds nothing, a manual-entry prompt.
		title := Library.EntryFieldValueity(key, TitleField)
		if title != "" {
			titleBucket := Library.TitleIndex[TeXStringIndexer(title)]
			seen := map[string]bool{}
			for _, s := range (&titleBucket).ElementsSorted() {
				s = Library.MapEntryKey(s)
				if seen[s] {
					continue
				}
				seen[s] = true
				if !Library.EntryExists(s) {
					continue
				}
				if Library.EntryFieldValueity(s, DBLPField) != "" {
					continue
				}
				if !maybeFindDBLPCandidates(s) {
					messagef("%s\n", Library.EntryString(s, "", "  "))
					if dblpKey, err := Reporting.AskForInput(fmt.Sprintf("No DBLP match found after merge for %s — enter DBLP key or Enter to skip", s)); err == nil && dblpKey != "" {
						Library.AssociateDblpKey(s, dblpKey)
					}
				}
			}
		}
	} else {
		maybeFindDBLPCandidates(key)
	}
	doC2Checks(key)
	doC3Checks(key)
}

func cleanKey(rawKey string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(rawKey, "\\cite{", ""), "cite{", ""), "}", ""))
}

var BibFile string

// The lock files are held open for the lifetime of the process to keep their
// flocks. Each needs its own variable: when one variable held both, taking the
// DBLP lock dropped the last reference to the instance lock file, and the garbage
// collector could then close it, releasing that lock during a long -update_dblp.
var (
	instanceLockFile *os.File
	dblpLockFile     *os.File
)

// flockPath opens lockPath and obtains an exclusive non-blocking flock on it.
// Exits immediately if another instance already holds that lock.
// The OS releases the lock automatically when the process exits.
// loadMaps loads the hand-maintained maps (kept in the database, map_files).
func loadMaps() {
	loadMapContentsFromDB()
	loadUnicodeMap(localDBFolder + "unicode_map.csv")
	loadHtmlCommandsMap(localDBFolder + "html_commands_map.csv")
	loadHtmlCharacterMap(localDBFolder + "html_character_map.csv")
	loadLatexIndexerMap(localDBFolder + "latex_indexer.csv")
	loadHarvestIgnoreKeys(localDBFolder + "harvest_ignore_keys.csv")
}

// rescanModifierArgs takes the modifier flags that follow positional arguments
// (Go's flag parser stops at the first positional one, e.g. -entry_key_alias
// key -map) out of args, and sets them.
func rescanModifierArgs(args []string) []string {
	var filtered []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-log", "--log", "-l", "--l":
			if i+1 < len(args) {
				logFilePath = args[i+1]
				i++
			}
		case "-map", "--map":
			cmdMap = true
		case "-use_aliases", "--use_aliases":
			cmdUseAliases = true
		default:
			filtered = append(filtered, a)
		}
	}
	return filtered
}

func flockPath(lockPath string) *os.File {
	os.MkdirAll(filepath.Dir(lockPath), 0755) //nolint:errcheck // OpenFile reports a failure
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		messagef("Could not open lock file %s: %s\n", lockPath, err)
		endRun(1)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		messagef("Another instance is already running (%s)\n", lockPath)
		endRun(1)
	}
	return f
}

func acquireInstanceLock() {
	instanceLockFile = flockPath(localDBFolder + bibTeXBaseName + LockFileExtension)
}

func acquireDblpLock() {
	dblpLockFile = flockPath(dblpFolder() + "dblp" + LockFileExtension)
}

// requireNoDblpImport exits with a clear message if a DBLP import is currently
// in progress in another process (i.e. the DBLP lock file is held).
func requireNoDblpImport() {
	lockPath := dblpFolder() + "dblp" + LockFileExtension
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return // can't tell; let the command proceed
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		messageln("DBLP import in progress — try again later.")
		endRun(1)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint — check only, not holding
}

// cleanKeys strips \cite{} wrappers from each arg and splits comma-joined cite
// lists, returning the individual key strings.
func cleanKeys(args []string) []string {
	var result []string
	for _, a := range args {
		for _, k := range strings.Split(cleanKey(a), ",") {
			if k != "" {
				result = append(result, k)
			}
		}
	}
	return result
}

// normalizeAuthorEditorEntryFields applies current name mappings to all stored author/editor
// field values in bib_entries. Any entry whose stored value contains at least one mapped alias
// is updated in-place; the old value is recorded as superseded in superseded_field_values.
// Called at startup so that name mappings added between runs are immediately propagated.
func normalizeAuthorEditorEntryFields() {
	if len(Library.NameAliasToName) == 0 {
		return
	}

	rows, err := db.Query(`SELECT entry_key, field, value FROM bib_entries WHERE field IN ('author', 'editor')`)
	if err != nil {
		return
	}
	type fieldRow struct{ key, field, value string }
	var fields []fieldRow
	for rows.Next() {
		var r fieldRow
		if rows.Scan(&r.key, &r.field, &r.value) == nil {
			fields = append(fields, r)
		}
	}
	rows.Close()

	updated := 0
	for _, r := range fields {
		parts := strings.Split(r.value, " and ")
		changed := false
		for i, name := range parts {
			name = strings.TrimSpace(name)
			if canonical, ok := Library.NameAliasToName[name]; ok {
				parts[i] = canonical
				changed = true
			}
		}
		if !changed {
			continue
		}
		newValue := strings.Join(parts, " and ")
		dbExecSave("normalizeAuthorEditorEntryFields",
			`INSERT INTO superseded_field_values (entry_key, field, value) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, r.key, r.field, r.value)
		Library.SetEntryFieldValue(r.key, r.field, newValue)
		updated++
	}
	if updated > 0 {
		Library.Progress("Normalized %d author/editor field(s) via name mappings", updated)
	}
}

// retireResolvedAuthorEditorSuperseded deletes superseded_field_values rows for author/editor
// fields where the superseded value is equal to the current entry winner after applying
// name mappings. Called at startup so that name mappings added outside of triage
// automatically cascade to clean up resolved pairs.
func retireResolvedAuthorEditorSuperseded() {
	rows, err := db.Query(`SELECT entry_key, field, value FROM superseded_field_values WHERE field IN ('author', 'editor')`)
	if err != nil {
		return
	}
	type supersededRow struct{ key, field, superseded string }
	var pairs []supersededRow
	for rows.Next() {
		var r supersededRow
		if rows.Scan(&r.key, &r.field, &r.superseded) == nil {
			pairs = append(pairs, r)
		}
	}
	rows.Close()

	splitAndFilter := func(value string) []string {
		var out []string
		for _, n := range strings.Split(value, " and ") {
			n = strings.TrimSpace(n)
			lc := strings.ToLower(n)
			if n != "" && lc != "others" && lc != "et.al." && lc != "et al." {
				out = append(out, n)
			}
		}
		return out
	}

	normList := func(names []string) []string {
		result := make([]string, len(names))
		for i, n := range names {
			if canonical, ok := Library.NameAliasToName[n]; ok {
				result[i] = canonical
			} else {
				result[i] = n
			}
		}
		return result
	}

	sliceEqual := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	retired := 0
	for _, p := range pairs {
		winner := Library.EntryFieldValueity(p.key, p.field)
		if winner == "" {
			continue
		}
		if sliceEqual(normList(splitAndFilter(winner)), normList(splitAndFilter(p.superseded))) {
			dbExecSave("retireResolvedAuthorEditorSuperseded",
				`DELETE FROM superseded_field_values WHERE entry_key=? AND field=? AND value=?`, p.key, p.field, p.superseded)
			retired++
		}
	}
	if retired > 0 {
		Library.Progress("  Retired %d resolved author/editor pair(s) via name mappings", retired)
	}
}

// --- Command functions ---

// doTriageAuthorMappings interactively reviews all author/editor pairs in superseded_field_values.
// Each pair is classified and resolved: brace-wrap (auto-fixed), name-equal-after-mapping
// (auto-retired), single-name variant (name-mapping offered), missing-authors (prompt),
// multi-name variant (kept), or unclassifiable (flagged).
func doTriageAuthorMappings() {
	if !openLibraryToUpdate() {
		return
	}

	maybeMigrateSkippedToNonDoubleContributorNames(&Library)
	loadNonDoubleContributorNamesFromDb(&Library)

	rows, err := bibQuery(`SELECT entry_key, field, value FROM superseded_field_values WHERE field IN ('author', 'editor') AND triage_status IS NULL ORDER BY entry_key, field`)
	if err != nil {
		// Index corruption (e.g. from old multi-connection SQLite access) makes
		// ORDER BY fail with SQLITE_CORRUPT. Rebuild the index and retry once.
		bibExec(`REINDEX superseded_field_values`) //nolint:errcheck
		rows, err = bibQuery(`SELECT entry_key, field, value FROM superseded_field_values WHERE field IN ('author', 'editor') AND triage_status IS NULL ORDER BY entry_key, field`)
	}
	if err != nil {
		Library.Warning("Could not query superseded_field_values: %s", err)
		return
	}
	type triagePair struct{ key, field, superseded string }
	var pairs []triagePair
	for rows.Next() {
		var p triagePair
		if scanErr := rows.Scan(&p.key, &p.field, &p.superseded); scanErr == nil {
			pairs = append(pairs, p)
		}
	}
	rows.Close()

	if len(pairs) == 0 {
		return
	}
	stderrPrintf("\n")

	retireSuperseded := func(key, field, superseded string) {
		if err := bibExec(`DELETE FROM superseded_field_values WHERE entry_key=? AND field=? AND value=?`, key, field, superseded); err != nil {
			// Previously silent — see the matching comment in TKeyAliasTable.Load.
			Library.Warning("retireSuperseded: delete failed (key=%s, field=%s): %s", key, field, err)
			markDbWriteFailed(fmt.Sprintf("retireSuperseded: delete failed (key=%s, field=%s, value=%s): %s", key, field, superseded, err))
		}
	}

	markKept := func(key, field, superseded string) {
		if err := bibExec(`UPDATE superseded_field_values SET triage_status='kept' WHERE entry_key=? AND field=? AND value=?`, key, field, superseded); err != nil {
			// Previously silent — see the matching comment in TKeyAliasTable.Load.
			Library.Warning("markKept: update failed (key=%s, field=%s): %s", key, field, err)
			markDbWriteFailed(fmt.Sprintf("markKept: update failed (key=%s, field=%s, value=%s): %s", key, field, superseded, err))
		}
	}

	splitOnAnd := func(value string) []string {
		parts := strings.Split(value, " and ")
		var out []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			lc := strings.ToLower(p)
			if lc != "others" && lc != "et.al." && lc != "et al." {
				out = append(out, p)
			}
		}
		return out
	}

	normalizeName := func(name string) string {
		if canonical, ok := Library.NameAliasToName[name]; ok {
			return canonical
		}
		return name
	}

	isBraceWrapped := func(value string) bool {
		return len(value) >= 2 && value[0] == '{' && value[len(value)-1] == '}' &&
			strings.Contains(value[1:len(value)-1], " and ")
	}

	normList := func(names []string) []string {
		result := make([]string, len(names))
		for i, n := range names {
			result[i] = normalizeName(n)
		}
		return result
	}

	sliceEqual := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

outer:
	for _, p := range pairs {
		winner := Library.EntryFieldValueity(p.key, p.field)
		if winner == "" {
			retireSuperseded(p.key, p.field, p.superseded)
			continue
		}

		Library.ResetQuestionFlag()

		// Re-check: a name mapping added during this run may have already equated them.
		wNames := splitOnAnd(winner)
		lNames := splitOnAnd(p.superseded)
		if sliceEqual(normList(wNames), normList(lNames)) {
			retireSuperseded(p.key, p.field, p.superseded)
			continue
		}

		// Brace-wrap: either side has outer braces containing " and "
		if isBraceWrapped(winner) {
			fixed := winner[1 : len(winner)-1]
			Library.SetEntryFieldValue(p.key, p.field, fixed)
			Library.Progress("Auto-fixed brace-wrap for %s %s", p.key, p.field)
			retireSuperseded(p.key, p.field, p.superseded)
			continue
		}
		if isBraceWrapped(p.superseded) {
			Library.Progress("Auto-retired brace-wrap superseded value for %s %s", p.key, p.field)
			retireSuperseded(p.key, p.field, p.superseded)
			continue
		}

		normW := normList(wNames)
		normL := normList(lNames)

		if len(wNames) == len(lNames) {
			var diffPos []int
			for i := range normW {
				// The same name in the other order ("Santos, Ricardo" / "Ricardo
				// Santos") is no difference: the current form stays, no question.
				if normW[i] != normL[i] && !sameNameOtherOrder(wNames[i], lNames[i]) {
					diffPos = append(diffPos, i)
				}
			}
			if len(diffPos) == 0 {
				retireSuperseded(p.key, p.field, p.superseded)
				continue
			}
			if len(diffPos) == 1 {
				pos := diffPos[0]
				if isNonDoubleContributorNamePair(&Library, wNames[pos], lNames[pos]) {
					markKept(p.key, p.field, p.superseded)
					continue
				}
				if _, ok := Library.autoResolveByORCIDSide(p.key, p.field, wNames[pos], lNames[pos]); ok {
					retireSuperseded(p.key, p.field, p.superseded)
					continue
				}
				Library.Progress("\nEntry: %s / field: %s\n  Current:    %s\n  Superseded: %s", p.key, p.field, winner, p.superseded)
				resultName, quit, mapped := Library.resolveNamePair(p.key, p.field, pos+1, len(wNames), 1, 1, wNames[pos], lNames[pos])
				if quit {
					break outer
				}
				if mapped {
					retireSuperseded(p.key, p.field, p.superseded)
				} else {
					addNonDoubleContributorNamePair(&Library, wNames[pos], lNames[pos])
					if contributorRolesActive {
						if wID, wOK := resolveNameToContributorID(&Library, wNames[pos]); wOK {
							if lID, lOK := resolveNameToContributorID(&Library, lNames[pos]); lOK {
								addNonDoubleContributorPair(wID, lID)
							}
						}
					}
					markKept(p.key, p.field, p.superseded)
				}
				_ = resultName
			} else {
				Library.Progress("Multi-name variant (%d diffs) kept: %s %s", len(diffPos), p.key, p.field)
				markKept(p.key, p.field, p.superseded)
			}
		} else {
			wSet := make(map[string]bool, len(normW))
			for _, n := range normW {
				wSet[n] = true
			}
			lSet := make(map[string]bool, len(normL))
			for _, n := range normL {
				lSet[n] = true
			}
			isSubset := func(smaller, larger map[string]bool) bool {
				for k := range smaller {
					if !larger[k] {
						return false
					}
				}
				return true
			}
			switch {
			case isSubset(lSet, wSet):
				// Superseded (older/losing) value is a strict subset of the current
				// value — current already has every name it had, plus more. Nothing
				// to review; retire the now-fully-subsumed superseded value.
				Library.Progress("Auto-retired subsumed author/editor variant for %s %s", p.key, p.field)
				retireSuperseded(p.key, p.field, p.superseded)
			case isSubset(wSet, lSet):
				// Current value is a strict subset of the superseded one — the
				// superseded value has co-author(s) the current value is missing
				// (e.g. a DBLP challenge with an extra author that was previously
				// rejected/left unresolved). Silently keeping the incomplete list
				// forever is exactly the "missing co-author" gap; ask instead.
				ynOptions := TStringSetNew()
				ynOptions.Add("y", "n")
				about := TQuestion{Key: p.key, Field: p.field, Values: []TQuestionValue{
					{Role: "current", Value: winner, Note: "fewer names"}, {Role: "superseded", Value: p.superseded, Note: "more names"}},
					Options: []TQuestionOption{{Key: "y", Label: "adopt the fuller list"}, {Key: "n", Label: "keep the current list"}}}
				switch Library.warningQuestionCore(&about, "Adopt the fuller author/editor list?", ynOptions,
					"Superseded %s value for %s has name(s) not present in the current value — looks like a missing co-author", true, p.field, p.key) {
				case "y":
					Library.SetEntryFieldValue(p.key, p.field, p.superseded)
					Library.Progress("Adopted fuller author/editor list for %s %s", p.key, p.field)
					retireSuperseded(p.key, p.field, p.superseded)
				case "n":
					markKept(p.key, p.field, p.superseded)
				default:
					// "q" (or anything else): no decision was made — do not mark
					// kept, leave it genuinely pending for the next run.
				}
				if Reporting.QuitWasRequested() {
					break outer
				}
			default:
				// Neither is a subset of the other — a genuine mix of additions and
				// removals, not a clean "one extra/missing name" case. No automatic
				// resolution is safe here; keep as before.
				markKept(p.key, p.field, p.superseded)
			}
		}

		if Reporting.QuitWasRequested() {
			break outer
		}
	}
	reportORCIDSideAutoResolves()
}

// reportSkippedNameMappings appends the name mappings skipped as garbled during the
// ORCID enrichment to orcid_skipped_names.log, and reports their count.
func reportSkippedNameMappings(lines []string) {
	if len(lines) == 0 {
		return
	}
	logPath := libraryBase() + tablesFolderSuffix + "/orcid_skipped_names.log"
	os.MkdirAll(libraryBase()+tablesFolderSuffix, 0o755) //nolint:errcheck
	if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintf(f, "# %s\n%s\n", time.Now().Format("2006-01-02 15:04:05"), strings.Join(lines, "\n"))
		f.Close()
	}
	Library.Progress("ORCID name forms skipped as garbled (lists, titles): %d (see orcid_skipped_names.log; Tables, Name mappings in the web UI takes one anyway).", len(lines))
}

// reportORCIDSideAutoResolves reports the name questions autoResolveByORCIDSide
// answered, with the details in orcid_side_auto_resolves.log.
func reportORCIDSideAutoResolves() {
	lines := Library.orcidSideAutoResolves
	if len(lines) == 0 {
		return
	}
	logPath := libraryBase() + tablesFolderSuffix + "/orcid_side_auto_resolves.log"
	os.MkdirAll(libraryBase()+tablesFolderSuffix, 0o755) //nolint:errcheck
	// Appended, with the date: each run's answers stay, for an audit (30.39; the
	// file was overwritten, so the Campbell merge of 2026-11-22 left no trace).
	if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintf(f, "# %s\n%s\n", time.Now().Format("2006-01-02 15:04:05"), strings.Join(lines, "\n"))
		f.Close()
	}
	Library.Progress("  Answered by the name with the ORCID: %d name question(s) (see orcid_side_auto_resolves.log).", len(lines))
	Library.orcidSideAutoResolves = nil
}

func doTriageContributorAliases() {
	if !openLibraryToUpdate() {
		return
	}

	type aliasPair struct {
		alias, id string
		count     int
	}
	rows, err := db.Query(`
		SELECT ecn.name_used, ecn.contributor_id, COUNT(*) AS cnt
		FROM entry_contributor_names ecn
		WHERE NOT EXISTS (
		    SELECT 1 FROM contributor_names cn
		    WHERE cn.id = ecn.contributor_id AND cn.name = ecn.name_used
		)
		GROUP BY ecn.name_used, ecn.contributor_id
		ORDER BY ecn.name_used`)
	if err != nil {
		Library.Warning("Could not query entry_contributor_names: %s", err)
		return
	}
	var pairs []aliasPair
	for rows.Next() {
		var p aliasPair
		if rows.Scan(&p.alias, &p.id, &p.count) == nil {
			pairs = append(pairs, p)
		}
	}
	rows.Close()
	if len(pairs) == 0 {
		Library.Progress("No entry-specific contributor aliases pending triage.")
		return
	}

	options := TStringSetNew()
	options.Add("g", "k", "q")
	generalised := 0
	suffix := func(n int) string {
		if n == 1 {
			return "entry"
		}
		return "entries"
	}
outer:
	for _, p := range pairs {
		contrib := Library.ContributorByID[p.id]
		if contrib == nil {
			continue
		}
		Library.ResetQuestionFlag()

		// {others} is BibTeX's "et al." convention — always auto-generalise to "others".
		if p.alias == "{others}" && contrib.Name == "others" {
			upsertContributorNameToDB(p.id, p.alias)
			Library.NameToContributorID[p.alias] = p.id
			Library.FindAliases(contrib.Name, p.alias)
			dbExecSave("doTriageContributorAliases: generalise",
				`DELETE FROM entry_contributor_names WHERE name_used = ? AND contributor_id = ?`, p.alias, p.id)
			generalised++
			continue
		}

		answer := Library.WarningQuestionAbout(TQuestion{Text: "Generalise the alias, or keep it local?",
			Options: []TQuestionOption{{Key: "g", Label: "generalise"}, {Key: "k", Label: "keep local"}}},
			"Generalise alias (g), keep local (k), quit (q)?",
			options,
			"Entry-specific alias %q → contributor %q (%d %s)",
			p.alias, contrib.Name, p.count, suffix(p.count))
		switch answer {
		case "g":
			upsertContributorNameToDB(p.id, p.alias)
			Library.NameToContributorID[p.alias] = p.id
			Library.FindAliases(contrib.Name, p.alias)
			dbExecSave("doTriageContributorAliases: generalise",
				`DELETE FROM entry_contributor_names WHERE name_used = ? AND contributor_id = ?`, p.alias, p.id)
			generalised++
		case "k":
			// no action — alias remains entry-specific
		case "q":
			break outer
		}
		if Reporting.QuitWasRequested() {
			break outer
		}
	}
	if generalised > 0 {
		Library.Progress("Generalised %d contributor alias(es) to global contributor_names.", generalised)
	}
}

func doDisambiguateContributors() {
	if !openLibraryToUpdate() {
		return
	}

	type ambigRow struct {
		key, role string
		position  int
		id, alias string
	}
	rows, err := db.Query(`
		SELECT entry_key, role, position, contributor_id, name_used
		FROM entry_contributor_names
		ORDER BY name_used, entry_key, role, position`)
	if err != nil {
		Library.Warning("Could not query entry_contributor_names: %s", err)
		return
	}
	var ambig []ambigRow
	for rows.Next() {
		var r ambigRow
		if rows.Scan(&r.key, &r.role, &r.position, &r.id, &r.alias) == nil {
			if _, ok := Library.AmbiguousNameToContributorIDs[r.alias]; ok {
				ambig = append(ambig, r)
			}
		}
	}
	rows.Close()
	if len(ambig) == 0 {
		Library.Progress("No ambiguous contributor assignments found.")
		absorbDblpNamesCore()
		absorbDblpOrcidsCore()
		return
	}

outer:
	for _, r := range ambig {
		candidates := Library.AmbiguousNameToContributorIDs[r.alias]
		if len(candidates) == 0 {
			continue
		}
		current := Library.ContributorByID[r.id]
		if current == nil {
			continue
		}
		// Before offering the choice, check whether any candidate without a DblpKey
		// has entries with potential DBLP matches. Resolving those first may
		// automatically collapse the ambiguity via absorbDblpNamesCore.
		for _, candID := range candidates {
			cand := Library.ContributorByID[candID]
			if cand == nil || cand.DblpKey != "" {
				continue
			}
			noCandRows, qErr := db.Query(`
				SELECT DISTINCT cr.entry_key FROM contributor_roles cr
				WHERE cr.contributor_id = ?
				  AND NOT EXISTS (
				      SELECT 1 FROM bib_entries be
				      WHERE be.entry_key = cr.entry_key AND be.field = ?
				  )`, candID, DBLPField)
			if qErr != nil {
				continue
			}
			var entriesWithCandidates []string
			for noCandRows.Next() {
				var entryKey string
				if noCandRows.Scan(&entryKey) == nil {
					hash := libraryTitleHash(Library.EntryFieldValueity(entryKey, TitleField))
					if hash == "" {
						continue
					}
					existing := Library.NonDoubleEntries[entryKey]
					for _, c := range readDblpTitleLinks(hash) {
						if !existing.Set().Contains(KeyForDBLP(c)) {
							entriesWithCandidates = append(entriesWithCandidates, entryKey)
							break
						}
					}
				}
			}
			noCandRows.Close()
			if len(entriesWithCandidates) == 0 {
				continue
			}
			Library.Progress("  Contributor %s has %d entry/ies with potential DBLP match(es). "+
				"Resolving these first may eliminate this ambiguity automatically.",
				candID, len(entriesWithCandidates))
			Library.ResetQuestionFlag()
			if !Library.WarningYesNoQuestion("Fix DBLP for these entries now?",
				"Contributor %s has %d entry/ies with potential DBLP match(es); resolving these first may eliminate the ambiguity of %q",
				candID, len(entriesWithCandidates), r.alias) {
				continue
			}
			exclusions := TStringSetNew()
			for _, key := range entriesWithCandidates {
				key = Library.MapEntryKey(key)
				if !Library.EntryExists(key) {
					continue
				}
				found := findLibraryEqualWithDblp(key)
				if !found {
					found = maybeFindDBLPCandidatesExcluding(key, exclusions)
				}
				key = Library.MapEntryKey(key)
				if found {
					doAllChecks(key)
					key = Library.MapEntryKey(key)
					if d := Library.EntryFieldValueity(key, DBLPField); d != "" {
						exclusions.Add(normalizeDblpKey(d))
					}
				}
			}
			// Re-run DBLP name absorption so the contributor gets a DblpKey if
			// the newly linked entries now match a DBLP person entry.
			absorbDblpNamesCore()
			absorbDblpOrcidsCore()
			// Re-check: if the ambiguity has collapsed (only 1 candidate remains or
			// all candidates now point to the same contributor), skip the dialog.
			newCandidates := Library.AmbiguousNameToContributorIDs[r.alias]
			if len(newCandidates) <= 1 {
				Library.Progress("Ambiguity for %q resolved automatically after DBLP linking.", r.alias)
				continue outer
			}
			// Refresh candidates for the choice below.
			candidates = newCandidates
		}

		// Pre-compute "best" candidate for the merge option so the user can see it.
		bestIdx := 1
		bestScore := -1
		for i, candID := range candidates {
			if cand := Library.ContributorByID[candID]; cand != nil {
				score := 0
				if cand.ORCID != "" {
					score += 2
				}
				if cand.DblpKey != "" {
					score += 2
				}
				var cnt int
				db.QueryRow(`SELECT COUNT(*) FROM contributor_roles WHERE contributor_id = ?`, candID).Scan(&cnt) //nolint:errcheck
				score += cnt
				if score > bestScore {
					bestScore = score
					bestIdx = i + 1
				}
			}
		}
		Library.ResetQuestionFlag()
		about := TQuestion{Text: "Which contributor?", Key: r.key}
		var keys []string
		for i, candID := range candidates {
			k := strconv.Itoa(i + 1)
			keys = append(keys, k)
			about.Options = append(about.Options, TQuestionOption{Key: k, Label: contributorLabel(candID, candID == r.id)})
		}
		about.Options = append(about.Options, TQuestionOption{Key: "k", Label: "keep the current one"},
			TQuestionOption{Key: "m", Label: "merge all into " + contributorLabel(candidates[bestIdx-1], false)})
		raw := Library.WarningQuestionOrderedAbout(about, "Which contributor?", append(keys, "k", "m"),
			"Entry %s %s pos %d: name %q is an alias for %d contributors", r.key, r.role, r.position, r.alias, len(candidates))
		switch raw {
		case "q":
			break outer
		case "k":
			// keep current assignment
		case "m":
			// Merge all candidates into pre-computed best (#bestIdx).
			bestID := candidates[bestIdx-1]
			for _, candID := range candidates {
				if candID == bestID {
					continue
				}
				if fromCand := Library.ContributorByID[candID]; fromCand != nil {
					Library.Progress("Merging %q (%s) into %q (%s).",
						fromCand.Name, candID,
						Library.ContributorByID[bestID].Name, bestID)
					if mergeContributorInDB(candID, bestID) {
						for n, id := range Library.NameToContributorID {
							if id == candID {
								Library.NameToContributorID[n] = bestID
							}
						}
						// Any ORCID still pointing at the absorbed contributor must be
						// redirected too, or a later lookup resolves to a deleted
						// contributor ID and the next write through it hits a FOREIGN
						// KEY violation (see bibtex_dblp_xml.go's DBLP-key merge sites
						// for the same fix).
						for orcid, id := range Library.ORCIDToContributorID {
							if id == candID {
								Library.ORCIDToContributorID[orcid] = bestID
							}
						}
						delete(Library.ContributorByID, candID)
					}
				}
			}
		default:
			n, convErr := strconv.Atoi(raw)
			if convErr != nil || n < 1 || n > len(candidates) {
				continue
			}
			chosenID := candidates[n-1]
			if chosenID == r.id {
				continue
			}
			dbExecSave("doDisambiguateContributors: reassign entry_contributor_names",
				`UPDATE entry_contributor_names SET contributor_id = ? WHERE entry_key = ? AND role = ? AND position = ?`,
				chosenID, r.key, r.role, r.position)
			dbExecSave("doDisambiguateContributors: reassign contributor_roles",
				`UPDATE contributor_roles SET contributor_id = ? WHERE entry_key = ? AND role = ? AND position = ?`,
				chosenID, r.key, r.role, r.position)
			if cand := Library.ContributorByID[chosenID]; cand != nil {
				Library.Progress("Assigned %s %s pos %d → %q.", r.key, r.role, r.position, cand.Name)
			}
		}
		if Reporting.QuitWasRequested() {
			break outer
		}
	}
}

// reportHomework prints a summary of remaining work:
func countUnresolvedGroups() int {
	n := 0
	for _, keys := range Library.TitleIndex {
		var canonicals []string
		for _, k := range keys.ElementsSorted() {
			if k == Library.MapEntryKey(k) {
				canonicals = append(canonicals, k)
			}
		}
		groupUnresolved := false
		for i, a := range canonicals {
			for _, b := range canonicals[i+1:] {
				if Library.NonDoubleEntries[a].Set().Contains(b) {
					continue
				}
				if Library.EvidenceForBeingDifferentEntries(a, b) {
					continue
				}
				groupUnresolved = true
				break
			}
			if groupUnresolved {
				break
			}
		}
		if groupUnresolved {
			n++
		}
	}
	return n
}

func countDblpCandidates() int {
	n := 0
	forEachBibEntryKey(func(key string) bool {
		if Library.EntryFieldValueity(key, DBLPField) != "" {
			return true
		}
		hash := libraryTitleHash(Library.EntryFieldValueity(key, TitleField))
		if hash == "" {
			return true
		}
		existing := Library.NonDoubleEntries[key]
		for _, c := range readDblpTitleLinks(hash) {
			if !existing.Set().Contains(KeyForDBLP(c)) {
				n++
				return true
			}
		}
		return true
	})
	return n
}

// reportHomework prints, at the end of a write session, what the session changed
// and the homework still open (each with the action that does it), among which:
//   - number of title-hash groups that contain at least one unresolved potential
//     duplicate pair (title-equal, not in non_doubles, no divergent DBLP/DOI evidence)
//   - number of entries without a dblp field that have at least one unresolved DBLP
//     title-index candidate (not already in non_doubles)
func reportHomework() {
	// Session-change summary: compare current state to what was captured at session open.
	currentEntries := countBibEntries()
	currentDblpKeys := countDblpKeyedEntries()
	var currentFieldMaps int
	bibQueryRow(`SELECT COUNT(*) FROM field_mappings`).Scan(&currentFieldMaps)
	newEntries := currentEntries - sessionStartEntryCount
	newDblpLinks := currentDblpKeys - sessionStartDblpKeyCount
	newFieldMaps := currentFieldMaps - sessionStartFieldMapCount
	unresolvedGroups := countUnresolvedGroups()
	dblpCandidates := countDblpCandidates()

	var changeRows []statRow
	if newEntries != 0 {
		changeRows = append(changeRows, statRow{StatEntries, fmt.Sprintf("%+d", newEntries), ""})
	}
	if newDblpLinks != 0 {
		changeRows = append(changeRows, statRow{StatDblpLinks, fmt.Sprintf("%+d", newDblpLinks), ""})
	}
	if newFieldMaps != 0 {
		changeRows = append(changeRows, statRow{StatFieldMappings, fmt.Sprintf("%+d", newFieldMaps), ""})
	}
	if delta := unresolvedGroups - sessionStartUnresolvedGroups; delta != 0 {
		changeRows = append(changeRows, statRow{StatTitleGroupsWithUnresolvedDuplicates, fmt.Sprintf("%+d", delta), ""})
	}
	if delta := dblpCandidates - sessionStartDblpCandidates; delta != 0 {
		changeRows = append(changeRows, statRow{StatEntriesWithUnresolvedDblpCandidates, fmt.Sprintf("%+d", delta), ""})
	}
	if sessionManualDblpAssignments > 0 {
		changeRows = append(changeRows, statRow{StatDblpKeysManuallyEntered, fmt.Sprintf("%d", sessionManualDblpAssignments), ""})
	}
	if len(changeRows) > 0 {
		printStatBlock("Session changes:", changeRows, true)
	}

	// Count contributors with an ORCID that have never been enriched (no seen record).
	// Only available when the contributor_orcid_seen table exists (dev tree).
	newOrcidContributors := 0
	var seenTableExists int
	bibQueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='contributor_orcid_seen'`).Scan(&seenTableExists)
	if seenTableExists > 0 {
		bibQueryRow(`SELECT COUNT(*) FROM contributors WHERE orcid != '' AND NOT EXISTS (SELECT 1 FROM contributor_orcid_seen s WHERE s.contributor_id = contributors.id AND s.canonical != '')`).Scan(&newOrcidContributors)
	}

	// Each line of homework names the action that does it.
	homework := TMessage{Kind: "table", Text: "Homework"}
	todo := func(label string, count int, action string) {
		row := TMessageRow{Label: label, Value: fmt.Sprintf("%d", count)}
		if count > 0 {
			row.Note, row.Action = dialogueActions[action].Title, action
		}
		homework.Rows = append(homework.Rows, row)
	}
	var triagePending int
	bibQueryRow(`SELECT COUNT(*) FROM superseded_field_values WHERE field IN ('author', 'editor') AND triage_status IS NULL`).Scan(&triagePending)

	if seenTableExists > 0 {
		todo(StatContributorsWithOrcidNotYetEnriched, newOrcidContributors, "enrich")
	}
	todo(StatLosingValuesPending, triagePending, "triage")
	todo(StatEntriesWithUnresolvedDblpCandidates, dblpCandidates, "fix_candidates")
	todo(StatTitleGroupsWithUnresolvedDuplicates, unresolvedGroups, "fix_duplicates")
	if isTTY {
		emit(homework)
		emit(TMessage{Kind: "note"})
	}
	stderrPrintf("\n")
}

// doFixCandidates interactively links library entries that have no DBLP key yet
// to DBLP records. Entries are processed per title-index bucket so that DBLP
// keys already claimed by one variation in the bucket are not offered to another
// (variation-aware exclusion). Within each bucket, entries that have no DBLP key
// are offered candidates; entries that already have one are skipped.
func doFixCandidates() {
	if openLibraryToUpdate() {
	outer:
		for _, keySet := range Library.TitleIndex {
			// Collect canonicals that still need a DBLP key; build exclusion set
			// from canonicals that already have one.
			exclusions := TStringSetNew()
			var needDblp []string
			for _, k := range keySet.ElementsSorted() {
				if k != Library.MapEntryKey(k) {
					continue
				}
				if d := Library.EntryFieldValueity(k, DBLPField); d != "" {
					exclusions.Add(normalizeDblpKey(d))
				} else {
					needDblp = append(needDblp, k)
				}
			}
			if len(needDblp) == 0 {
				continue
			}
			Library.ResetQuestionFlag()
			for _, key := range needDblp {
				key = Library.MapEntryKey(key)
				if !Library.EntryExists(key) {
					continue
				}
				found := findLibraryEqualWithDblp(key)
				if !found {
					found = maybeFindDBLPCandidatesExcluding(key, exclusions)
				}
				key = Library.MapEntryKey(key)
				if found {
					doAllChecks(key)
					// Re-resolve: doAllChecks may have merged key into another entry,
					// making key a superseded alias whose DB row is gone. Use the surviving canonical.
					key = Library.MapEntryKey(key)
					if d := Library.EntryFieldValueity(key, DBLPField); d != "" {
						exclusions.Add(normalizeDblpKey(d))
					}
				}
			}
			if Library.QuitWasRequested() {
				break outer
			}
		}
	}
}

func doDefaultRun() {
	if openLibraryToUpdate() {
		clearEntryWarnings()
		Library.ReadURLsIgnoreFile()
		announceStep("Analysing and checking library")

		Library.BeginDeferringMessages()
		Library.CheckEntries()
		Library.FlushDeferredMessages()
		if Library.QuitWasRequested() {
			return
		}

		Library.ReadKeyNonDoublesFile()

		Library.BeginDeferringMessages()
		Library.FixDblpHierarchy()
		Library.FlushDeferredMessages()
		if Library.QuitWasRequested() {
			return
		}

		Library.BeginDeferringMessages()
		Library.CheckAlignTitles(false)
		Library.FlushDeferredMessages()
		if Library.QuitWasRequested() {
			return
		}

		Library.BeginDeferringMessages()
		Library.CheckDuplicateDBLPKeys()
		Library.CheckLoneProceedings()
		Library.FlushDeferredMessages()
		if Library.QuitWasRequested() {
			return
		}

		// The entry_actions script: group rules and the sanity rules of step 24, so
		// every check both repairs and defends (about 0.1 s for a few rules).
		runScript()
		if Library.QuitWasRequested() {
			return
		}

		Library.ScanOrphanPDFs()

		Library.BeginDeferringMessages()
		Library.CheckAllURLs()
		Library.FlushDeferredMessages()
	}
}

func doCheckPDFs() {
	if openLibraryToUpdate() {
		Library.ReadKeyNonDoublesFile()
		Library.ReadURLsIgnoreFile()
		Library.CheckPDFHealth()
	}
}

// contributorLabel names a contributor in a question: name, id, ORCID, DBLP key,
// number of entries.
func contributorLabel(id string, current bool) string {
	label := id
	if c := Library.ContributorByID[id]; c != nil {
		label = fmt.Sprintf("%s [%s", c.Name, id)
		if c.ORCID != "" {
			label += ", ORCID " + c.ORCID
		}
		if c.DblpKey != "" {
			label += ", DBLP " + c.DblpKey
		}
		var n int
		bibQueryRow(`SELECT COUNT(*) FROM contributor_roles WHERE contributor_id = ?`, id).Scan(&n) //nolint:errcheck
		label += fmt.Sprintf(", %d entries]", n)
	}
	if current {
		label += " (current)"
	}
	return label
}

func doAddFailedURLs() {
	if openLibraryToUpdate() {
		Library.AddFailedURLsToIgnore()
	}
}

func doGetPdfs() {
	if openLibraryToUpdate() {
		Library.ReadURLsIgnoreFile()
		Library.GetPDFs()
	}
}

func doFindEntries(args []string) {
	if openLibraryToReport() {
		field := strings.ToLower(args[0])
		value := ""
		if len(args) > 1 {
			value = args[1]
		}
		var matches []TBibFieldMatch
		if field == "groups" {
			matches = findBibEntriesByGroup(value)
		} else {
			matches = findBibEntriesByField(field, value)
		}
		for _, m := range matches {
			resultf("%s\t%s\n", m.Key, m.Value)
		}
	}
}

func doAddToGroup(args []string) {
	if openLibraryToUpdate() {
		key := resolveUserKey(args[0])
		group := args[1]
		if err := addBibGroupEntry(group, key); err != nil {
			messagef("Could not add %s to group %s: %s\n", key, group, err)
			return
		}
		Library.GroupEntries.AddValueToStringSetMap(group, key)
		bibEntriesModified = true
	}
}

func doCreateGroup(args []string) {
	if openLibraryToUpdate() {
		groupName := args[0]
		// Register group in memory even if no entries are supplied.
		Library.GroupEntries.AddValueToStringSetMap(groupName, "")
		Library.GroupEntries.DeleteValueFromStringSetMap(groupName, "")
		for _, rawKey := range args[1:] {
			key := resolveUserKey(rawKey)
			if key == "" {
				messagef("Unknown key: %s\n", rawKey)
				continue
			}
			if err := addBibGroupEntry(groupName, key); err != nil {
				messagef("Could not add %s to group %s: %s\n", key, groupName, err)
				continue
			}
			Library.GroupEntries.AddValueToStringSetMap(groupName, key)
		}
		bibEntriesModified = true
	}
}

func doRenderAsBibTeX(args []string) {
	if openLibraryToReport() {
		result(Library.renderAsBibTeX(resolveInputKey(cleanKey(args[0]))))
	}
}

func doRenderGroup(args []string, useAliases bool) {
	if openLibraryToReport() {
		group := args[0]
		pubsFolder := args[1]
		if !strings.HasSuffix(pubsFolder, "/") {
			pubsFolder += "/"
		}
		citationsFolder := args[2]
		if !strings.HasSuffix(citationsFolder, "/") {
			citationsFolder += "/"
		}
		writeFile := func(path, content string) {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				messagef("Could not create directory for %s: %s\n", path, err)
				return
			}
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				messagef("Could not write %s: %s\n", path, err)
			}
		}
		for _, m := range findBibEntriesByGroup(group) {
			key := Library.MapEntryKey(m.Key)
			if key == "" {
				continue
			}

			fileKey := key
			if useAliases {
				if alias := Library.PreferredKey(key); alias != "" {
					fileKey = alias
				}
			}

			bib := Library.renderAsBibTeX(key, fileKey)
			if bib == "" {
				continue
			}

			entry := loadEntryFromDb(key)
			year := entry.FieldValue("year")
			rg := entry.FieldValue("researchgate")
			dblp := entry.FieldValue("dblp")
			if year == "" || dblp == "" || rg == "" {
				if parent, _ := Library.resolveParent(entry); parent != nil {
					if year == "" {
						year = parent.FieldValue("year")
					}
					if dblp == "" {
						dblp = parent.FieldValue("dblp")
					}
					if rg == "" {
						rg = parent.FieldValue("researchgate")
					}
				}
			}

			writeFile(pubsFolder+fileKey+".bib", bib)
			writeFile(citationsFolder+fileKey+".html", Library.renderAsHTML(key)+"\n")
			writeFile(citationsFolder+fileKey+".tex", Library.renderAsTeX(key)+"\n")

			if useAliases {
				resultf("%s|%s|%s|%s|%s\n", year, fileKey, rg, dblp, key)
			} else {
				resultf("%s|%s|%s|%s\n", year, key, rg, dblp)
			}
		}
	}
}

func doListGroupAliases(args []string) {
	if openLibraryToReport() {
		for _, m := range findBibEntriesByGroup(args[0]) {
			key := Library.MapEntryKey(m.Key)
			if key == "" {
				continue
			}
			alias := Library.PreferredKey(key)
			if alias == "" {
				alias = key
			}
			resultf("%s|%s\n", key, alias)
		}
	}
}

func doRenderAsTex(args []string) {
	if openLibraryToReport() {
		resultln(Library.renderAsTeX(resolveInputKey(cleanKey(args[0]))))
	}
}

func doRenderAsHTML(args []string) {
	if openLibraryToReport() {
		resultln(Library.renderAsHTML(resolveInputKey(cleanKey(args[0]))))
	}
}

func doRenderAsText(args []string) {
	if openLibraryToReport() {
		resultln(Library.renderAsText(resolveInputKey(cleanKey(args[0]))))
	}
}

func doSetField(args []string) {
	if openLibraryToUpdate() {
		key := resolveUserKey(args[0])
		if !Library.EntryExists(key) {
			messagef("set_field: entry not found: %s\n", args[0])
			return
		}
		value := ""
		if len(args) >= 3 {
			value = args[2]
		}
		setFieldByHand(key, args[1], value)
		bibEntriesModified = true
	}
}

// setFieldByHand sets (value "": removes) a field as -set_field does.
func setFieldByHand(key, field, value string) {
	entry := loadEntryFromDb(key)
	if value == "" {
		Library.deleteEntryField(entry, field)
		return
	}
	Library.setEntryField(entry, field, value)
	// A value set by hand wins (Erik, 2026-11-23): a superseded value recorded
	// for this field (e.g. by a merge) whose winner differs would otherwise map
	// the new value back on output.
	winners := map[string]bool{}
	for _, winner := range Library.EntryFieldSourceToTarget[key][field] {
		winners[winner] = true
	}
	for winner := range winners {
		if winner != value {
			Library.UpdateEntryFieldAlias(key, field, winner, value)
		}
	}
}

// TEntryEdit is what -edit_entry applies (the web UI's Save, 22.3c): fields to
// set ("" removes one; "entrytype" for the type), groups to join and leave.
type TEntryEdit struct {
	Set   map[string]string `json:"set"`
	Join  []string          `json:"join"`
	Leave []string          `json:"leave"`
}

// doEditEntry is -edit_entry <key> <json>: an entry's changes in one write
// session, as -set_field, -add_to_group and -remove_from_group each would.
func doEditEntry(args []string) {
	var edit TEntryEdit
	if err := json.Unmarshal([]byte(args[1]), &edit); err != nil {
		messagef("edit_entry: not a valid edit: %s\n", err)
		endRun(1)
	}
	if !openLibraryToUpdate() {
		return
	}
	key := resolveUserKey(args[0])
	if !Library.EntryExists(key) {
		messagef("edit_entry: entry not found: %s\n", args[0])
		return
	}
	fields := make([]string, 0, len(edit.Set))
	for f := range edit.Set {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	if v, ok := edit.Set["entrytype"]; ok && v != "" { // the type first: it decides what fits
		setFieldByHand(key, "entrytype", v)
	}
	for _, f := range fields {
		if f != "entrytype" {
			setFieldByHand(key, f, edit.Set[f])
		}
	}
	for _, g := range edit.Leave {
		if err := removeBibGroupEntry(g, key); err != nil {
			messagef("Could not remove %s from group %s: %s\n", key, g, err)
			continue
		}
		Library.GroupEntries.DeleteValueFromStringSetMap(g, key)
	}
	for _, g := range edit.Join {
		if err := addBibGroupEntry(g, key); err != nil {
			messagef("Could not add %s to group %s: %s\n", key, g, err)
			continue
		}
		Library.GroupEntries.AddValueToStringSetMap(g, key)
	}
	bibEntriesModified = true
}

func doRemoveFromGroup(args []string) {
	if openLibraryToUpdate() {
		key := resolveUserKey(args[0])
		group := args[1]
		if err := removeBibGroupEntry(group, key); err != nil {
			messagef("Could not remove %s from group %s: %s\n", key, group, err)
			return
		}
		Library.GroupEntries.DeleteValueFromStringSetMap(group, key)
		bibEntriesModified = true
	}
}

// doSetGroups implements -set_groups <key> [+] <group>...
// Without '+': replaces the entry's current group membership with the given list.
// With '+' as the second argument: adds the given groups to the existing membership.
func doSetGroups(args []string) {
	if !openLibraryToUpdate() {
		return
	}
	key := resolveUserKey(args[0])
	if !Library.EntryExists(key) {
		messagef("Unknown entry key: %s\n", args[0])
		return
	}

	additive := len(args) > 1 && args[1] == "+"
	var newGroups []string
	if additive {
		newGroups = args[2:]
	} else {
		newGroups = args[1:]
	}

	if !additive {
		// Remove the entry from all its current groups.
		rows, err := db.Query(`SELECT group_name FROM bib_groups WHERE entry_key = ?`, key)
		if err != nil {
			messagef("Could not query current groups for %s: %s\n", key, err)
			return
		}
		var current []string
		for rows.Next() {
			var g string
			if rows.Scan(&g) == nil {
				current = append(current, g)
			}
		}
		rows.Close()
		for _, g := range current {
			if err := removeBibGroupEntry(g, key); err != nil {
				messagef("Could not remove %s from group %s: %s\n", key, g, err)
				return
			}
			Library.GroupEntries.DeleteValueFromStringSetMap(g, key)
		}
	}

	for _, g := range newGroups {
		if g == "" {
			continue
		}
		if err := addBibGroupEntry(g, key); err != nil {
			messagef("Could not add %s to group %s: %s\n", key, g, err)
			return
		}
		Library.GroupEntries.AddValueToStringSetMap(g, key)
	}
	bibEntriesModified = true
}

// resolveInputKey maps a user-supplied key to its canonical entry key,
// falling back to HintToKey when MapEntryKey returns the input unchanged.
func resolveInputKey(raw string) string {
	resolved := Library.MapEntryKey(raw)
	if resolved == raw {
		if hint, ok := Library.HintToKey.Get(raw); ok {
			resolved = Library.MapEntryKey(hint)
		}
	}
	return resolved
}

func doEntryKey(args []string) {
	if openLibraryToReport() {
		resultln(resolveInputKey(cleanKey(args[0])))
	}
}

func doEntryKeyAlias(args []string, withMap bool) {
	Reporting.SetInteractionOff()
	if openLibraryToUpdate() {
		rawKey := cleanKey(args[0])
		key := Library.MapEntryKey(rawKey)
		alias := Library.PreferredKey(key)
		if alias == "" && key != rawKey && validPreferredKeyAlias.MatchString(rawKey) {
			// Input is a valid preferred-alias that resolved to a canonical key;
			// use it directly so -map works before preferredalias is set on the entry.
			alias = rawKey
		}
		if alias == "" {
			// No preferred alias yet — try to generate one from author/year/title.
			entry := Library.buildEntry(key)
			if derived := Library.derivePreferredAlias(entry); derived != "" {
				Library.setPreferredAlias(entry, derived)
				alias = derived
			}
		}
		if alias != "" {
			resultln(alias)
			if withMap {
				appendToKeysFile(alias, key)
			}
		}
	}
}

// loadBackupKeyOldies reads a backup key_oldies CSV (format: canonical;alias)
// and returns a map from alias → canonical for use as a secondary key remap source.
func loadBackupKeyOldies(path string) map[string]string {
	m := map[string]string{}
	if !FileExists(path) {
		return m
	}
	processFile(path, func(line string) {
		parts := strings.SplitN(line, csvDelimiter, 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			m[parts[1]] = parts[0] // alias → canonical
		}
	})
	return m
}

// doRestoreKeyHints reads a backup key_hints CSV (format: key;hint per line),
// maps each old entry key to the current canonical key via MapEntryKey/EntryExists,
// and inserts surviving hints into key_hints. When the current library does not
// recognise an old key, it also tries the backup key_oldies (auto-derived from
// the same directory) to chain through an extra level of remapping.
// Processes in batches of 10 000 rows for performance.
func doRestoreKeyHints(csvPath string) {
	if csvPath == "" {
		messageln("-restore_key_hints requires -hints_csv <path>")
		return
	}
	if !openLibraryToUpdate() {
		return
	}

	// Load the backup key_oldies from the same directory as the hints CSV.
	oldiesPath := filepath.Join(filepath.Dir(csvPath), "key_oldies.csv")
	backupOldies := loadBackupKeyOldies(oldiesPath)
	if len(backupOldies) > 0 {
		Library.Progress("Loaded %d backup key_oldies entries from %s", len(backupOldies), oldiesPath)
	}

	f, err := os.Open(csvPath)
	if err != nil {
		messagef("Could not open %s: %s\n", csvPath, err)
		return
	}
	defer f.Close()

	// resolveKey tries to find a current canonical key for oldKey.
	// First via the live library's alias chain, then via the backup key_oldies.
	resolveKey := func(oldKey string) string {
		canon := Library.MapEntryKey(oldKey)
		if Library.EntryExists(canon) {
			return canon
		}
		if backupCanon, ok := backupOldies[oldKey]; ok {
			canon = Library.MapEntryKey(backupCanon)
			if Library.EntryExists(canon) {
				return canon
			}
		}
		return ""
	}

	// Load the current key_hints table so we can detect ambiguities before inserting.
	Library.Progress("Loading current key_hints for ambiguity check")
	currentHints := map[string]string{} // hint → current canonical key
	if rows, err2 := db.Query(`SELECT hint, key FROM key_hints`); err2 == nil {
		for rows.Next() {
			var h, k string
			if rows.Scan(&h, &k) == nil {
				currentHints[h] = Library.MapEntryKey(k)
			}
		}
		rows.Close()
	}

	insertOnly := `INSERT INTO key_hints (hint, key) VALUES (?, ?) ON CONFLICT(hint) DO NOTHING`

	const batchSize = 10000
	total, imported, skipped, remapped, ambiguous := 0, 0, 0, 0, 0

	commitBatch := func(tx *sql.Tx) {
		if err := tx.Commit(); err != nil {
			dbInteraction.Warning("key_hints restore commit failed: %s", err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		messagef("Could not begin transaction: %s\n", err)
		return
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, csvDelimiter, 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		oldKey, hint := parts[0], parts[1]
		total++

		canon := resolveKey(oldKey)
		if canon == "" {
			skipped++
		} else if existing, conflict := currentHints[hint]; conflict && existing != canon {
			Library.Warning("Ambiguous key hint during restore: %s already maps to %s, refusing %s", hint, existing, canon)
			ambiguous++
		} else {
			if canon != oldKey {
				remapped++
			}
			if !conflict {
				currentHints[hint] = canon // track newly inserted hints for intra-batch conflict detection
				tx.Exec(insertOnly, hint, canon)
			}
			imported++
		}

		if total%batchSize == 0 {
			commitBatch(tx)
			Library.Progress("Restoring key hints: %d processed, %d imported, %d skipped, %d remapped, %d ambiguous", total, imported, skipped, remapped, ambiguous)
			tx, err = db.Begin()
			if err != nil {
				messagef("Could not begin transaction: %s\n", err)
				return
			}
		}
	}
	commitBatch(tx)

	if err := scanner.Err(); err != nil {
		Library.Warning("Error reading %s: %s", csvPath, err)
	}

	Library.Progress("Key hints restore complete: %d total, %d imported, %d skipped (no entry), %d remapped via key_oldies, %d refused (ambiguous)", total, imported, skipped, remapped, ambiguous)
}

func doShowEntry(args []string) {
	if openLibraryToReport() {
		resultln(Library.EntryString(resolveInputKey(cleanKey(args[0])), ""))
	}
}

func doFixEntries(args []string) {
	if openLibraryToUpdate() {
		Library.ReadKeyNonDoublesFile()
		Library.FixDblpHierarchy()
		for _, key := range cleanKeys(args) {
			Library.Progress("  Checking %s", Library.MapEntryKey(key))
			doAllChecks(key)
		}
	}
}

// hasUnresolvedPairs reports whether the canonical list contains at least one
// pair that is not yet recorded as a non-double and shows no contradicting
// evidence (different DBLP or DOI). Used to skip fully-resolved buckets quickly.
func hasUnresolvedPairs(canonicals []string) bool {
	for i, a := range canonicals {
		for _, b := range canonicals[i+1:] {
			if Library.NonDoubleEntries[a].Set().Contains(b) {
				continue
			}
			if Library.EvidenceForBeingDifferentEntries(a, b) {
				continue
			}
			return true
		}
	}
	return false
}

// doFixDuplicates processes each title-index bucket that contains at least one
// unresolved duplicate pair. Within each bucket it runs ResolveVariationSet
// (fixed-point content-equal auto-merge + interactive merge/non-double for
// differing pairs) and then links each surviving variation to DBLP while
// honouring the invariant that no two variations share a DBLP key.
func doFixDuplicates() {
	if openLibraryToUpdate() {
		Library.ReadKeyNonDoublesFile()
		Library.FixDblpHierarchy()
	outer:
		for _, keySet := range Library.TitleIndex {
			var canonicals []string
			for _, k := range keySet.ElementsSorted() {
				if k == Library.MapEntryKey(k) {
					canonicals = append(canonicals, k)
				}
			}
			if len(canonicals) < 2 || !hasUnresolvedPairs(canonicals) {
				continue
			}
			Library.ResetQuestionFlag()
			variations := Library.ResolveVariationSet(canonicals)
			doVariationSetDblpLinking(variations)
			if Reporting.QuitWasRequested() {
				break outer
			}
		}
	}
}

func doUpsertDblpEntries() {
	if openLibraryToUpdate() {
		Library.ReadKeyNonDoublesFile()

		// Collect bookish and non-bookish keys separately so bookish entries are
		// processed first — proceedings/books establish venue field mappings that
		// inpapers/incollections then inherit, preventing double-counting artifacts
		// (e.g. ScitePress keynotes appearing in both proceedings and inproceedings).
		// seenKeys tracks every key visited so far; entries added during the bookish
		// pass (e.g. children added by CheckEntry → ForEachChildOfDBLPKey) are caught
		// by the drain loop and fixed before the non-bookish pass begins.
		var bookishKeys, otherKeys []string
		seenKeys := make(map[string]bool)
		forEachBibEntryType(func(key, entryType string) {
			seenKeys[key] = true
			if BibTeXBookish.Contains(entryType) {
				bookishKeys = append(bookishKeys, key)
			} else {
				otherKeys = append(otherKeys, key)
			}
		})

		inDblpUpdate = true
		scanned := 0
		dblpUpdated := 0
		announceStep("Doing analysis based on DBLP data")
		Library.FixDblpHierarchy()
		// Same definition as the "DBLP links" startup stat (countDblpKeyedEntries),
		// so the ticker's denominator matches what's reported at session start
		// instead of the much larger, less meaningful total library size.
		dblpLinkedCount := countDblpKeyedEntries()
		total := dblpLinkedCount
		ticker := Library.NewProgressTicker(ProgressFixingDblpEntries, total)
		beginBibTransaction()
		newDblpLinkedTotal := 0
		processKey := func(key string) {
			// scanned/total both only count DBLP-linked entries — matching what
			// dblpLinkedCount (the ticker's baseline) counts. Children inspected along
			// the way via doC2Checks are deliberately NOT added here even though they
			// take real time: every such child is already represented in this same
			// total, either because it already had a dblp key (already inside
			// dblpLinkedCount) or because it's newly created (added to
			// newDblpLinkedTotal by the drain loop below) — counting it a second time
			// here would inflate total for entries this pass will visit anyway,
			// exactly the "why is total now way past DBLP links" confusion this
			// replaced. ticker.SetCount is still called for every key, DBLP-linked or
			// not, purely so a "q" typed mid-run is polled promptly; it just repeats
			// the same count for a non-DBLP-linked key rather than advancing it.
			dblpVal := Library.EntryFieldValueity(key, DBLPField)
			if dblpVal != "" {
				scanned++
				newTotal := dblpLinkedCount + newDblpLinkedTotal
				if newTotal != total {
					total = newTotal
					ticker.SetTotal(total)
				}
			}
			if ticker.SetCount(scanned) {
				return
			}
			if dblpVal != "" {
				Library.ResetQuestionFlag()
				doC1Checks(key)
				modified, _ := doC2Checks(key)
				if modified {
					dblpUpdated++
				}
				doC3Checks(key)
			}
		}
		for _, key := range bookishKeys {
			if Library.QuitWasRequested() {
				break
			}
			processKey(key)
		}
		// Drain: fix any entries added during the bookish pass (children created by
		// CheckEntry). Loop until stable — grandchildren are non-bookish so depth is
		// bounded, but looping is safer than assuming exactly one level.
		for !Library.QuitWasRequested() {
			var newKeys []string
			forEachBibEntryType(func(key, entryType string) {
				if !seenKeys[key] {
					seenKeys[key] = true
					newKeys = append(newKeys, key)
					if Library.EntryFieldValueity(key, DBLPField) != "" {
						newDblpLinkedTotal++
					}
				}
			})
			if len(newKeys) == 0 {
				break
			}
			for _, key := range newKeys {
				if Library.QuitWasRequested() {
					break
				}
				processKey(key)
			}
		}
		for _, key := range otherKeys {
			if Library.QuitWasRequested() {
				break
			}
			processKey(key)
		}
		commitBibTransaction()
		inDblpUpdate = false
		ticker.Done()
		bibEntriesModified = true
		if Library.QuitWasRequested() {
			// Every key loop above breaks immediately on QuitWasRequested — a single "q"
			// answer (or an auto-quit from a non-TTY session hitting its first question)
			// silently truncates the rest of the pass, with entries after that point in
			// iteration order never even looked at. Without this, the summary line below
			// looks identical to a genuinely complete pass, which is exactly how a
			// not-yet-DBLP-synced entry can go unnoticed run after run — say so explicitly.
			remaining := total - scanned
			if remaining < 0 {
				remaining = 0
			}
			Library.Progress("  DBLP update pass stopped early: %d entries checked (%d updated), ~%d not yet checked — re-run to cover the rest.",
				scanned, dblpUpdated, remaining)
		} else {
			Library.Progress("  DBLP data applied: %d/%d entries.", dblpUpdated, scanned)
		}
		if Library.orcidAutoResolveSameCount > 0 || Library.orcidAutoResolveDiffCount > 0 {
			Library.Progress("  Auto-resolved by ORCID: %d same-person mapping(s), %d different-person disambiguation(s).",
				Library.orcidAutoResolveSameCount, Library.orcidAutoResolveDiffCount)
		}
		Library.orcidAutoResolveSameCount = 0
		Library.orcidAutoResolveDiffCount = 0
		if len(Library.dblpNameFormAutoAccepts) > 0 {
			logPath := libraryBase() + tablesFolderSuffix + "/dblp_name_form_auto_accepts.log"
			os.MkdirAll(libraryBase()+tablesFolderSuffix, 0o755) //nolint:errcheck
			if f, err := os.Create(logPath); err == nil {
				for _, line := range Library.dblpNameFormAutoAccepts {
					fmt.Fprintln(f, line)
				}
				f.Close()
			}
			Library.Progress("  Auto-accepted DBLP name form: %d (see dblp_name_form_auto_accepts.log)", len(Library.dblpNameFormAutoAccepts))
			Library.dblpNameFormAutoAccepts = nil
		}
		// The role cross-check (accumulate/applyContributorMatchesFromEntries) is gone
		// (step 23.2.3): the DBLP merge decides the positions (ARCHITECTURE.md 7.6).
		Library.Progress("  Absorbing DBLP name data...")
		absorbDblpNamesCore()
		Library.FlushAmbiguousAssignments()
	}
}

// doPush implements -push: push the local changes that are not in the shared DB
// folder yet. Opening the library as a write run does (prepareWorkingDatabase
// sees the unpushed marker) pushes them at the end of the job.
func doPush() {
	if readLibraryUnpushed() == nil {
		messagef("Nothing to push: %s has this machine's changes.\n", sharedDBFolder)
		return
	}
	openLibraryToUpdate()
}

func doNewKey() {
	// -new_key runs before the database is opened, so the key prefix (kept in
	// the database's config) is not loaded yet; read it as the maps are read.
	if keyPrefix == "" {
		keyPrefix = readKeyPrefixFromDB()
	}
	resultln(KeyFromTime(time.Now()))
}

func doUpsertDblpFor(args []string) {
	if openLibraryToUpdate() {
		Library.ReadKeyNonDoublesFile()
		Library.FixDblpHierarchy()
		for _, rawKey := range cleanKeys(args) {
			key := Library.MapEntryKey(rawKey)
			if key == "" {
				// Bare DBLP path (no "DBLP:" prefix) — try with the prefix.
				key = Library.MapEntryKey(KeyForDBLP(rawKey))
			}
			if key != "" {
				doAllChecks(key)
			}
		}
	}
}

func doUpsertDblpEntryFromDblpKeys(args []string) {
	if openLibraryToUpdate() {
		Library.ReadKeyNonDoublesFile()
		stderrPrintf("\n")
		for _, dblpKey := range args {
			dblpKey = normalizeDblpKey(dblpKey)
			if existing := Library.LookupDBLPKey(dblpKey); existing != "" {
				resultln(existing)
			} else if added := Library.MaybeAddDBLPEntry(dblpKey); added != "" {
				Library.MarkDblpKeyMissing(added, dblpKey)
				resultln(added)
				doAllChecks(added)
			}
		}
	}
}

// resolveNameToORCID looks up the DBLP persons index for canonicalName, scans
// for a homepages/ key, and returns the first ORCID found in that entry.
// Returns "" if no ORCID can be found.
func resolveNameToORCID(canonicalName string) string {
	for _, key := range readDblpPersonEntries(canonicalName) {
		if !strings.HasPrefix(key, "homepages/") {
			continue
		}
		je := readDblpJSONEntry(key)
		if je == nil {
			continue
		}
		for _, u := range je.Multi["url"] {
			if strings.HasPrefix(u, "https://orcid.org/") {
				return strings.TrimPrefix(u, "https://orcid.org/")
			}
		}
	}
	return ""
}

// resolveORCIDToName returns the canonical DBLP name for orcid.
// Strategy 1: find a homepages/ entry in the ORCID index and read its first author.
// Strategy 2: scan publication entries in the ORCID index for an author/editor
// whose ORCID field matches — more reliable because homepages entries are often
// not indexed under the ORCID.
func resolveORCIDToName(orcid string) string {
	keys := readDblpORCIDEntries(orcid)
	// Strategy 1: homepage entry.
	for _, key := range keys {
		if !strings.HasPrefix(key, "homepages/") {
			continue
		}
		je := readDblpJSONEntry(key)
		if je != nil && len(je.Authors) > 0 && je.Authors[0].Name != "" {
			return je.Authors[0].Name
		}
	}
	// Strategy 2: match ORCID field on authors/editors in publication entries.
	for _, key := range keys {
		je := readDblpJSONEntry(key)
		if je == nil {
			continue
		}
		for _, p := range je.Authors {
			if p.ORCID == orcid && p.Name != "" {
				return p.Name
			}
		}
		for _, p := range je.Editors {
			if p.ORCID == orcid && p.Name != "" {
				return p.Name
			}
		}
	}
	return ""
}

// upgradeWatchEntries resolves name→orcid upgrades and fills missing name comments
// on orcid entries. Returns the (possibly modified) slice and whether any change occurred.
func upgradeWatchEntries(entries []TWatchEntry) ([]TWatchEntry, bool) {
	changed := false
	for i, e := range entries {
		switch e.EntryType {
		case "name":
			orcid := resolveNameToORCID(e.Value)
			if orcid == "" {
				continue
			}
			// Upgrade name → orcid, preserving the name as a comment.
			comment := e.Value
			if e.Comment != "" {
				comment = e.Comment
			}
			entries[i] = TWatchEntry{EntryType: "orcid", Value: orcid, Comment: comment}
			Library.Progress("Watch: upgraded %q → orcid %s", e.Value, orcid)
			changed = true
		case "orcid":
			if e.Comment != "" {
				continue
			}
			name := resolveORCIDToName(e.Value)
			if name == "" {
				continue
			}
			entries[i].Comment = name
			changed = true
		case "contributor":
			// A watched contributor ID can be absorbed into another one by a contributor
			// merge (mergeContributorInDB), which moves all names/ORCIDs off the old ID
			// and records the mapping in contributor_id_oldies. Left unresolved, the old
			// ID has no names/ORCIDs left to look up and silently goes dark — "No DBLP
			// entries found" — even though the person is still fully present in the
			// library under the surviving ID. Forward the watch entry itself so the
			// person is never dropped from the watch.
			if canonical := Library.ResolveContributorID(e.Value); canonical != e.Value {
				comment := e.Comment
				if contrib, ok := Library.ContributorByID[canonical]; ok && contrib.Name != "" {
					comment = contrib.Name
				}
				Library.Progress("Watch: contributor %s was merged into %s — updated watch entry", e.Value, canonical)
				entries[i] = TWatchEntry{EntryType: "contributor", Value: canonical, Comment: comment}
				changed = true
				continue
			}
			if e.Comment != "" {
				continue
			}
			if contrib, ok := Library.ContributorByID[e.Value]; ok && contrib.Name != "" {
				entries[i].Comment = contrib.Name
				changed = true
			}
		}
	}
	return entries, changed
}

// watchEntryDblpKeys returns the complete set of DBLP entry keys for a watch entry,
// unioning the ORCID index and the person-name index.
//
// The ORCID index only contains entries where DBLP explicitly recorded the ORCID —
// typically only recent publications. The person-name index (keyed by the DBLP
// first-last name stored in w.Comment) covers all entries regardless of ORCID
// annotation. Both are needed for complete coverage.
func watchEntryDblpKeys(w TWatchEntry) []string {
	seen := map[string]bool{}
	var keys []string
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	switch w.EntryType {
	case "name":
		for _, k := range readDblpPersonEntries(w.Value) {
			add(k)
		}
		if orcid := resolveNameToORCID(w.Value); orcid != "" {
			for _, k := range readDblpORCIDEntries(orcid) {
				add(k)
			}
		}
	case "orcid":
		for _, k := range readDblpORCIDEntries(w.Value) {
			add(k)
		}
		// w.Comment holds the DBLP first-last name (e.g. "Sjaak Brinkkemper").
		// The person-name index is keyed by this format and is more complete than
		// the ORCID index, which only covers papers where DBLP recorded the ORCID.
		if w.Comment != "" {
			for _, k := range readDblpPersonEntries(w.Comment) {
				add(k)
			}
		}
	case "contributor":
		// Union all ORCID-indexed DBLP entries with person-name-indexed entries for
		// every name alias the contributor is known by. Resolve through any contributor
		// merge first — mergeContributorInDB moves names/ORCIDs off an absorbed ID, so
		// looking up the raw watch-file ID after a merge would find nothing even though
		// the person is still fully present under the surviving ID. upgradeWatchEntries
		// normally rewrites the watch file to the canonical ID already; this is a
		// defensive fallback for the same run before that rewrite lands.
		id := Library.ResolveContributorID(w.Value)
		for _, orcid := range contributorORCIDs(id) {
			for _, k := range readDblpORCIDEntries(orcid) {
				add(k)
			}
		}
		for _, alias := range contributorAliasesFromDB(id) {
			for _, k := range readDblpPersonEntries(alias) {
				add(k)
			}
		}
	}
	return keys
}

// watchEntryORCIDs returns all ORCID values relevant to a watch entry for use in
// the online ORCID works fetch. For contributor entries this is the full set from
// contributor_orcids; for orcid entries the single ORCID; for name entries the
// ORCID resolved via DBLP (if any).
func watchEntryORCIDs(w TWatchEntry) []string {
	switch w.EntryType {
	case "contributor":
		return contributorORCIDs(Library.ResolveContributorID(w.Value))
	case "orcid":
		return []string{w.Value}
	case "name":
		if orcid := resolveNameToORCID(w.Value); orcid != "" {
			return []string{orcid}
		}
	}
	return nil
}

// parseBibTeXStringToEntry parses a raw BibTeX string and returns the first entry
// it contains, or nil on failure. Uses the capturedDBLPEntry mechanism so that
// the entry is captured in memory without being written to the library DB.
func parseBibTeXStringToEntry(bibtex string) *TBibTeXEntry {
	// CrossRef occasionally returns BibTeX with unbalanced braces. Append any
	// missing closing braces so the parser can close the entry cleanly.
	if extra := strings.Count(bibtex, "{") - strings.Count(bibtex, "}"); extra > 0 {
		bibtex += strings.Repeat("}", extra)
	}
	Library.capturedDBLPEntry = &TBibTeXEntry{Key: "", Fields: map[string]string{}}
	Library.ignoreIllegalFields = true
	Library.ParseBibString(bibtex + "\n")
	Library.ignoreIllegalFields = false
	entry := Library.capturedDBLPEntry
	Library.capturedDBLPEntry = nil
	if entry == nil || !entry.Exists() {
		return nil
	}
	return entry
}

// addDoiEntry creates a new library entry from a DOI-fetched TBibTeXEntry, assigns
// a new key, normalises all fields, and runs the standard entry checks.
// Returns the new library key, or "" if the entry could not be added.
func addDoiEntry(entry *TBibTeXEntry, label string) string {
	entryType := entry.EntryType()
	if entryType == "" || !BibTeXAllowedEntries.Contains(entryType) {
		return ""
	}
	key := Library.NewKey()
	Library.SetEntryType(key, entryType)
	// Only the fields this entry type allows: Crossref gives e.g. publisher and the
	// (special issue) editor for a journal article, which the checks would then ask
	// about one by one.
	allowed := BibTeXAllowedEntryFields[entryType].Set()
	for field, value := range entry.Fields {
		if field == EntryTypeField || !allowed.Contains(field) {
			continue
		}
		Library.SetEntryFieldValue(key, field, Library.NormaliseFieldValue(field, value))
	}
	Library.Progress("Watch: added from DOI (%s): %s", label, key)
	doAllChecks(key)
	return key
}

// runWatchORCID performs the online pass of the watch loop. For each watch entry
// it collects all known ORCIDs, fetches the ORCID works list, and for any DOI
// not already in the library it fetches BibTeX from doi.org and adds the entry.
func runWatchORCID(w TWatchEntry, label string) {
	orcids := watchEntryORCIDs(w)
	if len(orcids) == 0 {
		return
	}
	for _, orcid := range orcids {
		if Library.QuitWasRequested() {
			return
		}
		works := getORCIDWorks(orcid)
		seenDOI := map[string]bool{}
		for _, work := range works {
			for _, doi := range work.DOIs {
				if Library.QuitWasRequested() {
					return
				}
				if seenDOI[doi] {
					continue
				}
				seenDOI[doi] = true
				if entryExistsWithDOI(doi) || doiHasSupersededRecord(doi) || doiHasAlias(doi) {
					continue
				}
				bibtex := fetchDoiBibTeX(doi)
				if bibtex == "" {
					continue
				}
				entry := parseBibTeXStringToEntry(bibtex)
				if entry == nil {
					continue
				}
				// The canonical DOI in the fetched BibTeX may differ from the ORCID-reported
				// DOI (doi aliasing / CrossRef redirects). Check both to avoid re-adding.
				if resolvedDOI := entry.Fields["doi"]; resolvedDOI != "" && resolvedDOI != doi && (entryExistsWithDOI(resolvedDOI) || doiHasSupersededRecord(resolvedDOI) || doiHasAlias(resolvedDOI)) {
					continue
				}
				Library.ResetQuestionFlag()
				SpinnerInterrupt()
				stderrPrintf("\nWatching %s (ORCID %s) — adding missing publication:\n", label, orcid)
				stderrPrintf("  DOI: %s\n", doi)
				for _, f := range []string{"title", "booktitle", "journal", "year", "author", "editor"} {
					if v := entry.Fields[f]; v != "" {
						stderrPrintf("  %-9s: %s\n", f, v)
					}
				}
				addDoiEntry(entry, label)
				if Reporting.QuitWasRequested() {
					return
				}
			}
		}
	}
}

// runWatch processes the watch file, adding any missing publications.
// Assumes the library is already open. Returns false (silently) if the watch
// file is absent or contains no valid entries.
// Per-entry "all publications present" lines are written to watching.log; only
// additions and warnings appear on screen. A summary "Watched N, added M entries."
// is printed after the loop via Library.Progress (inheriting any progressPrefix).
func runWatch() bool {
	Library.ReadKeyNonDoublesFile()

	filePath := libraryBase() + scriptsFolderSuffix + "/watch"
	entries := ReadWatchFile(filePath)
	if len(entries) == 0 {
		return false
	}

	// Upgrade name→orcid and fill missing name comments; rewrite file if changed.
	if upgraded, changed := upgradeWatchEntries(entries); changed {
		entries = upgraded
		WriteWatchFile(filePath, entries)
	}

	logPath := libraryBase() + tablesFolderSuffix + "/watching.log"
	os.MkdirAll(libraryBase()+tablesFolderSuffix, 0o755) //nolint:errcheck
	var watchLog *os.File
	if f, err := os.Create(logPath); err == nil {
		watchLog = f
		fmt.Fprintf(watchLog, "Watch run on %s\n\n", time.Now().Format("2006-01-02 15:04:05"))
		defer watchLog.Close()
	}

	ticker := Library.NewProgressTicker("Watching", len(entries))
	totalAdded, watched := 0, 0

	for _, w := range entries {
		if ticker.Step() {
			break
		}
		watched++
		keys := watchEntryDblpKeys(w)

		label := w.Tag
		if label == "" {
			label = w.Value
		}

		if len(keys) == 0 {
			Library.Warning("No DBLP entries found for %s %q — person may not be in file store", w.EntryType, w.Value)
		}

		newCount := 0
		for _, key := range keys {
			// Skip homepage/www entries — they are not publications.
			if strings.HasPrefix(key, "homepages/") || strings.HasPrefix(key, "homepage/") {
				continue
			}
			// Skip entries already in the library.
			if Library.LookupDBLPKey(key) != "" {
				continue
			}
			newCount++

			Library.ResetQuestionFlag()
			SpinnerInterrupt()
			stderrPrintf("\nWatching %s — adding missing publication:\n", label)
			stderrPrintf("  DBLP key: %s\n", key)
			if entry := dblpEntryFromStore(key); entry != nil {
				if t := entry.EntryType(); t != "" {
					stderrPrintf("  type    : %s\n", t)
				}
				for _, f := range []string{"title", "booktitle", "journal", "year", "author", "editor"} {
					if v := entry.Fields[f]; v != "" {
						stderrPrintf("  %-9s: %s\n", f, v)
					}
				}
			} else {
				stderrPrintf("  (not in local file store)\n")
			}

			if added := Library.MaybeAddDBLPEntry(key); added != "" {
				Library.MarkDblpKeyMissing(added, key)
				doAllChecks(added)
				totalAdded++
			}

			if Reporting.QuitWasRequested() {
				break
			}
		}

		// Per-entry status goes to the log file, not the screen.
		if watchLog != nil {
			if newCount == 0 {
				fmt.Fprintf(watchLog, "Watching %s: all publications present (%d total)\n", label, len(keys))
			} else {
				fmt.Fprintf(watchLog, "Watching %s: %d missing publication(s) checked\n", label, newCount)
			}
		}

		// Online pass: fetch works from ORCID API and add entries not in DBLP yet.
		if Online && !Library.QuitWasRequested() {
			runWatchORCID(w, label)
		}
	}
	if Library.QuitWasRequested() {
		Library.Progress("Watching stopped after %d of %d, added %d entries.", watched, len(entries), totalAdded)
		return true
	}
	ticker.Done()
	Library.Progress("Watched %d, added %d entries.", len(entries), totalAdded)
	return true
}

// runWatchAndScript runs the watch and script phases as sub-steps under a
// "Running scripts:" section header with one extra level of indentation.
// Used by commands that embed both phases (e.g. -dblp_update).
func runWatchAndScript() {
	announceStep("Running scripts")
	Library.progressPrefix = "  "
	runWatch()
	runScript()
	Library.progressPrefix = ""
}

// doWatch is the -watch entry point.
func doWatch() {
	if !openLibraryToUpdate() {
		return
	}
	stderrPrintf("\n")
	if !runWatch() {
		filePath := libraryBase() + scriptsFolderSuffix + "/watch"
		Library.Progress("No watch entries found in %s", filePath)
	}
}

// runScript runs the entry_actions script if present; assumes the library is
// already open. Silent if the script file does not exist.
func runScript() bool {
	scriptPath := libraryBase() + ScriptFilePath
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return false
	}
	Library.ApplyScript(scriptPath)
	return true
}

func doAddKeyMapping(args []string) {
	if openLibraryToUpdate() {
		target := resolveUserKey(args[len(args)-1])
		for _, alias := range args[:len(args)-1] {
			Library.Progress("  Mapping %s → %s", cleanKey(alias), target)
			Library.AddKeyAlias(cleanKey(alias), target)
		}
		doAllChecks(target)
	}
}

// resolveOrImportKey resolves rawKey to a library entry key. When the key is
// not in the library but looks like a DBLP path (has a "DBLP:" prefix or
// contains "/"), it is imported via MaybeAddDBLPEntry and *importedCount is
// incremented. Returns "" when the key cannot be resolved at all.
func resolveOrImportKey(rawKey string, importedCount *int) string {
	canon := resolveInputKey(rawKey)
	if Library.EntryExists(canon) {
		return canon
	}
	dblpKey := normalizeDblpKey(rawKey)
	if existing := Library.LookupDBLPKey(dblpKey); existing != "" {
		return existing
	}
	if strings.HasPrefix(rawKey, "DBLP:") || strings.Contains(rawKey, "/") {
		if added := Library.MaybeAddDBLPEntry(dblpKey); added != "" {
			*importedCount++
			return added
		}
	}
	return ""
}

func doMergeEntries(args []string) {
	keys := cleanKeys(args)
	if openLibraryToUpdate() {
		Library.ReadKeyNonDoublesFile()
		importedCount := 0
		resolvedKeys := make([]string, 0, len(keys))
		rawKeys := make([]string, 0, len(keys))
		for _, rawKey := range keys {
			resolved := resolveOrImportKey(rawKey, &importedCount)
			if resolved == "" {
				messagef("Unknown key: %s\n", rawKey)
				return
			}
			resolvedKeys = append(resolvedKeys, resolved)
			rawKeys = append(rawKeys, rawKey)
		}
		if importedCount > 1 {
			messageln("Error: -merge_entries accepts at most one not-yet-imported DBLP entry")
			return
		}
		target := resolvedKeys[len(resolvedKeys)-1]
		if len(resolvedKeys) > 1 {
			stderrPrintf("\nMerge: %s → %s\n", strings.Join(resolvedKeys[:len(resolvedKeys)-1], ", "), target)
		}
		for i, alias := range resolvedKeys[:len(resolvedKeys)-1] {
			if alias == target {
				// Both keys resolve to the same canonical. Check for a ghost bib_entries
				// row under the raw key (entry still exists in bib_entries even though
				// key_oldies already records it as an alias). Merge its fields into the
				// canonical first — don't just delete, in case the ghost has un-merged data.
				rawKey := rawKeys[i]
				if rawKey != target && bibEntryExists(rawKey) {
					Library.Progress("  Ghost entry %s found (alias for %s) — merging fields before cleanup", rawKey, target)
					Library.MergeEntries(rawKey, target)
				} else {
					messagef("  %s is already an alias for %s — nothing to merge.\n", rawKey, target)
				}
				continue
			}
			Library.MergeEntries(alias, target)
		}
		doAllChecks(target)
	}
}

func doSetPreferredAlias(args []string) {
	if openLibraryToUpdate() {
		alias := args[0]
		key := resolveUserKey(args[1])
		entry := Library.buildEntry(key)
		if !entry.Exists() {
			messagef("Entry %s does not exist\n", key)
			return
		}
		if !validPreferredKeyAlias.MatchString(alias) {
			Library.Error(ErrorSetPreferredAliasInvalidFormat, alias)
			return
		}
		if target := Library.HintToKey.GetValue(alias); target != "" && target != key {
			Library.Error(ErrorSetPreferredAliasAlreadyInUse, alias, target)
			return
		}
		Library.setPreferredAlias(entry, alias)
		doAllChecks(key)
	}
}

// doSplitPosition gives one author/editor position of an entry a new contributor of
// its own, recorded as a different person from the one it had. It is for a name two
// people share that no rule tells apart (no ORCID, and DBLP lists both as one person).
// The new contributor gets no DBLP key, so the other papers on that DBLP person stay
// where they are; the entry record keeps the position with the new contributor.
func doSplitPosition(args []string) {
	if !openLibraryToUpdate() {
		return
	}
	key := resolveUserKey(args[0])
	role := args[1]
	position, err := strconv.Atoi(args[2])
	if key == "" || !Library.EntryExists(key) {
		messagef("Entry %s does not exist\n", args[0])
		return
	}
	if (role != "author" && role != "editor") || err != nil || position < 1 {
		messageln("Usage: -split_position <key> <author|editor> <position>")
		return
	}
	var oldID string
	bibQueryRow(`SELECT contributor_id FROM contributor_roles WHERE entry_key = ? AND role = ? AND position = ?`,
		key, role, position).Scan(&oldID) //nolint:errcheck
	if oldID == "" {
		messagef("%s has no %s at position %d\n", key, role, position)
		return
	}
	var name string
	bibQueryRow(`SELECT name_used FROM entry_contributor_names WHERE entry_key = ? AND role = ? AND position = ?`,
		key, role, position).Scan(&name) //nolint:errcheck
	if name == "" {
		if names := contributorFieldNames(Library.EntryFieldValueity(key, role)); position <= len(names) {
			name = names[position-1].name
		}
	}
	if name == "" {
		if c := Library.ContributorByID[oldID]; c != nil {
			name = c.Name
		}
	}
	newID := Library.addContributor(name)
	makeNameAmbiguous(&Library, name, oldID, newID)
	retroactivelyBackfillDisambiguation(&Library, name)
	dbExecSave("doSplitPosition: entry_contributor_names",
		`INSERT INTO entry_contributor_names (entry_key, role, position, contributor_id, name_used) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(entry_key, role, position) DO UPDATE SET contributor_id = excluded.contributor_id`,
		key, role, position, newID, name)
	dbExecSave("doSplitPosition: contributor_roles",
		`UPDATE contributor_roles SET contributor_id = ? WHERE entry_key = ? AND role = ? AND position = ?`,
		newID, key, role, position)
	addNonDoubleContributorPair(oldID, newID)
	Library.Progress("%s %s %d (%s): %s → %s, a different person.", key, role, position, name, oldID, newID)
}

func doApplyScript() {
	scriptPath := libraryBase() + ScriptFilePath
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		messagef("No script file found at %s\n", scriptPath)
		return
	}
	if openLibraryToUpdate() {
		stderrPrintf("\n")
		runScript()
	}
}

// resolveContributorArg resolves an EP_XXXX contributor ID or any known name
// form (canonical or alias) to a contributor ID and canonical name.
// Prints an error and returns ok=false when the argument cannot be resolved.
func resolveContributorArg(arg string) (id, name string, ok bool) {
	if strings.HasPrefix(arg, "EP-") {
		c, exists := Library.ContributorByID[arg]
		if !exists {
			messagef("No contributor found with ID %q\n", arg)
			return "", "", false
		}
		return arg, c.Name, true
	}
	cid, found := Library.NameToContributorID[arg]
	if !found {
		messagef("No contributor found for %q\n", arg)
		return "", "", false
	}
	return cid, Library.ContributorByID[cid].Name, true
}

func doAddNameMapping(args []string) {
	if !openLibraryToUpdate() {
		return
	}
	alias, canonical := args[0], args[1]
	if strings.HasPrefix(args[0], "EP-") {
		if _, name, ok := resolveContributorArg(args[0]); !ok {
			return
		} else {
			alias = name
		}
	}
	if strings.HasPrefix(args[1], "EP-") {
		if _, name, ok := resolveContributorArg(args[1]); !ok {
			return
		} else {
			canonical = name
		}
	}
	forceNameMapping = true
	Library.AddNameMapping(canonical, alias)
	forceNameMapping = false
	Library.RenormaliseNameFields()
}

// doRenameContributor is -rename_contributor <id> <name> (the web UI's author
// edit, Erik 2026-11-24): the contributor's stored name becomes name; the old
// one stays a name form of theirs (an alias), as does the new one. A name that
// is another contributor's stored name is refused (merge them instead).
func doRenameContributor(args []string) {
	if !openLibraryToUpdate() {
		return
	}
	id, oldName, ok := resolveContributorArg(args[0])
	if !ok {
		return
	}
	newName := strings.TrimSpace(args[1])
	if newName == "" || newName == oldName {
		messageln("rename_contributor: nothing to change.")
		return
	}
	var other string
	bibQueryRow(`SELECT id FROM contributors WHERE name = ? AND id != ?`, newName, id).Scan(&other) //nolint:errcheck
	if other != "" {
		messagef("rename_contributor: %q is the name of contributor %s; merge them instead (Mark for merge).\n", newName, other)
		return
	}
	if err := bibExec(`UPDATE contributors SET name = ? WHERE id = ?`, newName, id); err != nil {
		Library.Error("contributors rename failed: %s", err)
		return
	}
	upsertContributorNameToDB(id, oldName)
	upsertContributorNameToDB(id, newName)
	if c := Library.ContributorByID[id]; c != nil {
		c.Name = newName
	}
	Library.NameToContributorID[newName] = id
	Library.NameToContributorID[oldName] = id
	// The loaded library is current now but for the entries naming them: those the
	// service refreshes (no load afresh).
	var keys []string
	if rows, err := bibQuery(`SELECT DISTINCT entry_key FROM contributor_roles WHERE contributor_id = ?`, id); err == nil {
		for rows.Next() {
			var k string
			if rows.Scan(&k) == nil {
				keys = append(keys, k)
			}
		}
		rows.Close()
	}
	writesKeptInMemory([]string{"contributors", "contributor_names"}, keys)
	messagef("Contributor %s is now %q (%q kept as a name form).\n", id, newName, oldName)
}

func doCorrectName(args []string) {
	if !openLibraryToUpdate() {
		return
	}
	oldName, newName := args[0], args[1]
	if oldName == newName {
		messageln("Old and new names are identical — nothing to do.")
		return
	}

	// Find all contributor IDs that have oldName as a stored name form.
	oldRows, err := bibQuery(`SELECT DISTINCT id FROM contributor_names WHERE name = ?`, oldName)
	if err != nil {
		Library.Error("contributor_names query failed: %s", err)
		return
	}
	var oldIDs []string
	for oldRows.Next() {
		var id string
		if e := oldRows.Scan(&id); e == nil {
			oldIDs = append(oldIDs, id)
		}
	}
	oldRows.Close()

	if len(oldIDs) == 0 {
		messagef("Name not found in contributor_names: %q\n", oldName)
		return
	}
	if len(oldIDs) > 1 {
		messagef("Name %q is stored for %d contributors — resolve ambiguity manually.\n", oldName, len(oldIDs))
		return
	}
	oldID := oldIDs[0]

	// Find the contributor that already has newName as canonical (if any).
	var newID string
	bibQueryRow(`SELECT id FROM contributors WHERE name = ?`, newName).Scan(&newID) //nolint:errcheck

	about := TQuestion{Values: []TQuestionValue{{Role: "bad name", Value: oldName}, {Role: "correct name", Value: newName}}}
	needMerge := newID != "" && newID != oldID
	if needMerge {
		about.Warning = "The correct name is the canonical name of a different contributor: they are merged"
	}
	if !Reporting.ConfirmActionAbout(about, "Correct this name?") {
		return
	}

	// When new name belongs to a different contributor, merge old into new first.
	// mergeContributorInDB moves all names, roles, and entry_contributor_names from
	// oldID to newID, then deletes oldID. After the merge, oldName is a stored form
	// under newID — the cleanup below removes it.
	if needMerge {
		if !mergeContributorInDB(oldID, newID) {
			return
		}
		for n, id := range Library.NameToContributorID {
			if id == oldID {
				Library.NameToContributorID[n] = newID
			}
		}
		for orcid, id := range Library.ORCIDToContributorID {
			if id == oldID {
				Library.ORCIDToContributorID[orcid] = newID
			}
		}
		delete(Library.ContributorByID, oldID)
	}

	// Remove the bad name form from contributor_names.
	if err := bibExec(`DELETE FROM contributor_names WHERE name = ?`, oldName); err != nil {
		Library.Error("contributor_names delete failed: %s", err)
		return
	}

	// Pure rename: no pre-existing target contributor — update canonical and add new form.
	if newID == "" {
		if err := bibExec(`UPDATE contributors SET name = ? WHERE id = ?`, newName, oldID); err != nil {
			Library.Error("contributors rename failed: %s", err)
			return
		}
		if err := bibExec(`INSERT OR IGNORE INTO contributor_names (id, name) VALUES (?, ?)`, oldID, newName); err != nil {
			Library.Error("contributor_names insert failed: %s", err)
			return
		}
	}

	// Fix entry_contributor_names.
	if err := bibExec(`UPDATE entry_contributor_names SET name_used = ? WHERE name_used = ?`, newName, oldName); err != nil {
		Library.Error("entry_contributor_names update failed: %s", err)
		return
	}

	// Fix bib_entries author/editor field values.
	if err := bibExec(`UPDATE bib_entries SET value = REPLACE(value, ?, ?) WHERE field IN ('author','editor') AND value LIKE '%' || ? || '%'`, oldName, newName, oldName); err != nil {
		Library.Error("bib_entries update failed: %s", err)
		return
	}

	// Fix non_double_contributor_names: rewire pairs involving oldName.
	ndRows, err := bibQuery(`SELECT name1, name2 FROM non_double_contributor_names WHERE name1 = ? OR name2 = ?`, oldName, oldName)
	if err == nil {
		var pairs [][2]string
		for ndRows.Next() {
			var n1, n2 string
			if e := ndRows.Scan(&n1, &n2); e == nil {
				pairs = append(pairs, [2]string{n1, n2})
			}
		}
		ndRows.Close()
		for _, p := range pairs {
			if err := bibExec(`DELETE FROM non_double_contributor_names WHERE name1 = ? AND name2 = ?`, p[0], p[1]); err != nil {
				// Previously silent — see the matching comment in TKeyAliasTable.Load.
				Library.Warning("non_double_contributor_names delete failed (name1=%s, name2=%s): %s", p[0], p[1], err)
				markDbWriteFailed(fmt.Sprintf("non_double_contributor_names delete failed (name1=%s, name2=%s): %s", p[0], p[1], err))
			}
			n1, n2 := p[0], p[1]
			if n1 == oldName {
				n1 = newName
			}
			if n2 == oldName {
				n2 = newName
			}
			if n1 != n2 {
				if n1 > n2 {
					n1, n2 = n2, n1
				}
				if err := bibExec(`INSERT OR IGNORE INTO non_double_contributor_names (name1, name2) VALUES (?, ?)`, n1, n2); err != nil {
					// Previously silent — see the matching comment in TKeyAliasTable.Load.
					Library.Warning("non_double_contributor_names insert failed (name1=%s, name2=%s): %s", n1, n2, err)
					markDbWriteFailed(fmt.Sprintf("non_double_contributor_names insert failed (name1=%s, name2=%s): %s", n1, n2, err))
				}
			}
		}
	}

	loadContributorsFromDb(&Library)
	Library.CheckNameMappingConsistency()
	Library.Progress("Name correction complete.")
}

// doMergeContributors merges one or more "from" contributors into a single "into"
// contributor — args[len(args)-1] is the target, everything before it is a source,
// mirroring cp's "sources... dest" argument order.
func doMergeContributors(args []string) {
	if !openLibraryToUpdate() {
		return
	}
	into := args[len(args)-1]
	froms := args[:len(args)-1]

	toID, toName, ok := resolveContributorArg(into)
	if !ok {
		return
	}

	for _, fromArg := range froms {
		fromID, fromName, ok := resolveContributorArg(fromArg)
		if !ok {
			continue
		}
		if toID == fromID {
			messagef("%q resolves to the same contributor as %q — skipping.\n", fromArg, into)
			continue
		}
		Library.Progress("Merging %q (%s) into %q (%s)", fromName, fromID, toName, toID)
		if !mergeContributorInDB(fromID, toID) {
			continue
		}
		// Update in-memory maps before RenormaliseNameFields reloads contributors.
		for n, id := range Library.NameToContributorID {
			if id == fromID {
				Library.NameToContributorID[n] = toID
			}
		}
		for orcid, id := range Library.ORCIDToContributorID {
			if id == fromID {
				Library.ORCIDToContributorID[orcid] = toID
			}
		}
		if toContrib, ok := Library.ContributorByID[toID]; ok {
			if toContrib.ORCID == "" {
				if fromContrib, exists := Library.ContributorByID[fromID]; exists {
					toContrib.ORCID = fromContrib.ORCID
				}
			}
		}
		delete(Library.ContributorByID, fromID)
		Library.Progress("Merged %q into %q (%s).", fromName, toName, toID)
	}
	Library.RenormaliseNameFields()
}

// isValidORCID checks that s matches the ORCID format NNNN-NNNN-NNNN-NNN[0-9X].
func isValidORCID(s string) bool {
	if len(s) != 19 {
		return false
	}
	for i, ch := range s {
		switch i {
		case 4, 9, 14:
			if ch != '-' {
				return false
			}
		case 18:
			if (ch < '0' || ch > '9') && ch != 'X' {
				return false
			}
		default:
			if ch < '0' || ch > '9' {
				return false
			}
		}
	}
	return true
}

// doEnrichOrcidProfilesCore is the body of -enrich_orcid_profiles, callable
// from both the standalone command and the combined -enrich_contributor_data pass.
// Fetches ORCID person records, adds aliases, and challenges canonical mismatches.
func doEnrichOrcidProfilesCore() {
	var skippedForms []string
	collectSkippedNameMappings = &skippedForms
	defer func() {
		collectSkippedNameMappings = nil
		reportSkippedNameMappings(skippedForms)
	}()
	loadNonDoubleContributorNamesFromDb(&Library)

	if !Online {
		Library.Progress("Not online — ORCID profile enrichment skipped.")
		return
	}

	rows, err := db.Query(`SELECT contributor_id, orcid FROM contributor_orcids`)
	if err != nil {
		Library.Warning("Could not query contributor_orcids: %s", err)
		return
	}
	type idOrcid struct{ id, orcid string }
	var pairs []idOrcid
	for rows.Next() {
		var id, orcid string
		if rows.Scan(&id, &orcid) == nil {
			pairs = append(pairs, idOrcid{id, orcid})
		}
	}
	rows.Close()

	if len(pairs) == 0 {
		Library.Progress("No contributors with ORCIDs found.")
		return
	}

	// Count contributors that need processing, distinguishing disk-cache hits
	// (fast) from genuine network fetches (slow, cache miss).
	needsFetch := 0
	fromCache := 0
	fromNetwork := 0
	reCheckCount := 0
	for _, p := range pairs {
		if c, ok := Library.ContributorByID[p.id]; ok {
			s := loadORCIDSeen(p.id, p.orcid)
			if s.canonical == c.Name && s.canonical != "" {
				continue // already up to date
			}
			needsFetch++
			if loadCachedORCIDPerson(p.orcid) != nil {
				fromCache++
			} else {
				fromNetwork++
			}
			if s.canonical != "" {
				reCheckCount++
			}
		}
	}
	if needsFetch == 0 {
		Library.Progress("All %d ORCID contributor record(s) already up to date.", len(pairs))
		return
	}
	Library.Progress("%d ORCID contributor record(s) to process: %d from cache, %d need network fetch, %d re-check (%d already up to date).",
		needsFetch, fromCache, fromNetwork, reCheckCount, len(pairs)-needsFetch)
	newAliases := 0
	matchedOnly := cmdMatchedOrcidDataOnly
	skipped := 0
	ticker := Library.NewProgressTicker("Fetching ORCID person records", needsFetch)
	fetched := 0
	if matchedOnly {
		Library.Progress("Matched-only pass: challenges will be skipped (they remain as homework).")
	}

	for _, p := range pairs {
		if Library.QuitWasRequested() {
			break
		}
		contrib, ok := Library.ContributorByID[p.id]
		if !ok {
			continue
		}
		Library.ResetQuestionFlag()

		// Fast path: if the seen record's canonical matches the current canonical, the
		// ORCID data is unchanged and no challenge is needed — skip the network fetch.
		storedSeen := loadORCIDSeen(p.id, p.orcid)
		if storedSeen.canonical == contrib.Name && storedSeen.canonical != "" {
			continue
		}

		// Slow path: canonical changed or never seen — use cache then network.
		fetched++
		if ticker.SetCount(fetched) {
			break
		}
		person := getORCIDPerson(p.orcid)
		if person == nil {
			// Profile unavailable (private or not found): record as seen so the
			// homework counter and fast-path don't keep flagging this contributor.
			upsertORCIDSeen(p.id, p.orcid, orcidSeenRecord{canonical: contrib.Name})
			continue
		}
		// Titles ("Dr.", "PhD") are no part of a name: compare and offer without them.
		stripped := *person
		stripped.CreditName = stripNameTitles(person.CreditName)
		stripped.DeclaredName = stripNameTitles(person.DeclaredName)
		stripped.OtherNames = nil
		for _, n := range person.OtherNames {
			stripped.OtherNames = append(stripped.OtherNames, stripNameTitles(n))
		}
		person = &stripped

		// Build the full set of name forms from the ORCID record:
		// credit-name, declared (Last,First), natural order (First Last), other-names.
		passportNatural := "" // given-names + " " + family-name (First Last order)
		if person.DeclaredName != "" {
			passportNatural = swapBibTeXNameFormat(person.DeclaredName) // Last,First → First Last
		}
		swappedCredit := swapBibTeXNameFormat(person.CreditName)
		inferredName := inferBibTeXFromCreditName(person.CreditName, person.DeclaredName)
		allForms := uniqueForms(
			append([]string{person.CreditName, person.DeclaredName, passportNatural, swappedCredit, inferredName},
				person.OtherNames...)...)

		// Full seen-record check: compare all ORCID-provided name values (canonical
		// already confirmed to match above for the fast path; this catches cases where
		// the canonical changed externally, which bypasses the fast path).
		sortedOtherNames := append([]string(nil), person.OtherNames...)
		sort.Strings(sortedOtherNames)
		currentSeen := orcidSeenRecord{
			canonical:    contrib.Name,
			creditName:   person.CreditName,
			declaredName: person.DeclaredName,
			otherNames:   strings.Join(sortedOtherNames, "|"),
		}
		alreadySeen := storedSeen == currentSeen

		// Empty profile: ORCID has no names at all — nothing to decide.
		// Mark as seen silently so it doesn't recur as homework.
		if person.CreditName == "" && person.DeclaredName == "" && len(person.OtherNames) == 0 {
			upsertORCIDSeen(p.id, p.orcid, currentSeen)
			continue
		}

		// Determine credit-name and declared-name status BEFORE registering new aliases
		// so the "was it already explicitly mapped?" signal is not contaminated by this run.
		//
		// The manual canonical wins (suppresses challenge) when any of:
		//   (a) credit-name matches the canonical in some ordering,
		//   (b) credit-name (or its swap) was already mapped to this canonical in a prior run,
		//   (c) declared name (ORCID family+given) matches the canonical exactly or via swap —
		//       this is the most authoritative signal: ORCID's own passport-name matches.
		// Exception: a different surname in the declared name overrides all of the above,
		// since it suggests the canonical's surname boundary may be wrong.
		priorCreditMapped := Library.NameAliasToName[person.CreditName] == contrib.Name ||
			(swappedCredit != "" && Library.NameAliasToName[swappedCredit] == contrib.Name)
		creditMatches := creditNameMatches(contrib.Name, person.CreditName)
		declaredMatches := creditNameMatches(contrib.Name, person.DeclaredName)
		surnameBoundaryDiffers := declaredNameHasDifferentSurname(contrib.Name, person.DeclaredName)
		manualNameWins := ((creditMatches || priorCreditMapped || declaredMatches) && !surnameBoundaryDiffers) ||
			(alreadySeen && !surnameBoundaryDiffers)

		// Register all unambiguous, unmapped forms as aliases for this contributor.
		for _, form := range allForms {
			if form == contrib.Name {
				continue
			}
			_, mapped := Library.NameToContributorID[form]
			_, ambiguous := Library.AmbiguousNameToContributorIDs[form]
			if mapped || ambiguous {
				continue
			}
			upsertContributorNameToDB(p.id, form)
			Library.NameToContributorID[form] = p.id
			Library.AddNameMapping(contrib.Name, form)
			newAliases++
		}
		// Also register swapped forms of other-names.
		for _, alias := range person.OtherNames {
			if swapped := swapBibTeXNameFormat(alias); swapped != "" && swapped != contrib.Name {
				_, mapped := Library.NameToContributorID[swapped]
				_, ambiguous := Library.AmbiguousNameToContributorIDs[swapped]
				if !mapped && !ambiguous {
					upsertContributorNameToDB(p.id, swapped)
					Library.NameToContributorID[swapped] = p.id
					Library.AddNameMapping(contrib.Name, swapped)
					newAliases++
				}
			}
		}

		// Manual name wins: credit-name or declared-name matches, or already mapped.
		if manualNameWins {
			if !alreadySeen {
				upsertORCIDSeen(p.id, p.orcid, currentSeen)
			}
			continue
		}

		// For contributors with a DBLP key, DBLP has established their identity —
		// add ORCID aliases without a challenge. One exception: if the ORCID declared
		// surname differs significantly from the DBLP canonical, the ORCID↔DBLP link
		// may be wrong on either side. Flag it and skip rather than auto-apply.
		if contrib.DblpKey != "" {
			// DBLP has established this contributor's identity. If the ORCID declared
			// surname still differs (after case-fold and containment normalisation), the
			// DBLP↔ORCID link may be imprecise (cultural naming conventions, wrong link,
			// etc.) but there is nothing actionable here at run time. Mark as seen;
			// enrichment will be limited to the aliases already registered above.
			upsertORCIDSeen(p.id, p.orcid, currentSeen)
			continue
		}

		// Contributor has no DBLP key — fall through to challenge logic.
		if matchedOnly {
			skipped++
			continue
		}

		if person.CreditName == "" {
			// No credit-name to challenge with — mark as seen so this ORCID does not
			// recur as homework.  We still registered any declared-name aliases above.
			upsertORCIDSeen(p.id, p.orcid, currentSeen)
			continue
		}
		_, ambiguous := Library.AmbiguousNameToContributorIDs[person.CreditName]
		existingID, mapped := Library.NameToContributorID[person.CreditName]
		if ambiguous || (mapped && existingID != p.id) {
			Library.Warning("ORCID %s: credit-name %q is ambiguous or claimed by another contributor — not challenging",
				p.orcid, person.CreditName)
			upsertORCIDSeen(p.id, p.orcid, currentSeen)
			continue
		}

		// Commit the spinner line (make it permanently visible) before the challenge
		// dialog so the user can see the fetch count above the dialog.
		SpinnerCommit()

		// Present the options for the new canonical.
		options := TStringSetNew()
		options.Add("k", "o")
		offerDeclared := person.DeclaredName != ""
		if offerDeclared {
			options.Add("d")
		}
		if passportNatural != "" && passportNatural != person.CreditName && passportNatural != person.DeclaredName {
			options.Add("n") // natural order of declared name
		}
		offerInferred := inferredName != "" && inferredName != contrib.Name &&
			inferredName != person.DeclaredName && inferredName != person.CreditName
		if offerInferred {
			options.Add("i")
		}
		options.Add("e")
		values := []TQuestionValue{{Role: "current name", Value: contrib.Name}, {Role: "ORCID credit-name", Value: person.CreditName}}
		if person.DeclaredName != "" {
			v := TQuestionValue{Role: "declared name", Value: person.DeclaredName}
			if surnameBoundaryDiffers {
				v.Note = "surname boundary differs from the current name"
			}
			values = append(values, v)
		}
		if offerInferred {
			values = append(values, TQuestionValue{Role: "inferred name", Value: inferredName, Note: "credit-name parsed using the declared surname"})
		}
		labels := []TQuestionOption{{Key: "k", Label: fmt.Sprintf("keep %q", contrib.Name)},
			{Key: "o", Label: fmt.Sprintf("use ORCID credit-name (%q)", person.CreditName)},
			{Key: "d", Label: fmt.Sprintf("use declared (%q)", person.DeclaredName)},
			{Key: "n", Label: fmt.Sprintf("natural order of declared (%q)", passportNatural)},
			{Key: "i", Label: fmt.Sprintf("use inferred (%q)", inferredName)}, {Key: "e", Label: "edit", Value: "the canonical name"}}
		answer := Library.WarningQuestionAbout(TQuestion{Text: "Which name?", Options: labels, Values: values}, "", options,
			"ORCID %s: credit-name %q differs from canonical %q for %s",
			p.orcid, person.CreditName, contrib.Name, p.id)

		var newCanon string
		switch answer {
		case "o":
			newCanon = person.CreditName
		case "d":
			newCanon = person.DeclaredName
		case "i":
			newCanon = inferredName
		case "n":
			newCanon = passportNatural
		case "e":
			raw, _ := Library.AskForInput("Enter new canonical name")
			newCanon = strings.TrimSpace(raw)
		default: // "k" or empty → keep
		}

		if newCanon != "" && newCanon != contrib.Name {
			oldCanon := contrib.Name
			contrib.Name = newCanon
			upsertContributorToDB(p.id, newCanon, contrib.ORCID)
			upsertContributorNameToDB(p.id, oldCanon)
			Library.NameToContributorID[oldCanon] = p.id
			Library.NameToContributorID[newCanon] = p.id
			Library.AddNameMapping(newCanon, oldCanon)
			newAliases++
		}

		// Record the seen signature with the final canonical (after any promotion).
		// On future runs: if canonical, credit-name, declared-name, and other-names
		// all match this record, the challenge is suppressed. Any change to any of
		// these values — including an external canonical update — triggers a re-challenge.
		currentSeen.canonical = contrib.Name
		upsertORCIDSeen(p.id, p.orcid, currentSeen)

		Library.Progress("ORCID %s processed (%d/%d fetched, %d new alias(es) so far).",
			p.orcid, fetched, needsFetch, newAliases)

		if Library.QuitWasRequested() {
			break
		}
	}
	ticker.Done()
	if matchedOnly && skipped > 0 {
		Library.Progress("ORCID profile enrichment: %d new alias(es) added; %d challenge(s) skipped (Enrich contributors asks about them).", newAliases, skipped)
	} else {
		Library.Progress("ORCID profile enrichment: %d new alias(es) added.", newAliases)
	}
}

// doEnrichOrcidProfiles is the -enrich_orcid_profiles entry point.
func doEnrichOrcidProfiles() {
	if !openLibraryToUpdate() {
		return
	}
	doEnrichOrcidProfilesCore()
}

func doAssignOrcid(args []string) {
	if !openLibraryToUpdate() {
		return
	}
	loadNonDoubleContributorNamesFromDb(&Library)

	orcid := strings.ToUpper(strings.TrimSpace(args[1]))

	if !isValidORCID(orcid) {
		messagef("Invalid ORCID format %q — expected NNNN-NNNN-NNNN-NNN[0-9X]\n", orcid)
		return
	}

	id, _, ok := resolveContributorArg(args[0])
	if !ok {
		return
	}

	contrib := Library.ContributorByID[id]

	// Check for ownership conflict.
	if existingID := orcidToContributorID(&Library, orcid); existingID != "" && existingID != id {
		existing := Library.ContributorByID[existingID]
		messagef("ORCID %s is already assigned to contributor %s (%s)\n",
			orcid, existingID, existing.Name)
		return
	}

	canonical := contrib.ORCID == ""
	upsertContributorORCIDToDB(id, orcid, canonical)
	Library.ORCIDToContributorID[orcid] = id
	if canonical {
		contrib.ORCID = orcid
		Library.Progress("Set canonical ORCID %s for contributor %s (%s)", orcid, id, contrib.Name)
	} else {
		Library.Progress("Added additional ORCID %s for contributor %s (%s) — canonical is %s",
			orcid, id, contrib.Name, contrib.ORCID)
	}
}

// Command flags, at package level so the command table (commands.go) can use them.
var (
	cmdSync                     bool
	cmdGetPdfs                  bool
	cmdAddFailedURLs            bool
	cmdFindEntries              bool
	cmdAuditMappings            bool
	cmdEntryKey                 bool
	cmdEntryKeyAlias            bool
	cmdShowEntry                bool
	cmdFixEntries               bool
	cmdFixDuplicates            bool // -fix_duplicates: fix entries in unresolved title groups
	cmdFixCandidates            bool // -fix_candidates: link unmatched entries to DBLP
	cmdTriageAuthorMappings     bool // -triage_author_mappings: triage author/editor superseded_field_values
	cmdTriageContributorAliases bool // -triage_contributor_aliases: generalise or keep entry-specific contributor aliases
	cmdDisambiguateContributors bool // -disambiguate_contributors: resolve ambiguous contributor-name assignments
	cmdAssignOrcid              bool // -assign_orcid: assign an ORCID to a contributor
	cmdEnrichContributorData    bool // -enrich_contributor_data: run full contributor enrichment pipeline
	cmdMergeContributors        bool // -merge_contributors: merge two contributors into one
	cmdMergeOrcidDuplicates     bool // -merge_orcid_duplicates: merge contributors sharing an ORCID, or by ORCID's duplicates
	cmdAddDblpEntry             bool
	cmdAddDblpEntries           bool
	cmdWatch                    bool
	cmdAddGroup                 bool // -add_group: create a new BibDesk static group (with optional initial entries)
	cmdAddKeyMapping            bool
	cmdMergeEntries             bool
	cmdAddNameMapping           bool
	cmdCorrectName              bool
	cmdAddDoiAlias              bool
	cmdSetPreferredAlias        bool
	cmdSplitPosition            bool
	logFilePath                 string
	cmdNewKey                   bool
	cmdDeleteEntry              bool
	cmdMap                      bool
	cmdUseAliases               bool
	cmdAddToGroup               bool
	cmdRemoveFromGroup          bool
	cmdSetGroups                bool
	cmdSetField                 bool
	cmdEditEntry                bool
	cmdRenameContributor        bool
	cmdTableEdit                bool
	cmdRenderAsBibTeX           bool
	cmdRenderGroup              bool
	cmdListGroupAliases         bool
	cmdRenderAsTex              bool
	cmdRenderAsHTML             bool
	cmdRenderAsText             bool
	cmdCheckPdfs                bool
	cmdAlignBooktitleCountries  bool
	cmdUpdateOrcidCache         bool
	cmdLoadDblpXml              bool
	cmdPushDblp                 bool
	cmdRestoreDblp              bool
	cmdUpdateDblp               bool
	cmdRestoreKeyHints          bool
	restoreKeyHintsPath         string
	cmdDeleteGarbage            bool
	cmdRestoreFromDump          bool
	cmdRestoreBackupName        string // -restore <name>: restore named backup to home DB
	cmdApplyScript              bool
	// Unified table export/import (v23.0).
	// Bare flag (-export) means "all"; with value (-export t1,t2) means those tables.
	cmdExport tableListFlag
	cmdImport tableListFlag

	cmdSetSyncStatus string // -set_sync_status <status> <source_key> <stem>: set/clear a sync status flag

	cmdImportBib bool

	cmdHarvest bool // -harvest: harvest entries from a bib file (path from args) or stdin
)

func main() {
	if os.Getenv(envServiceJob) == "" { // a service job: its command line printed this already
		stderrPrintf("%s %s\n", filepath.Base(os.Args[0]), AppVersion)
	}

	// Without this, Ctrl-C during a long run (e.g. -update_all_dblp_entries, which
	// wraps its entire pass in one open transaction, committed only at the very end)
	// kills the process with zero cleanup: no commit, no finaliseWorkingDatabase.
	// Every decision made during that run — including every interactively-answered
	// question — is silently discarded, as if it never happened.
	//
	// Unlike a typed "q" (handled synchronously on the main goroutine, inside
	// whatever prompt is asking — see interaction.go), SIGINT is delivered to this
	// goroutine asynchronously while the main goroutine keeps running. Calling
	// quitNow() (commit/save/finalise, touching db) directly from here used to race
	// the main goroutine's own db use — observed live as a storm of "database is
	// locked" errors followed by a nil-pointer panic once finaliseWorkingDatabase
	// closed out from under a still-running main-goroutine query. So this handler
	// only sets the interrupted flag and returns; every outer scan loop already checks
	// QuitWasRequested() at the top of each iteration (that's what quitNow()'s
	// v29.29 unification was for), so the main goroutine unwinds itself to main()'s
	// own cleanup tail on its own — the only goroutine ever touching db.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		if relayingToService.Load() {
			return // the service job reports and handles it (service.go)
		}
		stderrPrintf("\nInterrupted — finishing the current step, then committing and saving...\n")
		interrupted.Store(true)
	}()

	flag.StringVar(&keyPrefixArg, "key_prefix", "", "the key prefix of a new library: two letters (e.g. -key_prefix EP)")
	baseFlag := flag.String("base", "", "path/basename of the library (required)")
	jobsFile := flag.String("jobs_file", "", "(test) run the commands in a file, one job per line, in one process")
	flag.BoolVar(&forceWrite, "force_write", false, "force write even if unchanged")

	flag.BoolVar(&cmdTrustHints, "trust_hints", false, "harvest: auto-accept key-hint matches without confirmation")
	flag.BoolVar(&cmdCollectKeys, "collect_keys", false, "harvest: add source entry keys to the hints DB when unambiguous")
	flag.StringVar(&logFilePath, "log", "", "also write the run's output, questions and answers to this file (appended): -log <file>")
	flag.StringVar(&logFilePath, "l", "", "short for -log")

	flag.BoolVar(&cmdStartService, "start", false, "start the service for this library in the background (commands then run through it) and open its web UI (-ui=false: not)")
	flag.BoolVar(&cmdStopService, "stop", false, "stop the service gracefully (a running job is stopped as with Ctrl-C)")
	flag.BoolVar(&cmdRestartService, "restart", false, "stop the service (when it runs) and start it again, opening its web UI (-ui=false: not)")
	flag.BoolVar(&cmdOpenUI, "ui", false, "open the service's web UI in the browser (the service must run: -start)")
	flag.BoolVar(&cmdSelect, "select", false, "open a Select tab in the web UI per .keys file of this folder's bib.config (optional arg: one file)")
	flag.BoolVar(&cmdServiceStatus, "status", false, "tell whether the service runs, and which job")
	flag.BoolVar(&cmdPush, "push", false, "push this machine's changes to the shared DB folder (Nextcloud) when they are not there yet")
	flag.BoolVar(&cmdLoadLibrary, "load_library", false, "(internal) open the library and change nothing; the service runs it when it starts")
	flag.BoolVar(&cmdServe, "serve", false, "(internal) run as the service; started by -start")

	var cmdVersion bool
	flag.BoolVar(&cmdVersion, "version", false, "print version and exit")
	flag.BoolVar(&cmdVersion, "v", false, "print version and exit")

	flag.BoolVar(&cmdSync, "sync", false, "sync library to bib file(s) via exchange config; optional arg narrows to one file")
	flag.BoolVar(&cmdPull, "pull", false, "with -sync: skip up-sync (phase 1) and re-import; only write bib output from DB")
	flag.BoolVar(&cmdGetPdfs, "get_pdfs", false, "download missing PDFs into the files folder")
	flag.BoolVar(&cmdAddFailedURLs, "add_failed_urls", false, "add the URLs of the last failed PDF downloads (urls_failed.csv) to urls_ignore, so -get_pdfs skips them")
	flag.BoolVar(&cmdAuditMappings, "audit_mappings", false, "report field_mappings rows that never apply, are undone or chained (read-only; details in <base>.tables/mapping_audit.log)")
	flag.BoolVar(&cmdFindEntries, "find_entries", false, "list entries matching field [value] (key TAB value per line)")
	flag.BoolVar(&cmdEntryKey, "entry_key", false, "resolve alias to canonical key")
	flag.BoolVar(&cmdEntryKeyAlias, "entry_key_alias", false, "get preferred alias for a key")
	flag.BoolVar(&cmdShowEntry, "show_entry", false, "print full entry content")
	flag.BoolVar(&cmdFixEntries, "fix_entries", false, "fix/check specific entries")
	flag.BoolVar(&cmdFixDuplicates, "fix_duplicates", false, "interactively resolve title-duplicate pairs in the library")
	flag.BoolVar(&cmdTriageAuthorMappings, "triage_author_mappings", false, "triage author/editor entries in superseded_field_values")
	flag.BoolVar(&cmdTriageContributorAliases, "triage_contributor_aliases", false, "generalise or keep entry-specific contributor aliases")
	flag.BoolVar(&cmdDisambiguateContributors, "disambiguate_contributors", false, "resolve ambiguous contributor-name assignments in entry_contributor_names")
	flag.BoolVar(&cmdAssignOrcid, "assign_orcid", false, "assign an ORCID to a contributor: -assign_orcid <name-or-EP-id> <orcid>")
	flag.BoolVar(&cmdEnrichContributorData, "enrich_contributor_data", false, "run full contributor enrichment pipeline: absorb DBLP names+ORCIDs, enrich from ORCID profiles, merge ORCID duplicates")
	flag.BoolVar(&cmdMatchedOrcidDataOnly, "matched_orcid_data_only", false, "with -enrich_contributor_data: skip ORCID challenges in step 3, leaving them as homework")
	flag.BoolVar(&cmdMergeContributors, "merge_contributors", false, "merge one or more contributors into another: -merge_contributors <from...> <into>")
	flag.BoolVar(&cmdMergeOrcidDuplicates, "merge_orcid_duplicates", false, "merge contributors that share an ORCID, or whose ORCIDs ORCID itself merged (step 4 of -enrich_contributor_data)")
	flag.BoolVar(&cmdAddDblpEntries, "update_all_dblp_entries", false, "update all library entries that have a DBLP key with fresh DBLP data")
	flag.BoolVar(&cmdFixCandidates, "fix_candidates", false, "interactively link library entries without a DBLP key to DBLP records")
	flag.BoolVar(&cmdFix, "fix", false, "apply full per-entry checks when combined with -sync or -harvest")
	flag.BoolVar(&cmdAddDblpEntry, "add_dblp_entry", false, "upsert DBLP data for one or more given entries (library or DBLP keys)")
	flag.BoolVar(&cmdWatch, "watch", false, "check watched persons/ORCIDs for missing publications")
	flag.BoolVar(&cmdAddGroup, "add_group", false, "create a new BibDesk static group: -add_group <group_name> [key...]")
	flag.BoolVar(&cmdAddKeyMapping, "add_key_mapping", false, "add key alias(es) to a canonical key: -add_key_mapping <alias>... <canonical>")
	flag.BoolVar(&cmdMergeEntries, "merge_entries", false, "merge entries into target: -merge_entries <key>... <target>")
	flag.BoolVar(&cmdAddNameMapping, "add_name_mapping", false, "add a name alias mapping: -add_name_mapping <alias> <canonical>")
	flag.BoolVar(&cmdCorrectName, "correct_name", false, "correct a name spelling everywhere: -correct_name <bad_name> <correct_name>")
	flag.BoolVar(&cmdAddDoiAlias, "add_doi_alias", false, "record a DOI as an alias for an entry: -add_doi_alias <doi> <entry_key>")
	flag.BoolVar(&cmdSetPreferredAlias, "set_preferred_alias", false, "set preferred alias for a key: -set_preferred_alias <alias> <key>")
	flag.BoolVar(&cmdSplitPosition, "split_position", false, "give an author/editor position a new contributor of its own: -split_position <key> <author|editor> <position>")
	flag.BoolVar(&cmdNewKey, "new_key", false, "print a fresh canonical key and exit")
	flag.BoolVar(&cmdDeleteEntry, "delete_entry", false, "delete one or more library entries: -delete_entry <key>...")
	flag.BoolVar(&cmdMap, "map", false, "also record alias→key in the project map file (with -entry_key_alias)")
	flag.BoolVar(&cmdAddToGroup, "add_to_group", false, "add an entry to a group: -add_to_group <key> <group>")
	flag.BoolVar(&cmdRemoveFromGroup, "remove_from_group", false, "remove an entry from a group")
	flag.BoolVar(&cmdSetGroups, "set_groups", false, "set group membership: -set_groups <key> [+] <group>...")
	flag.BoolVar(&cmdTableEdit, "table_edit", false, "internal (web UI): change a row of a mapping or alias table: -table_edit <table> <json {op, row, old}>")
	flag.BoolVar(&cmdRenameContributor, "rename_contributor", false, "give a contributor another stored name, keeping the old one as a name form: -rename_contributor <EP-id> <name>")
	flag.BoolVar(&cmdEditEntry, "edit_entry", false, "internal (web UI): apply an entry's changes in one go: -edit_entry <key> <json {set, join, leave}>")
	flag.BoolVar(&cmdSetField, "set_field", false, "set a field on an entry: -set_field <key> <field> [<value>] (omit value to clear)")
	flag.BoolVar(&cmdRenderGroup, "render_group", false, "render all entries in a group to pubs/citations folders")
	flag.BoolVar(&cmdListGroupAliases, "list_group_aliases", false, "list canonical|alias pairs for all entries in a group")
	flag.BoolVar(&cmdUseAliases, "use_aliases", false, "use preferred aliases as file names in -render_group")
	flag.BoolVar(&cmdRenderAsBibTeX, "render_as_bibtex", false, "render entry as self-contained BibTeX")
	flag.BoolVar(&cmdRenderAsTex, "render_as_tex", false, "render entry as TeX bibliography reference")
	flag.BoolVar(&cmdRenderAsHTML, "render_as_html", false, "render entry as HTML bibliography reference")
	flag.BoolVar(&cmdRenderAsText, "render_as_text", false, "render entry as plain-text bibliography reference")
	flag.BoolVar(&cmdCheckPdfs, "check_pdfs", false, "check PDF health, orphan files, and duplicates in the files folder")
	flag.BoolVar(&cmdAlignBooktitleCountries, "align_booktitle_countries", false, "detect and fix unbraced country names in booktitle fields")
	flag.BoolVar(&cmdUpdateOrcidCache, "update_orcid", false, "refresh the ORCID disk cache for all known contributors (oldest-first; Ctrl-C to stop)")
	flag.BoolVar(&cmdLoadDblpXml, "load_dblp_xml", false, "load a DBLP .xml.gz export into the local DBLP store")
	flag.BoolVar(&cmdPushDblp, "push_dblp", false, "copy the local DBLP store to DBLP/ in the shared DB folder (Nextcloud)")
	flag.BoolVar(&cmdRestoreDblp, "restore_dblp", false, "replace the local DBLP store with the copy in DBLP/ in the shared DB folder")
	flag.BoolVar(&cmdUpdateDblp, "update_dblp", false, "import the DBLP XML export: downloaded from dblp_url (settings) when set, else found in your Downloads folder")
	flag.BoolVar(&cmdRestoreKeyHints, "restore_key_hints", false, "restore key hints from a backup CSV, remapping old keys via key_oldies")
	flag.StringVar(&restoreKeyHintsPath, "hints_csv", "", "path to the backup key_hints.csv for -restore_key_hints")
	flag.BoolVar(&cmdDeleteGarbage, "delete_garbage", false, "delete DBLP trash folder contents and exit")
	flag.BoolVar(&cmdRestoreFromDump, "restore_from_dump", false, "rebuild the local database from $base.dump in the shared DB folder and push it (use after corruption)")
	flag.StringVar(&cmdRestoreBackupName, "restore", "", "restore the local database from a named backup and push it to the shared DB folder (e.g. -restore ErikProper_20260709_123456)")
	flag.BoolVar(&cmdNoGarbageCleaning, "no_garbage_cleaning", false, "skip background cleanup of the DBLP trash folder")
	flag.BoolVar(&cmdApplyScript, "do_entry_actions", false, "evaluate group assignment rules from <base>.scripts/entry_actions")
	// Unified table export / import (v23.0)
	flag.Var(&cmdExport, "export", "export tables to <base>.tables/ (bare = all; or comma-separated table names)")
	flag.Var(&cmdImport, "import", "import tables from <base>.tables/, replace-all with confirmation (bare = all; or comma-separated table names)")
	flag.StringVar(&cmdSetSyncStatus, "set_sync_status", "", "set/clear a sync status flag: -set_sync_status <status|''> <source_key> <stem>")
	flag.BoolVar(&cmdImportBib, "import_bib", false, "import a bib file into the DB (requires filename argument; use to initialise or reinitialise bib_entries)")
	flag.BoolVar(&cmdHarvest, "harvest", false, "interactively ingest entries from a bib file (path from args) or stdin into the library")

	flag.Parse()
	args := flag.Args()

	// Modifier flags after positional arguments (e.g. -entry_key_alias key -map).
	args = rescanModifierArgs(args)

	if cmdVersion {
		resultln(AppVersion) // on stdout, for scripts (bib.common compares it)
		endRun(0)
	}

	// -set_sync_status operates on an explicit .sync file; no -base needed.
	if cmdSetSyncStatus != "" {
		if len(args) < 2 {
			messageln("Usage: bibdb_manager -set_sync_status <status|''> <source_key> <sync-file-stem>")
			endRun(1)
		}
		DoSetSyncStatus(cmdSetSyncStatus, args[0], args[1])
		endRun(0)
	}

	if *baseFlag == "" {
		messageln("Usage: bibdb_manager -base <path/basename> [-command] [args...]")
		flag.PrintDefaults()
		endRun(1)
	}

	if absBase, err := filepath.Abs(*baseFlag); err == nil {
		*baseFlag = stripKnownBaseExtension(absBase)
	}
	bibTeXFolder = filepath.Dir(*baseFlag) + "/"
	bibTeXBaseName = filepath.Base(*baseFlag)
	BibFile = bibTeXBaseName + BibFileExtension

	Reporting = TInteraction{}

	loadBibTeXFolders(libraryBase() + SettingsFileExtension)
	if backupFolder == "" {
		backupFolder = libraryBase() + ".backups/"
	}
	if localDBFolder == "" {
		localDBFolder = bibTeXFolder
	}
	if sharedDBFolder == "" {
		sharedDBFolder = bibTeXFolder
	}
	// The service (step 22.2): -start/-stop/-status/-serve, and any other command
	// is handed to a running service. Before the instance lock: the service's
	// jobs take it themselves. Before -log: the job writes the log.
	if handleServiceCommands() {
		endRun(0)
	}

	if logFilePath != "" {
		l, err := newLoggingIO(userIO, logFilePath)
		if err != nil {
			messagef("Cannot open log file %s: %s\n", logFilePath, err)
			endRun(1)
		}
		userIO = l
	}

	maybeMigrateFolderNames()
	loadMaps()

	if cmdNewKey {
		doNewKey()
		endRun(0)
	}

	// Acquired here, before cmdUpdateDblp, rather than further down: -update_dblp
	// does a slow (multi-minute) import and does not endRun() afterward — it
	// falls through into the normal session below. acquireDblpLock alone does
	// not stop a second, unrelated invocation (e.g. -watch) from acquiring this same
	// instance lock and running a full session against the same working DB while
	// the import is still in progress; when -update_dblp later reaches its own
	// DB work, both processes can be writing at once, producing SQLITE_BUSY_SNAPSHOT
	// ("database is locked (517)") on whichever write loses the race.
	acquireInstanceLock()

	if cmdPushDblp || cmdRestoreDblp {
		lock := tryLockDblp()
		if lock == nil {
			messageln("DBLP import in progress — try again later.")
			endRun(1)
		}
		var err error
		if cmdPushDblp {
			err = pushDblpStore()
		} else {
			err = restoreDblpStore()
		}
		lock.Close()
		if err != nil {
			messageln(err)
			endRun(1)
		}
		endRun(0)
	}

	if !cmdUpdateDblp && !cmdLoadDblpXml {
		maybeRestoreDblpStore()
	}

	if cmdUpdateDblp {
		acquireDblpLock()
		maybeStartDblpTrashCleanup()
		endRunOnError(doUpdateDblp())
	}

	if cmdLoadDblpXml {
		if len(args) == 0 {
			messageln("Usage: -load_dblp_xml <path.xml.gz>")
			endRun(1)
		}
		acquireDblpLock()
		maybeStartDblpTrashCleanup()
		endRunOnError(doLoadDblpXml(args))
		endRun(0)
	}

	if cmdDeleteGarbage {
		des, err := os.ReadDir(dblpTrashFolder())
		if err != nil || len(des) == 0 {
			messagef("No DBLP trash to delete.\n")
			endRun(0)
		}
		messagef("Deleting DBLP trash...\n")
		start := time.Now()
		if err := os.RemoveAll(dblpTrashFolder()); err != nil {
			messagef("Error deleting trash: %s\n", err)
			endRun(1)
		}
		messagef("Done (%.0fs).\n", time.Since(start).Seconds())
		endRun(0)
	}

	if cmdRestoreFromDump {
		endRunOnError(doRestoreFromDump())
		endRun(0)
	}

	if cmdRestoreBackupName != "" {
		endRunOnError(doRestoreFromBackup(cmdRestoreBackupName))
		endRun(0)
	}

	maybeMigrateDblpNameFiles()
	connectToDatabase()

	if !cmdSync && !cmdFindEntries && !cmdEntryKey && !cmdEntryKeyAlias && !cmdShowEntry {
		maybeStartDblpTrashCleanup()
	}

	if *jobsFile != "" {
		runJobsFromFile(*jobsFile)
		return
	}
	runJob(selectCommand(), args)
}

// gracefulQuit runs the normal post-check and DB finalisation sequence, then
// exits with code 0. Called when the user presses 'q' during interactive name
// resolution so that changes made earlier in the run are not lost.
func gracefulQuit() {
	forceCommitBibTransaction()
	saveJobWork()
	endRun(0)
}

// quitOnce guards quitNow so a second trigger (another Ctrl-C while the first is
// still committing/finalising, or a racing SIGINT and typed "q") cannot re-enter
// the commit/finalise sequence. The second (and any later) caller just blocks on
// Do — harmlessly, since the first caller's endRun(0) tears down the whole
// process, including that blocked goroutine, the moment it completes.
var quitOnce sync.Once

// quitNow is the single, unconditional exit point for "the user wants to stop
// right now" — a "q" answer to any interactive question, or SIGINT/SIGTERM. It
// never returns. Every prompt in the interaction layer (interaction.go) calls
// this directly the moment "q" is read, rather than only setting quitRequested
// and hoping every layer of every caller remembers to check it before asking
// anything else — that per-call-site propagation repeatedly proved leaky (see
// PROJECT.md's 2026-08 quit-propagation incidents).
func quitNow() {
	quitOnce.Do(func() {
		if !dbWriteSessionActive {
			// Nothing has been written this run — either a read-only command
			// (openLibraryToReport never calls prepareWorkingDatabase, so there is no
			// working database to commit or finalise) or a "q" landing before
			// openLibraryToUpdate's setup got that far. gracefulQuit()'s commit/finalise
			// chain assumes a write session actually started; calling it here touches
			// possibly-uninitialised DB/Library state (the same nil pointer panic
			// class reported 2026-07-30 from a Ctrl-C during a bare -render_as_tex).
			// There is nothing to lose by exiting immediately.
			endRun(0)
		}
		gracefulQuit()
	})
}
