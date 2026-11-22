/*
 *
 * Module:    bibtex_check
 * Component:
 * - bibtex_dblp
 *   - bibtex_dblp_db
 *
 * SQLite DBLP store: dblp.sqlite3 in dblpFolder(), one row per entry plus link
 * tables from titles, persons and ORCIDs to entry keys (plan step 21.3). It replaces the file
 * store of step 16 (one data.json per entry), which needed ≈ 112 GB of disk for
 * ≈ 7 GB of data and could not be copied or backed up as a whole.
 *
 * The store is never updated in place. An import builds dblp.sqlite3.new from the
 * XML and renames it over dblp.sqlite3 when complete, so the old store stays
 * usable until the new one is ready, and a failed or interrupted import leaves
 * nothing half-written behind.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 13.11.2026
 *
 */

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

const (
	dblpDBFileName = "dblp.sqlite3"

	// dblpDBRowsPerInsert is the number of rows per multi-row INSERT during a
	// build. One row per INSERT made a full load take 19 minutes instead of 82 s.
	dblpDBRowsPerInsert = 500

	// dblpDBRowsPerTransaction bounds the size of one build transaction.
	dblpDBRowsPerTransaction = 1_000_000

	// dblpReaderConnections bounds the read connections (32 MB page cache each).
	dblpReaderConnections = 8
)

// dblpDBSchema creates the tables of a new store.
//
//   - entries.data holds the TDblpJSONEntry JSON; crossref is copied out of it
//     (NULL when absent) so that crossref children can be found by index.
//   - The link tables hold the entry key itself, sorted under the lookup value,
//     so a lookup reads one contiguous range. (Linking to entries.id instead
//     cost one random read in the large entries table per key: 7 ms for an
//     author with 1,000 entries.) title_links.hash is dblpTitleHash of a title
//     or booktitle; person_entries.name is the LaTeX form of the canonical name;
//     orcid_entries.orcid is the raw ORCID.
//
// The link tables are filled by finish from staging tables (dblpDBStagingSchema)
// in sorted order: inserting into a sorted table in XML order is slow.
var dblpDBSchema = []string{
	`CREATE TABLE entries (
		id       INTEGER PRIMARY KEY,
		key      TEXT NOT NULL,
		mdate    TEXT NOT NULL,
		crossref TEXT,
		data     BLOB NOT NULL
	)`,
	`CREATE TABLE title_links (hash TEXT, key TEXT, PRIMARY KEY (hash, key)) WITHOUT ROWID`,
	`CREATE TABLE person_entries (name TEXT, key TEXT, PRIMARY KEY (name, key)) WITHOUT ROWID`,
	`CREATE TABLE orcid_entries (orcid TEXT, key TEXT, PRIMARY KEY (orcid, key)) WITHOUT ROWID`,
	`CREATE TABLE meta (name TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID`,
	// The DBLP persons (homepages records), from Pass 1 (step 23.1; before, CSV files).
	// persons: display name and, when it differs, the bibtex= form (both LaTeX).
	`CREATE TABLE persons (key TEXT PRIMARY KEY, canonical TEXT NOT NULL, bibtex TEXT) WITHOUT ROWID`,
	// person_names: every name form with every person that has it; a name with
	// several keys is shared by several persons.
	`CREATE TABLE person_names (name TEXT, key TEXT, PRIMARY KEY (name, key)) WITHOUT ROWID`,
	// person_orcids: every ORCID of a person; preferred = 1 for the one DBLP shows first.
	`CREATE TABLE person_orcids (orcid TEXT, key TEXT, preferred INTEGER NOT NULL, PRIMARY KEY (orcid, key)) WITHOUT ROWID`,
	// person_oldies: a homepages record that only redirects (crossref) to another.
	`CREATE TABLE person_oldies (key TEXT PRIMARY KEY, target TEXT NOT NULL) WITHOUT ROWID`,
}

// dblpDBStagingSchema holds the links during a build, in a separate file that
// finish deletes. entry_id tells repeated occurrences of a key apart.
var dblpDBStagingSchema = []string{
	`CREATE TABLE staging.title_links (value TEXT, key TEXT, entry_id INTEGER)`,
	`CREATE TABLE staging.person_entries (value TEXT, key TEXT, entry_id INTEGER)`,
	`CREATE TABLE staging.orcid_entries (value TEXT, key TEXT, entry_id INTEGER)`,
}

