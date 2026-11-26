/*
 *
 * Module:    bibdb_manager
 * Component:
 * - bibtex_dblp
 *   - bibtex_dblp_xml
 *
 * Streaming importer from DBLP XML exports (.xml.gz) into the SQLite DBLP
 * store (bibtex_dblp_db.go).
 * Field values are stored VERBATIM: HTML entity references (&iacute; etc.) are
 * preserved as "&name;" strings, HTML inline tags (<i>, <sup> etc.) are
 * reconstructed as "<tag>...</tag>" text, and Unicode characters from ISO-8859-1
 * decoding are stored as-is. Conversion to LaTeX happens at read time via
 * dblpRawToLaTeX (html_commands_map → html_character_map → unicode_map).
 *
 * Import is two-pass: the first pass collects name forms from www (person
 * homepage) entries; the second pass builds a new store with names already
 * mapped to their canonical bibtex= forms.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 19.11.2026
 *
 */

package main

import (
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

// dblpDisambigSuffix matches the trailing disambiguation number that DBLP
// appends to person names in the bibtex= attribute (e.g. "Robert Winter 0001").
// These suffixes are DBLP-internal and are stripped when building name maps.
var dblpDisambigSuffix = regexp.MustCompile(` \d{4}$`)

// --- XML parser types ---

type dblpXMLPerson struct {
	Name   string
	Bibtex string
	ORCID  string
}

type dblpXMLField struct {
	Name     string
	Value    string
	Position int
}

// dblpKnownEntryTypes is the set of element names that are direct children of
// the <dblp> root and represent publications or person homepages.
var dblpKnownEntryTypes = map[string]bool{
	"article": true, "inproceedings": true, "proceedings": true,
	"book": true, "incollection": true, "inbook": true,
	"phdthesis": true, "mastersthesis": true,
	"www": true, "data": true, "misc": true,
}

// dblpMultiValuedFields lists child elements that may appear more than once
// within a single DBLP entry; each occurrence gets a distinct position value.
var dblpMultiValuedFields = map[string]bool{
	"ee": true, "url": true, "cite": true, "note": true,
}

// dblpTitleIndexedFields lists the entry fields whose normalised values are
// stored in the title index for fast title-based duplicate detection.
var dblpTitleIndexedFields = map[string]bool{
	"title": true, "booktitle": true,
}

// xmlCollectText reads XML tokens until the matching close tag and returns the
// concatenated text, storing HTML inline elements verbatim as "<tag>...</tag>".
func xmlCollectText(d *xml.Decoder) (string, error) {
	var buf strings.Builder
	var stack []string

	for {
		tok, err := d.Token()
		if err != nil {
			return buf.String(), err
		}
		switch t := tok.(type) {
		case xml.CharData:
			buf.Write(t)
		case xml.StartElement:
			tag := t.Name.Local
			buf.WriteString("<" + tag + ">")
			stack = append(stack, tag)
		case xml.EndElement:
			if len(stack) == 0 {
				return strings.TrimSpace(buf.String()), nil
			}
			tag := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			buf.WriteString("</" + tag + ">")
		}
	}
}

// newDblpDecoder creates an XML decoder for DBLP XML with ISO-8859-1 support
// and the entity passthrough map. The caller must advance past the <dblp> root.
func newDblpDecoder(r io.Reader) *xml.Decoder {
	d := xml.NewDecoder(r)
	d.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		if strings.EqualFold(charset, "iso-8859-1") {
			return transform.NewReader(input, charmap.ISO8859_1.NewDecoder()), nil
		}
		return input, nil
	}
	d.Entity = xmlEntityPassthrough
	d.Strict = false
	return d
}

// advanceToDblpRoot reads tokens until the <dblp> root element is found.
func advanceToDblpRoot(d *xml.Decoder) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return fmt.Errorf("seeking <dblp> root: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "dblp" {
			return nil
		}
	}
}

// --- First pass: name map ---

// dblpBuildNameMap does a first streaming pass over the DBLP XML, collecting
// only www (person homepage) entries to build a plain-name → canonical-name map
// and a canonical-name → ORCID map. The canonical name comes from the bibtex=
// attribute of the first author that carries one; the disambiguation suffix
// (e.g. " 0001") is stripped. The orcid= attribute of that same author element
// dblpPersonMaps holds the person-level maps built during Pass 1 of XML import.
// All maps are keyed around the DBLP homepages entry key (e.g. "homepages/93/4573").
type dblpPersonMaps struct {
	nameToKey      map[string]string // LaTeX name form → DBLP key; "" = two people share this form
	orcidToKey     map[string]string // ORCID → DBLP key
	keyToCanonical map[string]string // DBLP key → display canonical (LaTeX, first <author> text)
	keyToBibtex    map[string]string // DBLP key → bibtex= form (LaTeX), only when it differs from display
	keyOldies      map[string]string // child DBLP key → parent DBLP key (crossref redirects)
	// Built by Pass 1 only (not by loadDblpPersonMaps), for the person tables:
	nameKeys  map[string][]string // LaTeX name form → every DBLP key with that form
	keyOrcids map[string][]string // DBLP key → every ORCID of that person, the preferred first
}

// aliasToCanonical derives the alias→canonical map needed by dblpImportFromReader.
// Uses the bibtex= form as canonical when present (preserves compatibility of the stored entry JSON);
// falls back to display canonical.
func (pm dblpPersonMaps) aliasToCanonical() map[string]string {
	m := make(map[string]string, len(pm.nameToKey))
	for name, key := range pm.nameToKey {
		if key == "" {
			continue
		}
		canon := pm.keyToBibtex[key]
		if canon == "" {
			canon = pm.keyToCanonical[key]
		}
		if canon != "" && name != canon {
			m[name] = canon
		}
	}
	return m
}

