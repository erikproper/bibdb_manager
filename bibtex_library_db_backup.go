/*
 *
 * Module:    bibtex_check
 * Component:
 * - bibtex_library
 *   - bibtex_library_db
 *     - bibtex_library_db_backup
 *
 * Nextcloud copy of the library database (plan step 21.2). The database is used
 * locally (dbPath(), in cache_folder); the home folder (home_folder in the settings,
 * by default the library's folder) only holds a copy: $base.sqlite3, $base.dump and
 * the stamp $base.stamp.
 *
 * Every push gets a generation number, one higher than any generation seen so far.
 * It is written twice: into the database itself (table_modification_times, row
 * push_generation) and into the stamp, together with machine, time and size. A
 * restore checks that the copy it got carries the stamp's generation, so a copy that
 * Nextcloud has not fully synced yet is never used. (The size alone cannot tell:
 * SQLite files often keep their size when their contents change.)
 *
 * Local changes not yet pushed are recognised by the write-session markers (a run
 * that did not finish) or by the file $base.unpushed next to the local database,
 * written before a push and removed after it. Both carry the generation the local
 * changes started from; a stamp with a higher generation means another machine has
 * pushed in the meantime, a conflict that the user resolves.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 18.11.2026
 *
 */

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	libraryStampExtension    = ".stamp"
	libraryUnpushedExtension = ".unpushed"
	libraryDumpExtension     = ".dump"
	libraryRestoreSuffix     = ".restore"

	// pushGenerationMarker is the table_modification_times row that holds the
	// generation of the last push or restore of this database.
	pushGenerationMarker = "push_generation"
)

// TLibraryStamp describes the copy in the home folder.
type TLibraryStamp struct {
	Generation int64  `json:"generation"`
	PushedAt   string `json:"pushed_at"` // RFC 3339
	Machine    string `json:"machine"`
	Size       int64  `json:"size"` // of $base.sqlite3
}

// TLibraryUnpushed marks local changes that are not (yet) in the home folder.
type TLibraryUnpushed struct {
	BaseGeneration int64 `json:"base_generation"` // generation the changes started from
}

// libraryPushNeeded forces a push at the end of the run, also when the run itself
// wrote nothing: set when local changes from an earlier run are still unpushed,
// and when there is no stamp yet.
var libraryPushNeeded bool

// writeSessionOpenFailed is set when prepareWorkingDatabase refused to start a
// write session. The end of main then stops: its steps assume a loaded library,
// and saveKeyNonDoublesToDb would drop every non-double pair of an empty one.
var writeSessionOpenFailed bool

func libraryStampPath() string { return homeFolder + bibTeXBaseName + libraryStampExtension }
func libraryDumpPath() string  { return homeFolder + bibTeXBaseName + libraryDumpExtension }

func libraryUnpushedPath() string {
	return strings.TrimSuffix(dbPath(), cacheFileExtension) + libraryUnpushedExtension
}

// writeJSONViaPart writes v as JSON to path through path+".part" and a rename.
func writeJSONViaPart(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	part := path + dblpBackupPartSuffix
	if err := os.WriteFile(part, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(part, path)
}

// readLibraryStamp returns the stamp of the home copy, or nil when there is none.
func readLibraryStamp() *TLibraryStamp {
	data, err := os.ReadFile(libraryStampPath())
	if err != nil {
		return nil
	}
	var stamp TLibraryStamp
	if json.Unmarshal(data, &stamp) != nil || stamp.Generation <= 0 {
		return nil
	}
	return &stamp
}

func readLibraryUnpushed() *TLibraryUnpushed {
	data, err := os.ReadFile(libraryUnpushedPath())
	if err != nil {
		return nil
	}
	var u TLibraryUnpushed
	if json.Unmarshal(data, &u) != nil {
		return nil
	}
	return &u
}

// markLibraryUnpushed records that local changes starting from generation base
// are not pushed yet. An existing mark is kept: it has the older base.
func markLibraryUnpushed(base int64) {
	if readLibraryUnpushed() != nil {
		return
	}
	if err := writeJSONViaPart(libraryUnpushedPath(), TLibraryUnpushed{BaseGeneration: base}); err != nil {
		dbInteraction.Warning("Could not write %s: %s", libraryUnpushedPath(), err)
	}
}

func clearLibraryUnpushed() {
	os.Remove(libraryUnpushedPath()) //nolint:errcheck
}

// nextPushGeneration returns a generation above both local and the stamp's.
func nextPushGeneration(local int64) int64 {
	gen := local
	if stamp := readLibraryStamp(); stamp != nil && stamp.Generation > gen {
		gen = stamp.Generation
	}
	return gen + 1
}

// openClosedDbFile opens a database file that no other connection uses. With
// immutable, SQLite neither locks it nor looks for a WAL file: right for a fresh
// copy, which has none.
func openClosedDbFile(path string, immutable bool) (*sql.DB, error) {
	dsn := sqliteDSN(path)
	if immutable {
		dsn = "file:" + path + "?immutable=1"
	}
	return sql.Open(sqliteDatabaseDriver, dsn)
}

// dbFilePushGeneration returns the push generation stored in a closed database
// file (0 when it has none).
func dbFilePushGeneration(path string) (int64, error) {
	conn, err := openClosedDbFile(path, true)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	var gen int64
	err = conn.QueryRow(`SELECT modification_time FROM table_modification_times WHERE table_name = ?`,
		pushGenerationMarker).Scan(&gen)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return gen, err
}

// setDbFilePushGeneration stores gen in a closed database file.
func setDbFilePushGeneration(path string, gen int64) error {
	conn, err := openClosedDbFile(path, false)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Exec(`
		INSERT INTO table_modification_times (table_name, modification_time)
		  VALUES (?, ?)
		  ON CONFLICT(table_name) DO UPDATE SET modification_time = excluded.modification_time`,
		pushGenerationMarker, gen)
	return err
}

// checkDbFile runs PRAGMA quick_check on a closed database file.
func checkDbFile(path string) error {
	conn, err := openClosedDbFile(path, true)
	if err != nil {
		return err
	}
	defer conn.Close()
	var check string
	if err := conn.QueryRow(`PRAGMA quick_check`).Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return fmt.Errorf("quick_check: %s", check)
	}
	return nil
}

