/*
 *
 * Module:    bibdb_manager
 * Component:
 * - ORCID records in the reference database
 *
 * Step 23.1, slice 2: the ORCID records fetched from the ORCID API live in the
 * table orcid_records of dblp.sqlite3 (to become reference.sqlite3), next to the
 * DBLP data. Unlike the DBLP tables they are written by any run that fetches a
 * record, so they have their own read-write connection. An import, which builds a
 * new store, copies the table across (copyOrcidRecordsInto). The JSON cache
 * (ORCID/<4 digits>/<orcid>/data.json) is moved in once, on first use, and then
 * left alone.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 19.11.2026
 *
 */

package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// orcidRecordsSchema is created on first use, also in a store built before slice 2.
// other_names and works are JSON; primary_orcid is for ORCID's deprecated records
// (step 23.3).
const orcidRecordsSchema = `CREATE TABLE IF NOT EXISTS orcid_records (
	orcid            TEXT PRIMARY KEY,
	credit_name      TEXT NOT NULL DEFAULT '',
	declared_name    TEXT NOT NULL DEFAULT '',
	other_names      TEXT NOT NULL DEFAULT '[]',
	fetched_at       TEXT NOT NULL DEFAULT '',
	works            TEXT NOT NULL DEFAULT '[]',
	works_fetched_at TEXT NOT NULL DEFAULT '',
	primary_orcid    TEXT
) WITHOUT ROWID`

var orcidStore struct {
	sync.Mutex
	db *sql.DB
}

// orcidStoreDB returns the read-write connection to the ORCID records, opening it
// (and moving the JSON cache in) when needed. nil when there is no store yet.
func orcidStoreDB() *sql.DB {
	orcidStore.Lock()
	defer orcidStore.Unlock()
	if orcidStore.db != nil {
		return orcidStore.db
	}
	if !FileExists(dblpDBPath()) {
		return nil
	}
	db, err := sql.Open(sqliteDatabaseDriver, "file:"+dblpDBPath()+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return nil
	}
	db.SetMaxOpenConns(1) // the writes come one at a time; SQLite has one writer anyway
	if _, err := db.Exec(orcidRecordsSchema); err != nil {
		messagef("Warning: could not create orcid_records in %s: %s\n", dblpDBPath(), err)
		db.Close()
		return nil
	}
	orcidStore.db = db
	moveOrcidJSONCacheIn(db)
	return db
}

// closeOrcidStore closes the connection, so the next use opens the file that is now
// at dblpDBPath() (after an import or a restore swapped in a new store).
func closeOrcidStore() {
	orcidStore.Lock()
	defer orcidStore.Unlock()
	if orcidStore.db != nil {
		orcidStore.db.Close()
		orcidStore.db = nil
	}
}

// moveOrcidJSONCacheIn copies the JSON cache files into an empty orcid_records table.
func moveOrcidJSONCacheIn(db *sql.DB) {
	var n int
	if db.QueryRow(`SELECT count(*) FROM orcid_records`).Scan(&n) != nil || n > 0 {
		return
	}
	paths, _ := filepath.Glob(orcidFolder() + "*/*/data.json")
	moved := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var e orcidCacheEntry
		if json.Unmarshal(data, &e) != nil {
			continue
		}
		if writeOrcidRecord(db, filepath.Base(filepath.Dir(path)), &e) == nil {
			moved++
		}
	}
	if moved > 0 {
		// Through Progress, which first ends a progress line in the making (this runs
		// on first use, often during one).
		Library.Progress("  Moved %d ORCID record(s) from %s into %s.", moved, orcidFolder(), dblpDBPath())
	}
}