// dblpBuildNameMap does Pass 1 of DBLP XML import: scans every <www> entry under
// a homepages/ key and populates the five person maps.
//
// Name forms are converted to LaTeX before insertion so that ambiguity detection
// works on the final representation (preventing HTML/Unicode encoding variants of
// the same name from slipping through as separate entries).
//
// Entries that only contain a <crossref> field are recorded as old keys
// (child → parent redirect) rather than being processed as person records.
func dblpBuildNameMap(r io.Reader) (dblpPersonMaps, error) {
	pm := dblpPersonMaps{
		nameToKey:      make(map[string]string),
		orcidToKey:     make(map[string]string),
		keyToCanonical: make(map[string]string),
		keyToBibtex:    make(map[string]string),
		keyOldies:      make(map[string]string),
		nameKeys:       make(map[string][]string),
		keyOrcids:      make(map[string][]string),
	}

	// addName inserts a LaTeX-converted name form → key mapping.
	// Ambiguity (same LaTeX form appearing under two different keys) is marked with "".
	addName := func(raw, key string) {
		if raw == "" || key == "" {
			return
		}
		latex := dblpPersonNameToLaTeX(raw)
		if latex == "" {
			return
		}
		if existing, seen := pm.nameToKey[latex]; seen && existing != key {
			pm.nameToKey[latex] = ""
		} else if !seen {
			pm.nameToKey[latex] = key
		}
		if keys := pm.nameKeys[latex]; !slices.Contains(keys, key) {
			pm.nameKeys[latex] = append(keys, key)
		}
		// The numbered form ("Hui Yang 0001") names this one person: kept as well, so a
		// paper's numbered author leads to its person (step 23.2, rule 2).
		if reDblpDisambig.MatchString(raw) {
			if numbered := dblpRawToLaTeX(raw); numbered != "" && !slices.Contains(pm.nameKeys[numbered], key) {
				pm.nameKeys[numbered] = append(pm.nameKeys[numbered], key)
			}
		}
	}

	d := newDblpDecoder(r)
	if err := advanceToDblpRoot(d); err != nil {
		return pm, err
	}

	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return pm, fmt.Errorf("building name map: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local != "www" {
			d.Skip()
			continue
		}

		var dblpKey string
		for _, a := range se.Attr {
			if a.Name.Local == "key" {
				dblpKey = a.Value
				break
			}
		}
		if !strings.HasPrefix(dblpKey, "homepages/") {
			d.Skip()
			continue
		}

		var authors []dblpXMLPerson
		var urlOrcid, crossrefKey string
		var urlOrcids []string
	childLoop:
		for {
			child, cerr := d.Token()
			if cerr != nil {
				break
			}
			switch ct := child.(type) {
			case xml.StartElement:
				switch ct.Name.Local {
				case "author":
					var bibtex, orcid string
					for _, a := range ct.Attr {
						switch a.Name.Local {
						case "bibtex":
							bibtex = a.Value
						case "orcid":
							orcid = a.Value
						}
					}
					text, _ := xmlCollectText(d)
					authors = append(authors, dblpXMLPerson{Name: text, Bibtex: bibtex, ORCID: orcid})
				case "url":
					text, _ := xmlCollectText(d)
					if strings.HasPrefix(text, "https://orcid.org/") {
						if urlOrcid == "" {
							urlOrcid = strings.TrimPrefix(text, "https://orcid.org/")
						}
						urlOrcids = append(urlOrcids, strings.TrimPrefix(text, "https://orcid.org/"))
					}
				case "crossref":
					text, _ := xmlCollectText(d)
					if crossrefKey == "" {
						crossrefKey = text
					}
				default:
					d.Skip()
				}
			case xml.EndElement:
				break childLoop
			}
		}

		// Crossref-only entry: record as a key redirect (oldie) and skip name processing.
		if crossrefKey != "" && len(authors) == 0 {
			pm.keyOldies[dblpKey] = crossrefKey
			continue
		}

		if len(authors) == 0 {
			continue
		}

		first := authors[0]

		// Display canonical: first author's text content (LaTeX-converted).
		displayLatex := dblpPersonNameToLaTeX(first.Name)
		if displayLatex == "" {
			continue
		}
		pm.keyToCanonical[dblpKey] = displayLatex

		// Bibtex form: bibtex= attribute (suffix stripped, LaTeX-converted), stored only
		// when it differs from the display canonical.
		if first.Bibtex != "" {
			bibtexLatex := dblpPersonNameToLaTeX(first.Bibtex)
			if bibtexLatex != "" && bibtexLatex != displayLatex {
				pm.keyToBibtex[dblpKey] = bibtexLatex
			}
		}

		// Add all name forms: display canonical, bibtex form, all alias authors.
		addName(first.Name, dblpKey)
		if first.Bibtex != "" {
			addName(first.Bibtex, dblpKey)
		}
		for _, a := range authors[1:] {
			addName(a.Name, dblpKey)
			if a.Bibtex != "" {
				addName(a.Bibtex, dblpKey)
			}
		}

		// ORCID: prefer orcid= on first author, fall back to <url> element.
		orcid := first.ORCID
		if orcid == "" {
			orcid = urlOrcid
		}
		if orcid != "" {
			pm.orcidToKey[orcid] = dblpKey
		}
		// All ORCIDs of the person, the preferred one first: a profile can list several.
		all := []string{orcid}
		for _, a := range authors {
			all = append(all, a.ORCID)
		}
		all = append(all, urlOrcids...)
		for _, o := range all {
			if o != "" && !slices.Contains(pm.keyOrcids[dblpKey], o) {
				pm.keyOrcids[dblpKey] = append(pm.keyOrcids[dblpKey], o)
				if _, seen := pm.orcidToKey[o]; !seen {
					pm.orcidToKey[o] = dblpKey
				}
			}
		}
	}
	return pm, nil
}


// loadDblpPersonMaps reads the DBLP person-map CSVs from dblpFolder().
// The three core files must exist; dblp_key_bibtex and dblp_homepages_oldies are optional.
// Returns ok=false when a core file is absent (run -dblp_update to regenerate).
func loadDblpPersonMaps() (pm dblpPersonMaps, ok bool) {
	if pm, ok := loadDblpPersonMapsFromDB(); ok {
		return pm, true
	}
	base := dblpFolder()
	if !FileExists(base+"dblp_name_key.csv") ||
		!FileExists(base+"dblp_orcid_key.csv") ||
		!FileExists(base+"dblp_key_canonical.csv") {
		return dblpPersonMaps{}, false
	}

	pm = dblpPersonMaps{
		nameToKey:      make(map[string]string),
		orcidToKey:     make(map[string]string),
		keyToCanonical: make(map[string]string),
		keyToBibtex:    make(map[string]string),
		keyOldies:      make(map[string]string),
	}
	processCSVFile(base+"dblp_name_key.csv", func(rec []string) {
		if len(rec) == 2 && rec[0] != "" && rec[1] != "" {
			pm.nameToKey[rec[0]] = rec[1]
		}
	})
	processCSVFile(base+"dblp_orcid_key.csv", func(rec []string) {
		if len(rec) == 2 && rec[0] != "" && rec[1] != "" {
			pm.orcidToKey[rec[0]] = rec[1]
		}
	})
	processCSVFile(base+"dblp_key_canonical.csv", func(rec []string) {
		if len(rec) == 2 && rec[0] != "" && rec[1] != "" {
			pm.keyToCanonical[rec[0]] = rec[1]
		}
	})
	if FileExists(base + "dblp_key_bibtex.csv") {
		processCSVFile(base+"dblp_key_bibtex.csv", func(rec []string) {
			if len(rec) == 2 && rec[0] != "" && rec[1] != "" {
				pm.keyToBibtex[rec[0]] = rec[1]
			}
		})
	}
	if FileExists(base + "dblp_homepages_oldies.csv") {
		processCSVFile(base+"dblp_homepages_oldies.csv", func(rec []string) {
			if len(rec) == 2 && rec[0] != "" && rec[1] != "" {
				pm.keyOldies[rec[0]] = rec[1]
			}
		})
	}
	return pm, true
}

// --- Second pass: main import ---

const dblpProgressInterval = 50_000

// errDblpImportInterrupted reports that the user interrupted an import (Ctrl-C).
var errDblpImportInterrupted = errors.New("interrupted")

