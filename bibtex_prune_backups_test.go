package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestPruneLibraryBackups(t *testing.T) {
	dir := t.TempDir() + "/"
	savedFolder, savedBase, savedN := backupFolder, bibTeXBaseName, nrOfBackups
	defer func() { backupFolder, bibTeXBaseName, nrOfBackups = savedFolder, savedBase, savedN }()
	backupFolder, bibTeXBaseName, nrOfBackups = dir, "ErikProper", 2

	for _, name := range []string{
		"ErikProper_20261123_100000.sqlite3", "ErikProper_20261123_110000.sqlite3", "ErikProper_20261123_120000.sqlite3",
		"ErikProper_20261123_100000.dump", "ErikProper_20261123_120000.dump",
		"ErikProper_20261111_pre_organization_fix.sqlite3", // made by hand: kept
		"Dev_ErikProper_20261101_100000.sqlite3",            // another library: kept
	} {
		os.WriteFile(filepath.Join(dir, name), nil, 0o644)
	}
	pruneLibraryBackups()

	entries, _ := os.ReadDir(dir)
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	want := []string{
		"Dev_ErikProper_20261101_100000.sqlite3",
		"ErikProper_20261111_pre_organization_fix.sqlite3",
		"ErikProper_20261123_100000.dump", "ErikProper_20261123_110000.sqlite3",
		"ErikProper_20261123_120000.dump", "ErikProper_20261123_120000.sqlite3",
	}
	sort.Strings(want)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v\nwant %v", got, want)
	}
}