// dblpDBLinkTables lists the link tables with their lookup column.
var dblpDBLinkTables = [][2]string{
	{"title_links", "hash"}, {"person_entries", "name"}, {"orcid_entries", "orcid"},
}

// dblpDBIndexes are created by finish, after loading: building an index once is
// much faster than maintaining it row by row.
var dblpDBIndexes = []string{
	`CREATE UNIQUE INDEX entries_key ON entries(key)`,
	// key in the index: children lookups then never read the (large) entry rows.
	`CREATE INDEX entries_crossref ON entries(crossref, key) WHERE crossref IS NOT NULL`,
}

// Meta names in the meta table; together they replace meta.json and current.txt.
const (
	dblpMetaXMLFile              = "xml_file"
	dblpMetaLoadedAt             = "loaded_at"
	dblpMetaEntryCount           = "entry_count"
	dblpMetaHtmlCommandsMapHash  = "html_commands_map_hash"
	dblpMetaHtmlCharacterMapHash = "html_character_map_hash"
	dblpMetaUnicodeMapHash       = "unicode_map_hash"
)

func dblpDBPath() string        { return dblpFolder() + dblpDBFileName }
func dblpDBNewPath() string     { return dblpDBPath() + ".new" }
func dblpDBStagingPath() string { return dblpDBPath() + ".staging" }

// --- Reading ---

// dblpReader is the read-only connection to dblp.sqlite3, opened on first use.
// sql.DB and sql.Stmt are safe for concurrent use; the mutex only guards
// opening and closing.
var dblpReader struct {
	sync.Mutex
	db      *sql.DB
	stmts   map[string]*sql.Stmt
	missing bool // dblp.sqlite3 was absent at the last open attempt (warned once)
}

// dblpReadQueries are the statements readers use, by name.
var dblpReadQueries = map[string]string{
	"entry":    `SELECT data FROM entries WHERE key = ?`,
	"children": `SELECT key FROM entries WHERE crossref = ? AND key <> crossref ORDER BY key`,
	"parents": `SELECT DISTINCT crossref FROM entries
		WHERE crossref >= ? AND crossref < ? ORDER BY crossref`,
	"title":  `SELECT key FROM title_links WHERE hash = ? ORDER BY key`,
	"person": `SELECT key FROM person_entries WHERE name = ? ORDER BY key`,
	"orcid":  `SELECT key FROM orcid_entries WHERE orcid = ? ORDER BY key`,
	"search": `SELECT key FROM entries INDEXED BY entries_key
		WHERE instr(key, ?) > 0 ORDER BY key LIMIT ?`,
	"meta": `SELECT name, value FROM meta`,
	// Person tables (step 23.1); absent in a store built before 30.21.
	"person_names":  `SELECT key FROM person_names WHERE name = ? ORDER BY key`,
	"person_orcids": `SELECT orcid FROM person_orcids WHERE key = ? ORDER BY preferred DESC, orcid`,
	"orcid_persons": `SELECT key FROM person_orcids WHERE orcid = ? ORDER BY key`,
	// The whole table, read once (personORCIDs): its key is (orcid, key), so a lookup
	// by person key would scan it.
	"all_person_orcids": `SELECT key, orcid FROM person_orcids ORDER BY key, preferred DESC, orcid`,
}

// dblpQuietQueries fail silently when their table is absent (an older store).
var dblpQuietQueries = map[string]bool{"person_names": true, "person_orcids": true, "orcid_persons": true,
	"all_person_orcids": true}

