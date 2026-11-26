/*
 *
 * Module:    bibdb_manager
 * Component:
 * - contributor identity per position
 *
 * Plan step 23.2 (ARCHITECTURE.md 7.6): one decision of which contributor sits at an
 * author or editor position of an entry, from the evidence in a fixed order (ORCID,
 * DBLP person, the entry's own record, the contributor in place, a unique name), so
 * that the DBLP merge, the ORCID step, the role cross-check, the absorb step and the
 * spurious merges no longer each decide, and undo each other.
 *
 * Step 23.2.1: the decision and its storage. Step 23.2.2: the contributor roles of
 * an entry are written from the decision (upsertContributorRolesForField), with DBLP's
 * persons as evidence during a DBLP merge (applyDblpIdentity replaces the ORCID step).
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 22.11.2026
 *
 */

package main

import (
	"database/sql"
	"slices"
	"strings"
	"sync"
	"time"
)

// TPositionEvidence is what a source says about one author/editor position.
type TPositionEvidence struct {
	EntryKey string
	Role     string // "author" or "editor"
	Position int    // 1-based
	Name     string // the name as the source gives it (LaTeX, DBLP number stripped)
	DblpName string // DBLP's raw name with its number ("Hui Yang 0001"); "" when not from DBLP
	ORCID    string // the ORCID the source gives at this position; may be empty
}

// The rules of decideContributor, in the order they are tried.
const (
	identityByORCID       = 1 // the ORCID at the position → the contributor holding it
	identityByDblpPerson  = 2 // the DBLP person → the contributor with that dblp_key
	identityByEntryRecord = 3 // the entry's own record (entry_contributor_names)
	identityInPlace       = 4 // the contributor already in place, when the names fit
	identityByUniqueName  = 5 // a name only one contributor has
	identityAmbiguous     = 6 // none of these: kept what was there (or nothing), reported
)

// TPositionDecision is the outcome of decideContributor.
type TPositionDecision struct {
	ContributorID string // "" when no contributor fits (the caller creates one)
	Rule          int    // identityBy…
	DblpKey       string // the DBLP person, when known
	// AddORCID: the source's ORCID belongs to ContributorID too (a second ORCID: DBLP
	// lists it for that person, or the person is the only one with that name).
	AddORCID bool
	// NewPerson: the source's ORCID belongs to nobody we know, and the contributor the
	// other evidence points at has another ORCID: a different person with the same
	// name (ORCID wins). The caller creates a contributor with Evidence.ORCID.
	NewPerson bool
}

// The reference database lookups the decision uses; variables so a test can replace
// them. All return "" / nil when the reference database has no answer.
var (
	// refPrimaryORCID: the primary iD when ORCID deprecated orcid into another.
	refPrimaryORCID = func(orcid string) string { return readPrimaryORCID(orcid) }
	// refPersonForName: the DBLP person for a numbered DBLP name, or for a name only one
	// DBLP person has.
	refPersonForName = func(name string) string { return readDblpPersonForName(name) }
	// refPersonORCIDs: every ORCID a DBLP person lists.
	refPersonORCIDs = func(key string) []string { return readDblpPersonORCIDs(key) }
	// refPersonForORCID: the DBLP person listing orcid, when only one does.
	refPersonForORCID = func(orcid string) string { return readDblpPersonForORCID(orcid) }
	// refORCIDWorkDOIs: the DOIs of an ORCID's works; known is false when the works
	// list is not stored and cannot be fetched now.
	refORCIDWorkDOIs = func(orcid string) (dois []string, known bool) { return orcidWorkDOIs(orcid) }
	// refContributorDOIs: the DOIs of the entries of a contributor, except one entry.
	refContributorDOIs = func(id, exceptEntry string) []string { return contributorDOIs(id, exceptEntry) }
)

