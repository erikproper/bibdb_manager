/*
 *
 * Module:    bibdb_manager
 * Component:
 * - jobs
 *
 * A job is one run of one command (plan step 22.1). beginJob resets the state of
 * a run, which so far a fresh process provided; endJob does what the end of main
 * did: the end-of-run checks, the post-check gate, saving the work and the push.
 * Today a process runs one job; the service (step 22.2) runs many.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 23.11.2026
 *
 */

package main

import (
	"flag"
	"os"
	"strings"
	"sync"
)

// runJob runs one command as a job.
func runJob(cmd *TCommand, args []string) {
	beginJob()
	cmd.run(args)
	endJob()
}

// beginJob resets the state of one run to what a fresh process has. Command
// options (the flags, forceWrite) and what main set up before the job (folders,
// maps, the instance lock) are not run state and stay.
func beginJob() {
	// Change tracking and the write session (bibtex_library_db*.go).
	bibEntriesModified = false
	c2TrackingActive = false
	c2EntryModified = false
	c2ChildrenChecked = 0
	entryModTrackingActive = false
	entryModified = false
	entrySnapshots = nil
	dbWriteSessionActive = false
	dbWriteFailed = false
	dbWriteFailureReason = ""
	changesAtSessionOpen = 0
	homeDbSizeAtOpen = 0
	preCloseHook = nil
	forceNameMapping = false
	rebuildDone = false
	safeParseTemp = ""
	safeParseOriginalCount = 0
	libraryPushNeeded = false
	writeSessionOpenFailed = false
	libraryBackupCreated = false

	// Flags set while a library is opened or a command runs.
	skipBibDbRefresh = false
	skipBibValidation = false
	skipStartupChecks = false
	inDblpUpdate = false
	subsetMergeActive = false

	// Session statistics (main.go).
	sessionStartEntryCount = 0
	sessionStartDblpKeyCount = 0
	sessionStartFieldMapCount = 0
	sessionStartUnresolvedGroups = 0
	sessionStartDblpCandidates = 0
	sessionManualDblpAssignments = 0

	// Per-run sets and one-shots.
	pdfDownloadAttempted = TStringSetNew()
	displayCommandLaunched = false
	activeTicker = nil
	quitOnce = sync.Once{}

	// Questions and messages.
	Reporting.quitRequested = false
	Reporting.questionsAnswered = 0
	Reporting.deferMessages = false
	Reporting.deferredWarnings = nil
	Reporting.deferredErrors = nil
	Reporting.progressSuppressed = false // a read-only job sets it
	Reporting.silenced = false
	Reporting.progressPrefix = ""

	// The DBLP store warns once per job, not once per process, when it is missing.
	dblpReader.Lock()
	dblpReader.missing = false
	dblpReader.Unlock()
}

// endJob ends a job as the end of main did: the end-of-run checks, then the work
// is saved (saveJobWork).
func endJob() {
	if writeSessionOpenFailed {
		stderrPrintf("\n")
		endRun(1)
	}

	Library.CheckDblpKeyMissingWarnings()

	// Drain any author/editor superseded-values backlog this run produced, rather than
	// leaving it as a separate manual step every command has to remember to trigger.
	// Still interactive where a real judgment call is needed — doTriageAuthorMappings
	// only prompts for cases it can't auto-resolve, and is a silent no-op when nothing
	// is pending. Runs once here for every command, instead of being wired into each
	// write path individually: only meaningful once a write session actually started
	// (dbWriteSessionActive), and skipped if the user quit early — they asked to stop,
	// so leave the backlog for the next run rather than pushing more questions at them.
	if dbWriteSessionActive && !Library.QuitWasRequested() {
		doTriageAuthorMappings()
	}

	// DB-primary (step 13.6 + 22.7): the bib file is never written during a normal run.
	// Bib file writes happen only via -sync (full mode). All entry changes are already
	// persisted to the working DB by setEntryField; finaliseWorkingDatabase flushes them home.
	if (forceWrite || bibEntriesModified) && !skipBibDbRefresh {
		clearTableDirty("bib_entries")
		refreshBibDbTimestamp()
	}

	saveJobWork()
	stderrPrintf("\n")
}

// saveJobWork saves the work of a job: the key non-doubles, then, when the
// post-check gate passes, the working database (and the push); otherwise the
// working database is moved aside. Shared by endJob and gracefulQuit.
func saveJobWork() {
	// A read job in the service: nothing to save, and the post-check gate (seconds
	// on a large library) guards a write session only (finaliseWorkingDatabase
	// does nothing without one).
	if inServiceJob && !dbWriteSessionActive {
		return
	}
	saveKeyNonDoublesToDb(&Library)
	// A write job of the service saves locally; the post-check gate and the push
	// follow shortly after, in the background (Erik, 2026-11-23).
	if inServiceJob && deferServicePush {
		closeWorkingDatabaseLocally()
		return
	}

	if !postCheckGate() {
		dbInteraction.Warning("Post-check gate failed — home database not updated")
		abandonWorkingDatabase()
	} else {
		finaliseWorkingDatabase()
	}
}

// runJobsFromFile runs the commands listed in a file in one process, one job per
// line (flags as on the command line; # starts a comment). A test aid for step
// 22.1 (several jobs on one library, as the service will run them), behind the
// hidden flag -jobs_file. Only commands from the command table are meant.
func runJobsFromFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		messageln(err)
		endRun(1)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		resetCommandFlags()
		if err := flag.CommandLine.Parse(strings.Fields(line)); err != nil {
			messageln(err)
			endRun(1)
		}
		if db == nil {
			connectToDatabase() // the previous job's end closed it
		}
		stderrPrintf("=== job: %s\n", line)
		runJob(selectCommand(), flag.Args())
	}
}

// resetCommandFlags sets every flag but -base and -jobs_file back to its default.
func resetCommandFlags() {
	flag.VisitAll(func(f *flag.Flag) {
		if f.Name == "base" || f.Name == "jobs_file" {
			return
		}
		f.Value.Set(f.DefValue) //nolint:errcheck
	})
}
