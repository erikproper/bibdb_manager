package main

import (
	"database/sql"
	"testing"
)

// TestLinkDecidedDblpKeysSkipsConflation: a DBLP key whose positions were decided for
// two contributors (Linda J. Campbell's DKE paper and the ultrasound paper's other
// Linda J. Campbell, both on DBLP 71/4640) is linked to neither; a key only one
// contributor's positions name is linked.
func TestLinkDecidedDblpKeysSkipsConflation(t *testing.T) {
	var err error
	db, err = sql.Open(sqliteDatabaseDriver, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	activeTx = nil
	for _, s := range []string{
		`CREATE TABLE contributors (id TEXT PRIMARY KEY, name TEXT, orcid TEXT, dblp_key TEXT, garbled INTEGER)`,
		`CREATE TABLE entry_contributor_names (entry_key TEXT, role TEXT, position INTEGER, contributor_id TEXT,
		   name_used TEXT, orcid_used TEXT, dblp_key_used TEXT, PRIMARY KEY (entry_key, role, position))`,
		`INSERT INTO contributors (id, name, dblp_key) VALUES ('lindaJ', 'Campbell, Linda J.', 'homepages/37/1433'),
		   ('lindaU', 'Campbell, Linda J.', NULL), ('solo', 'Mills, Gregory R.', NULL)`,
		`INSERT INTO entry_contributor_names VALUES
		   ('dke', 'author', 1, 'lindaJ', 'Campbell, Linda J.', NULL, 'homepages/71/4640'),
		   ('us', 'author', 3, 'lindaU', 'Campbell, Linda J.', NULL, 'homepages/71/4640'),
		   ('us', 'author', 4, 'solo', 'Mills, Gregory R.', NULL, 'homepages/87/5089')`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	Library.ContributorByID = map[string]*TContributor{
		"lindaJ": {Name: "Campbell, Linda J.", DblpKey: "homepages/37/1433"},
		"lindaU": {Name: "Campbell, Linda J."},
		"solo":   {Name: "Mills, Gregory R."},
	}
	Library.DblpKeyToContributorID = map[string]string{"homepages/37/1433": "lindaJ"}

	if n := linkDecidedDblpKeys(); n != 1 {
		t.Errorf("linked %d, want 1", n)
	}
	if k := Library.ContributorByID["lindaU"].DblpKey; k != "" {
		t.Errorf("lindaU got DBLP key %s, want none", k)
	}
	if k := Library.ContributorByID["solo"].DblpKey; k != "homepages/87/5089" {
		t.Errorf("solo got DBLP key %q, want homepages/87/5089", k)
	}
}