// decideContributor decides which contributor sits at the position ev describes.
func (l *TBibTeXLibrary) decideContributor(ev TPositionEvidence) TPositionDecision {
	inPlace := contributorInPlace(ev.EntryKey, ev.Role, ev.Position)

	// The DBLP person, from DBLP's (numbered) name; the reference database keys
	// persons by DBLP's own name forms, not by our LaTeX ones.
	personKey := ""
	if ev.DblpName != "" {
		personKey = refPersonForName(ev.DblpName)
	}

	// 1. ORCID (following ORCID's own merge of duplicates).
	if ev.ORCID != "" {
		orcid := ev.ORCID
		if primary := refPrimaryORCID(orcid); primary != "" {
			orcid = primary
		}
		for _, o := range []string{orcid, ev.ORCID} {
			if id := l.ORCIDToContributorID[o]; l.ContributorByID[id] != nil {
				return TPositionDecision{ContributorID: id, Rule: identityByORCID, DblpKey: personKey}
			}
		}
	}

	// 2. DBLP person.
	if personKey != "" {
		if id := l.DblpKeyToContributorID[personKey]; l.ContributorByID[id] != nil {
			d := TPositionDecision{ContributorID: id, Rule: identityByDblpPerson, DblpKey: personKey}
			return l.withSourceORCID(d, ev, personKey)
		}
	}

	// 3. The entry's own record.
	if id, ok := entryRecordFor(ev.EntryKey, ev.Role, ev.Position, ev.Name); ok && l.ContributorByID[id] != nil {
		return l.withSourceORCID(TPositionDecision{ContributorID: id, Rule: identityByEntryRecord, DblpKey: personKey}, ev, personKey)
	}

	// 4. The contributor in place, when the names fit.
	if c := l.ContributorByID[inPlace]; c != nil && l.nameFitsContributor(ev.Name, inPlace) {
		return l.withSourceORCID(TPositionDecision{ContributorID: inPlace, Rule: identityInPlace, DblpKey: personKey}, ev, personKey)
	}

	// 5. A name only one contributor has.
	if _, shared := l.AmbiguousNameToContributorIDs[ev.Name]; !shared {
		if id, ok := resolveNameToContributorID(l, ev.Name); ok && l.ContributorByID[id] != nil {
			return l.withSourceORCID(TPositionDecision{ContributorID: id, Rule: identityByUniqueName, DblpKey: personKey}, ev, personKey)
		}
	}

	// 6. Keep what is there; report.
	d := TPositionDecision{Rule: identityAmbiguous, DblpKey: personKey}
	if l.ContributorByID[inPlace] != nil {
		d.ContributorID = inPlace
	}
	if ev.ORCID != "" && d.ContributorID == "" {
		d.NewPerson = true
	}
	return d
}

// withSourceORCID settles a source ORCID that no contributor holds (rule 1 did not
// apply) against the contributor c the other evidence found, on DBLP's evidence:
//   - the same person with a second ORCID: c has no ORCID and nothing ties the ORCID
//     to another DBLP person, or DBLP ties the ORCID (or the name, as that person's
//     alone) to c's DBLP person;
//   - a different person with the same name (ORCID wins): DBLP ties the ORCID, or the
//     position, to another DBLP person than c's;
//   - otherwise nothing is known: c stays, without the ORCID (an ORCID next to one
//     c already has is no evidence of either; 30.25's first test made ~1,000 false
//     new persons out of that).
func (l *TBibTeXLibrary) withSourceORCID(d TPositionDecision, ev TPositionEvidence, personKey string) TPositionDecision {
	if ev.ORCID == "" {
		return d
	}
	c := l.ContributorByID[d.ContributorID]
	orcidPerson := refPersonForORCID(ev.ORCID)
	switch {
	case c.ORCID == "" && (orcidPerson == "" || c.DblpKey == "" || orcidPerson == c.DblpKey):
		d.AddORCID = true // nothing contradicts it
	case c.DblpKey != "" && orcidPerson == c.DblpKey:
		d.AddORCID = true // DBLP lists the ORCID for this person
	case personKey != "" && personKey == c.DblpKey &&
		(slices.Contains(refPersonORCIDs(personKey), ev.ORCID) || refPersonForName(dblpPlainName(ev.DblpName)) == personKey):
		d.AddORCID = true // DBLP ties the position, by a name that is this person's alone
	case c.DblpKey != "" && orcidPerson != "":
		d.NewPerson = true // DBLP lists the ORCID for another person
		d.DblpKey = orcidPerson
	case c.DblpKey != "" && personKey != "" && personKey != c.DblpKey:
		d.NewPerson = true // the position is another DBLP person
	}
	if d.AddORCID && c.ORCID == "" && !orcidWorksFit(ev.ORCID, d.ContributorID, ev.EntryKey) {
		d.AddORCID, d.NewPerson = false, true // ORCID's works are another person's (ORCID wins)
	}
	return d
}