// dblpImportFromReader streams DBLP XML from r into the store being built by b:
// one row per entry plus its title, person and ORCID links. nameMap contains the
// canonical name forms collected by dblpBuildNameMap. progress is called every
// dblpProgressInterval entries (may be nil). Returns the total entry count and
// any fatal error; stops with errDblpImportInterrupted after a Ctrl-C.
func dblpImportFromReader(r io.Reader, nameMap map[string]string, b *TDblpDBBuilder, progress func(n int)) (int, error) {
	d := newDblpDecoder(r)
	if err := advanceToDblpRoot(d); err != nil {
		return 0, err
	}

	applyNameMap := func(p dblpXMLPerson) TDblpJSONPerson {
		name := p.Name
		if mapped, ok := nameMap[name]; ok {
			name = mapped
		}
		return TDblpJSONPerson{Name: name, ORCID: p.ORCID}
	}

	count := 0
	var parseErr error

outer:
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			parseErr = fmt.Errorf("XML token near entry %d: %w", count, err)
			break
		}

		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !dblpKnownEntryTypes[se.Name.Local] {
			d.Skip()
			continue
		}

		entryType := se.Name.Local
		var dblpKey, mdate, pubType string
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "key":
				dblpKey = a.Value
			case "mdate":
				mdate = a.Value
			case "publtype":
				pubType = a.Value
			}
		}
		if dblpKey == "" {
			d.Skip()
			continue
		}

		if Reporting.QuitWasRequested() {
			parseErr = errDblpImportInterrupted
			break
		}

		var authors, editors []dblpXMLPerson
		var fields []dblpXMLField
		fieldPos := map[string]int{}

	childLoop:
		for {
			child, cerr := d.Token()
			if cerr != nil {
				parseErr = fmt.Errorf("reading entry %s: %w", dblpKey, cerr)
				break outer
			}
			switch ct := child.(type) {
			case xml.StartElement:
				name := ct.Name.Local
				var bibtex, orcid string
				for _, a := range ct.Attr {
					switch a.Name.Local {
					case "bibtex":
						bibtex = a.Value
					case "orcid":
						orcid = a.Value
					}
				}
				text, terr := xmlCollectText(d)
				if terr != nil {
					parseErr = fmt.Errorf("field %s in %s: %w", name, dblpKey, terr)
					break outer
				}
				switch name {
				case "author":
					authors = append(authors, dblpXMLPerson{Name: text, Bibtex: bibtex, ORCID: orcid})
				case "editor":
					editors = append(editors, dblpXMLPerson{Name: text, Bibtex: bibtex, ORCID: orcid})
				default:
					pos := 0
					if dblpMultiValuedFields[name] {
						pos = fieldPos[name]
						fieldPos[name]++
					}
					fields = append(fields, dblpXMLField{Name: name, Value: text, Position: pos})
				}
			case xml.EndElement:
				break childLoop
			}
		}

		// Build JSON entry.
		je := &TDblpJSONEntry{
			EntryType: entryType,
			Mdate:     mdate,
			PubType:   pubType,
		}

		for _, f := range fields {
			value := f.Value
			if dblpTitleIndexedFields[f.Name] {
				value = strings.TrimSuffix(value, ".")
			}
			if dblpMultiValuedFields[f.Name] {
				if je.Multi == nil {
					je.Multi = make(map[string][]string)
				}
				je.Multi[f.Name] = append(je.Multi[f.Name], value)
			} else {
				if je.Fields == nil {
					je.Fields = make(map[string]string)
				}
				je.Fields[f.Name] = value
			}
		}

		for _, p := range authors {
			je.Authors = append(je.Authors, applyNameMap(p))
		}
		for _, p := range editors {
			je.Editors = append(je.Editors, applyNameMap(p))
		}

		jsonBytes, _ := json.Marshal(je)

		var titleHashes, personNames, orcids []string
		// Person homepages (www) are left out of the title index: their 4.2 million
		// identical "Home Page" titles never match a library entry.
		if entryType != "www" {
			for _, fieldName := range []string{"title", "booktitle"} {
				if hash := dblpTitleHash(je.Fields[fieldName]); hash != "" {
					titleHashes = append(titleHashes, hash)
				}
			}
		}
		for _, p := range append(je.Authors, je.Editors...) {
			if name := dblpRawToLaTeX(p.Name); name != "" {
				personNames = append(personNames, name)
			}
			if p.ORCID != "" {
				orcids = append(orcids, p.ORCID)
			}
		}
		crossref := je.Fields["crossref"]
		if crossref == dblpKey {
			crossref = "" // a self-reference is no parent
		}
		if err := b.addEntry(dblpKey, mdate, crossref, jsonBytes, titleHashes, personNames, orcids); err != nil {
			parseErr = fmt.Errorf("storing entry %s: %w", dblpKey, err)
			break outer
		}

		count++
		if count%dblpProgressInterval == 0 && progress != nil {
			progress(count)
		}
	}

	return count, parseErr
}

// countingReader wraps an io.Reader and tracks the total number of bytes read.
// n is updated atomically so a ticker goroutine can read it concurrently.
type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.n.Add(int64(n))
	return n, err
}

// --- CLI: -load_dblp_xml ---