// dblpStmt returns the prepared statement for a named read query, opening the
// store when needed. Returns nil when there is no store (warned once per open
// attempt) or it cannot be opened.
func dblpStmt(name string) *sql.Stmt {
	dblpReader.Lock()
	defer dblpReader.Unlock()
	if dblpReader.db == nil {
		if !FileExists(dblpDBPath()) {
			if !dblpReader.missing {
				dblpReader.missing = true
				messagef("Warning: no DBLP database at %s; run -update_dblp to build it.\n", dblpDBPath())
			}
			return nil
		}
		db, err := sql.Open(sqliteDatabaseDriver,
			"file:"+dblpDBPath()+"?mode=ro&_pragma=busy_timeout(5000)&_pragma=cache_size(-32768)")
		if err != nil {
			messagef("Warning: could not open %s: %s\n", dblpDBPath(), err)
			return nil
		}
		// Some callers look up from many goroutines at once (e.g. 32 workers in
		// absorbDblpNamesCore); a few connections serve them, each with its own cache.
		db.SetMaxOpenConns(dblpReaderConnections)
		dblpReader.db, dblpReader.stmts, dblpReader.missing = db, map[string]*sql.Stmt{}, false
	}
	if s, ok := dblpReader.stmts[name]; ok {
		return s
	}
	s, err := dblpReader.db.Prepare(dblpReadQueries[name])
	if err != nil {
		if !dblpQuietQueries[name] {
			messagef("Warning: DBLP database %s: %s\n", dblpDBPath(), err)
		}
		dblpReader.stmts[name] = nil // tried once per open; not again
		return nil
	}
	dblpReader.stmts[name] = s
	return s
}

// closeDblpReader closes the read connection, so the next read opens the file
// that is now at dblpDBPath() (after an import has swapped in a new store).
func closeDblpReader() {
	dblpReader.Lock()
	defer dblpReader.Unlock()
	if dblpReader.db != nil {
		dblpReader.db.Close()
	}
	dblpReader.db, dblpReader.stmts, dblpReader.missing = nil, nil, false
}

// dblpQueryStrings runs a named read query and returns its first column.
func dblpQueryStrings(name string, args ...any) []string {
	s := dblpStmt(name)
	if s == nil {
		return nil
	}
	rows, err := s.Query(args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			out = append(out, v)
		}
	}
	return out
}

// readDblpJSONEntry returns the parsed entry for dblpKey, or nil when it is not
// in the store (or there is no store).
func readDblpJSONEntry(dblpKey string) *TDblpJSONEntry {
	s := dblpStmt("entry")
	if s == nil {
		return nil
	}
	var data []byte
	if s.QueryRow(dblpKey).Scan(&data) != nil {
		return nil
	}
	var je TDblpJSONEntry
	if json.Unmarshal(data, &je) != nil {
		return nil
	}
	return &je
}

// readDblpCrossrefChildren returns the DBLP keys whose crossref is parentKey.
func readDblpCrossrefChildren(parentKey string) []string {
	if parentKey == "" {
		return nil
	}
	return dblpQueryStrings("children", parentKey)
}

// readDblpCrossrefParentsIn returns the crossref parents with at least one child
// whose key lies directly in parentDir (e.g. "conf/er" → "conf/er/2002", …).
// This is what listing crossrefs/<parentDir>/ returned in the file store.
func readDblpCrossrefParentsIn(parentDir string) []string {
	prefix := parentDir + "/"
	// '0' is the character after '/', so this range holds exactly the prefixed keys.
	var out []string
	for _, p := range dblpQueryStrings("parents", prefix, parentDir+"0") {
		if !strings.Contains(p[len(prefix):], "/") {
			out = append(out, p)
		}
	}
	return out
}

// readDblpPersonEntries returns the DBLP entry keys for a given canonical name.
func readDblpPersonEntries(canonicalName string) []string {
	name := dblpRawToLaTeX(canonicalName)
	if name == "" {
		return nil
	}
	return dblpQueryStrings("person", name)
}

// readDblpORCIDEntries returns the DBLP entry keys for a given ORCID.
func readDblpORCIDEntries(orcid string) []string {
	if orcid == "" {
		return nil
	}
	return dblpQueryStrings("orcid", orcid)
}

// readDblpTitleLinks returns the DBLP keys whose title or booktitle has this hash.
func readDblpTitleLinks(hash string) []string {
	if hash == "" {
		return nil
	}
	return dblpQueryStrings("title", hash)
}

// searchDblpKeysContaining returns up to limit+1 DBLP keys containing substr, in
// key order (the extra one lets a caller distinguish "exactly limit matches" from
// "too many to be useful"). Scans the key index only, not the entry data.
func searchDblpKeysContaining(substr string, limit int) []string {
	return dblpQueryStrings("search", substr, limit+1)
}