// orcidWorksFit reports whether orcid can be the ORCID of contributor id, by ORCID's
// works list: it does not fit when the list has DOIs, id's other entries have DOIs,
// and none of these is in the list. With nothing to compare, it fits. (DBLP can
// conflate two persons and list the ORCID of one for both: Linda J. and Linda E.
// Campbell, 2026-11-21.)
func orcidWorksFit(orcid, id, exceptEntry string) bool {
	own := refContributorDOIs(id, exceptEntry)
	if len(own) == 0 {
		return true
	}
	if primary := refPrimaryORCID(orcid); primary != "" {
		orcid = primary
	}
	works, known := refORCIDWorkDOIs(orcid)
	if !known || len(works) == 0 {
		return true
	}
	listed := make(map[string]bool, len(works))
	for _, doi := range works {
		listed[normalisedDOI(doi)] = true
	}
	for _, doi := range own {
		if listed[normalisedDOI(doi)] {
			return true
		}
	}
	return false
}

// normalisedDOI is doi in lower case, without a doi.org prefix.
func normalisedDOI(doi string) string {
	doi = strings.ToLower(strings.TrimSpace(doi))
	for _, prefix := range []string{"https://doi.org/", "http://doi.org/", "https://dx.doi.org/", "http://dx.doi.org/", "doi:"} {
		doi = strings.TrimPrefix(doi, prefix)
	}
	return doi
}

// contributorDOIs returns the DOIs of the entries contributor id has a role in,
// except exceptEntry.
func contributorDOIs(id, exceptEntry string) []string {
	rows, err := bibQuery(`SELECT DISTINCT entry_key FROM contributor_roles WHERE contributor_id = ? AND entry_key <> ?`, id, exceptEntry)
	if err != nil {
		return nil
	}
	var keys []string
	for rows.Next() {
		var key string
		if rows.Scan(&key) == nil {
			keys = append(keys, key)
		}
	}
	rows.Close()
	var dois []string
	for _, key := range keys {
		if doi := Library.EntryFieldValueity(key, "doi"); doi != "" {
			dois = append(dois, doi)
		}
	}
	return dois
}

// orcidWorkFetch paces the works fetches of the decision: ORCID's public API allows
// a few requests a second, and backs off for a minute after a 429.
var orcidWorkFetch struct {
	last, backoffUntil time.Time
}

// orcidWorkDOIs returns the DOIs of orcid's works from the store, fetching and
// storing the works list when it is not there and we are online. known is false
// when the list is not stored and the fetch fails (offline, rate limited, error);
// the next run tries again.
func orcidWorkDOIs(orcid string) (dois []string, known bool) {
	var works []orcidCachedWork
	if e := loadCachedORCIDPerson(orcid); e != nil && !e.WorksFetchedAt.IsZero() {
		works, known = e.Works, true
	} else {
		if !Online || time.Now().Before(orcidWorkFetch.backoffUntil) {
			return nil, false
		}
		if wait := 150*time.Millisecond - time.Since(orcidWorkFetch.last); wait > 0 {
			time.Sleep(wait)
		}
		var status orcidFetchStatus
		works, status = fetchORCIDWorks(orcid)
		orcidWorkFetch.last = time.Now()
		switch status {
		case orcidFetchOK, orcidFetchNotFound:
			saveCachedORCIDWorks(orcid, works)
			known = true
		case orcidFetchRateLimited:
			orcidWorkFetch.backoffUntil = time.Now().Add(time.Minute)
			return nil, false
		default:
			return nil, false
		}
	}
	for _, w := range works {
		dois = append(dois, w.DOIs...)
	}
	return dois, known
}

// nameFitsContributor reports whether name is a form of contributor id: its name,
// its name in the other order, or a name that resolves to it.
func (l *TBibTeXLibrary) nameFitsContributor(name, id string) bool {
	c := l.ContributorByID[id]
	if c == nil {
		return false
	}
	if name == c.Name || sameNameOtherOrder(name, c.Name) {
		return true
	}
	if rid, ok := resolveNameToContributorID(l, name); ok && rid == id {
		return true
	}
	return slices.Contains(l.AmbiguousNameToContributorIDs[name], id)
}

// contributorInPlace returns the contributor at (entry, role, position), or "".
func contributorInPlace(entryKey, role string, position int) string {
	var id string
	bibQueryRow(`SELECT contributor_id FROM contributor_roles WHERE entry_key = ? AND role = ? AND position = ?`,
		entryKey, role, position).Scan(&id) //nolint:errcheck
	return id
}

