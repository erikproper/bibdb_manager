package main

import (
	"database/sql"
	"testing"
)

// identityFixture sets up a small library: contributors, their positions in one
// entry, and stub reference lookups. The cases are the ones met on 2026-11-19.
func identityFixture(t *testing.T) {
	t.Helper()
	var err error
	db, err = sql.Open(sqliteDatabaseDriver, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, s := range []string{
		`CREATE TABLE contributor_roles (entry_key TEXT, role TEXT, position INTEGER, contributor_id TEXT,
		   PRIMARY KEY (entry_key, role, position))`,
		`CREATE TABLE entry_contributor_names (entry_key TEXT, role TEXT, position INTEGER, contributor_id TEXT,
		   name_used TEXT, orcid_used TEXT, dblp_key_used TEXT, PRIMARY KEY (entry_key, role, position))`,
		`CREATE TABLE contributor_names (id TEXT, name TEXT, PRIMARY KEY (id, name))`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	activeTx = nil

	Library.ContributorByID = map[string]*TContributor{
		"grace":  {Name: "Grace Hui Yang", ORCID: "0000-0001-6095-8358", DblpKey: "homepages/04/999-1"},
		"huiY":   {Name: "Yang, Hui", ORCID: "0000-0002-7410-4617", DblpKey: "homepages/04/999-25"},
		"anton":  {Name: "Agafonov, Anton", ORCID: "0000-0001-7483-7936", DblpKey: "homepages/172/3826"},
		"lin42":  {Name: "Zhang, Lin", ORCID: "0009-0008-7651-4127", DblpKey: "homepages/37/1629-42"},
		"lin9":   {Name: "Lin Zhang", ORCID: "0000-0003-1989-6102", DblpKey: "homepages/37/1629-9"},
		"rsant":  {Name: "Santos, Ricardo", DblpKey: "homepages/85/2621-2"},
		"erik":   {Name: "Proper, Henderik A.", ORCID: "0000-0002-7318-2496"},
		"single": {Name: "Wahab, Abdul"},
	}
	Library.ORCIDToContributorID = map[string]string{}
	Library.DblpKeyToContributorID = map[string]string{}
	for id, c := range Library.ContributorByID {
		if c.ORCID != "" {
			Library.ORCIDToContributorID[c.ORCID] = id
		}
		if c.DblpKey != "" {
			Library.DblpKeyToContributorID[c.DblpKey] = id
		}
	}
	Library.NameToContributorID = map[string]string{"Wahab, Abdul": "single", "Abdul Wahab": "single",
		"Proper, Henderik A.": "erik"}
	Library.NameAliasToName = map[string]string{}
	Library.AmbiguousNameToContributorIDs = map[string][]string{
		"Yang, Hui": {"grace", "huiY"}, "Hui Yang": {"grace", "huiY"},
		"Zhang, Lin": {"lin42", "lin9"}, "Lin Zhang": {"lin42", "lin9"},
	}

	refPrimaryORCID = func(o string) string {
		if o == "0000-0002-0000-0001" { // a deprecated iD of Erik's
			return "0000-0002-7318-2496"
		}
		return ""
	}
	refPersonForName = func(n string) string {
		switch n {
		case "Hui Yang 0001":
			return "homepages/04/999-1"
		case "Anton Agafonov":
			return "homepages/172/3826"
		}
		return "" // "Lin Zhang", "Hui Yang": shared by several DBLP persons
	}
	refPersonForORCID = func(o string) string {
		if o == "0009-0005-8749-9581" { // the third Lin Zhang
			return "homepages/37/1629-77"
		}
		return ""
	}
	refORCIDWorkDOIs = func(string) ([]string, bool) { return nil, false }
	refContributorDOIs = func(string, string) []string { return nil }
	refPersonORCIDs = func(key string) []string {
		if key == "homepages/172/3826" {
			return []string{"0000-0001-7483-7936"}
		}
		return nil
	}
}

func placeAt(t *testing.T, entry, role string, pos int, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO contributor_roles VALUES (?, ?, ?, ?)`, entry, role, pos, id); err != nil {
		t.Fatal(err)
	}
}

func TestDecideContributor(t *testing.T) {
	identityFixture(t)
	placeAt(t, "sigir", "author", 3, "huiY")  // wrongly placed homonym
	placeAt(t, "rec", "author", 1, "anton")   // Anton Agafonov's paper
	placeAt(t, "bibm", "author", 1, "lin42")  // a third Lin Zhang's paper
	placeAt(t, "lrec", "author", 2, "rsant")  // Santos, Ricardo
	placeAt(t, "amb", "author", 1, "lin9")    // no evidence beyond the name
	placeAt(t, "uniq", "author", 1, "single") // name form of a unique contributor
	db.Exec(`INSERT INTO entry_contributor_names VALUES ('rec2', 'author', 1, 'erik', 'Henderik A. Proper', NULL, NULL)`)
	placeAt(t, "rec2", "author", 1, "lin9")

	cases := []struct {
		label string
		ev    TPositionEvidence
		want  TPositionDecision
	}{
		{"ORCID decides (Grace Hui Yang)",
			TPositionEvidence{"sigir", "author", 3, "Hui Yang", "Hui Yang 0001", "0000-0001-6095-8358"},
			TPositionDecision{ContributorID: "grace", Rule: identityByORCID, DblpKey: "homepages/04/999-1"}},
		{"deprecated ORCID follows its primary",
			TPositionEvidence{"x", "author", 1, "Henderik A. Proper", "", "0000-0002-0000-0001"},
			TPositionDecision{ContributorID: "erik", Rule: identityByORCID}},
		{"numbered DBLP name decides without ORCID",
			TPositionEvidence{"sigir", "author", 3, "Hui Yang", "Hui Yang 0001", ""},
			TPositionDecision{ContributorID: "grace", Rule: identityByDblpPerson, DblpKey: "homepages/04/999-1"}},
		{"second ORCID of a unique DBLP person (Anton Agafonov)",
			TPositionEvidence{"rec", "author", 1, "Anton Agafonov", "Anton Agafonov", "0000-0002-7960-8929"},
			TPositionDecision{ContributorID: "anton", Rule: identityByDblpPerson, DblpKey: "homepages/172/3826", AddORCID: true}},
		{"unknown ORCID, shared name, other ORCID in place: a new person (Lin Zhang)",
			TPositionEvidence{"bibm", "author", 1, "Lin Zhang", "Lin Zhang", "0009-0005-8749-9581"},
			TPositionDecision{ContributorID: "lin42", Rule: identityInPlace, DblpKey: "homepages/37/1629-77", NewPerson: true}},
		{"unknown ORCID next to the one in place, nothing known: keep, no ORCID (Henrik Leopold)",
			TPositionEvidence{"bibm", "author", 1, "Lin Zhang", "Lin Zhang", "0000-0003-4862-1829"},
			TPositionDecision{ContributorID: "lin42", Rule: identityInPlace}},
		{"other name order keeps the contributor in place (Santos, Ricardo)",
			TPositionEvidence{"lrec", "author", 2, "Ricardo Santos", "Ricardo Santos", ""},
			TPositionDecision{ContributorID: "rsant", Rule: identityInPlace}},
		{"the entry's own record",
			TPositionEvidence{"rec2", "author", 1, "Proper, Henderik A.", "", ""},
			TPositionDecision{ContributorID: "erik", Rule: identityByEntryRecord}},
		{"a unique name",
			TPositionEvidence{"new", "author", 1, "Abdul Wahab", "", ""},
			TPositionDecision{ContributorID: "single", Rule: identityByUniqueName}},
		{"shared name, nothing in place: ambiguous, nobody",
			TPositionEvidence{"none", "author", 1, "Lin Zhang", "Lin Zhang", ""},
			TPositionDecision{Rule: identityAmbiguous}},
	}
	for _, c := range cases {
		if got := Library.decideContributor(c.ev); got != c.want {
			t.Errorf("%s:\n got  %+v\n want %+v", c.label, got, c.want)
		}
	}
}

func TestStoreDecisionIsUsedNextTime(t *testing.T) {
	identityFixture(t)
	placeAt(t, "e", "author", 1, "lin9")
	ev := TPositionEvidence{"e", "author", 1, "Lin Zhang", "Lin Zhang", ""}
	// Decided once (here: kept in place), stored; the next run finds the record (rule 3).
	d := Library.decideContributor(ev)
	storeDecision(ev, d)
	db.Exec(`UPDATE contributor_roles SET contributor_id = 'lin42'`) // something moved it meanwhile
	if got := Library.decideContributor(ev); got.ContributorID != "lin9" || got.Rule != identityByEntryRecord {
		t.Errorf("stored decision not used: %+v", got)
	}
}

func TestPositionEvidence(t *testing.T) {
	identityFixture(t)
	Library.DblpSourceKey = "e"
	Library.DblpSourcePersons = map[string][]TDblpJSONPerson{"author": {
		{Name: "Hui Yang 0001", ORCID: "0000-0001-6095-8358"},
		{Name: "Ricardo Santos"},
		{Name: "Someone Else", ORCID: "0000-0003-0000-0000"},
	}}
	defer func() { Library.DblpSourcePersons = nil }()

	if ev := Library.positionEvidence("e", "author", 1, 3, "Yang, Hui"); ev.DblpName != "Hui Yang 0001" || ev.ORCID != "0000-0001-6095-8358" {
		t.Errorf("other order should take DBLP's person: %+v", ev)
	}
	if ev := Library.positionEvidence("e", "author", 2, 3, "Ricardo Santos"); ev.DblpName != "Ricardo Santos" {
		t.Errorf("same name should take DBLP's person: %+v", ev)
	}
	if ev := Library.positionEvidence("e", "author", 3, 3, "Wahab, Abdul"); ev.DblpName != "" || ev.ORCID != "" {
		t.Errorf("a name DBLP does not have there must not take its ORCID: %+v", ev)
	}
	if ev := Library.positionEvidence("e", "author", 1, 2, "Yang, Hui"); ev.DblpName != "" {
		t.Errorf("a different number of names must not take DBLP's persons: %+v", ev)
	}
	if ev := Library.positionEvidence("parent", "author", 1, 3, "Yang, Hui"); ev.DblpName != "" {
		t.Errorf("another entry must not take DBLP's persons: %+v", ev)
	}
}

func TestGiveContributorNameFormSharedName(t *testing.T) {
	identityFixture(t)
	// "Zhang, Lin" is shared by lin42 and lin9: a form for lin42 must go to lin42, not
	// to a new contributor named "Zhang, Lin" (as AddNameForm does).
	before := len(Library.ContributorByID)
	Library.giveContributorNameForm("lin42", "Zhang, L.")
	if len(Library.ContributorByID) != before {
		t.Errorf("a new contributor was created")
	}
	if id := Library.NameToContributorID["Zhang, L."]; id != "lin42" {
		t.Errorf("form tied to %q, want lin42", id)
	}
	var id string
	db.QueryRow(`SELECT id FROM contributor_names WHERE name = 'Zhang, L.'`).Scan(&id)
	if id != "lin42" {
		t.Errorf("stored for %q, want lin42", id)
	}
}

func TestUpsertNameFormSharedName(t *testing.T) {
	identityFixture(t)
	// "Zhang, Lin" is shared: an alias for it must not create a contributor named so.
	before := len(Library.ContributorByID)
	upsertNameForm("zhang, lin", "Zhang, Lin")
	if len(Library.ContributorByID) != before {
		t.Errorf("a new contributor was created for a shared name")
	}
}

// TestORCIDWorksFit: an ORCID is not given to a contributor without one when ORCID's
// works list misses all of the contributor's DOIs (Linda J. and Linda E. Campbell,
// whom DBLP conflates under one person with Linda E.'s ORCID).
func TestORCIDWorksFit(t *testing.T) {
	identityFixture(t)
	placeAt(t, "ijhci", "author", 1, "rsant")
	ev := TPositionEvidence{"ijhci", "author", 1, "Santos, Ricardo", "", "0000-0002-8872-9626"}
	own := []string{"10.1016/0169-023X(96)00005-5"}
	refContributorDOIs = func(id, except string) []string { return own }

	cases := []struct {
		label string
		works []string
		known bool
		own   []string
		want  TPositionDecision
	}{
		{"works miss every DOI of the contributor: a new person",
			[]string{"10.1080/10447318.2025.2474491"}, true, own,
			TPositionDecision{ContributorID: "rsant", Rule: identityInPlace, NewPerson: true}},
		{"works list one of its DOIs (other spelling): the same person",
			[]string{"https://doi.org/10.1016/0169-023x(96)00005-5"}, true, own,
			TPositionDecision{ContributorID: "rsant", Rule: identityInPlace, AddORCID: true}},
		{"works not known: as before",
			nil, false, own,
			TPositionDecision{ContributorID: "rsant", Rule: identityInPlace, AddORCID: true}},
		{"works without DOIs: nothing to compare",
			nil, true, own,
			TPositionDecision{ContributorID: "rsant", Rule: identityInPlace, AddORCID: true}},
		{"contributor without DOIs: nothing to compare",
			[]string{"10.1080/10447318.2025.2474491"}, true, nil,
			TPositionDecision{ContributorID: "rsant", Rule: identityInPlace, AddORCID: true}},
	}
	for _, c := range cases {
		works, known, own := c.works, c.known, c.own
		refORCIDWorkDOIs = func(string) ([]string, bool) { return works, known }
		refContributorDOIs = func(string, string) []string { return own }
		if got := Library.decideContributor(ev); got != c.want {
			t.Errorf("%s:\n got  %+v\n want %+v", c.label, got, c.want)
		}
	}
}