// readDblpMeta returns the metadata of the current store, or nil without one.
func readDblpMeta() *TDblpMeta {
	s := dblpStmt("meta")
	if s == nil {
		return nil
	}
	rows, err := s.Query()
	if err != nil {
		return nil
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var name, value string
		if rows.Scan(&name, &value) == nil {
			values[name] = value
		}
	}
	if len(values) == 0 {
		return nil
	}
	meta := &TDblpMeta{
		XMLFile:              values[dblpMetaXMLFile],
		LoadedAt:             values[dblpMetaLoadedAt],
		HtmlCommandsMapHash:  values[dblpMetaHtmlCommandsMapHash],
		HtmlCharacterMapHash: values[dblpMetaHtmlCharacterMapHash],
		UnicodeMapHash:       values[dblpMetaUnicodeMapHash],
	}
	fmt.Sscan(values[dblpMetaEntryCount], &meta.EntryCount)
	return meta
}

// readDblpCurrentXML returns the basename of the XML file the current store was
// built from, or "" when there is no store (without the missing-store warning:
// callers ask this to decide whether to build one).
func readDblpCurrentXML() string {
	if !FileExists(dblpDBPath()) {
		return ""
	}
	if meta := readDblpMeta(); meta != nil {
		return meta.XMLFile
	}
	return ""
}

// --- Building ---

// TDblpDBBuilder builds a new store in dblpDBNewPath(). Rows are buffered per
// table and written with multi-row INSERTs; entry ids are assigned here, so the
// link tables can refer to entries without reading anything back.
type TDblpDBBuilder struct {
	db       *sql.DB
	tx       *sql.Tx
	nextID   int64
	inTx     int
	tables   []*dblpDBTableBuffer
	entries  *dblpDBTableBuffer
	titles   *dblpDBTableBuffer
	persons  *dblpDBTableBuffer
	orcids   *dblpDBTableBuffer
	stmtFull map[*dblpDBTableBuffer]*sql.Stmt
}

// dblpDBTableBuffer collects the values of rows not yet inserted into one table.
type dblpDBTableBuffer struct {
	table   string
	columns int
	values  []any
}

func (t *dblpDBTableBuffer) rows() int { return len(t.values) / t.columns }

// insertSQL returns a multi-row INSERT for n rows of this table.
func (t *dblpDBTableBuffer) insertSQL(n int) string {
	row := "(?" + strings.Repeat(",?", t.columns-1) + ")"
	return "INSERT INTO " + t.table + " VALUES " + row + strings.Repeat(","+row, n-1)
}

// newDblpDBBuilder starts a new store, replacing any .new and .staging files
// left behind by an earlier, unfinished build.
func newDblpDBBuilder() (*TDblpDBBuilder, error) {
	if err := os.MkdirAll(dblpFolder(), 0755); err != nil {
		return nil, err
	}
	removeDblpDBFiles(dblpDBNewPath())
	removeDblpDBFiles(dblpDBStagingPath())
	db, err := sql.Open(sqliteDatabaseDriver, dblpDBNewPath())
	if err != nil {
		return nil, err
	}
	// One connection: ATTACH and the temporary tables belong to a connection.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	// Both files are thrown away when a build fails, so no journal and no syncs.
	pragmas := []string{"PRAGMA journal_mode = OFF", "PRAGMA synchronous = OFF",
		"PRAGMA page_size = 8192", "PRAGMA cache_size = -524288",
		"ATTACH DATABASE '" + strings.ReplaceAll(dblpDBStagingPath(), "'", "''") + "' AS staging",
		"PRAGMA staging.journal_mode = OFF", "PRAGMA staging.synchronous = OFF"}
	schema := append(append(pragmas, dblpDBSchema...), dblpDBStagingSchema...)
	for _, s := range schema {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", s, err)
		}
	}
	b := &TDblpDBBuilder{db: db, nextID: 1, stmtFull: map[*dblpDBTableBuffer]*sql.Stmt{}}
	b.entries = &dblpDBTableBuffer{table: "entries", columns: 5}
	b.titles = &dblpDBTableBuffer{table: "staging.title_links", columns: 3}
	b.persons = &dblpDBTableBuffer{table: "staging.person_entries", columns: 3}
	b.orcids = &dblpDBTableBuffer{table: "staging.orcid_entries", columns: 3}
	b.tables = []*dblpDBTableBuffer{b.entries, b.titles, b.persons, b.orcids}
	for _, t := range b.tables {
		if b.stmtFull[t], err = db.Prepare(t.insertSQL(dblpDBRowsPerInsert)); err != nil {
			db.Close()
			return nil, err
		}
	}
	if b.tx, err = db.Begin(); err != nil {
		db.Close()
		return nil, err
	}
	return b, nil
}