// entryRecordFor returns the contributor the entry's own record names for the
// position, when its name fits name (also in the other order).
func entryRecordFor(entryKey, role string, position int, name string) (string, bool) {
	var id, used string
	if bibQueryRow(`SELECT contributor_id, name_used FROM entry_contributor_names
		 WHERE entry_key = ? AND role = ? AND position = ?`, entryKey, role, position).Scan(&id, &used) != nil {
		return "", false
	}
	if used == name || sameNameOtherOrder(used, name) {
		return id, true
	}
	if c := Library.ContributorByID[id]; c != nil && (c.Name == name || sameNameOtherOrder(c.Name, name)) {
		return id, true
	}
	return "", false
}

// dblpPlainName is a DBLP name without its number ("Hui Yang 0001" → "Hui Yang").
func dblpPlainName(dblpName string) string {
	return reDblpDisambig.ReplaceAllString(dblpName, "")
}

// storeDecision records the decision for the position in entry_contributor_names, so
// the next run starts from it (rule 3). ev.Name is the name as the entry gives it. A
// record that already says the same is left alone.
func storeDecision(ev TPositionEvidence, d TPositionDecision) {
	if d.ContributorID == "" {
		return
	}
	var orcid, dblpKey any
	if ev.ORCID != "" {
		orcid = ev.ORCID
	}
	if d.DblpKey != "" {
		dblpKey = d.DblpKey
	}
	var oldID, oldName string
	var oldORCID, oldKey sql.NullString
	if bibQueryRow(`SELECT contributor_id, name_used, orcid_used, dblp_key_used FROM entry_contributor_names
		 WHERE entry_key = ? AND role = ? AND position = ?`, ev.EntryKey, ev.Role, ev.Position).
		Scan(&oldID, &oldName, &oldORCID, &oldKey) == nil &&
		oldID == d.ContributorID && oldName == ev.Name &&
		(ev.ORCID == "" || oldORCID.String == ev.ORCID) && (d.DblpKey == "" || oldKey.String == d.DblpKey) {
		return
	}
	dbExecSave("storeDecision",
		`INSERT INTO entry_contributor_names (entry_key, role, position, contributor_id, name_used, orcid_used, dblp_key_used)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(entry_key, role, position) DO UPDATE SET contributor_id = excluded.contributor_id,
		   name_used = excluded.name_used, orcid_used = COALESCE(excluded.orcid_used, orcid_used),
		   dblp_key_used = COALESCE(excluded.dblp_key_used, dblp_key_used)`,
		ev.EntryKey, ev.Role, ev.Position, d.ContributorID, ev.Name, orcid, dblpKey)
}

// worthStoring reports whether a decision says more than the name alone would: it
// rests on an ORCID or a DBLP person, the name is shared, or the entry uses a name
// form other than the contributor's.
func (l *TBibTeXLibrary) worthStoring(ev TPositionEvidence, d TPositionDecision) bool {
	if ev.ORCID != "" || d.DblpKey != "" {
		return true
	}
	if _, shared := l.AmbiguousNameToContributorIDs[ev.Name]; shared {
		return true
	}
	c := l.ContributorByID[d.ContributorID]
	return c != nil && c.Name != ev.Name
}

// positionEvidence is the evidence for one position: the entry's name, plus, during a
// DBLP merge, DBLP's person at that position, when DBLP has as many names and its name
// fits the entry's (the same, the other order, or a form of the same contributor).
func (l *TBibTeXLibrary) positionEvidence(key, role string, position, names int, name string) TPositionEvidence {
	ev := TPositionEvidence{EntryKey: key, Role: role, Position: position, Name: name}
	if key != l.DblpSourceKey {
		return ev // another entry written during the merge (a crossref parent, say)
	}
	persons := l.DblpSourcePersons[role]
	if len(persons) != names || position < 1 || position > len(persons) {
		return ev
	}
	p := persons[position-1]
	dblpName := dblpPersonNameToLaTeX(p.Name)
	fits := dblpName == name || sameNameOtherOrder(dblpName, name)
	if !fits {
		id1, ok1 := resolveNameToContributorID(l, name)
		id2, ok2 := resolveNameToContributorID(l, dblpName)
		fits = ok1 && ok2 && id1 == id2
	}
	if fits {
		ev.DblpName = p.Name
		ev.ORCID = p.ORCID
	}
	return ev
}

