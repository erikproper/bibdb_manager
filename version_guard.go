/*
 *
 * Module:    bibdb_manager
 * Component:
 * - bibtex_library
 *   - version_guard
 *
 * Which versions may use a library's database (plan step 22.4a). Every Mac works on
 * the shared library, with a program of its own: one that is older than a change to
 * the database's tables must not go on as if nothing changed. The database keeps
 * the lowest version that may use it (its config table; also in the stamp of the
 * copy in the shared DB folder). Only a migration that older versions cannot cope
 * with raises it. It is checked before anything is written.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 25.11.2026
 *
 */

package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// minVersionConfig is the config key of the lowest version that may use the database.
const minVersionConfig = "min_program_version"

// olderVersion tells whether version a comes before b ("30.9" before "30.160").
func olderVersion(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(strings.TrimSpace(as[i]))
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(strings.TrimSpace(bs[i]))
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// requiredVersion is the lowest version that may use the database conn is open on,
// "" when it says none.
func requiredVersion(conn *sql.DB) string {
	var v string
	conn.QueryRow(`SELECT value FROM config WHERE key = ?`, minVersionConfig).Scan(&v) //nolint:errcheck // no such table: a new database
	return v
}

// requiredVersionOf is requiredVersion of the database file at path, which no one
// has open ("" when it is not there). It is opened as usual, not read-only: a
// read-only connection cannot open a database in WAL mode that no one has open.
func requiredVersionOf(path string) string {
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		return ""
	}
	conn, err := sql.Open(sqliteDatabaseDriver, "file:"+path)
	if err != nil {
		return ""
	}
	defer conn.Close()
	return requiredVersion(conn)
}

// tooOldFor says why this program may not use a database that needs required ("":
// it may).
func tooOldFor(required string) string {
	if required == "" || !olderVersion(AppVersion, required) {
		return ""
	}
	return fmt.Sprintf("The library needs version %s or newer; this %s is %s. Update the program on this machine; nothing was changed.",
		required, filepath.Base(os.Args[0]), AppVersion)
}

// errTooOld is the error of a program too old for a database.
type errTooOld struct{ why string }

func (e errTooOld) Error() string { return e.why }

// reportTooOld says why the program may not use the database, also to a script
// (errors and warnings are silent without a person).
func reportTooOld(why string) {
	if isTTY {
		emit(TMessage{Kind: "error", Text: why})
	} else {
		messageln(why)
	}
}

// refuseIfTooOld ends the run when this program is too old for the database just
// opened; called before anything is written to it.
func refuseIfTooOld() {
	if db == nil {
		return
	}
	if why := tooOldFor(requiredVersion(db)); why != "" {
		db.Close()
		db = nil
		writeSessionOpenFailed = true
		reportTooOld(why)
		endRun(1)
	}
}

// requireVersion raises the lowest version that may use the open database (a
// migration that older versions cannot cope with; never lowers it).
func requireVersion(v string) {
	if olderVersion(GetConfig(minVersionConfig, ""), v) {
		SetConfig(minVersionConfig, v)
	}
}

// renamedTablesVersion is the first version with the tables renamed in step 22.4d
// (key_hints → key_aliases, its column hint → alias; key_oldies → old_keys): an older
// program would make the old tables anew, empty, and go on without them.
const renamedTablesVersion = "30.164"

// renameTables gives the tables renamed in step 22.4d their new names, before
// anything makes them anew; it tells whether it renamed one (requireRenamedTables
// then raises the database's version, once the config table is there).
func renameTables() bool {
	exists := func(name string) bool {
		var n int
		db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n) //nolint:errcheck
		return n > 0
	}
	renamed := false
	for _, r := range []struct{ old, now, oldColumn, newColumn string }{
		{"key_hints", "key_aliases", "hint", "alias"},
		{"key_oldies", "old_keys", "", ""},
	} {
		if !exists(r.old) || exists(r.now) {
			continue
		}
		steps := []string{`ALTER TABLE ` + r.old + ` RENAME TO ` + r.now}
		if r.oldColumn != "" {
			steps = append(steps, `ALTER TABLE `+r.now+` RENAME COLUMN `+r.oldColumn+` TO `+r.newColumn)
		}
		for _, step := range steps {
			if _, err := db.Exec(step); err != nil {
				messagef("Could not rename table %s to %s: %s\n", r.old, r.now, err)
				endRun(1)
			}
		}
		db.Exec(`UPDATE table_modification_times SET table_name = ? WHERE table_name = ?`, r.now, r.old) //nolint:errcheck
		messagef("  Table %s is now %s\n", r.old, r.now)
		renamed = true
	}
	return renamed
}