func doLoadDblpXml(args []string) error {
	xmlGzPath := args[0]
	if !FileExists(xmlGzPath) {
		messagef("File not found: %s\n", xmlGzPath)
		return errReported
	}

	fi, err := os.Stat(xmlGzPath)
	if err != nil {
		messagef("Cannot stat %s: %s\n", xmlGzPath, err)
		return errReported
	}
	totalBytes := fi.Size()

	if !FileExists(dblpDBPath()) {
		messagef("No DBLP store yet; building %s.\n", dblpDBPath())
	} else if meta := readDblpMeta(); meta != nil {
		messagef("Existing DBLP store: %d entries from %s (loaded %s)\n",
			meta.EntryCount, meta.XMLFile, meta.LoadedAt)
	}

	start := time.Now()

	// First pass: collect name forms from www entries.
	messagef("Pass 1: building name map from www entries...\n")
	f1, err := os.Open(xmlGzPath)
	if err != nil {
		messagef("Could not open %s: %s\n", xmlGzPath, err)
		return errReported
	}
	gz1, err := gzip.NewReader(f1)
	if err != nil {
		f1.Close()
		messagef("Could not read gzip from %s: %s\n", xmlGzPath, err)
		return errReported
	}
	pm, err := dblpBuildNameMap(gz1)
	gz1.Close()
	f1.Close()
	if err != nil {
		messagef("Warning: name map build error: %s\n", err)
	}
	messagef("  %d person entries collected (%.0fs).\n", len(pm.keyToCanonical), time.Since(start).Seconds())
	nameMap := pm.aliasToCanonical()

	// Second pass: import all entries into a new store.
	builder, err := newDblpDBBuilder()
	if err != nil {
		messagef("Could not create %s: %s\n", dblpDBNewPath(), err)
		return errReported
	}
	messagef("Pass 2: importing DBLP XML from %s into %s...\n", xmlGzPath, dblpDBNewPath())
	f2, err := os.Open(xmlGzPath)
	if err != nil {
		messagef("Could not open %s: %s\n", xmlGzPath, err)
		return errReported
	}
	defer f2.Close()

	cr := &countingReader{r: f2}
	gz2, err := gzip.NewReader(cr)
	if err != nil {
		messagef("Could not read gzip from %s: %s\n", xmlGzPath, err)
		return errReported
	}
	defer gz2.Close()

	pass2Start := time.Now()
	var processed atomic.Int64

	pass2Done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				n := processed.Load()
				elapsed := time.Since(pass2Start).Seconds()
				pct := float64(cr.n.Load()) * 100.0 / float64(totalBytes)
				mbRead := float64(cr.n.Load()) / 1e6
				mbTotal := float64(totalBytes) / 1e6
				emit(TMessage{Kind: "progress", Indent: "  ", Live: true, Text: fmt.Sprintf("%d entries processed (%.0fs elapsed, %.0f%%, %.1f/%.0f MB)...",
					n, elapsed, pct, mbRead, mbTotal)})
			case <-pass2Done:
				return
			}
		}
	}()

	count, err := dblpImportFromReader(gz2, nameMap, builder, func(n int) {
		processed.Store(int64(n))
	})
	close(pass2Done)
	clearCounter()
	emit(TMessage{Kind: "progress", Indent: "  ", Text: fmt.Sprintf("%d entries processed (%.0fs).", count, time.Since(pass2Start).Seconds())})
	if err != nil {
		builder.abort()
		if errors.Is(err, errDblpImportInterrupted) {
			messagef("Import interrupted after %d entries; the current store is unchanged.\n", count)
		} else {
			messagef("Import failed after %d entries: %s\nThe current store is unchanged.\n", count, err)
		}
		return errReported
	}

	meta := TDblpMeta{
		XMLFile:              filepath.Base(xmlGzPath),
		LoadedAt:             time.Now().UTC().Format(time.RFC3339),
		EntryCount:           count,
		HtmlCommandsMapHash:  csvFileHash(localDBFolder + "html_commands_map.csv"),
		HtmlCharacterMapHash: csvFileHash(localDBFolder + "html_character_map.csv"),
		UnicodeMapHash:       csvFileHash(localDBFolder + "unicode_map.csv"),
	}
	finishStart := time.Now()
	messagef("Writing the DBLP person tables...\n")
	if err := builder.addPersonMaps(pm); err != nil {
		builder.abort()
		messagef("Could not write the DBLP person tables: %s\n", err)
		return errReported
	}
	err = builder.finish(meta, func(format string, args ...any) {
		messagef(format+"\n", args...)
	})
	if err != nil {
		messagef("Could not complete %s: %s\nThe current store is unchanged.\n", dblpDBNewPath(), err)
		return errReported
	}
	messagef("  %s ready (%.0fs).\n", dblpDBPath(), time.Since(finishStart).Seconds())
	// The person maps are in the store now (step 23.1); the CSV files of older imports go.
	for _, name := range dblpNameMapFiles {
		os.Remove(dblpFolder() + name)
	}

	messagef("DBLP import complete: %d entries in %.1fs\n",
		count, time.Since(start).Seconds())

	if err := pushDblpStore(); err != nil {
		messagef("Could not copy the DBLP store to %s: %s\n", dblpBackupFolder(), err)
	}
	return nil
}

// --- CLI: -update_dblp ---

const dblpXMLDownloadURL = "https://dblp.org/xml/dblp.xml.gz"

var reDblpXMLFilename = regexp.MustCompile(`dblp-\d{4}-\d{2}-\d{2}\.xml\.gz`)

// downloadsFolder returns the current user's OS-standard Downloads directory,
// or "" if the home directory cannot be determined.
func downloadsFolder() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Downloads")
}

// findDblpDownload returns the DBLP export in dlFolder: dblp.xml.gz, or dblp.xml
// when the browser unpacked it (Safari does so by default). When both exist, the
// newer one wins. Returns "" when neither exists.
func findDblpDownload(dlFolder string) (string, os.FileInfo) {
	var bestPath string
	var bestInfo os.FileInfo
	for _, name := range []string{"dblp.xml.gz", "dblp.xml"} {
		path := filepath.Join(dlFolder, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			if bestInfo == nil || info.ModTime().After(bestInfo.ModTime()) {
				bestPath, bestInfo = path, info
			}
		}
	}
	return bestPath, bestInfo
}

// fileIsGzipped reports whether path starts with the gzip magic bytes, so the
// file's content decides, not its name.
func fileIsGzipped(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	magic := make([]byte, 2)
	if _, err := io.ReadFull(f, magic); err != nil {
		return false, err
	}
	return magic[0] == 0x1f && magic[1] == 0x8b, nil
}

// gzipFileAtomic writes a gzip-compressed copy of src to dst via a temporary
// file, so an interrupted run never leaves a truncated dst behind.
func gzipFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := fmt.Sprintf("%s.tmp-%d", dst, os.Getpid())
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	gz, err := gzip.NewWriterLevel(out, gzip.BestSpeed)
	if err == nil {
		_, err = io.Copy(gz, in)
	}
	if err == nil {
		err = gz.Close()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// downloadDblpExport downloads the DBLP export from dblp_url into dlFolder as
// dblp.xml.gz, via a .part file, and gives it the release's Last-Modified time,
// which names the dated copy. It returns current = true, without downloading,
// when that release is already imported.
func downloadDblpExport(dlFolder string) (current bool, err error) {
	client := &http.Client{} // no timeout: the export is over a gigabyte
	head, err := http.NewRequest("HEAD", dblpURL, nil)
	if err != nil {
		return false, err
	}
	head.Header.Set("User-Agent", bibCheckHTTPUserAgent)
	resp, err := client.Do(head)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%s", resp.Status)
	}
	released, _ := http.ParseTime(resp.Header.Get("Last-Modified"))
	if !released.IsZero() {
		// Local time, as doUpdateDblp names the copy from the file's mtime.
		latest := "dblp-" + released.Local().Format("2006-01-02") + ".xml.gz"
		if readDblpCurrentXML() == latest {
			messagef("Already have %s — nothing to do.\n", latest)
			return true, nil
		}
	}

	req, err := newBibCheckHTTPRequest(dblpURL)
	if err != nil {
		return false, err
	}
	resp, err = client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%s", resp.Status)
	}
	if err := os.MkdirAll(dlFolder, 0755); err != nil {
		return false, err
	}
	dest := filepath.Join(dlFolder, "dblp.xml.gz")
	part := dest + ".part"
	out, err := os.Create(part)
	if err != nil {
		return false, err
	}
	totalMB := 0
	if resp.ContentLength > 0 {
		totalMB = int(resp.ContentLength >> 20)
	}
	ticker := Library.NewProgressTicker("Downloading "+dblpURL+" (MB)", totalMB)
	var written int64
	buf := make([]byte, 1<<20)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err = out.Write(buf[:n]); err != nil {
				break
			}
			written += int64(n)
			ticker.SetCount(int(written >> 20))
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			err = readErr
			break
		}
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil && resp.ContentLength > 0 && written != resp.ContentLength {
		err = fmt.Errorf("download incomplete: %d of %d bytes", written, resp.ContentLength)
	}
	if err != nil {
		ticker.DoneQuiet()
		os.Remove(part)
		return false, err
	}
	ticker.Done()
	if !released.IsZero() {
		os.Chtimes(part, released, released) //nolint:errcheck
	}
	return false, os.Rename(part, dest)
}

