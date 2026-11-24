package main

// What a job wrote (plan step 22.2b, follow-up 2026-11-23). SQLite's pre-update
// hook reports every row an INSERT, UPDATE or DELETE touches, with its table, on
// every connection of the library database. After a write job the service
// refreshes only what the job wrote (refreshAfterWrites), instead of loading the
// whole library afresh; that also covers writes done in SQL directly, which the
// loaded library does not follow by itself.

import (
	"database/sql"
	"strings"
	"sync"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// TDbWrites is what was written: rows per table, and the entries (with the
// fields, or for contributor_roles the roles) of bib_entries and contributor_roles.
type TDbWrites struct {
	tables  map[string]int
	entries map[string]map[string]bool
	kept    map[string]bool // tables the job kept up to date in memory itself (see writesKeptInMemory)
}

func newDbWrites() TDbWrites {
	return TDbWrites{tables: map[string]int{}, entries: map[string]map[string]bool{}, kept: map[string]bool{}}
}

// writesKeptInMemory tells that the running job keeps the loaded library up to
// date with its writes to tables itself, and which entries to refresh (e.g.
// -rename_contributor: the contributor in memory, and the author fields of
// their entries, which name them).
func writesKeptInMemory(tables []string, entries []string) {
	dbWritesMu.Lock()
	defer dbWritesMu.Unlock()
	for _, t := range tables {
		dbWrites.kept[t] = true
	}
	for _, key := range entries {
		if dbWrites.entries[key] == nil {
			dbWrites.entries[key] = map[string]bool{}
		}
		dbWrites.entries[key]["author"] = true
	}
}

var (
	dbWritesMu sync.Mutex
	dbWrites   = newDbWrites()
	// dbWriteHookActive: record (set by the service; a run on its own does not).
	dbWriteHookActive bool
)

func init() {
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, dsn string) error {
		// Only the library database, not the DBLP store, the ORCID cache or a .sync file.
		if !strings.Contains(dsn, bibTeXBaseName+cacheFileExtension) {
			return nil
		}
		if hooker, ok := conn.(sqlite.HookRegisterer); ok {
			hooker.RegisterPreUpdateHook(recordDbWrite)
		}
		return nil
	})
}

// recordDbWrite is the pre-update hook.
func recordDbWrite(data sqlite.SQLitePreUpdateData) {
	if !dbWriteHookActive {
		return
	}
	var key, field string
	if data.TableName == "bib_entries" || data.TableName == "contributor_roles" {
		values := make([]any, data.Count())
		var err error
		if data.Op == sqlite3.SQLITE_DELETE {
			err = data.Old(values...)
		} else {
			err = data.New(values...)
		}
		if err == nil && len(values) >= 2 {
			key, _ = values[0].(string)
			field, _ = values[1].(string)
		}
	}
	dbWritesMu.Lock()
	defer dbWritesMu.Unlock()
	dbWrites.tables[data.TableName]++
	if key != "" {
		if dbWrites.entries[key] == nil {
			dbWrites.entries[key] = map[string]bool{}
		}
		dbWrites.entries[key][field] = true
	}
}

// resetDbWrites starts recording afresh; takeDbWrites returns what was recorded.
func resetDbWrites() {
	dbWritesMu.Lock()
	dbWrites = newDbWrites()
	dbWritesMu.Unlock()
}

func takeDbWrites() TDbWrites {
	dbWritesMu.Lock()
	defer dbWritesMu.Unlock()
	w := dbWrites
	dbWrites = newDbWrites()
	return w
}

// Tables the loaded library does not hold (read from the database when needed),
// so writing them leaves it current.
var tablesNotInMemory = map[string]bool{
	"config":                   true, // read into settings at connect; writes are the version row
	"table_modification_times": true,
	"entry_warnings":           true,
}

// Fields whose change needs more than refreshing the entry: they feed the key
// aliases (key hints, DBLP key oldies), which refreshing would have to write.
var fieldsNeedingReload = map[string]bool{PreferredAliasField: true, DBLPField: true}

// refreshAfterWrites brings the loaded library up to date with what a job wrote:
// the entries written (entry cache, title index) and the groups. It returns
// false, with the reason, when the writes go beyond that: the library is then
// loaded afresh by the next job.
func refreshAfterWrites(w TDbWrites) (bool, string) {
	groups := false
	for table := range w.tables {
		switch {
		case tablesNotInMemory[table], w.kept[table]:
		case table == "bib_entries" || table == "contributor_roles":
		case table == "bib_groups":
			groups = true
		default:
			return false, "wrote " + table
		}
	}
	for key, fields := range w.entries {
		for field := range fields {
			if fieldsNeedingReload[field] {
				return false, "changed " + field + " of " + key
			}
		}
	}
	if entryCache == nil {
		return false, "no entry cache"
	}

	// Read with a connection of its own: the job's is closed by now.
	ro, err := sql.Open(sqliteDatabaseDriver, "file:"+dbPath()+"?mode=ro")
	if err != nil {
		return false, err.Error()
	}
	defer ro.Close()
	savedDb, savedTx := db, activeTx
	db, activeTx = ro, nil
	defer func() { db, activeTx = savedDb, savedTx }()

	for key := range w.entries {
		oldTitle := ""
		if old := entryCache[key]; old != nil {
			oldTitle = old.Fields[TitleField]
		}
		fresh := loadEntryFromDbDirect(key)
		if len(fresh.Fields) == 0 {
			delete(entryCache, key)
		} else {
			entryCache[key] = fresh
		}
		if newTitle := fresh.Fields[TitleField]; newTitle != oldTitle {
			if oldTitle != "" {
				Library.TitleIndex.DeleteValueFromStringSetMap(TeXStringIndexer(oldTitle), key)
			}
			if newTitle != "" {
				Library.TitleIndex.AddValueToStringSetMap(TeXStringIndexer(newTitle), key)
			}
		}
	}
	if groups {
		Library.GroupEntries = TStringSetMap{}
		loadGroupsFromDb(&Library)
		resolveGroupEntriesKeys(&Library)
	}
	return true, ""
}
