package main

import (
	"database/sql"
	"os"
	"testing"
	"time"
)

// writeDblpMetaStore writes a store at path holding only a meta table naming xml.
func writeDblpMetaStore(t *testing.T, path, xml string) {
	t.Helper()
	db, err := sql.Open(sqliteDatabaseDriver, "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{`CREATE TABLE meta (name TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO meta VALUES ('xml_file', '` + xml + `')`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDblpReaderSeesSwappedStore: a store swapped in by another process (a job run
// as the service's child) is read once the recheck interval has passed.
func TestDblpReaderSeesSwappedStore(t *testing.T) {
	saved := localDBFolder
	localDBFolder = t.TempDir() + "/"
	t.Cleanup(func() { closeDblpReader(); localDBFolder = saved })
	closeDblpReader()
	if err := os.MkdirAll(dblpFolder(), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDblpMetaStore(t, dblpDBPath(), "dblp-old.xml.gz")
	if got := readDblpCurrentXML(); got != "dblp-old.xml.gz" {
		t.Fatalf("before the swap: %q", got)
	}
	writeDblpMetaStore(t, dblpDBNewPath(), "dblp-new.xml.gz")
	if err := os.Rename(dblpDBNewPath(), dblpDBPath()); err != nil {
		t.Fatal(err)
	}
	dblpReader.Lock()
	dblpReader.checked = time.Time{} // as if the interval had passed
	dblpReader.Unlock()
	if got := readDblpCurrentXML(); got != "dblp-new.xml.gz" {
		t.Fatalf("after the swap: %q", got)
	}
}