// doUpdateDblp imports a DBLP XML export. With dblp_url set in the settings it
// downloads the export first (downloadDblpExport); otherwise, or when that
// fails, it expects one that the user downloaded by hand into their Downloads
// folder (DBLP's site has at times rejected automated fetches).
// The dated destination filename is derived from the download's mtime, so
// re-running after a fresh manual download is detected as a new release; the
// previous .xml.gz is left in place (files have distinct dated names).
func doUpdateDblp() error {
	dlFolder := downloadsFolder()
	if dlFolder == "" {
		messagef("Could not determine your Downloads folder.\n")
		return errReported
	}
	if dblpURL != "" {
		current, err := downloadDblpExport(dlFolder)
		if err != nil {
			messagef("Could not download %s: %s\n", dblpURL, err)
			messagef("Looking for a manual download in %s instead.\n", dlFolder)
		} else if current {
			return nil
		}
	}
	sourcePath, info := findDblpDownload(dlFolder)
	if sourcePath == "" && dblpURL != "" && readDblpCurrentXML() != "" {
		// The download failed (e.g. off-line), but a release is imported: go on with it.
		messagef("Going on with the imported %s.\n", readDblpCurrentXML())
		return nil
	}
	if sourcePath == "" {
		messagef("No DBLP export (dblp.xml.gz) in %s.\n", dlFolder)
		messagef("Please download %s in your browser,\n", dblpXMLDownloadURL)
		messagef("save it to %s, then run Update cache (web UI: Actions, DBLP) again.\n", dlFolder)
		if dblpURL == "" {
			messagef("(Or set dblp_url in the settings file to download it automatically.)\n")
		}
		return errReported
	}

	if err := os.MkdirAll(dblpFolder(), 0755); err != nil {
		messagef("Could not create DBLP folder: %s\n", err)
		return errReported
	}

	latest := "dblp-" + info.ModTime().Format("2006-01-02") + ".xml.gz"
	destPath := dblpFolder() + latest
	if FileExists(destPath) {
		if readDblpCurrentXML() == latest {
			messagef("Already have %s — nothing to do.\n", latest)
			if err := os.Remove(sourcePath); err != nil {
				messagef("Warning: could not remove %s: %s\n", sourcePath, err)
			}
			return nil
		}
		messagef("Found %s but import incomplete — re-importing...\n", latest)
		if err := doLoadDblpXml([]string{destPath}); err != nil {
			return err
		}
		cleanupDblpXmlFiles()
		return nil
	}

	compressed, err := fileIsGzipped(sourcePath)
	if err != nil {
		messagef("Could not read %s: %s\n", sourcePath, err)
		return errReported
	}
	if compressed {
		messagef("Moving %s → %s\n", sourcePath, destPath)
		err = copyFileAtomic(sourcePath, destPath)
	} else {
		messagef("%s is not compressed (browser unpacked it); compressing → %s...\n", sourcePath, destPath)
		err = gzipFileAtomic(sourcePath, destPath)
	}
	if err != nil {
		messagef("Could not copy %s to %s: %s\n", sourcePath, destPath, err)
		return errReported
	}
	if err := os.Remove(sourcePath); err != nil {
		messagef("Warning: could not remove %s: %s\n", sourcePath, err)
	}

	if err := doLoadDblpXml([]string{destPath}); err != nil {
		return err
	}

	// Import succeeded — keep only the two most recent XML files.
	cleanupDblpXmlFiles()
	return nil
}

// swapBibTeXNameFormat converts between the two BibTeX name orderings:
//   - "ss, ff"      ↔  "ff ss"
//   - "ss, gg, ff"  ↔  "ff ss, gg"
//
// Returns "" when the input has no ", " separator (natural-order with unknown surname).
func swapBibTeXNameFormat(name string) string {
	parts := strings.SplitN(name, ", ", 3)
	switch len(parts) {
	case 2:
		return parts[1] + " " + parts[0]
	case 3:
		return parts[2] + " " + parts[0] + ", " + parts[1]
	}
	return ""
}

// parseSurnameGeneration extracts the surname and optional generation from a
// surname-first BibTeX name ("ss, ff" or "ss, gg, ff").
// "de Boer, Remco C."      → ("de Boer", "")
// "King, Jr., Martin Luther" → ("King", "Jr.")
func parseSurnameGeneration(surnameFirstName string) (surname, generation string) {
	parts := strings.SplitN(surnameFirstName, ", ", 3)
	switch len(parts) {
	case 2:
		return parts[0], ""
	case 3:
		return parts[0], parts[1]
	}
	return "", ""
}

// simpleSurnameSwap converts a natural-order name to "Last, First" using simple
// heuristics that work for the vast majority of DBLP person names:
//
//   - Braced compound surname at end: "First {Compound Last}" → "Compound Last, First"
//   - Simple last token: "First Middle Last" → "Last, First Middle"
//
// Returns "" for single-token names or when the input already contains a comma.
func simpleSurnameSwap(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, ",") {
		return ""
	}
	// Braced compound surname: "First {Compound Last}" → "Compound Last, First"
	if strings.HasSuffix(name, "}") {
		if start := strings.LastIndex(name, " {"); start >= 0 {
			first := strings.TrimSpace(name[:start])
			last := name[start+2 : len(name)-1]
			if first != "" && last != "" {
				return last + ", " + first
			}
		}
	}
	// Simple last-token heuristic.
	i := strings.LastIndex(name, " ")
	if i < 0 {
		return ""
	}
	return name[i+1:] + ", " + strings.TrimSpace(name[:i])
}

// naturalOrderToSurnameFirst converts "ff ss" → "ss, ff" and "ff ss, gg" → "ss, gg, ff"
// using the known surname and optional generation from the DBLP canonical.
// Returns "" when the alias does not end with the expected surname.
func naturalOrderToSurnameFirst(alias, surname, generation string) string {
	if surname == "" {
		return ""
	}
	// Form 1: "ff ss, gg" with known generation
	if generation != "" && strings.HasSuffix(alias, " "+surname+", "+generation) {
		fn := strings.TrimSuffix(alias, " "+surname+", "+generation)
		if strings.TrimSpace(fn) == "" {
			return ""
		}
		return surname + ", " + generation + ", " + fn
	}
	// Form 2: "ff ss"
	if strings.HasSuffix(alias, " "+surname) {
		fn := strings.TrimSuffix(alias, " "+surname)
		if strings.TrimSpace(fn) == "" {
			return ""
		}
		return surname + ", " + fn
	}
	return ""
}