// settleDecision carries out a decision and returns the contributor for the position:
// it creates the new person a decision asks for, gives a contributor its second
// ORCID, and for a shared name nothing decides falls back on the co-authors (and else
// the first candidate, reported by FlushAmbiguousAssignments). A name nobody has
// gets a new contributor. resolvedSoFar holds the entry's earlier positions.
func (l *TBibTeXLibrary) settleDecision(ev TPositionEvidence, d *TPositionDecision, resolvedSoFar []string) string {
	orcid := ev.ORCID
	if primary := refPrimaryORCID(orcid); orcid != "" && primary != "" {
		orcid = primary
	}
	switch {
	case d.NewPerson:
		other := d.ContributorID
		id := l.addContributor(ev.Name)
		if other == "" {
			other = l.NameToContributorID[ev.Name]
		}
		if other != "" {
			makeNameAmbiguous(l, ev.Name, other, id)
			retroactivelyBackfillDisambiguation(l, ev.Name)
		}
		l.giveContributorORCID(id, orcid)
		if d.DblpKey != "" && l.ContributorByID[l.DblpKeyToContributorID[d.DblpKey]] == nil {
			setContributorDblpKey(l, id, d.DblpKey)
		}
		d.ContributorID = id
	case d.AddORCID:
		l.giveContributorORCID(d.ContributorID, orcid)
	case d.ContributorID == "":
		if candidates := l.AmbiguousNameToContributorIDs[ev.Name]; len(candidates) > 0 {
			id := coauthorInference(candidates, resolvedSoFar)
			if id == "" {
				id = candidates[0]
				l.ambiguousAssignmentCount[ev.Name]++
				if _, seen := l.ambiguousAssignmentPick[ev.Name]; !seen {
					l.ambiguousAssignmentPick[ev.Name] = id
				}
			}
			d.ContributorID = id
		} else {
			d.ContributorID = l.addContributor(ev.Name)
			l.NameToContributorID[ev.Name] = d.ContributorID
		}
	}
	return d.ContributorID
}

// addContributor creates a contributor named name.
func (l *TBibTeXLibrary) addContributor(name string) string {
	id := l.NewKey()
	l.ContributorByID[id] = &TContributor{Name: name}
	upsertContributorToDB(id, name, "")
	upsertContributorNameToDB(id, name)
	return id
}

// giveContributorNameForm records form as a name form of contributor id. Unlike
// AddNameForm, which works by name and for a name shared by several contributors
// creates a new one, it ties the form to this contributor. The caller makes sure no
// other contributor has the form (nameFormIsFree).
func (l *TBibTeXLibrary) giveContributorNameForm(id, form string) {
	if l.ContributorByID[id] == nil || form == "" {
		return
	}
	upsertContributorNameToDB(id, form)
	l.NameToContributorID[form] = id
}

// giveContributorORCID records orcid for contributor id, as its canonical ORCID when
// it has none yet; not when another contributor already holds it.
func (l *TBibTeXLibrary) giveContributorORCID(id, orcid string) {
	c := l.ContributorByID[id]
	if c == nil || orcid == "" {
		return
	}
	if holder := l.ORCIDToContributorID[orcid]; holder != "" && l.ContributorByID[holder] != nil {
		return
	}
	canonical := c.ORCID == ""
	upsertContributorORCIDToDB(id, orcid, canonical)
	l.ORCIDToContributorID[orcid] = id
	if canonical {
		c.ORCID = orcid
	}
}

// reportMove reports a position that changes contributor while the name stays a form
// of the one in place: a change of identity, not a shifted or edited name list.
func (l *TBibTeXLibrary) reportMove(ev TPositionEvidence, from, to string, d TPositionDecision) {
	if from == "" || from == to || !l.nameFitsContributor(ev.Name, from) {
		return
	}
	fromName, toName := from, to
	if c := l.ContributorByID[from]; c != nil {
		fromName = c.Name
	}
	if c := l.ContributorByID[to]; c != nil {
		toName = c.Name
	}
	how := identityRuleNames[d.Rule]
	if d.NewPerson {
		how = "a new person (ORCID " + ev.ORCID + ")"
	}
	l.Progress("%s %s %d (%s): %s → %s, %s.", ev.EntryKey, ev.Role, ev.Position, ev.Name,
		fromName+" ["+from+"]", toName+" ["+to+"]", how)
}

var identityRuleNames = map[int]string{
	identityByORCID: "by ORCID", identityByDblpPerson: "by DBLP person", identityByEntryRecord: "by the entry's record",
	identityInPlace: "in place", identityByUniqueName: "by unique name", identityAmbiguous: "ambiguous",
}

