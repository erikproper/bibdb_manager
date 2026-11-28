/*
 *
 * Module:    bibdb_manager
 * Component:
 * - bibtex_config
 *   - bibtex_config_file
 *
 * Loads the per-library bootstrap file (<basename>.settings), writes it when absent,
 * and migrates the legacy <basename>.folders (and, before that, <basename>.config)
 * file on the first run after upgrading.
 *
 * The .settings file holds the settings that must be known before the DB can be
 * opened (bibtex_folder, local_db_folder, shared_db_folder, backup_folder) plus a couple of standalone display knobs
 * (display_file, display_command) that have no DB to live in yet at the point
 * they're needed. All other settings (key_prefix, csv_delimiter, backup_folder)
 * live in the DB config table.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 18.11.2026
 *
 */

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// TBibTeXFolders is the structure of the <basename>.settings bootstrap file.
// It holds the path settings required to locate the database and backup files,
// plus a couple of standalone display knobs (see displayContent).
type TBibTeXFolders struct {
	// The four locations (step 22.1b, Erik 2026-11-18):
	// BibTeXFolder holds the operational library files ($base.bib, .files/,
	// .scripts/, .tables/, .exchange/). Default: the folder of the settings file.
	BibTeXFolder string `json:"bibtex_folder,omitempty"`
	// LocalDBFolder holds the databases the programme works on (the library DB,
	// DBLP/, ORCID/), the instance lock, and for now the maps. Machine-local.
	LocalDBFolder string `json:"local_db_folder,omitempty"`
	// SharedDBFolder holds the cloud copy: $base.sqlite3, .dump, .stamp, DBLP/.
	// reset.test writes the test library's settings itself, so they never point
	// at the production copy.
	SharedDBFolder string `json:"shared_db_folder,omitempty"`
	// BackupFolder holds the timestamped backups. Machine-local.
	BackupFolder string `json:"backup_folder,omitempty"`
	// DblpURL is where -update_dblp downloads the DBLP export from. When absent,
	// -update_dblp expects the export in the Downloads folder.
	DblpURL string `json:"dblp_url,omitempty"`
	// ServiceIdleMinutes stops the service (-start) after that many minutes
	// without a job, pushing what is not pushed yet. Absent or 0: no timeout.
	ServiceIdleMinutes int `json:"service_idle_minutes,omitempty"`
	// ServicePort is the localhost port of the service's web UI (-ui). One per
	// library, so several libraries can run side by side. Absent: 8401.
	ServicePort int `json:"service_port,omitempty"`
	// NrOfBackups is how many timestamped backups (copy and dump each) a save
	// keeps in backup_folder; older ones are removed (pruneLibraryBackups).
	// Absent: 4 (as ~/BiBTeX/clean.backups did). 0 or less: keep all.
	NrOfBackups *int `json:"nr_of_backups,omitempty"`
	// FieldURLs makes a field's value a link in the web UI (Erik, 2026-11-24):
	// field name → URL, "$" standing for the value, e.g. "repositum":
	// "https://doi.org/$". Added to (and may override) the built-in ones for doi
	// and dblp (webFieldURLs).
	// A field may also have an icon (Erik, 2026-11-27): "researchgate": {"url": "…/$",
	// "icon": "https://www.researchgate.net/favicon.ico"}; see TFieldURL.
	FieldURLs map[string]TFieldURL `json:"field_urls,omitempty"`
	// EprintPDF says per eprinttype where an eprint's PDF comes from (step 25), e.g.
	// "jstor": {"url": "https://www.jstor.org/stable/pdf/$.pdf", "browser": "Firefox",
	// "noauto": true}. Added to (and may override) the built-in ones (builtinEprintPDF).
	EprintPDF map[string]TEprintPDF `json:"eprint_pdf,omitempty"`
	// Colours are the web UI's colours (Erik, 2026-11-26), by daylight and by night
	// (the OS's dark mode): name → CSS colour; see colours.go. Written with the
	// defaults when absent.
	Colours *TColours `json:"colours,omitempty"`
	// WebBrowser opens the web UI (-ui) instead of the default browser: on macOS
	// an application name ("Firefox"), otherwise a command ("firefox"); see
	// openInBrowser.
	WebBrowser string `json:"web_browser,omitempty"`

	// Old keys (before 30.11): read once, when the new ones are absent, and
	// dropped from the file.
	LibraryFolderLegacy string `json:"library_folder,omitempty"` // → bibtex_folder
	CacheFolderLegacy   string `json:"cache_folder,omitempty"`   // → local_db_folder
	GlobalFolderLegacy  string `json:"global_folder,omitempty"`  // → local_db_folder
	HomeFolderLegacy    string `json:"home_folder,omitempty"`    // → shared_db_folder
	DblpFolderLegacy    string `json:"dblp_folder,omitempty"`    // older still → local_db_folder

	// key_prefix was here in v22.x; v23.0 moves it to the DB config table only.
	// Read and migrate on first run, but never write back.
	KeyPrefixLegacy string `json:"key_prefix,omitempty"`
	// DisplayFile is written by displayContent() with content for external, live
	// preview (e.g. a DBLP table-of-contents while resolving a child's DBLP key).
	DisplayFile string `json:"display_file,omitempty"`
	// DisplayCommand opens DisplayFile in an external, auto-refreshing viewer. Run
	// once per bib_check invocation, the first time displayContent() is called —
	// or before every write when DisplayReopen is true (the default).
	DisplayCommand string `json:"display_command,omitempty"`
	// DisplayReopen controls whether DisplayCommand re-runs on every displayContent
	// call (true — brings the viewer window to front each time there's something
	// new to look at) or only once per session (false). A pointer so "absent from
	// the file" (defaults to true) is distinguishable from "explicitly false".
	DisplayReopen *bool `json:"display_reopen,omitempty"`
}