// addEntry adds one DBLP entry with its index links. A key seen before in this
// build replaces the earlier entry (see finish).
func (b *TDblpDBBuilder) addEntry(dblpKey, mdate, crossref string, data []byte,
	titleHashes, personNames, orcids []string) error {
	id := b.nextID
	b.nextID++
	var cr any // NULL keeps the row out of the partial crossref index
	if crossref != "" {
		cr = crossref
	}
	if err := b.add(b.entries, id, dblpKey, mdate, cr, data); err != nil {
		return err
	}
	for _, h := range titleHashes {
		if err := b.add(b.titles, h, dblpKey, id); err != nil {
			return err
		}
	}
	for _, n := range personNames {
		if err := b.add(b.persons, n, dblpKey, id); err != nil {
			return err
		}
	}
	for _, o := range orcids {
		if err := b.add(b.orcids, o, dblpKey, id); err != nil {
			return err
		}
	}
	return nil
}

// add buffers one row, inserting the buffer when it is full.
func (b *TDblpDBBuilder) add(t *dblpDBTableBuffer, values ...any) error {
	t.values = append(t.values, values...)
	if t.rows() < dblpDBRowsPerInsert {
		return nil
	}
	if _, err := b.tx.Stmt(b.stmtFull[t]).Exec(t.values...); err != nil {
		return fmt.Errorf("inserting into %s: %w", t.table, err)
	}
	t.values = t.values[:0]
	if b.inTx += dblpDBRowsPerInsert; b.inTx >= dblpDBRowsPerTransaction {
		if err := b.tx.Commit(); err != nil {
			return err
		}
		var err error
		if b.tx, err = b.db.Begin(); err != nil {
			return err
		}
		b.inTx = 0
	}
	return nil
}

// flush inserts all buffered rows.
func (b *TDblpDBBuilder) flush() error {
	for _, t := range b.tables {
		if n := t.rows(); n > 0 {
			if _, err := b.tx.Exec(t.insertSQL(n), t.values...); err != nil {
				return fmt.Errorf("inserting into %s: %w", t.table, err)
			}
			t.values = t.values[:0]
		}
	}
	return nil
}

// addPersonMaps writes the person tables from Pass 1, each in key order (a
// WITHOUT ROWID table fills fastest in its own order).
func (b *TDblpDBBuilder) addPersonMaps(pm dblpPersonMaps) error {
	if err := b.flush(); err != nil {
		return err
	}
	insert := func(query string, rows [][]any) error {
		// Prepared in the transaction: the builder has one connection, and the open
		// transaction holds it (b.db.Prepare would wait for it forever).
		stmt, err := b.tx.Prepare(query)
		if err != nil {
			return err
		}
		for i, r := range rows {
			if _, err := stmt.Exec(r...); err != nil {
				return fmt.Errorf("%s: %w", query, err)
			}
			if i%dblpDBRowsPerTransaction == dblpDBRowsPerTransaction-1 {
				stmt.Close()
				if err := b.tx.Commit(); err != nil {
					return err
				}
				if b.tx, err = b.db.Begin(); err != nil {
					return err
				}
				if stmt, err = b.tx.Prepare(query); err != nil {
					return err
				}
			}
		}
		return stmt.Close()
	}
	sorted := func(m map[string]string) []string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}

	var rows [][]any
	for _, key := range sorted(pm.keyToCanonical) {
		var bibtex any
		if bt := pm.keyToBibtex[key]; bt != "" {
			bibtex = bt
		}
		rows = append(rows, []any{key, pm.keyToCanonical[key], bibtex})
	}
	if err := insert(`INSERT INTO persons VALUES (?, ?, ?)`, rows); err != nil {
		return err
	}

	rows = rows[:0]
	names := make([]string, 0, len(pm.nameKeys))
	for n := range pm.nameKeys {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		keys := append([]string(nil), pm.nameKeys[n]...)
		sort.Strings(keys)
		for _, k := range keys {
			rows = append(rows, []any{n, k})
		}
	}
	if err := insert(`INSERT INTO person_names VALUES (?, ?)`, rows); err != nil {
		return err
	}

	type orcidRow struct {
		orcid, key string
		preferred  int
	}
	var orcids []orcidRow
	for key, list := range pm.keyOrcids {
		for i, o := range list {
			pref := 0
			if i == 0 {
				pref = 1
			}
			orcids = append(orcids, orcidRow{o, key, pref})
		}
	}
	sort.Slice(orcids, func(i, j int) bool {
		if orcids[i].orcid != orcids[j].orcid {
			return orcids[i].orcid < orcids[j].orcid
		}
		return orcids[i].key < orcids[j].key
	})
	rows = rows[:0]
	for _, o := range orcids {
		rows = append(rows, []any{o.orcid, o.key, o.preferred})
	}
	if err := insert(`INSERT INTO person_orcids VALUES (?, ?, ?)`, rows); err != nil {
		return err
	}

	rows = rows[:0]
	for _, key := range sorted(pm.keyOldies) {
		rows = append(rows, []any{key, pm.keyOldies[key]})
	}
	return insert(`INSERT INTO person_oldies VALUES (?, ?)`, rows)
}

