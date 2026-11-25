package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOlderVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"30.9", "30.160", true}, {"30.160", "30.9", false}, {"30.160", "30.160", false}, {"30", "30.1", true},
		{"30.1", "30", false}, {"29.999", "30.0", true}, {"", "30.1", true}, {"31.0", "30.200", false},
	} {
		if got := olderVersion(c.a, c.b); got != c.want {
			t.Errorf("olderVersion(%q, %q) = %v", c.a, c.b, got)
		}
	}
	if tooOldFor("") != "" || tooOldFor(AppVersion) != "" || tooOldFor("1.0") != "" {
		t.Error("refused a version that may use it")
	}
	if tooOldFor("9999.1") == "" {
		t.Error("an older program is not refused")
	}
}

func TestRequiredVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.sqlite3")
	if requiredVersionOf(path) != "" {
		t.Error("no database")
	}
	conn, err := sql.Open(sqliteDatabaseDriver, "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`PRAGMA journal_mode=WAL`)
	exec(`CREATE TABLE other (x TEXT)`)
	if requiredVersion(conn) != "" {
		t.Error("no config table")
	}
	exec(`CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL)`)
	old := db
	db = conn
	defer func() { db = old }()
	requireVersion("30.200")
	requireVersion("30.90") // never lowers
	if got := requiredVersion(conn); got != "30.200" {
		t.Errorf("%q", got)
	}
	conn.Close()
	if got := requiredVersionOf(path); got != "30.200" { // closed, in WAL mode
		t.Errorf("closed: %q", got)
	}
}