var (
	csvDelimiter       = ";"
	keyPrefix          = ""
	localDBFolder      = ""                  // set by loadBibTeXFolders; default: the bibtex folder
	sharedDBFolder     = ""                  // set by loadBibTeXFolders; default: the bibtex folder
	backupFolder       = ""                  // set by loadBibTeXFolders; default: libraryBase()+".backups/"
	dblpURL            = ""                  // set by loadBibTeXFolders; see doUpdateDblp
	serviceIdleMinutes = 0                   // set by loadBibTeXFolders; see service.go
	nrOfBackups        = defaultNrOfBackups  // set by loadBibTeXFolders; see pruneLibraryBackups
	servicePort        = 0                   // set by loadBibTeXFolders; see web_ui.go
	fieldURLs          map[string]string     // set by loadBibTeXFolders; see webFieldLink
	fieldIcons         map[string]string     // set by loadBibTeXFolders; see webFieldIconLink
	eprintPDF          map[string]TEprintPDF // set by loadBibTeXFolders; see eprintPDFSetting
	webBrowser         = ""                  // set by loadBibTeXFolders; see openInBrowser
	displayFile        = ""                  // set by loadBibTeXFolders; see displayContent
	displayCommand     = ""                  // set by loadBibTeXFolders; see displayContent
	displayReopen      = true                // set by loadBibTeXFolders; see displayContent
)

func writeBibTeXFolders(foldersPath string, f TBibTeXFolders) {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		messagef("Warning: could not marshal folders file: %s\n", err)
		return
	}
	if err := os.WriteFile(foldersPath, append(data, '\n'), 0644); err != nil {
		messagef("Warning: could not write folders file %s: %s\n", foldersPath, err)
	}
}

// keyPrefixArg is -key_prefix: the key prefix of a new library.
var keyPrefixArg string

// newKeyPrefix is the key prefix given for a library that has none yet (-key_prefix
// or key_prefix in its settings file, two upper-case letters); the run ends when
// there is none (no question: ARCHITECTURE.md §15.7).
func newKeyPrefix() string {
	s := strings.ToUpper(keyPrefixArg)
	if len(s) == 2 && s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z' {
		return s
	}
	messageln("This library has no key prefix yet. Give one of two letters: -key_prefix EP")
	endRun(1)
	return ""
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + path[1:]
		}
	}
	return path
}