// doAbsorbDblpNames gives each contributor with a DBLP key that DBLP person's name
// forms (both orders, without DBLP's number), by contributor id, where no other
// contributor has them (step 23.2.3). A final RenormaliseNameFields pass follows.
// absorbDblpNamesCore does the actual work of -absorb_dblp_names and is called both
// from doAbsorbDblpNames (standalone command) and from doUpsertDblpEntries (implicit
// post-update absorption).  The library must already be open for writing.
func absorbDblpNamesCore() {
	pm, filesOK := loadDblpPersonMaps()
	if filesOK {
		Library.Progress("  Loaded DBLP person maps from cache (%d persons, %d ORCIDs).",
			len(pm.keyToCanonical), len(pm.orcidToKey))
	} else {
		xmlFilename := readDblpCurrentXML()
		if xmlFilename == "" {
			Library.Error("No DBLP import on record; run -load_dblp_xml first.")
			return
		}
		xmlGzPath := dblpFolder() + xmlFilename
		if !FileExists(xmlGzPath) {
			Library.Error("DBLP XML file not found: %s", xmlGzPath)
			return
		}
		f, err := os.Open(xmlGzPath)
		if err != nil {
			Library.Error("Cannot open DBLP XML: %s", err)
			return
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			Library.Error("Cannot read DBLP XML gzip: %s", err)
			return
		}
		Library.Progress("  Building DBLP person maps from %s ...", xmlFilename)
		var buildErr error
		pm, buildErr = dblpBuildNameMap(gz)
		gz.Close()
		f.Close()
		if buildErr != nil {
			Library.Warning("DBLP person map build partial: %s", buildErr)
		}
		Library.Progress("    %d DBLP persons found.", len(pm.keyToCanonical))
	}

	// Build key → names (inverted nameToKey, non-ambiguous entries only).
	keyToNames := make(map[string][]string, len(pm.keyToCanonical))
	for name, key := range pm.nameToKey {
		if key != "" {
			keyToNames[key] = append(keyToNames[key], name)
		}
	}

	// Build key → ORCID (inverted orcidToKey).
	keyToOrcid := make(map[string]string, len(pm.orcidToKey))
	for orcid, key := range pm.orcidToKey {
		keyToOrcid[key] = orcid
	}

	// Step 23.2.3 (ARCHITECTURE.md 7.6): absorb never merges contributors and never
	// decides who sits where. It links a DBLP key where the decisions tied a contributor
	// to that DBLP person, and gives each contributor with a DBLP key that person's name
	// forms (those no other contributor has, and DBLP gives no other person) and, when
	// it has none, the person's ORCID.
	keysLinked := linkDecidedDblpKeys()
	absorbed, orcidsSet := 0, 0
	var absorbedLog []string // "contributor id | its name | form", for dblp_absorbed_names.log
	for id, contrib := range Library.ContributorByID {
		if contrib == nil || contrib.Garbled || contrib.DblpKey == "" {
			continue
		}
		key := contrib.DblpKey
		canonical := dblpPersonCanonical(pm.keyToBibtex[key], dblpPlainName(pm.keyToCanonical[key]))
		if canonical == "" {
			continue
		}
		surname, generation := parseSurnameGeneration(canonical)
		// DBLP's number ("Ryan Peterson 0001") is not part of a name: without it the
		// display name is the name as printed; numbered names are not name forms (a
		// swap would make "0001, Ryan Peterson").
		displayCanonical := dblpPlainName(pm.keyToCanonical[key])
		forms := []string{
			canonical, swapBibTeXNameFormat(canonical),
			displayCanonical, swapBibTeXNameFormat(displayCanonical), simpleSurnameSwap(displayCanonical),
		}
		for _, name := range keyToNames[key] {
			if reDblpDisambig.MatchString(name) {
				continue
			}
			forms = append(forms, name, swapBibTeXNameFormat(name),
				simpleSurnameSwap(name), naturalOrderToSurnameFirst(name, surname, generation))
		}
		seen := map[string]bool{}
		for _, form := range forms {
			if form == "" || form == contrib.Name || seen[form] {
				continue
			}
			seen[form] = true
			if other, ok := resolveNameToContributorID(&Library, form); ok && other == id {
				continue // already a form of this contributor
			}
			if !nameFormIsFree(form, id) {
				continue
			}
			Library.giveContributorNameForm(id, form)
			absorbedLog = append(absorbedLog, id+" | "+contrib.Name+" | "+form)
			absorbed++
		}

		if contrib.ORCID == "" {
			orcid := keyToOrcid[key]
			if listed := refPersonORCIDs(key); len(listed) > 0 {
				orcid = listed[0] // the preferred one
			}
			if orcid != "" && Library.ContributorByID[Library.ORCIDToContributorID[orcid]] == nil {
				Library.giveContributorORCID(id, orcid)
				orcidsSet++
			}
		}
	}
	Library.Progress("  Linked %d DBLP person key(s), absorbed %d name alias(es), set %d ORCID(s).",
		keysLinked, absorbed, orcidsSet)
	if len(absorbedLog) > 0 {
		sort.Strings(absorbedLog)
		os.MkdirAll(libraryBase()+tablesFolderSuffix, 0o755) //nolint:errcheck
		os.WriteFile(libraryBase()+tablesFolderSuffix+"/dblp_absorbed_names.log", //nolint:errcheck
			[]byte(strings.Join(absorbedLog, "\n")+"\n"), 0o644)
	}
	Library.RenormaliseNameFields()
}

// dblpPersonCanonical derives a DBLP person's name in "Last, First" form: (1) the
// bibtex= form when it has a comma ("Lowe, David B."), (2) the surname swap of the
// bibtex= form ("Claire {Le Goues}" → "Le Goues, Claire"), (3) the surname swap of the
// display name ("Margot Brereton" → "Brereton, Margot"), (4) the display name as is.
func dblpPersonCanonical(bibtexForm, displayForm string) string {
	switch {
	case strings.Contains(bibtexForm, ","):
		return bibtexForm
	case bibtexForm != "":
		if ss := simpleSurnameSwap(bibtexForm); ss != "" {
			return ss
		}
		return bibtexForm
	}
	if ss := simpleSurnameSwap(displayForm); ss != "" {
		return ss
	}
	return displayForm
}

// nameFormIsFree reports whether form may become a name form of contributor id: no
// other contributor has it, and it is not shared by several.
func nameFormIsFree(form, id string) bool {
	if _, shared := Library.AmbiguousNameToContributorIDs[form]; shared {
		return false
	}
	other, ok := resolveNameToContributorID(&Library, form)
	return !ok || other == id
}

// linkDecidedDblpKeys gives a contributor without DBLP key the DBLP person its
// positions were decided for (entry_contributor_names.dblp_key_used), when they all
// name the same person and no other contributor has that key. A key that positions
// of several contributors were decided for is DBLP conflating people (the split of
// -split_position, say), and is linked to none of them. Returns the count.
func linkDecidedDblpKeys() int {
	shared := map[string]bool{}
	if rows, err := bibQuery(`SELECT dblp_key_used FROM entry_contributor_names
		WHERE COALESCE(dblp_key_used, '') != ''
		GROUP BY dblp_key_used HAVING COUNT(DISTINCT contributor_id) > 1`); err == nil {
		for rows.Next() {
			var key string
			if rows.Scan(&key) == nil {
				shared[key] = true
			}
		}
		rows.Close()
	}
	rows, err := bibQuery(`SELECT ecn.contributor_id, ecn.dblp_key_used
		FROM entry_contributor_names ecn JOIN contributors c ON c.id = ecn.contributor_id
		WHERE COALESCE(ecn.dblp_key_used, '') != '' AND COALESCE(c.dblp_key, '') = ''
		GROUP BY ecn.contributor_id, ecn.dblp_key_used`)
	if err != nil {
		return 0
	}
	keysOf := map[string][]string{}
	for rows.Next() {
		var id, key string
		if rows.Scan(&id, &key) == nil {
			keysOf[id] = append(keysOf[id], key)
		}
	}
	rows.Close()
	linked := 0
	for id, keys := range keysOf {
		if len(keys) != 1 || shared[keys[0]] || Library.ContributorByID[id] == nil ||
			Library.ContributorByID[Library.DblpKeyToContributorID[keys[0]]] != nil {
			continue
		}
		setContributorDblpKey(&Library, id, keys[0])
		linked++
	}
	return linked
}