// applyDblpIdentity decides, with DBLP's evidence, every author/editor position of
// an entry being merged from DBLP. The merge rewrites the positions only of a field
// it changed; this also covers the fields it left as they were.
func applyDblpIdentity(l *TBibTeXLibrary, key string) {
	for _, role := range []string{"author", "editor"} {
		if len(l.DblpSourcePersons[role]) == 0 {
			continue
		}
		names := contributorFieldNames(l.EntryFieldValueity(key, role))
		var resolved []string
		for i, n := range names {
			inPlace := contributorInPlace(key, role, i+1)
			resolved = append(resolved, inPlace)
			if n.garbled || inPlace == "" {
				continue
			}
			ev := l.positionEvidence(key, role, i+1, len(names), n.name)
			if ev.DblpName == "" {
				continue // DBLP says nothing about this position
			}
			d := l.decideContributor(ev)
			id := l.settleDecision(ev, &d, resolved[:i])
			if id != inPlace {
				l.reportMove(ev, inPlace, id, d)
				dbExecSave("applyDblpIdentity: reassign role",
					`UPDATE contributor_roles SET contributor_id = ? WHERE entry_key = ? AND role = ? AND position = ?`,
					id, key, role, i+1)
				resolved[i] = id
				setTableDate("contributor_roles", time.Now().UnixMicro())
			}
			if l.worthStoring(ev, d) {
				storeDecision(ev, d)
			}
		}
	}
}

// tContributorFieldName is one name of an author/editor field, as stored.
type tContributorFieldName struct {
	name    string // braced when garbled
	garbled bool
	wrapped bool // braced here (it was not in the field)
}

// contributorFieldNames splits an author/editor field into its positions: "et al."
// tails become "others", "et al." tokens take no position, garbled names are braced.
func contributorFieldNames(value string) []tContributorFieldName {
	if value == "" {
		return nil
	}
	var names []tContributorFieldName
	for _, name := range splitBibNameField(normalizeEtAlTail(value)) {
		lc := strings.ToLower(name)
		if lc == "et.al." || lc == "et al." {
			continue
		}
		n := tContributorFieldName{name: name}
		if isGarbledContributorName(name) {
			n.garbled = true
			if !strings.HasPrefix(name, "{") {
				n.name = "{" + name + "}"
				n.wrapped = true
			}
		}
		names = append(names, n)
	}
	return names
}

// --- reference database lookups ---

func readPrimaryORCID(orcid string) string {
	db := orcidStoreDB()
	if db == nil {
		return ""
	}
	var primary sql.NullString
	db.QueryRow(`SELECT primary_orcid FROM orcid_records WHERE orcid = ?`, orcid).Scan(&primary) //nolint:errcheck
	return primary.String
}

// readDblpPersonForName returns the DBLP person with that name form when it is the
// only one; "" when several persons share it, or the store has no person tables.
func readDblpPersonForName(name string) string {
	keys := dblpQueryStrings("person_names", name)
	if len(keys) == 1 {
		return keys[0]
	}
	return ""
}

// readDblpPersonForORCID returns the DBLP person listing orcid, when only one does.
func readDblpPersonForORCID(orcid string) string {
	keys := dblpQueryStrings("orcid_persons", orcid)
	if len(keys) == 1 {
		return keys[0]
	}
	return ""
}

// readDblpPersonORCIDs returns the ORCIDs a DBLP person lists, the preferred first.
func readDblpPersonORCIDs(key string) []string {
	return personORCIDs()[key]
}

// personORCIDs holds person_orcids in memory (key → ORCIDs, preferred first), read
// on first use; resetPersonORCIDs drops it when a new store is swapped in.
var (
	personORCIDsMu    sync.Mutex
	personORCIDsByKey map[string][]string
)

func personORCIDs() map[string][]string {
	personORCIDsMu.Lock()
	defer personORCIDsMu.Unlock()
	if personORCIDsByKey != nil {
		return personORCIDsByKey
	}
	personORCIDsByKey = map[string][]string{}
	if s := dblpStmt("all_person_orcids"); s != nil {
		if rows, err := s.Query(); err == nil {
			for rows.Next() {
				var key, orcid string
				if rows.Scan(&key, &orcid) == nil {
					personORCIDsByKey[key] = append(personORCIDsByKey[key], orcid)
				}
			}
			rows.Close()
		}
	}
	return personORCIDsByKey
}

func resetPersonORCIDs() {
	personORCIDsMu.Lock()
	personORCIDsByKey = nil
	personORCIDsMu.Unlock()
}