// loadBibTeXFolders reads <basename>.settings (bootstrap + key_prefix + display).
// On first run after upgrading from a pre-29.26 binary, it reads the legacy
// <basename>.folders file instead (left in place, untouched); if that is also
// absent, it falls back further to the legacy <basename>.config file (pre-13.3b),
// copying all non-bootstrap fields into memory for maybeBootstrapConfigFromFile()
// to persist to the DB. Either way, it then writes the new .settings file.
func loadBibTeXFolders(settingsPath string) {
	var f TBibTeXFolders
	needsWrite := false

	data, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		// .settings absent — try legacy .folders for one-time migration.
		foldersPath := strings.TrimSuffix(settingsPath, SettingsFileExtension) + FoldersFileExtension
		if legacyData, legacyErr := os.ReadFile(foldersPath); legacyErr == nil && json.Unmarshal(legacyData, &f) == nil {
			messagef("Migrating %s.folders → %s.settings\n",
				strings.TrimSuffix(settingsPath, SettingsFileExtension),
				strings.TrimSuffix(settingsPath, SettingsFileExtension))
		} else {
			// .folders absent too — try legacy .config for one-time migration.
			configPath := strings.TrimSuffix(settingsPath, SettingsFileExtension) + ConfigFileExtension
			legacyData, legacyErr := os.ReadFile(configPath)
			if legacyErr == nil {
				var legacy struct {
					CSVDelimiter     string `json:"csv_delimiter"`
					KeyPrefix        string `json:"key_prefix"`
					GlobalFolder     string `json:"global_folder"`
					DblpFolderLegacy string `json:"dblp_folder,omitempty"`
					BackupFolder     string `json:"backup_folder"`
					CacheFolder      string `json:"cache_folder"`
				}
				if json.Unmarshal(legacyData, &legacy) == nil {
					// Carry non-bootstrap fields into memory so maybeBootstrapConfigFromFile
					// can write them to the DB config table on the first open.
					if legacy.CSVDelimiter != "" {
						csvDelimiter = legacy.CSVDelimiter
					}
					if legacy.BackupFolder != "" {
						backupFolder = expandHome(legacy.BackupFolder)
						if !strings.HasSuffix(backupFolder, "/") {
							backupFolder += "/"
						}
					}
					f.GlobalFolderLegacy = legacy.GlobalFolder
					f.DblpFolderLegacy = legacy.DblpFolderLegacy
					f.CacheFolderLegacy = legacy.CacheFolder
					f.KeyPrefixLegacy = legacy.KeyPrefix
					messagef("Migrating %s.config → %s.settings\n",
						strings.TrimSuffix(settingsPath, SettingsFileExtension),
						strings.TrimSuffix(settingsPath, SettingsFileExtension))
				}
			}
		}
		needsWrite = true
	} else if err := json.Unmarshal(data, &f); err != nil {
		messagef("Warning: could not parse settings file %s: %s\n", settingsPath, err)
		needsWrite = true
	}

	// Old keys → new names (30.11). The local DB folder was split over
	// cache_folder (library DB) and global_folder (DBLP, ORCID, maps); one folder now.
	migrate := func(newKey *string, old ...*string) {
		for _, o := range old {
			if *newKey == "" && *o != "" {
				*newKey = *o
			}
			if *o != "" {
				*o = "" // the old key is dropped from the file
				needsWrite = true
			}
		}
	}
	migrate(&f.BibTeXFolder, &f.LibraryFolderLegacy)
	migrate(&f.LocalDBFolder, &f.CacheFolderLegacy, &f.GlobalFolderLegacy, &f.DblpFolderLegacy)
	migrate(&f.SharedDBFolder, &f.HomeFolderLegacy)
	f.DblpFolderLegacy = ""

	// Migrate key_prefix from folders to DB (v23.0): carry value into memory so
	// maybeBootstrapConfigFromFile can persist it to the config table, then strip
	// it from the struct so it is no longer written to the .settings file.
	if f.KeyPrefixLegacy != "" {
		keyPrefix = f.KeyPrefixLegacy
		f.KeyPrefixLegacy = ""
		needsWrite = true
	}
	// All path keys are written to the file, filled in with their defaults, so
	// every location can be seen in one place (Erik, 2026-11-18). The library
	// folder comes first: the other defaults derive from it.
	settingsFolder := withSlash(filepath.Dir(settingsPath))
	fill := func(key *string, def string) string {
		if *key == "" {
			*key = tildePath(def)
			needsWrite = true
		}
		return withSlash(expandHome(*key))
	}
	bibTeXFolder = fill(&f.BibTeXFolder, settingsFolder)
	localDBFolder = fill(&f.LocalDBFolder, bibTeXFolder)
	sharedDBFolder = fill(&f.SharedDBFolder, bibTeXFolder)
	if f.BackupFolder == "" && backupFolder != "" {
		f.BackupFolder = tildePath(backupFolder) // from a legacy .config
	}
	backupFolder = fill(&f.BackupFolder, libraryBase()+".backups/")
	dblpURL = f.DblpURL
	serviceIdleMinutes = f.ServiceIdleMinutes
	servicePort = f.ServicePort
	fieldURLs, fieldIcons = map[string]string{}, map[string]string{}
	for field, u := range f.FieldURLs {
		fieldURLs[field] = u.URL
		if u.Icon != "" {
			fieldIcons[field] = u.Icon
		}
	}
	eprintPDF = f.EprintPDF
	if f.Colours == nil {
		f.Colours = defaultColours()
		needsWrite = true
	}
	webColours = *f.Colours
	webBrowser = f.WebBrowser
	nrOfBackups = defaultNrOfBackups
	if f.NrOfBackups != nil {
		nrOfBackups = *f.NrOfBackups
	}
	if f.DisplayFile != "" {
		displayFile = expandHome(f.DisplayFile)
	}
	displayCommand = f.DisplayCommand
	if f.DisplayReopen != nil {
		displayReopen = *f.DisplayReopen
	}

	// key_prefix is no longer stored in .settings. If it is still absent after
	// reading the legacy field, it will be prompted and written to the DB config
	// table by loadBibTeXSettings() once the DB is open.

	if needsWrite {
		writeBibTeXFolders(settingsPath, f)
	}
}