// pushLibraryDatabase copies the local database (closed, or checkpointed with no
// write in progress) to the home folder, with a dump when withDump, and writes the
// stamp last. The database must already carry generation gen.
func pushLibraryDatabase(gen int64, withDump bool) error {
	if !dbIsolationActive() {
		return nil
	}
	if err := os.MkdirAll(homeFolder, 0o755); err != nil {
		return err
	}
	working := dbPath()
	home := dbHomePath()
	// The copy takes the main file only: writes still in the WAL would be lost.
	if info, err := os.Stat(working + "-wal"); err == nil && info.Size() > 0 {
		return fmt.Errorf("%s still has %d bytes in its WAL file", working, info.Size())
	}
	if err := copyFileAtomic(working, home); err != nil {
		return err
	}
	info, err := os.Stat(home)
	if err != nil {
		return err
	}
	if withDump {
		writeDatabaseDump(working)
	}
	machine, _ := os.Hostname()
	if err := writeJSONViaPart(libraryStampPath(), TLibraryStamp{
		Generation: gen,
		PushedAt:   time.Now().Format(time.RFC3339),
		Machine:    machine,
		Size:       info.Size(),
	}); err != nil {
		return err
	}
	clearLibraryUnpushed()
	return nil
}

// restoreLibraryDatabase replaces the local database with the home copy described
// by stamp. The copy is checked first (size, quick_check, generation), so a copy
// that is damaged or not fully synced leaves the local database untouched. The
// caller closes db before, and reopens it after.
func restoreLibraryDatabase(stamp *TLibraryStamp) error {
	home := dbHomePath()
	working := dbPath()
	tmp := working + libraryRestoreSuffix

	info, err := os.Stat(home)
	if err != nil {
		return err
	}
	if info.Size() != stamp.Size {
		return fmt.Errorf("the copy in %s is not fully synced yet (%d of %d bytes)", homeFolder, info.Size(), stamp.Size)
	}
	removeDblpDBFiles(tmp)
	if err := copyFileAtomic(home, tmp); err != nil {
		removeDblpDBFiles(tmp)
		return err
	}
	if err := checkDbFile(tmp); err != nil {
		removeDblpDBFiles(tmp)
		return fmt.Errorf("the copy in %s is damaged: %w", homeFolder, err)
	}
	gen, err := dbFilePushGeneration(tmp)
	if err != nil {
		removeDblpDBFiles(tmp)
		return err
	}
	if gen != stamp.Generation {
		removeDblpDBFiles(tmp)
		return fmt.Errorf("the copy in %s has generation %d, its stamp says %d (Nextcloud still syncing?)",
			homeFolder, gen, stamp.Generation)
	}
	os.Remove(working + "-wal") //nolint:errcheck
	os.Remove(working + "-shm") //nolint:errcheck
	if err := os.Rename(tmp, working); err != nil {
		removeDblpDBFiles(tmp)
		return err
	}
	clearLibraryUnpushed()
	return nil
}

// saveAsideToBackup copies path to the backup folder under a name that says what
// it was, so the side that loses a conflict can still be inspected.
func saveAsideToBackup(path, label string) {
	dst := backupFolder + bibTeXBaseName + "_" + label + "_" + time.Now().Format("20060102_150405") + cacheFileExtension
	if err := os.MkdirAll(backupFolder, 0o755); err == nil {
		err = copyFile(path, dst)
		if err == nil {
			dbInteraction.Progress("  Saved %s as %s", path, dst)
			return
		}
	}
	dbInteraction.Warning("Could not save %s to the backup folder", path)
}