// loadDblpPersonMapsFromDB reads the person maps from the person tables. ok is
// false for a store built before step 23.1 (no person tables): the caller then
// reads the CSV files.
func loadDblpPersonMapsFromDB() (pm dblpPersonMaps, ok bool) {
	if !FileExists(dblpDBPath()) {
		return pm, false
	}
	db, err := sql.Open(sqliteDatabaseDriver, "file:"+dblpDBPath()+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return pm, false
	}
	defer db.Close()
	var n int
	if db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'persons'`).Scan(&n) != nil || n == 0 {
		return pm, false
	}
	pm = dblpPersonMaps{
		nameToKey:      make(map[string]string),
		orcidToKey:     make(map[string]string),
		keyToCanonical: make(map[string]string),
		keyToBibtex:    make(map[string]string),
		keyOldies:      make(map[string]string),
	}
	read := func(query string, fn func(a, b string)) bool {
		rows, err := db.Query(query)
		if err != nil {
			return false
		}
		defer rows.Close()
		for rows.Next() {
			var a, b string
			if rows.Scan(&a, &b) == nil {
				fn(a, b)
			}
		}
		return rows.Err() == nil
	}
	ok = read(`SELECT key, canonical FROM persons`, func(k, c string) { pm.keyToCanonical[k] = c }) &&
		read(`SELECT key, bibtex FROM persons WHERE bibtex IS NOT NULL`, func(k, b string) { pm.keyToBibtex[k] = b }) &&
		// A name shared by several persons maps to "" (as Pass 1 marks it).
		read(`SELECT name, CASE WHEN count(*) = 1 THEN min(key) ELSE '' END FROM person_names GROUP BY name`,
			func(n, k string) {
				if k != "" {
					pm.nameToKey[n] = k
				}
			}) &&
		// An ORCID listed by several persons: the one that prefers it, else the first.
		read(`SELECT orcid, key FROM person_orcids ORDER BY orcid, preferred DESC, key`,
			func(o, k string) {
				if _, seen := pm.orcidToKey[o]; !seen {
					pm.orcidToKey[o] = k
				}
			}) &&
		read(`SELECT key, target FROM person_oldies`, func(k, t string) { pm.keyOldies[k] = t })
	return pm, ok
}

// removeDuplicates keeps only the last occurrence of a key that appeared more
// than once in the XML, together with that occurrence's links, as the file
// store did when it rewrote an entry seen earlier in the same import.
func (b *TDblpDBBuilder) removeDuplicates() (int, error) {
	if _, err := b.db.Exec(`CREATE INDEX entries_key_build ON entries(key, id)`); err != nil {
		return 0, err
	}
	if _, err := b.db.Exec(`CREATE TEMP TABLE dropped AS
		SELECT e.id FROM entries e
		JOIN (SELECT key, max(id) AS keep FROM entries GROUP BY key HAVING count(*) > 1) d
		  ON e.key = d.key AND e.id <> d.keep`); err != nil {
		return 0, err
	}
	var n int
	if err := b.db.QueryRow(`SELECT count(*) FROM dropped`).Scan(&n); err != nil {
		return 0, err
	}
	if n > 0 {
		for _, t := range []string{"entries WHERE id", "staging.title_links WHERE entry_id",
			"staging.person_entries WHERE entry_id", "staging.orcid_entries WHERE entry_id"} {
			if _, err := b.db.Exec(`DELETE FROM ` + t + ` IN (SELECT id FROM dropped)`); err != nil {
				return 0, err
			}
		}
	}
	_, err := b.db.Exec(`DROP INDEX entries_key_build`)
	return n, err
}

// finish completes the build: inserts what is buffered, drops duplicate keys,
// fills the link tables, creates the indexes, stores meta, checks the file and
// renames it over dblpDBPath(). The read connection is closed so later reads see
// the new store. On error the .new file is removed and the current store is left
// untouched.
func (b *TDblpDBBuilder) finish(meta TDblpMeta, progress func(format string, args ...any)) error {
	err := b.finishBuild(meta, progress)
	if cerr := b.db.Close(); err == nil {
		err = cerr
	}
	removeDblpDBFiles(dblpDBStagingPath())
	if err != nil {
		removeDblpDBFiles(dblpDBNewPath())
		return err
	}
	return swapInDblpDBNew()
}

// swapInDblpDBNew renames a complete dblpDBNewPath() over dblpDBPath(), after an
// import or a restore. The read connection is closed so later reads see the new
// store.
func swapInDblpDBNew() error {
	resetPersonORCIDs()
	closeDblpReader()
	closeOrcidStore()
	// A stale journal of the old file must not attach to the new one. The old
	// file itself is replaced by the rename, so there is never a moment without one.
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		os.Remove(dblpDBPath() + suffix)
	}
	return os.Rename(dblpDBNewPath(), dblpDBPath())
}

func (b *TDblpDBBuilder) finishBuild(meta TDblpMeta, progress func(format string, args ...any)) error {
	if err := b.flush(); err != nil {
		return err
	}
	if err := b.tx.Commit(); err != nil {
		return err
	}
	progress("Removing duplicate keys...")
	n, err := b.removeDuplicates()
	if err != nil {
		return fmt.Errorf("removing duplicate keys: %w", err)
	}
	if n > 0 {
		progress("  %d earlier occurrences of repeated keys removed.", n)
	}
	progress("Sorting title, person and ORCID links...")
	for _, lt := range dblpDBLinkTables {
		// DISTINCT: an entry can link twice, e.g. a title equal to its booktitle.
		if _, err := b.db.Exec(`INSERT INTO main.` + lt[0] + ` SELECT DISTINCT value, key FROM staging.` +
			lt[0] + ` ORDER BY value, key`); err != nil {
			return fmt.Errorf("filling %s: %w", lt[0], err)
		}
	}
	if _, err := b.db.Exec(`DETACH DATABASE staging`); err != nil {
		return err
	}
	progress("Copying the ORCID records...")
	if err := copyOrcidRecordsInto(b.db); err != nil {
		return fmt.Errorf("copying orcid_records: %w", err)
	}
	progress("Creating indexes...")
	for _, s := range dblpDBIndexes {
		if _, err := b.db.Exec(s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	values := map[string]string{
		dblpMetaXMLFile:              meta.XMLFile,
		dblpMetaLoadedAt:             meta.LoadedAt,
		dblpMetaEntryCount:           fmt.Sprint(meta.EntryCount),
		dblpMetaHtmlCommandsMapHash:  meta.HtmlCommandsMapHash,
		dblpMetaHtmlCharacterMapHash: meta.HtmlCharacterMapHash,
		dblpMetaUnicodeMapHash:       meta.UnicodeMapHash,
	}
	for name, value := range values {
		if _, err := b.db.Exec(`INSERT INTO meta VALUES (?, ?)`, name, value); err != nil {
			return err
		}
	}
	if _, err := b.db.Exec(`ANALYZE`); err != nil {
		return err
	}
	progress("Checking the new database...")
	var check string
	if err := b.db.QueryRow(`PRAGMA quick_check`).Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return fmt.Errorf("quick_check: %s", check)
	}
	return nil
}

// abort discards an unfinished build.
func (b *TDblpDBBuilder) abort() {
	b.tx.Rollback()
	b.db.Close()
	removeDblpDBFiles(dblpDBNewPath())
	removeDblpDBFiles(dblpDBStagingPath())
}

// removeDblpDBFiles removes a database file with its journal files, if present.
func removeDblpDBFiles(path string) {
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		os.Remove(path + suffix)
	}
}
