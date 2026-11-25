/*
 *
 * Module:    bibdb_manager
 * Component:
 * - bibtex_dblp
 *   - bibtex_dblp_files
 *
 * DBLP folder, title hashes, and the JSON form of DBLP entries. The store
 * itself (dblp.sqlite3) lives in bibtex_dblp_db.go; until step 21.3 it was a
 * file store here, with one data.json per entry.
 * Text values are stored verbatim from the XML; LaTeX conversion via
 * dblpRawToLaTeX happens at read time.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 13.11.2026
 *
 */

package main

import (
	"crypto/md5"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dblpFolder returns the folder holding the DBLP store, XML downloads and name maps.
func dblpFolder() string {
	return localDBFolder + dblpSubfolder
}

// maybeMigrateFolderNames renames the subfolders of the 2026-11-18 layout (step
// 22.1b) on the first run after upgrading: in the local DB folder DBLP.cache/ →
// DBLP/ and ORCID.cache/ → ORCID/; in the shared DB folder DBLP.backup/ → DBLP/.
// Called at startup, before anything uses those folders.
func maybeMigrateFolderNames() {
	for _, m := range []struct{ folder, old, new string }{
		{localDBFolder, "DBLP.cache/", dblpSubfolder},
		{localDBFolder, "ORCID.cache/", orcidSubfolder},
		{sharedDBFolder, "DBLP.backup/", dblpSubfolder},
	} {
		oldPath, newPath := m.folder+m.old, m.folder+m.new
		if pathExists(newPath) || !pathExists(oldPath) { // folders: not FileExists
			continue
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			messagef("Could not rename %s to %s: %s\n", oldPath, newPath, err)
			continue
		}
		stderrPrintf("Renamed %s → %s\n", oldPath, newPath)
	}
}

// maybeMigrateDblpNameFiles moves the Pass-1 DBLP name CSVs (dblp_name_bibtex.csv,
// dblp_name_orcid.csv) from the legacy localDBFolder location into dblpFolder(),
// where they belong alongside the rest of the DBLP-derived cache. Called at
// startup before any access to those files.
// pathExists reports whether path exists, file or folder.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

const (
	dblpSubfolder  = "DBLP/"  // the DBLP store, in the local and in the shared DB folder
	orcidSubfolder = "ORCID/" // the ORCID cache, in the local DB folder
)

func maybeMigrateDblpNameFiles() {
	if localDBFolder == "" {
		return // no separate folder; files were always alongside the DBLP store
	}
	for _, name := range []string{"dblp_name_bibtex.csv", "dblp_name_orcid.csv"} {
		oldPath := localDBFolder + name
		newPath := dblpFolder() + name
		if _, err := os.Stat(newPath); err == nil {
			continue // already migrated
		}
		if _, err := os.Stat(oldPath); err != nil {
			continue // nothing to migrate
		}
		if err := os.MkdirAll(dblpFolder(), 0755); err != nil {
			continue
		}
		if err := os.Rename(oldPath, newPath); err == nil {
			dbInteraction.Progress("Migrated %s into the DBLP store folder", name)
		}
	}
}

func dblpTrashFolder() string { return dblpFolder() + "trash/" }

// moveToDblpTrash moves path into the DBLP trash folder with a timestamp
// suffix. The rename is an O(1) syscall regardless of how many files are inside.
func moveToDblpTrash(path string) error {
	if err := os.MkdirAll(dblpTrashFolder(), 0755); err != nil {
		return err
	}
	dest := dblpTrashFolder() + filepath.Base(path) + "-" + time.Now().Format("20060102-150405")
	return os.Rename(path, dest)
}

// maybeStartDblpTrashCleanup launches a background goroutine to remove the
// contents of the DBLP trash folder when it is non-empty. The goroutine is
// not waited on; if the process exits before it completes the remaining trash
// is cleaned on the next invocation. Skipped when cmdNoGarbageCleaning is set.
func maybeStartDblpTrashCleanup() {
	if cmdNoGarbageCleaning {
		return
	}
	des, err := os.ReadDir(dblpTrashFolder())
	if err != nil || len(des) == 0 {
		return
	}
	messagef("Deleting DBLP trash in background.\n")
	go os.RemoveAll(dblpTrashFolder())
}

// csvFileHash returns the MD5 hex digest of the file at path, or "" on error.
func csvFileHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := md5.New()
	io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

// dblpTitleHash returns the MD5 hex digest of the normalised, indexed form of
// a verbatim title value (as stored in the entry JSON). Returns "" for empty titles.
func dblpTitleHash(verbatimTitle string) string {
	idx := TeXStringIndexer(dblpRawToLaTeX(verbatimTitle))
	if idx == "" {
		return ""
	}
	h := md5.Sum([]byte(idx))
	return hex.EncodeToString(h[:])
}

// libraryTitleHash returns the MD5 hex digest of the normalised, indexed form
// of a LaTeX title value (as stored in the library). Produces the same hash as
// dblpTitleHash for equivalent titles since TeXStringIndexer normalises encoding
// differences. Returns "" for empty titles.
func libraryTitleHash(latexTitle string) string {
	idx := TeXStringIndexer(latexTitle)
	if idx == "" {
		return ""
	}
	h := md5.Sum([]byte(idx))
	return hex.EncodeToString(h[:])
}

// --- JSON types ---

// TDblpJSONPerson is the JSON representation of an author or editor.
type TDblpJSONPerson struct {
	Name  string `json:"name"`
	ORCID string `json:"orcid,omitempty"`
}

// TDblpJSONEntry is the JSON representation of a DBLP entry (entries.data).
// Fields holds single-valued fields (title, year, journal, etc.).
// Multi holds multi-valued fields (ee, url, cite, note).
// All text values are stored verbatim from the XML.
type TDblpJSONEntry struct {
	EntryType string              `json:"entrytype"`
	Mdate     string              `json:"mdate,omitempty"`
	PubType   string              `json:"publtype,omitempty"`
	Fields    map[string]string   `json:"fields,omitempty"`
	Authors   []TDblpJSONPerson   `json:"authors,omitempty"`
	Editors   []TDblpJSONPerson   `json:"editors,omitempty"`
	Multi     map[string][]string `json:"multi,omitempty"`
}

// TDblpMeta describes the current store (the meta table of dblp.sqlite3).
type TDblpMeta struct {
	XMLFile              string `json:"xml_file"` // basename of the XML the store was built from
	LoadedAt             string `json:"loaded_at"`
	EntryCount           int    `json:"entry_count"`
	HtmlCommandsMapHash  string `json:"html_commands_map_hash,omitempty"`
	HtmlCharacterMapHash string `json:"html_character_map_hash,omitempty"`
	UnicodeMapHash       string `json:"unicode_map_hash,omitempty"`
}

// dblpEntryFromStore builds a TBibTeXEntry from the DBLP store for dblpKey.
// Returns nil when the entry does not exist in the store.
func dblpEntryFromStore(dblpKey string) *TBibTeXEntry {
	je := readDblpJSONEntry(dblpKey)
	if je == nil {
		return nil
	}
	return jsonEntryToLibEntry(dblpKey, je)
}

// dblpEntryTypeToBibTeX maps DBLP-specific entry types to their BibTeX equivalents.
// DBLP's "data" (dataset) and "www" (person homepage) have no BibTeX counterpart;
// both are represented as "misc".
var dblpEntryTypeToBibTeX = map[string]string{
	"data": "misc",
	"www":  "misc",
}

// jsonEntryToLibEntry converts a TDblpJSONEntry to a TBibTeXEntry.
func jsonEntryToLibEntry(dblpKey string, je *TDblpJSONEntry) *TBibTeXEntry {
	entry := &TBibTeXEntry{Key: KeyForDBLP(dblpKey), Fields: map[string]string{}}
	entryType := je.EntryType
	if mapped, ok := dblpEntryTypeToBibTeX[entryType]; ok {
		entryType = mapped
	}
	entry.Fields[EntryTypeField] = entryType
	for field, value := range je.Fields {
		entry.Fields[field] = dblpRawToLaTeX(value)
	}
	// ee: derive doi when possible; otherwise keep first HTTP(S) value as url.
	if ees, ok := je.Multi["ee"]; ok && entry.Fields["doi"] == "" {
		for _, ee := range ees {
			normalised := NormaliseURLValue(nil, ee)
			if strings.HasPrefix(normalised, "https://doi.org/") {
				entry.Fields["doi"] = strings.TrimPrefix(normalised, "https://doi.org/")
				break
			} else if (strings.HasPrefix(normalised, "http://") || strings.HasPrefix(normalised, "https://")) && entry.Fields["url"] == "" {
				entry.Fields["url"] = normalised
			}
		}
	}
	buildNames := func(role string, persons []TDblpJSONPerson) {
		if len(persons) == 0 {
			return
		}
		names := make([]string, 0, len(persons))
		for _, p := range persons {
			names = append(names, dblpPersonNameToLaTeX(p.Name))
		}
		entry.Fields[role] = strings.Join(names, " and ")
	}
	buildNames("author", je.Authors)
	buildNames("editor", je.Editors)
	return entry
}

// dblpWithdrawnInfoFromStore returns (true, mdate) when the stored entry for
// dblpKey has publtype == "withdrawn". Always reads the store (not a cache).
func dblpWithdrawnInfoFromStore(dblpKey string) (bool, string) {
	je := readDblpJSONEntry(dblpKey)
	if je == nil || je.PubType != "withdrawn" {
		return false, ""
	}
	return true, je.Mdate
}