func writeOrcidRecord(db *sql.DB, orcid string, e *orcidCacheEntry) error {
	otherNames, _ := json.Marshal(nonNilStrings(e.OtherNames))
	works := []byte("[]")
	if len(e.Works) > 0 {
		works, _ = json.Marshal(e.Works)
	}
	var primary any
	if e.PrimaryORCID != "" {
		primary = e.PrimaryORCID
	}
	_, err := db.Exec(`INSERT INTO orcid_records
		(orcid, credit_name, declared_name, other_names, fetched_at, works, works_fetched_at, primary_orcid)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(orcid) DO UPDATE SET credit_name = excluded.credit_name,
		  declared_name = excluded.declared_name, other_names = excluded.other_names,
		  fetched_at = excluded.fetched_at, works = excluded.works,
		  works_fetched_at = excluded.works_fetched_at, primary_orcid = excluded.primary_orcid`,
		orcid, e.CreditName, e.DeclaredName, string(otherNames), orcidTime(e.FetchedAt),
		string(works), orcidTime(e.WorksFetchedAt), primary)
	return err
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// orcidTime stores a time as RFC 3339; the zero time ("not fetched") as "".
func orcidTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseOrcidTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// readOrcidRecord returns the stored record for orcid, or nil.
func readOrcidRecord(orcid string) *orcidCacheEntry {
	db := orcidStoreDB()
	if db == nil {
		return nil
	}
	var e orcidCacheEntry
	var otherNames, works, fetchedAt, worksFetchedAt string
	var primary sql.NullString
	err := db.QueryRow(`SELECT credit_name, declared_name, other_names, fetched_at, works, works_fetched_at, primary_orcid
		FROM orcid_records WHERE orcid = ?`, orcid).
		Scan(&e.CreditName, &e.DeclaredName, &otherNames, &fetchedAt, &works, &worksFetchedAt, &primary)
	e.PrimaryORCID = primary.String
	if err != nil {
		return nil
	}
	json.Unmarshal([]byte(otherNames), &e.OtherNames) //nolint:errcheck
	json.Unmarshal([]byte(works), &e.Works)           //nolint:errcheck
	if len(e.OtherNames) == 0 {
		e.OtherNames = nil
	}
	if len(e.Works) == 0 {
		e.Works = nil
	}
	e.FetchedAt, e.WorksFetchedAt = parseOrcidTime(fetchedAt), parseOrcidTime(worksFetchedAt)
	return &e
}

// saveOrcidRecord stores e for orcid. Without a store (no DBLP import yet) it
// falls back to the JSON cache, so nothing fetched is lost.
func saveOrcidRecord(orcid string, e *orcidCacheEntry) {
	if db := orcidStoreDB(); db != nil {
		if err := writeOrcidRecord(db, orcid, e); err != nil {
			messagef("Warning: could not store ORCID record %s: %s\n", orcid, err)
		}
		return
	}
	path := orcidCachePath(orcid)
	if path == "" {
		return
	}
	if err := os.MkdirAll(path[:strings.LastIndex(path, "/")], 0755); err != nil {
		return
	}
	if data, err := json.Marshal(e); err == nil {
		os.WriteFile(path, data, 0644) //nolint:errcheck
	}
}

// copyOrcidRecordsInto copies orcid_records from the current store into the store
// being built (newDB, which must not be in a transaction), so an import keeps them.
func copyOrcidRecordsInto(newDB *sql.DB) error {
	if !FileExists(dblpDBPath()) {
		_, err := newDB.Exec(orcidRecordsSchema)
		return err
	}
	if _, err := newDB.Exec(orcidRecordsSchema); err != nil {
		return err
	}
	if _, err := newDB.Exec(`ATTACH DATABASE ? AS previous`, dblpDBPath()); err != nil {
		return err
	}
	defer newDB.Exec(`DETACH DATABASE previous`) //nolint:errcheck
	var n int
	if err := newDB.QueryRow(`SELECT count(*) FROM previous.sqlite_master WHERE name = 'orcid_records'`).Scan(&n); err != nil || n == 0 {
		return err
	}
	_, err := newDB.Exec(`INSERT INTO main.orcid_records SELECT * FROM previous.orcid_records`)
	return err
}
