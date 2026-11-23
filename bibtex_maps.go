/*
 *
 * Module:    bibtex_check
 * Component:
 * - bibtex_maps
 *
 * The hand-maintained maps (unicode_map.csv, html_commands_map.csv,
 * html_character_map.csv, latex_indexer.csv, harvest_ignore_keys.csv,
 * hyphenations.csv) live in the library database (plan step 22.1b, Erik
 * 2026-11-18), so they travel with the cloud copy and are the same on every
 * machine. Each map is kept as its file text, unchanged, comments included, in
 * the table map_files; the loaders parse it as they parsed the file.
 *
 * The maps are needed before the database opens (the DBLP import uses them too),
 * so their texts are read at start-up straight from the local database file. A map
 * not yet in the database is read from its old file in the local DB folder, and
 * stored in the database by the next writing run. Edited with -export / -import
 * (in $base.tables/), like the other tables.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 18.11.2026
 *
 */

package main

import (
	"bufio"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// mapFileNames are the maps kept in map_files, by file name.
var mapFileNames = []string{
	"unicode_map.csv",
	"html_commands_map.csv",
	"html_character_map.csv",
	"latex_indexer.csv",
	"harvest_ignore_keys.csv",
	"hyphenations.csv",
}

// mapContents holds the map texts read from the database at start-up, by file name.
var mapContents = map[string]string{}

const mapFilesSchema = `
	CREATE TABLE IF NOT EXISTS map_files (
		name    TEXT PRIMARY KEY,
		content TEXT NOT NULL
	)`

// loadMapContentsFromDB reads the map texts from the local library database
// (read-only), before anything else opens it. No database or no table: no texts.
// readKeyPrefixFromDB reads the key prefix from the local database's config,
// read-only, before the database is opened; "" when there is none.
func readKeyPrefixFromDB() string {
	path := dbPath()
	if !FileExists(path) {
		return ""
	}
	conn, err := sql.Open(sqliteDatabaseDriver, "file:"+path+"?mode=ro")
	if err != nil {
		return ""
	}
	defer conn.Close()
	var prefix string
	conn.QueryRow(`SELECT value FROM config WHERE key = 'key_prefix'`).Scan(&prefix) //nolint:errcheck
	return prefix
}

func loadMapContentsFromDB() {
	path := dbPath()
	if !FileExists(path) {
		return
	}
	conn, err := sql.Open(sqliteDatabaseDriver, "file:"+path+"?mode=ro")
	if err != nil {
		return
	}
	defer conn.Close()
	rows, err := conn.Query(`SELECT name, content FROM map_files`)
	if err != nil {
		return // no map_files table yet
	}
	defer rows.Close()
	for rows.Next() {
		var name, content string
		if rows.Scan(&name, &content) == nil {
			mapContents[name] = content
		}
	}
}

// openMap opens a map by its (old) file path: the text from the database when
// there is one, else the file in the local DB folder.
func openMap(path string) (io.ReadCloser, error) {
	if content, ok := mapContents[filepath.Base(path)]; ok {
		return io.NopCloser(strings.NewReader(content)), nil
	}
	return os.Open(path)
}

// mapExists reports whether a map is available (database or file).
func mapExists(path string) bool {
	if _, ok := mapContents[filepath.Base(path)]; ok {
		return true
	}
	return FileExists(path)
}

// processMap calls process for each line of a map, like processFile.
func processMap(path string, process func(string)) bool {
	f, err := openMap(path)
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		process(scanner.Text())
	}
	return scanner.Err() == nil
}

// storeMapsInDB stores, in a write session, every map that is not in the database
// yet but has a file in the local DB folder (the migration from the files).
func storeMapsInDB() {
	if _, err := db.Exec(mapFilesSchema); err != nil {
		dbInteraction.Warning("Could not create map_files: %s", err)
		return
	}
	for _, name := range mapFileNames {
		if _, ok := mapContents[name]; ok {
			continue
		}
		data, err := os.ReadFile(localDBFolder + name)
		if err != nil {
			continue
		}
		if err := setMapContent(name, string(data)); err != nil {
			dbInteraction.Warning("Could not store %s in the database: %s", name, err)
			continue
		}
		dbInteraction.Progress("  Stored %s in the database", name)
	}
}

func setMapContent(name, content string) error {
	if _, err := db.Exec(mapFilesSchema); err != nil {
		return err
	}
	if err := bibExec(`INSERT INTO map_files (name, content) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET content = excluded.content`, name, content); err != nil {
		return err
	}
	mapContents[name] = content
	return nil
}

func init() {
	// Appended to tableRegistry (filled by bibtex_library_tables.go's init, which
	// runs first: Go initialises files in name order), so -export / -import know
	// the maps, by name without ".csv".
	for _, name := range mapFileNames {
		n := name
		tableRegistry = append(tableRegistry, tTableEntry{
			name:     strings.TrimSuffix(n, ".csv"),
			filePath: mapTablePath(n),
			exportFn: func() { exportMap(n) },
			importFn: func() { importMap(n) },
		})
	}
}

// mapTablePath is where -export writes a map and -import reads it, relative to
// libraryBase().
func mapTablePath(name string) string {
	return tablesFolderSuffix + "/" + name
}

// exportMap writes a map's text to $base.tables/<name>.
func exportMap(name string) {
	content, ok := mapContents[name]
	if !ok {
		data, err := os.ReadFile(localDBFolder + name)
		if err != nil {
			dbInteraction.Progress("No %s to export", name)
			return
		}
		content = string(data)
	}
	ensureTablesDir()
	path := tablesFilePath(mapTablePath(name))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		dbInteraction.Warning("Could not write %s: %s", path, err)
		return
	}
	dbInteraction.Progress("Exported %s to %s", name, path)
}

// importMap stores $base.tables/<name> in the database and reloads the map.
func importMap(name string) {
	path := tablesFilePath(mapTablePath(name))
	data, err := os.ReadFile(path)
	if err != nil {
		dbInteraction.Warning("Could not read %s: %s", path, err)
		return
	}
	if err := setMapContent(name, string(data)); err != nil {
		dbInteraction.Warning("Could not store %s in the database: %s", name, err)
		return
	}
	reloadMap(name)
	dbInteraction.Progress("Imported %s from %s", name, path)
}

// reloadMap parses a map again after an import.
func reloadMap(name string) {
	path := localDBFolder + name
	switch name {
	case "unicode_map.csv":
		loadUnicodeMap(path)
	case "html_commands_map.csv":
		loadHtmlCommandsMap(path)
	case "html_character_map.csv":
		loadHtmlCharacterMap(path)
	case "latex_indexer.csv":
		loadLatexIndexerMap(path)
	case "harvest_ignore_keys.csv":
		harvestIgnoreKeys = TStringSetNew()
		loadHarvestIgnoreKeys(path)
	}
	// hyphenations.csv is read where it is used (readHyphenations).
}
