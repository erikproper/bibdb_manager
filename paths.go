/*
 *
 * Module:    bibtex_check
 * Component:
 * - paths
 *
 * This module provides definitions of file paths and names
 *
 * Creator: Henderik A. Proper (erikproper@gmail.com)
 *
 * Version of: 18.11.2026
 *
 */

package main

import (
	"os"
	"strings"
)

var (
	// bibTeXFolder is the library folder (library_folder in $base.settings; by
	// default the folder of the settings file). It holds $base.bib, $base.files/,
	// $base.scripts/, $base.tables/, $base.exchange/ and $base.lock.
	bibTeXFolder   string
	bibTeXBaseName string
)

// libraryBase returns the library folder plus the base name; the library's own
// files and folders are this plus an extension (".bib", ".tables", ...).
func libraryBase() string {
	return bibTeXFolder + bibTeXBaseName
}

// withSlash returns folder with a trailing slash.
func withSlash(folder string) string {
	if folder != "" && !strings.HasSuffix(folder, "/") {
		return folder + "/"
	}
	return folder
}

// tildePath writes a path below the home folder as "~/...", so a settings file
// shared between machines with different user names stays valid on each.
func tildePath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}
	return path
}

// stripKnownBaseExtension removes a trailing known library extension from path
// so accidental tab-completion (e.g. "-base foo.bib") still works.
func stripKnownBaseExtension(path string) string {
	for _, ext := range []string{BibFileExtension, cacheFileExtension, ".sqlite3", ConfigFileExtension, LockFileExtension, SettingsFileExtension} {
		if strings.HasSuffix(path, ext) {
			return path[:len(path)-len(ext)]
		}
	}
	return path
}