// localEntryCount returns the number of entries in the open database (0 if none).
func localEntryCount() int {
	if db == nil {
		return 0
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM bib_entries WHERE field = ?`, EntryTypeField).Scan(&n) //nolint:errcheck
	return n
}

// alignWithLibraryStamp is phase 1 of a write session when the home folder holds a
// stamped copy: it decides between the local database and the home copy. Returns
// false when the run cannot go on.
func alignWithLibraryStamp(stamp *TLibraryStamp, hInfo os.FileInfo) bool {
	working := dbPath()
	hasLocal := localEntryCount() > 0
	var localGen int64
	if hasLocal {
		localGen = tableModTime(pushGenerationMarker)
	}
	crashed := hasLocal && writeSessionIsCrashed()
	unpushed := readLibraryUnpushed()
	base := localGen
	if unpushed != nil {
		base = unpushed.BaseGeneration
	}
	pending := hasLocal && (crashed || unpushed != nil)
	other := fmt.Sprintf("the copy pushed by %s at %s (generation %d)", stamp.Machine, stamp.PushedAt, stamp.Generation)

	restore := false
	switch {
	case !hasLocal:
		dbInteraction.Progress("  No local database yet; using %s", other)
		restore = true

	case pending && stamp.Generation > base:
		if !dbInteraction.WarningYesNoQuestion("Keep this machine's copy (no = use the other one)",
			"This machine has changes that are not in Nextcloud, but another machine has pushed since: %s. The copy not kept is saved in the backup folder.",
			other) {
			saveAsideToBackup(working, "local")
			restore = true
		} else {
			saveAsideToBackup(dbHomePath(), "pushed")
			libraryPushNeeded = true
		}

	case pending && crashed:
		if err := checkOpenDatabase(); err != nil {
			if !dbInteraction.WarningYesNoQuestion("Restore the database from the Nextcloud copy",
				"The local database, left by a run that did not finish, fails the integrity check (%s).", err) {
				return false
			}
			saveAsideToBackup(working, "damaged")
			restore = true
		} else if dbInteraction.WarningYesNoQuestion("Keep the changes of that run", WarningWorkingDbNewer) {
			libraryPushNeeded = true
		} else {
			restore = true
		}

	case pending:
		dbInteraction.Progress("  Local changes are not in Nextcloud yet; they are pushed at the end of this run.")
		libraryPushNeeded = true

	case stamp.Generation > localGen:
		dbInteraction.Progress("  Nextcloud holds a newer copy; using %s", other)
		restore = true

	case stamp.Generation < localGen:
		dbInteraction.Warning("The local database (generation %d) is newer than %s; it is pushed at the end of this run.",
			localGen, other)
		libraryPushNeeded = true

	default:
		dbInteraction.Progress(ProgressReusingWorkingDatabase)
	}

	if restore {
		// The open connection must not survive the file being replaced under it
		// (see prepareWorkingDatabase).
		if db != nil {
			db.Close()
			db = nil
		}
		if err := restoreLibraryDatabase(stamp); err != nil {
			dbInteraction.Warning("Could not use the Nextcloud copy: %s. The local database is left as it was; try again once Nextcloud has finished syncing.", err)
			// The rest of main still runs (without a write session) and needs a
			// connection.
			reopenDb(working)
			return false
		}
		reopenDb(working)
		if keyPrefix == "" {
			loadBibTeXSettings()
		}
	}

	homeDbSizeAtOpen = hInfo.Size()
	markWriteSessionOpen()
	db.QueryRow(`SELECT total_changes()`).Scan(&changesAtSessionOpen) //nolint:errcheck
	dbWriteSessionActive = true
	return true
}

// checkOpenDatabase runs PRAGMA quick_check on the open database.
func checkOpenDatabase() error {
	var check string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return fmt.Errorf("quick_check: %s", check)
	}
	return nil
}

// warnIfLibraryStampNewer is for read-only runs, which use the local database as it
// is: say so when another machine has pushed a newer copy.
func warnIfLibraryStampNewer() {
	if !dbIsolationActive() || db == nil {
		return
	}
	stamp := readLibraryStamp()
	if stamp == nil || stamp.Generation <= tableModTime(pushGenerationMarker) {
		return
	}
	Reporting.Warning("%s pushed a newer copy of the library at %s; this report uses the local database. A run that updates the library fetches it.",
		stamp.Machine, stamp.PushedAt)
}

// installRestoredDatabase gives the database just restored into dbPath() (from a
// dump or a backup) a new generation and pushes it.
func installRestoredDatabase() error {
	if !dbIsolationActive() {
		return nil
	}
	working := dbPath()
	local, err := dbFilePushGeneration(working)
	if err != nil {
		return err
	}
	gen := nextPushGeneration(local)
	if err := setDbFilePushGeneration(working, gen); err != nil {
		return err
	}
	clearLibraryUnpushed()
	return pushLibraryDatabase(gen, true)
}
