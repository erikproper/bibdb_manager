package main

// Jobs inside the service (plan step 22.2b). A command from the command table
// runs in the service process itself, on the library the service keeps loaded;
// the special commands main handles before the command table (DBLP import, push
// and restore, restoring the database, harvest from stdin, ...) still run as a
// child process (service.go), after which the library is loaded afresh.
//
// The library loaded by one job is reused by the next only when it is certainly
// current:
//   - what the job wrote is brought into the loaded library (refreshAfterWrites:
//     the entries written and the groups; other tables the library holds: load
//     afresh), and the job ended normally (not through endRun);
//   - nothing changed the databases between jobs (the working and shared
//     database files and the stamp are as they were at the end of the last job).
// Otherwise the next job loads the library afresh, as a run on its own does.
// So read jobs and jobs that change nothing get the loaded library; a job after
// a change pays the load once.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Load levels of the library kept by the service.
const (
	libraryNotLoaded    = 0
	libraryLoadedReport = 1 // by openLibraryToReport: entries, mappings, aliases
	libraryLoadedUpdate = 2 // by openLibraryToUpdate: also the title index and repairs
)

var (
	// inServiceJob is set while the service runs a job in its own process:
	// endRun then ends the job, not the process.
	inServiceJob bool

	// serviceLibraryLevel is how far the kept library is loaded (0: not, or stale).
	serviceLibraryLevel = libraryNotLoaded
	// serviceFingerprint is the state of the database files at the end of the last job.
	serviceFingerprint string
	// jobChangedLibrary is set by finaliseWorkingDatabase (and the flush and
	// abandon paths) when the job's write session changed the database.
	jobChangedLibrary bool
	// jobAbandonedDatabase is set when the working database was moved aside.
	jobAbandonedDatabase bool
	// serviceJobCommand is the name of the command the service job runs.
	serviceJobCommand string
	// deferServicePush: a write job of the service saves locally only; the
	// service pushes shortly after, in the background (TService.pushWhenDue).
	// Off for the service's own push and for -push.
	deferServicePush bool
)

// libraryLock is held by a service job while it runs, except while it waits for
// an answer (readStdinLine): the web UI renders from the loaded library only
// when it gets the lock at once (plan 22.1: one writer, reads alongside). Go
// does not allow a map to be read while it is written.
var libraryLock sync.RWMutex

// jobEnded is what endRun panics with inside a service job.
type jobEnded struct{ code int }

// reuseLoadedLibrary reports whether the open functions may skip loading,
// needing at least level. Outside the service it is always false.
func reuseLoadedLibrary(level int) bool {
	return inServiceJob && serviceLibraryLevel >= level
}

// markLibraryLoaded records that the open functions loaded the library to level.
func markLibraryLoaded(level int) {
	if inServiceJob && level > serviceLibraryLevel {
		serviceLibraryLevel = level
	}
}

// resetLibraryRunState gives a reused library the run state of a fresh one: the
// question state of this job, and the per-run fields Initialise sets.
func resetLibraryRunState() {
	l := &Library
	l.TInteraction = Reporting
	l.TBibTeXStream = TBibTeXStream{}
	l.TBibTeXStream.Initialise(Reporting, l)
	l.newKeyHints = TStringMap{}
	l.illegalFields = TStringSetNew()
	l.foundDoubles = false
	l.NoDBUpdating = false
	l.KeyIsTemporary = TStringSetNew()
	l.tempKeyMergedInto = map[string]string{}
	l.ambiguousAssignmentCount = map[string]int{}
	l.ambiguousAssignmentPick = map[string]string{}
	l.orcidAutoResolveSameCount = 0
	l.orcidAutoResolveDiffCount = 0
	l.dblpNameFormAutoAccepts = nil
	l.orcidSideAutoResolves = nil
	l.ignoreIllegalFields = false
	l.harvestNameAliases = false
	l.harvestCapturePDFFields = false
	l.harvestSourceDir = ""
	l.harvestSyncGroups = TStringSetNew()
	l.subsetLocalGroups = nil
	l.jabrefGroupingBlock = ""
	l.jabrefMetaBlocks = nil
	l.bibdeskMetaBlocks = nil
	l.capturedDBLPEntry = nil
	l.capturedHarvestEntries = nil
	l.DblpSourceData = TSourceFieldData{}
	l.DblpSourcePersons = nil
	l.DblpSourceKey = ""
}