// absorbDblpOrcidsCore sweeps contributors for ORCID assignment using the DBLP
// key→ORCID map.  For contributors that already have a DblpKey (set by absorbDblpNamesCore)
// this is a pure in-memory lookup — no file-store reads.  Contributors without a DblpKey
// get a name-based lookup against the nameToKey CSV.
func absorbDblpOrcidsCore() {
	total := len(Library.ContributorByID)
	if total == 0 {
		return
	}

	pm, ok := loadDblpPersonMaps()
	if !ok {
		Library.Error("DBLP person maps not found; run -load_dblp_xml first.")
		return
	}

	keyToOrcid := make(map[string]string, len(pm.orcidToKey))
	for orcid, key := range pm.orcidToKey {
		keyToOrcid[key] = orcid
	}

	Library.Progress("Scanning for ORCIDs via DBLP person keys (%d contributors).", total)
	ticker := Library.NewProgressTicker(ProgressScanningOrcids, total)
	orcidsSet, keysLinked, checked := 0, 0, 0

	for id, contrib := range Library.ContributorByID {
		if contrib.Name == "" {
			continue
		}
		checked++
		if ticker.SetCount(checked) {
			break
		}

		// For contributors without a DblpKey: try ORCID-based lookup first (direct,
		// authoritative), then fall back to name-based lookup.
		// The ORCID path covers the migration gap where an ORCID was assigned by
		// the deployed version before the dblp_key column existed.
		if contrib.DblpKey == "" {
			var key string
			if contrib.ORCID != "" {
				key = pm.orcidToKey[contrib.ORCID]
			}
			_, sharedName := Library.AmbiguousNameToContributorIDs[contrib.Name]
			if key == "" && !sharedName {
				key = pm.nameToKey[contrib.Name]
			}
			if key == "" && !sharedName {
				if swapped := swapBibTeXNameFormat(contrib.Name); swapped != "" {
					key = pm.nameToKey[swapped]
				}
			}
			if key != "" {
				// Another contributor holds this DBLP key: leave both as they are. A
				// name match is no evidence that they are the same person; the
				// decisions per position settle who is who (ARCHITECTURE.md 7.6).
				if Library.ContributorByID[Library.DblpKeyToContributorID[key]] != nil {
					continue
				}
				setContributorDblpKey(&Library, id, key)
				keysLinked++
			}
		}

		if contrib.DblpKey == "" || contrib.ORCID != "" {
			continue
		}

		orcid := keyToOrcid[contrib.DblpKey]
		if orcid == "" {
			continue
		}

		contrib.ORCID = orcid
		Library.ORCIDToContributorID[orcid] = id
		upsertContributorORCIDToDB(id, orcid, true)
		orcidsSet++
	}

	ticker.Done()
	if keysLinked > 0 {
		Library.Progress("Linked %d additional contributor(s) to DBLP keys.", keysLinked)
	}
	if orcidsSet > 0 {
		Library.Progress("DBLP ORCID sweep: %d new ORCID(s) assigned.", orcidsSet)
	}
}

func doAbsorbDblpNames() {
	if !openLibraryToUpdate() {
		return
	}
	absorbDblpNamesCore()
	absorbDblpOrcidsCore()
}

func doAbsorbDblpOrcids() {
	if !openLibraryToUpdate() {
		return
	}
	absorbDblpOrcidsCore()
}

// bestOrcidCanonical picks the preferred canonical name from a group that shares
// an ORCID.  Preference: "ss, ff" format (the standard BibTeX person-name form)
// over natural-order names; alphabetical tiebreak within each tier.
// dblpHomepagePrimaryName returns the first-listed author name from whichever of
// homepageKeys is a DBLP "homepages/" record in the local DBLP store, or "" when
// none resolve. DBLP lists a person's current name first on their homepage
// record and any former names (e.g. a maiden name after marriage) after it —
// the clearest available signal for "which of several name forms is current."
func dblpHomepagePrimaryName(homepageKeys []string) string {
	for _, key := range homepageKeys {
		if !strings.HasPrefix(key, "homepages/") {
			continue
		}
		je := readDblpJSONEntry(key)
		if je == nil || len(je.Authors) == 0 || je.Authors[0].Name == "" {
			continue
		}
		return je.Authors[0].Name
	}
	return ""
}

// bestOrcidCanonical picks the canonical name among candidates that share one
// ORCID. When one of homepageKeys resolves to a local DBLP homepage record,
// whichever candidate matches its first-listed (i.e. current, per DBLP's own
// convention — see dblpHomepagePrimaryName) name wins, in either name-order
// form — this is what stops a merge from defaulting to a stale name (e.g. a
// maiden name) just because it happens to already be in "Surname, First"
// form. Otherwise falls back to the original heuristic: prefer
// "Surname, First"-formatted candidates over natural-order ones (matches
// library convention), then alphabetically first — a real tiebreaker only
// when DBLP itself gives no better signal.
func bestOrcidCanonical(canonicals []string, homepageKeys []string) string {
	if primary := dblpHomepagePrimaryName(homepageKeys); primary != "" {
		primarySSFF := simpleSurnameSwap(primary)
		for _, c := range canonicals {
			if c == primary || c == primarySSFF || swapBibTeXNameFormat(c) == primary {
				return c
			}
		}
	}
	var ssff, natural []string
	for _, c := range canonicals {
		if strings.Contains(c, ", ") {
			ssff = append(ssff, c)
		} else {
			natural = append(natural, c)
		}
	}
	candidates := ssff
	if len(candidates) == 0 {
		candidates = natural
	}
	sort.Strings(candidates)
	return candidates[0]
}

// mergeOrcidDuplicatesCore finds contributors that share the same non-empty ORCID
// and merges each duplicate group into a single canonical record using
// mergeContributorInDB (which properly moves contributor_roles, orcids, etc.)
// followed by in-memory alias registration. Returns the number of pairs merged.
func mergeOrcidDuplicatesCore() int {
	// Group contributor IDs by ORCID.
	type idName struct{ id, name, dblpKey string }
	orcidGroups := map[string][]idName{} // orcid → []idName
	for id, contrib := range Library.ContributorByID {
		if contrib.ORCID == "" {
			continue
		}
		orcidGroups[contrib.ORCID] = append(orcidGroups[contrib.ORCID], idName{id, contrib.Name, contrib.DblpKey})
	}

	merged := 0
	for orcid, members := range orcidGroups {
		if len(members) < 2 {
			continue
		}
		// Pick the best canonical name; find its member record.
		canonicals := make([]string, len(members))
		homepageKeys := make([]string, len(members))
		for i, m := range members {
			canonicals[i] = m.name
			homepageKeys[i] = m.dblpKey
		}
		bestName := bestOrcidCanonical(canonicals, homepageKeys)
		var bestID string
		for _, m := range members {
			if m.name == bestName {
				bestID = m.id
				break
			}
		}
		if bestID == "" {
			continue
		}

		for _, other := range members {
			if other.id == bestID {
				continue
			}
			Library.Progress("ORCID %s: absorbing %q into %q", orcid, other.name, bestName)
			if absorbContributorInto(other.id, other.name, bestID, bestName) {
				merged++
			}
		}
	}
	return merged
}