// databaseFingerprint describes the database files: the working copy (and its
// WAL, when not empty), the shared copy and its stamp. Equal fingerprints:
// nothing wrote them.
func databaseFingerprint() string {
	var b strings.Builder
	for _, path := range []string{dbPath(), dbPath() + "-wal", dbHomePath(), libraryStampPath()} {
		// An empty WAL is no change: a read-only connection (the web UI's) creates
		// one when there is none.
		if info, err := os.Stat(path); err == nil && !(strings.HasSuffix(path, "-wal") && info.Size() == 0) {
			fmt.Fprintf(&b, "%d/%d;", info.Size(), info.ModTime().UnixNano())
		} else {
			b.WriteString("-;")
		}
	}
	return b.String()
}

// closeServiceDatabase closes the database between jobs, the WAL written into
// the database file, so the next job's fingerprint compares settled files.
func closeServiceDatabase() {
	if activeTx != nil {
		rollbackBibTransaction()
	}
	if db != nil {
		db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`) //nolint:errcheck
		db.Close()
		db = nil
	}
}

// runsInService reports whether the parsed command can run inside the service;
// the others run as a child process.
func runsInService() bool {
	if cmdUpdateDblp || cmdLoadDblpXml || cmdPushDblp || cmdRestoreDblp || cmdDeleteGarbage ||
		cmdRestoreFromDump || cmdRestoreBackupName != "" || cmdNewKey || cmdSetSyncStatus != "" ||
		cmdHarvest || cmdStartService || cmdStopService || cmdServiceStatus || cmdServe {
		return false
	}
	if f := flag.Lookup("jobs_file"); f != nil && f.Value.String() != "" {
		return false
	}
	if f := flag.Lookup("version"); f != nil && f.Value.String() == "true" {
		return false
	}
	return true
}

// currentJobIO is the running job's input/output (nil between jobs): the web
// UI shows when it waits for an answer, wherever it was started.
var currentJobIO atomic.Pointer[TSocketIO]

// jobAsksOnTerminal tells whether the running job, started from a command line,
// waits for an answer on its terminal.
func jobAsksOnTerminal() bool {
	sio := currentJobIO.Load()
	if sio == nil {
		return false
	}
	if _, web := sio.out.(teeSink); web {
		return false // a job of the web UI: it asks in the Actions tab
	}
	return sio.asked.Load()
}

// TSocketIO is the TUserIO of a job run for a command line over the socket.
type TSocketIO struct {
	out         frameSink
	interactive bool
	lines       chan string
	closeOnce   sync.Once
	finished    atomic.Bool // the job is over: its connection ending is no Ctrl-C for the next job
	asked       atomic.Bool // the command line was asked for a line, not yet answered
}

// interrupt stops the job gracefully, as Ctrl-C: a question waiting for its
// answer gets "q" (plan 22.1: a stop while a question is pending counts as "q").
func (s *TSocketIO) interrupt() {
	interrupted.Store(true)
	if s.asked.CompareAndSwap(true, false) {
		select {
		case s.lines <- "q":
		default:
		}
	}
}

// RequestInput asks the command line to read one line from its terminal.
func (s *TSocketIO) RequestInput() {
	if s.interactive && s.asked.CompareAndSwap(false, true) {
		s.out.send(serviceFrame{T: "ask"}) //nolint:errcheck
	}
}

func (s *TSocketIO) IsInteractive() bool { return s.interactive }
func (s *TSocketIO) WriteMessage(m string) {
	s.toWebLog(m)
	s.out.send(serviceFrame{T: "err", D: m}) //nolint:errcheck
}
func (s *TSocketIO) WriteResult(m string) {
	s.toWebLog(m)
	s.out.send(serviceFrame{T: "out", D: m}) //nolint:errcheck
}
func (s *TSocketIO) Input() <-chan string { return s.lines }
func (s *TSocketIO) endInput()            { s.closeOnce.Do(func() { close(s.lines) }) }

// toWebLog adds a job's output to the log page; the service's own jobs write to
// the service log, which reaches it already.
func (s *TSocketIO) toWebLog(m string) {
	if _, own := s.out.(logSink); own {
		return
	}
	if t, tee := s.out.(teeSink); tee {
		if _, own := t.a.(logSink); own {
			return
		}
	}
	webLog.write(m)
}

// frameSink takes the frames of a job's output: a command line's socket, or
// the service log (a job the service runs itself, e.g. the push when it stops).
type frameSink interface{ send(serviceFrame) error }

// logSink writes a job's output to the service log.
type logSink struct{}

func (logSink) send(f serviceFrame) error {
	if f.T == "out" || f.T == "err" || f.T == "info" {
		os.Stderr.WriteString(f.D) //nolint:errcheck
	}
	return nil
}

// runOwnJob runs a command the service gives itself (no command line, no
// person), with its output to sink. The caller holds the job slot.
func (s *TService) runOwnJob(args []string, sink frameSink) int {
	req := serviceFrame{T: "run", Args: append([]string{"-base", libraryBase()}, args...), Version: AppVersion, Base: libraryBase()}
	sio := &TSocketIO{out: sink, lines: make(chan string)}
	sio.endInput()
	flag.CommandLine.SetOutput(flagOutput{sio})
	positional, ok := parseJobArguments(req.Args)
	if !ok {
		return 2
	}
	req.Cwd, _ = os.Getwd()
	deferServicePush = false // the service's own jobs (the push) push themselves
	code := s.runJobInService(req, positional, sio)
	sio.finished.Store(true)
	return code
}

// flagOutput sends the flag package's messages (a bad flag, -help) to the job.
type flagOutput struct{ io *TSocketIO }

func (f flagOutput) Write(p []byte) (int, error) {
	f.io.WriteMessage(string(p))
	return len(p), nil
}

// parseJobArguments parses a job's arguments as main parses the command line.
// It returns the positional arguments, and false when they do not parse.
func parseJobArguments(args []string) ([]string, bool) {
	resetCommandFlags()
	cmdMap, cmdUseAliases, logFilePath = false, false, ""
	if err := flag.CommandLine.Parse(args); err != nil {
		return nil, false
	}
	return rescanModifierArgs(flag.Args()), true
}

// runJobInService runs the parsed command in this process and returns its exit
// code. The caller holds the service's job slot.
func (s *TService) runJobInService(req serviceFrame, positional []string, sio *TSocketIO) (code int) {
	currentJobIO.Store(sio)
	defer currentJobIO.Store(nil)
	savedIO, savedTTY := userIO, isTTY
	userIO, isTTY = sio, sio.interactive
	if logFilePath != "" {
		logArgs = req.Args
		if l, err := newLoggingIO(sio, logFilePath); err == nil {
			userIO = l
		} else {
			messagef("Cannot open log file %s: %s\n", logFilePath, err)
		}
	}
	interrupted.Store(false)
	jobChangedLibrary, jobAbandonedDatabase = false, false
	inServiceJob = true

	libraryLock.Lock()
	defer libraryLock.Unlock() // deferred first: released after the clean-up below
	ended := false
	var command *TCommand
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(jobEnded); ok {
				code = e.code
			} else {
				messagef("\nInternal error in the service: %v\n", r)
				fmt.Fprintf(os.Stderr, "internal error in job %s: %v\n", strings.Join(req.Args, " "), r)
				code = 1
			}
			ended = true
		}
		// A job that ended through endRun (an error, a quit, a usage message) may
		// have left the loaded library and the database apart: load afresh. So
		// when the working database was moved aside. Otherwise the library is
		// brought up to date with what the job wrote (db_write_hook.go).
		// A job that ended through endRun with its write session open (a refusal, a
		// quit) closes the session as a job that ends normally would: its writes are
		// kept and pushed shortly. Left open, the next job took it for a crash and
		// asked whether to keep "the changes of that run" (Erik, 2026-11-24).
		if ended && !jobAbandonedDatabase && dbWriteSessionActive && db != nil {
			if activeTx != nil {
				rollbackBibTransaction()
			}
			closeWorkingDatabaseLocally()
		}
		writes := takeDbWrites()
		switch {
		case serviceLibraryLevel == libraryNotLoaded:
		case ended:
			fmt.Fprintf(os.Stderr, "  job ended through endRun: loading afresh next time\n")
			serviceLibraryLevel = libraryNotLoaded
		case jobAbandonedDatabase:
			fmt.Fprintf(os.Stderr, "  working database moved aside: loading afresh next time\n")
			serviceLibraryLevel = libraryNotLoaded
		case cmdLoadLibrary:
			// The load itself (at service start): what it wrote while loading (e.g. maps
			// stored in map_files) is in what it loaded. Reloading for it left the web
			// UI without the library (its references) until the next job.
		default:
			if ok, why := refreshAfterWrites(writes); !ok {
				fmt.Fprintf(os.Stderr, "  %s: loading afresh next time\n", why)
				serviceLibraryLevel = libraryNotLoaded
			} else if len(writes.entries) > 0 {
				fmt.Fprintf(os.Stderr, "  library kept, %d entr%s refreshed\n", len(writes.entries),
					map[bool]string{true: "y", false: "ies"}[len(writes.entries) == 1])
			}
		}
		closeServiceDatabase()
		serviceFingerprint = databaseFingerprint()
		if instanceLockFile != nil {
			instanceLockFile.Close()
			instanceLockFile = nil
		}
		if l, ok := userIO.(*TLoggingIO); ok {
			l.close()
		}
		inServiceJob = false
		userIO, isTTY = savedIO, savedTTY
	}()

	// Nothing may have changed the databases since the last job.
	if fp := databaseFingerprint(); fp != serviceFingerprint {
		if serviceLibraryLevel != libraryNotLoaded {
			fmt.Fprintf(os.Stderr, "  the database changed since the last job (%s → %s)\n", serviceFingerprint, fp)
		}
		serviceLibraryLevel = libraryNotLoaded
	}
	if serviceLibraryLevel == libraryNotLoaded {
		fmt.Fprintf(os.Stderr, "  loading the library\n")
	} else {
		fmt.Fprintf(os.Stderr, "  library kept loaded\n")
	}
	serviceFingerprint = databaseFingerprint()
	if serviceLibraryLevel == libraryNotLoaded {
		loadMaps() // the maps live in the database too (-import can change them)
	}

	dbWriteHookActive = true
	resetDbWrites()
	acquireInstanceLock()
	maybeRestoreDblpStore()
	if db == nil {
		connectToDatabase()
	}
	command = selectCommand()
	serviceJobCommand = command.name
	runJob(command, positional)
	return 0
}

// relayInput feeds a job's input from the command line: typed lines, end of
// input, Ctrl-C. When the command line goes away, the job is stopped as with
// Ctrl-C. It returns when the connection ends.
func relayInput(dec interface{ Decode(any) error }, sio *TSocketIO) {
	for {
		var f serviceFrame
		if err := dec.Decode(&f); err != nil {
			if !sio.finished.Load() {
				interrupted.Store(true)
			}
			sio.endInput()
			return
		}
		switch f.T {
		case "in":
			sio.asked.Store(false)
			if sio.interactive {
				for _, line := range strings.SplitAfter(f.D, "\n") {
					if line != "" {
						sio.lines <- strings.TrimSuffix(line, "\n")
					}
				}
			}
		case "eof":
			sio.endInput()
		case "int":
			if !sio.finished.Load() && !interrupted.Load() {
				if sio.interactive {
					sio.WriteMessage("\nInterrupted — finishing the current step, then committing and saving...\n")
				}
				sio.interrupt()
			}
		}
	}
}

// discard is used for the flag package's output outside a job.
var _ io.Writer = flagOutput{}

// servicePushDelay is how long after the last write job the service pushes.
const servicePushDelay = time.Minute

// schedulePush (re)starts the wait before the background push.
func (s *TService) schedulePush() {
	s.mu.Lock()
	s.pushDue = time.Now().Add(servicePushDelay)
	s.mu.Unlock()
}

// pushWhenDue runs the push once it is due and no job runs: the post-check
// gate, the backup, the copy and the dump to the shared DB folder (the -push
// command, run by the service itself). Commands arriving meanwhile wait.
func (s *TService) pushWhenDue() {
	for range time.Tick(5 * time.Second) {
		s.reloadWhenIdle()
		s.mu.Lock()
		due := !s.pushDue.IsZero() && time.Now().After(s.pushDue) && !s.busy && !s.stopping.Load()
		if due {
			s.busy, s.ownJob, s.jobStop, s.jobArgs, s.jobFrom = true, true, nil, []string{"(push to the shared DB folder)"}, time.Now()
			s.ownWhat = "pushing to " + sharedDBFolder
		}
		s.mu.Unlock()
		if !due {
			continue
		}
		fmt.Fprintf(os.Stderr, "%s  pushing to %s\n", time.Now().Format(time.DateTime), sharedDBFolder)
		s.runOwnJob([]string{"-push"}, logSink{})
		s.mu.Lock()
		s.busy, s.ownJob, s.lastActive = false, false, time.Now()
		if readLibraryUnpushed() == nil {
			s.pushDue = time.Time{}
			fmt.Fprintf(os.Stderr, "%s  pushed\n", time.Now().Format(time.DateTime))
		} else {
			s.pushDue = time.Now().Add(5 * time.Minute) // Nextcloud not reachable, or the gate failed: try again
			fmt.Fprintf(os.Stderr, "%s  not pushed; trying again in 5 minutes\n", time.Now().Format(time.DateTime))
		}
		s.mu.Unlock()
	}
}

// reloadWhenIdle loads the library again in the background once no job runs,
// after a job left it unloaded (one that added an entry, changed a dblp key or
// preferred alias, ended with an error, ...): so the web UI has its references
// again, and the next command does not wait for the load (Erik, 2026-11-24).
func (s *TService) reloadWhenIdle() {
	s.mu.Lock()
	idle := !s.busy && !s.stopping.Load() && time.Since(s.lastActive) > 2*time.Second
	s.mu.Unlock()
	if idle && serviceLibraryLevel == libraryNotLoaded && time.Now().After(reloadNotBefore) {
		s.loadAtStart()
		if serviceLibraryLevel == libraryNotLoaded { // the load failed: not again for a while
			reloadNotBefore = time.Now().Add(10 * time.Minute)
		}
	}
}

// libraryNeedsAnswer: the service's own load stopped at a question (the busy
// light shows it amber, with what to do).
var libraryNeedsAnswer atomic.Bool

// reloadNotBefore holds back reloadWhenIdle after a load that failed.
var reloadNotBefore time.Time

// loadAtStart loads the library right after the service starts (Erik,
// 2026-11-24), so the first command finds it loaded; commands arriving
// meanwhile wait. The statistics go to the service log only.
func (s *TService) loadAtStart() {
	fmt.Fprintf(os.Stderr, "%s  loading the library\n", time.Now().Format(time.DateTime))
	// As the Actions tab's job: a question (e.g. whether to keep the changes of a
	// run that did not finish) is answered there.
	if s.runDialogueJob("Loading the library", [][]string{{"-load_library"}}, true, nil) != nil {
		return // busy or stopping
	}
	if serviceLibraryLevel == libraryNotLoaded {
		libraryNeedsAnswer.Store(true)
		fmt.Fprintf(os.Stderr, "%s  library not loaded (see the Actions tab); run bib.check, or Check in the web UI\n", time.Now().Format(time.DateTime))
		return
	}
	libraryNeedsAnswer.Store(false)
	fmt.Fprintf(os.Stderr, "%s  library loaded\n", time.Now().Format(time.DateTime))
}