// absorbContributorInto merges contributor fromID into intoID, in the DB and in
// memory. Returns false when the DB merge failed.
func absorbContributorInto(fromID, fromName, intoID, intoName string) bool {
	if !mergeContributorInDB(fromID, intoID) {
		return false
	}
	// Update in-memory state: redirect all names from fromID to intoID.
	for name, id := range Library.NameToContributorID {
		if id == fromID {
			Library.NameToContributorID[name] = intoID
		}
	}
	// Any ORCID still pointing at fromID must follow, or a later lookup resolves to a
	// deleted contributor ID and the next write through it hits a FOREIGN KEY violation.
	for o, id := range Library.ORCIDToContributorID {
		if id == fromID {
			Library.ORCIDToContributorID[o] = intoID
		}
	}
	delete(Library.ContributorByID, fromID)
	// The old canonical becomes a name form of the contributor merged into (by id:
	// AddNameForm works by name and would create a new contributor when intoName is
	// shared).
	Library.giveContributorNameForm(intoID, fromName)
	return true
}

// mergeDeprecatedOrcidsCore applies ORCID's own duplicate records (step 23.3): an
// iD that ORCID deprecated into a primary one (orcid_records.primary_orcid) belongs
// to the same person. The contributor holding the deprecated iD is merged into the
// one holding the primary; when no contributor holds the primary, it gets the
// primary as its ORCID. Returns merged, given.
func mergeDeprecatedOrcidsCore() (merged, given int) {
	db := orcidStoreDB()
	if db == nil {
		return 0, 0
	}
	rows, err := db.Query(`SELECT orcid, primary_orcid FROM orcid_records WHERE primary_orcid IS NOT NULL ORDER BY orcid`)
	if err != nil {
		return 0, 0
	}
	type pair struct{ deprecated, primary string }
	var pairs []pair
	for rows.Next() {
		var p pair
		if rows.Scan(&p.deprecated, &p.primary) == nil {
			pairs = append(pairs, p)
		}
	}
	rows.Close()

	for _, p := range pairs {
		fromID := Library.ORCIDToContributorID[p.deprecated]
		from := Library.ContributorByID[fromID]
		if from == nil {
			continue
		}
		intoID := Library.ORCIDToContributorID[p.primary]
		switch into := Library.ContributorByID[intoID]; {
		case into == nil:
			Library.Progress("ORCID %s was merged by ORCID into %s: %q gets %s", p.deprecated, p.primary, from.Name, p.primary)
			upsertContributorORCIDToDB(fromID, p.primary, false)
			Library.ORCIDToContributorID[p.primary] = fromID
			if from.ORCID == p.deprecated {
				from.ORCID = p.primary
				dbExecSave("mergeDeprecatedOrcids: primary ORCID",
					`UPDATE contributors SET orcid = ? WHERE id = ?`, p.primary, fromID)
				dbExecSave("mergeDeprecatedOrcids: canonical flags",
					`UPDATE contributor_orcids SET is_canonical = (orcid = ?) WHERE contributor_id = ?`, p.primary, fromID)
			}
			given++
		case intoID != fromID:
			Library.Progress("ORCID %s was merged by ORCID into %s: absorbing %q into %q", p.deprecated, p.primary, from.Name, into.Name)
			if absorbContributorInto(fromID, from.Name, intoID, into.Name) {
				merged++
			}
		}
	}
	return merged, given
}

// doMergeOrcidDuplicates is the -merge_orcid_duplicates entry point.
func doMergeOrcidDuplicates() {
	if !openLibraryToUpdate() {
		return
	}
	merged := mergeOrcidDuplicatesCore()
	Library.Progress("Merged %d ORCID-duplicate contributor pair(s).", merged)
	depMerged, depGiven := mergeDeprecatedOrcidsCore()
	Library.Progress("Deprecated ORCIDs: %d contributor(s) merged, %d given the primary ORCID.", depMerged, depGiven)
	merged += depMerged
	if merged > 0 || depGiven > 0 {
		Library.RenormaliseNameFields()
		Library.CheckNameFormConsistency()
	}
}

// doEnrichContributorData runs the full contributor enrichment pipeline in one pass:
//  1. Absorb DBLP name aliases (dblp_name_bibtex.csv → contributor names)
//  2. Scan DBLP persons index for ORCIDs not yet recorded
//  3. Fetch ORCID person records and add aliases / challenge canonical mismatches
//  4. Merge contributors that share the same ORCID
//  5. Re-normalise all author/editor fields to apply the merged mappings
func doEnrichContributorData() {
	if !openLibraryToUpdate() {
		return
	}
	announceStep("Enriching contributor data")

	Library.Progress("  Step 1/4: absorbing DBLP name forms...")
	Library.progressPrefix = "  " // "  " + existing "  " prefix = "    "
	absorbDblpNamesCore()
	Library.progressPrefix = ""

	Library.Progress("  Step 2/4: scanning DBLP persons for ORCIDs...")
	Library.progressPrefix = "    " // no hardcoded spaces in absorbDblpOrcidsCore messages
	absorbDblpOrcidsCore()
	Library.progressPrefix = ""

	Library.Progress("  Step 3/4: fetching ORCID person records...")
	Library.progressPrefix = "    " // no hardcoded spaces in doEnrichOrcidProfilesCore messages
	doEnrichOrcidProfilesCore()
	Library.progressPrefix = ""

	Library.Progress("  Step 4/4: merging ORCID duplicates...")
	Library.progressPrefix = "  " // "  " + existing "  " prefix = "    "
	merged := mergeOrcidDuplicatesCore()
	Library.Progress("  Merged %d ORCID-duplicate contributor pair(s).", merged)
	depMerged, depGiven := mergeDeprecatedOrcidsCore()
	Library.Progress("  Deprecated ORCIDs: %d contributor(s) merged, %d given the primary ORCID.", depMerged, depGiven)
	merged += depMerged
	if merged > 0 || depGiven > 0 {
		Library.RenormaliseNameFields()
		Library.CheckNameFormConsistency()
	}
	Library.progressPrefix = ""
}

// cleanupDblpXmlFiles removes all but the two most recent dblp-*.xml.gz files
// from dblpFolder(). Called only after a successful import.
func cleanupDblpXmlFiles() {
	des, err := os.ReadDir(dblpFolder())
	if err != nil {
		return
	}
	var xmlFiles []string
	for _, de := range des {
		if !de.IsDir() && reDblpXMLFilename.MatchString(de.Name()) {
			xmlFiles = append(xmlFiles, de.Name())
		}
	}
	sort.Strings(xmlFiles) // dblp-YYYY-MM-DD.xml.gz sorts chronologically by name
	if len(xmlFiles) <= 2 {
		return
	}
	for _, name := range xmlFiles[:len(xmlFiles)-2] {
		path := dblpFolder() + name
		messagef("Removing old XML: %s\n", path)
		os.Remove(path)
	}
}
