/*
 *
 * Module:    bibtex_check
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
 * Import is two-pass: the first pass collects name mappings from www (person
 * homepage) entries; the second pass builds a new store with names already
 * mapped to their canonical bibtex= forms.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 13.11.2026
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
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
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
// Entries that only contain a <crossref> field are recorded as key oldies
// (child → parent redirect) rather than being processed as person records.
func dblpBuildNameMap(r io.Reader) (dblpPersonMaps, error) {
	pm := dblpPersonMaps{
		nameToKey:      make(map[string]string),
		orcidToKey:     make(map[string]string),
		keyToCanonical: make(map[string]string),
		keyToBibtex:    make(map[string]string),
		keyOldies:      make(map[string]string),
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
					if urlOrcid == "" && strings.HasPrefix(text, "https://orcid.org/") {
						urlOrcid = strings.TrimPrefix(text, "https://orcid.org/")
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
	}
	return pm, nil
}

// saveDblpNameFiles writes the DBLP name maps from Pass 1 to two CSV files in
// dblpFolder(): dblp_name_bibtex.csv (alias;canonical) and dblp_name_orcid.csv
// (canonical;orcid). Names are converted to LaTeX format before writing.
// saveDblpNameFiles writes the three DBLP person-map CSVs to dblpFolder():
//   dblp_name_key.csv      — LaTeX name form ; DBLP homepages key
//   dblp_orcid_key.csv     — ORCID ; DBLP homepages key
//   dblp_key_canonical.csv — DBLP homepages key ; LaTeX canonical name
//
// Ambiguous name forms (pm.nameToKey[name] == "") are omitted from dblp_name_key.csv.
// saveDblpNameFiles writes the five DBLP person-map CSVs to dblpFolder().
//
//	dblp_name_key.csv         — LaTeX name ; DBLP key  (many:1, ambiguous names omitted)
//	dblp_orcid_key.csv        — ORCID ; DBLP key       (many:1)
//	dblp_key_canonical.csv    — DBLP key ; display canonical (LaTeX)
//	dblp_key_bibtex.csv       — DBLP key ; bibtex= form (LaTeX, only when ≠ display)
//	dblp_homepages_oldies.csv — child DBLP key ; parent DBLP key (crossref redirects)
func saveDblpNameFiles(pm dblpPersonMaps) {
	base := dblpFolder()

	writeCSV := func(filename string, lines []string) {
		sort.Strings(lines)
		path := base + filename
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not write %s: %s\n", path, err)
		} else {
			fmt.Fprintf(os.Stderr, "  Saved %d rows to %s\n", len(lines), path)
		}
	}

	nameLines := make([]string, 0, len(pm.nameToKey))
	for name, key := range pm.nameToKey {
		if key != "" && name != "" {
			nameLines = append(nameLines, name+";"+key)
		}
	}
	writeCSV("dblp_name_key.csv", nameLines)

	orcidLines := make([]string, 0, len(pm.orcidToKey))
	for orcid, key := range pm.orcidToKey {
		if orcid != "" && key != "" {
			orcidLines = append(orcidLines, orcid+";"+key)
		}
	}
	writeCSV("dblp_orcid_key.csv", orcidLines)

	canonLines := make([]string, 0, len(pm.keyToCanonical))
	for key, canon := range pm.keyToCanonical {
		if key != "" && canon != "" {
			canonLines = append(canonLines, key+";"+canon)
		}
	}
	writeCSV("dblp_key_canonical.csv", canonLines)

	bibtexLines := make([]string, 0, len(pm.keyToBibtex))
	for key, bibtex := range pm.keyToBibtex {
		if key != "" && bibtex != "" {
			bibtexLines = append(bibtexLines, key+";"+bibtex)
		}
	}
	writeCSV("dblp_key_bibtex.csv", bibtexLines)

	oldiesLines := make([]string, 0, len(pm.keyOldies))
	for child, parent := range pm.keyOldies {
		if child != "" && parent != "" {
			oldiesLines = append(oldiesLines, child+";"+parent)
		}
	}
	writeCSV("dblp_homepages_oldies.csv", oldiesLines)

}

// loadDblpPersonMaps reads the DBLP person-map CSVs from dblpFolder().
// The three core files must exist; dblp_key_bibtex and dblp_homepages_oldies are optional.
// Returns ok=false when a core file is absent (run -dblp_update to regenerate).
func loadDblpPersonMaps() (pm dblpPersonMaps, ok bool) {
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
// canonical name mappings collected by dblpBuildNameMap. progress is called every
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

func doLoadDblpXml(args []string) {
	xmlGzPath := args[0]
	if !FileExists(xmlGzPath) {
		fmt.Fprintf(os.Stderr, "File not found: %s\n", xmlGzPath)
		os.Exit(1)
	}

	fi, err := os.Stat(xmlGzPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot stat %s: %s\n", xmlGzPath, err)
		os.Exit(1)
	}
	totalBytes := fi.Size()

	if !FileExists(dblpDBPath()) {
		fmt.Fprintf(os.Stderr, "No DBLP store yet; building %s.\n", dblpDBPath())
	} else if meta := readDblpMeta(); meta != nil {
		fmt.Fprintf(os.Stderr, "Existing DBLP store: %d entries from %s (loaded %s)\n",
			meta.EntryCount, meta.XMLFile, meta.LoadedAt)
	}

	start := time.Now()

	// First pass: collect name mappings from www entries.
	fmt.Fprintf(os.Stderr, "Pass 1: building name map from www entries...\n")
	f1, err := os.Open(xmlGzPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not open %s: %s\n", xmlGzPath, err)
		os.Exit(1)
	}
	gz1, err := gzip.NewReader(f1)
	if err != nil {
		f1.Close()
		fmt.Fprintf(os.Stderr, "Could not read gzip from %s: %s\n", xmlGzPath, err)
		os.Exit(1)
	}
	pm, err := dblpBuildNameMap(gz1)
	gz1.Close()
	f1.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: name map build error: %s\n", err)
	}
	fmt.Fprintf(os.Stderr, "  %d person entries collected (%.0fs).\n", len(pm.keyToCanonical), time.Since(start).Seconds())
	saveDblpNameFiles(pm)
	nameMap := pm.aliasToCanonical()

	// Second pass: import all entries into a new store.
	builder, err := newDblpDBBuilder()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not create %s: %s\n", dblpDBNewPath(), err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Pass 2: importing DBLP XML from %s into %s...\n", xmlGzPath, dblpDBNewPath())
	f2, err := os.Open(xmlGzPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not open %s: %s\n", xmlGzPath, err)
		os.Exit(1)
	}
	defer f2.Close()

	cr := &countingReader{r: f2}
	gz2, err := gzip.NewReader(cr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not read gzip from %s: %s\n", xmlGzPath, err)
		os.Exit(1)
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
				fmt.Fprintf(os.Stderr, "\r  %d entries processed (%.0fs elapsed, %.0f%%, %.1f/%.0f MB)...",
					n, elapsed, pct, mbRead, mbTotal)
			case <-pass2Done:
				return
			}
		}
	}()

	count, err := dblpImportFromReader(gz2, nameMap, builder, func(n int) {
		processed.Store(int64(n))
	})
	close(pass2Done)
	fmt.Fprintf(os.Stderr, "\r\033[K  %d entries processed (%.0fs).\n", count, time.Since(pass2Start).Seconds())
	if err != nil {
		builder.abort()
		if errors.Is(err, errDblpImportInterrupted) {
			fmt.Fprintf(os.Stderr, "Import interrupted after %d entries; the current store is unchanged.\n", count)
		} else {
			fmt.Fprintf(os.Stderr, "Import failed after %d entries: %s\nThe current store is unchanged.\n", count, err)
		}
		os.Exit(1)
	}

	meta := TDblpMeta{
		XMLFile:              filepath.Base(xmlGzPath),
		LoadedAt:             time.Now().UTC().Format(time.RFC3339),
		EntryCount:           count,
		HtmlCommandsMapHash:  csvFileHash(globalFolder + "html_commands_map.csv"),
		HtmlCharacterMapHash: csvFileHash(globalFolder + "html_character_map.csv"),
		UnicodeMapHash:       csvFileHash(globalFolder + "unicode_map.csv"),
	}
	finishStart := time.Now()
	err = builder.finish(meta, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not complete %s: %s\nThe current store is unchanged.\n", dblpDBNewPath(), err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "  %s ready (%.0fs).\n", dblpDBPath(), time.Since(finishStart).Seconds())

	fmt.Fprintf(os.Stderr, "DBLP import complete: %d entries in %.1fs\n",
		count, time.Since(start).Seconds())

	if err := pushDblpStore(); err != nil {
		fmt.Fprintf(os.Stderr, "Could not copy the DBLP store to %s: %s\n", dblpBackupFolder(), err)
	}
}

// --- CLI: -update_dblp ---

const dblpXMLDownloadURL = "https://dblp.uni-trier.de/xml/dblp.xml.gz"

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

// doUpdateDblp imports a DBLP XML export that the user has downloaded by hand
// into their Downloads folder. DBLP's site added a bot check that rejects
// automated fetches, so dblp.xml.gz can no longer be pulled programmatically —
// the user must fetch it in a browser first.
// The dated destination filename is derived from the download's mtime, so
// re-running after a fresh manual download is detected as a new release; the
// previous .xml.gz is left in place (files have distinct dated names).
func doUpdateDblp() {
	dlFolder := downloadsFolder()
	if dlFolder == "" {
		fmt.Fprintf(os.Stderr, "Could not determine your Downloads folder.\n")
		os.Exit(1)
	}
	sourcePath, info := findDblpDownload(dlFolder)
	if sourcePath == "" {
		fmt.Fprintf(os.Stderr, "DBLP no longer allows automated downloads.\n")
		fmt.Fprintf(os.Stderr, "Please download %s manually in your browser,\n", dblpXMLDownloadURL)
		fmt.Fprintf(os.Stderr, "save it to %s, then re-run -update_dblp.\n", dlFolder)
		os.Exit(1)
	}

	if err := os.MkdirAll(dblpFolder(), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Could not create DBLP folder: %s\n", err)
		os.Exit(1)
	}

	latest := "dblp-" + info.ModTime().Format("2006-01-02") + ".xml.gz"
	destPath := dblpFolder() + latest
	if FileExists(destPath) {
		if readDblpCurrentXML() == latest {
			fmt.Fprintf(os.Stderr, "Already have %s — nothing to do.\n", latest)
			return
		}
		fmt.Fprintf(os.Stderr, "Found %s but import incomplete — re-importing...\n", latest)
		doLoadDblpXml([]string{destPath})
		cleanupDblpXmlFiles()
		return
	}

	compressed, err := fileIsGzipped(sourcePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not read %s: %s\n", sourcePath, err)
		os.Exit(1)
	}
	if compressed {
		fmt.Fprintf(os.Stderr, "Moving %s → %s\n", sourcePath, destPath)
		err = copyFileAtomic(sourcePath, destPath)
	} else {
		fmt.Fprintf(os.Stderr, "%s is not compressed (browser unpacked it); compressing → %s...\n", sourcePath, destPath)
		err = gzipFileAtomic(sourcePath, destPath)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not copy %s to %s: %s\n", sourcePath, destPath, err)
		os.Exit(1)
	}
	if err := os.Remove(sourcePath); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not remove %s: %s\n", sourcePath, err)
	}

	doLoadDblpXml([]string{destPath})

	// Import succeeded — keep only the two most recent XML files.
	cleanupDblpXmlFiles()
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

// doAbsorbDblpNames streams the stored DBLP XML, extracts the www-based name
// variant→canonical map, and feeds each person's names into the library's
// name_mappings table via AddNameMapping. Format variants (surname-first ↔
// natural-order) are generated so that existing library mappings in either
// ordering are merged into the DBLP canonical before adding the DBLP aliases.
// A final RenormaliseNameFields pass applies all new mappings to stored entries.
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

	mergeIfKnown := func(dblpCanonical, form string) {
		if form == "" || form == dblpCanonical {
			return
		}
		if existingID, ok := Library.NameToContributorID[form]; ok {
			existingCanonical := Library.ContributorByID[existingID].Name
			if existingCanonical != dblpCanonical {
				Library.AddNameMapping(dblpCanonical, existingCanonical)
			}
		}
	}

	isKnown := func(forms ...string) bool {
		for _, form := range forms {
			if form != "" {
				if _, ok := Library.NameToContributorID[form]; ok {
					return true
				}
				if _, ok := Library.AmbiguousNameToContributorIDs[form]; ok {
					return true
				}
			}
		}
		return false
	}

	keysLinked, absorbed, orcidsSet := 0, 0, 0
	var splitCandidates []dblpSplitCandidate

	for key, names := range keyToNames {
		// Derive the canonical in Last,First format.
		// Priority: (1) bibtex= form that already has a comma ("Lowe, David B."),
		// (2) simpleSurnameSwap of bibtex= form ("Claire {Le Goues}" → "Le Goues, Claire"),
		// (3) simpleSurnameSwap of display name ("Margot Brereton" → "Brereton, Margot"),
		// (4) fallback to display name as-is (single-token names, rare).
		bibtexForm := pm.keyToBibtex[key]
		displayForm := pm.keyToCanonical[key]
		var canonical string
		if strings.Contains(bibtexForm, ",") {
			canonical = bibtexForm
		} else if bibtexForm != "" {
			if ss := simpleSurnameSwap(bibtexForm); ss != "" {
				canonical = ss
			} else {
				canonical = bibtexForm
			}
		} else {
			canonical = simpleSurnameSwap(displayForm)
			if canonical == "" {
				canonical = displayForm
			}
		}
		if canonical == "" {
			continue
		}
		orcid := keyToOrcid[key]
		surname, generation := parseSurnameGeneration(canonical)
		naturalCanonical := swapBibTeXNameFormat(canonical)

		// Collect all name forms including the display canonical (may differ from bibtex).
		// simpleSurnameSwap bridges natural-order DBLP names to Last,First library entries
		// for the common case where no bibtex= attribute is present.
		displayCanonical := pm.keyToCanonical[key]
		allForms := []string{
			canonical, naturalCanonical,
			displayCanonical, swapBibTeXNameFormat(displayCanonical), simpleSurnameSwap(displayCanonical),
		}
		for _, name := range names {
			allForms = append(allForms, name, swapBibTeXNameFormat(name),
				simpleSurnameSwap(name), naturalOrderToSurnameFirst(name, surname, generation))
		}

		// Skip DBLP persons not represented by any contributor in this library.
		if !isKnown(allForms...) {
			continue
		}

		// Find the primary contributor FIRST so we can use their existing library
		// canonical as the merge target. This prevents creating ghost contributors
		// when the DBLP-derived canonical differs only in formatting (e.g. spacing).
		var contribID string
		for _, form := range allForms {
			if form == "" {
				continue
			}
			if id, ok := Library.NameToContributorID[form]; ok {
				contribID = id
				break
			}
			if ids, ok := Library.AmbiguousNameToContributorIDs[form]; ok && len(ids) > 0 {
				contribID = ids[0]
				break
			}
		}
		if contribID == "" {
			continue
		}
		contrib := Library.ContributorByID[contribID]
		if contrib == nil {
			continue
		}
		// Use the existing library canonical as the merge target — never a fresh
		// DBLP-derived form that may not yet exist as a contributor.
		mergeCanonical := contrib.Name

		// Merge other library contributors that are format variants into the primary.
		mergeIfKnown(mergeCanonical, naturalCanonical)
		mergeIfKnown(mergeCanonical, displayCanonical)
		mergeIfKnown(mergeCanonical, swapBibTeXNameFormat(displayCanonical))
		mergeIfKnown(mergeCanonical, simpleSurnameSwap(displayCanonical))
		for _, name := range names {
			if name != mergeCanonical {
				mergeIfKnown(mergeCanonical, name)
				mergeIfKnown(mergeCanonical, swapBibTeXNameFormat(name))
				mergeIfKnown(mergeCanonical, simpleSurnameSwap(name))
				mergeIfKnown(mergeCanonical, naturalOrderToSurnameFirst(name, surname, generation))
			}
		}

		// Add DBLP name forms and format variants as aliases of the primary.
		for _, form := range []string{naturalCanonical, displayCanonical, swapBibTeXNameFormat(displayCanonical)} {
			if form != "" && form != mergeCanonical {
				Library.AddNameMapping(mergeCanonical, form)
			}
		}
		for _, name := range names {
			if name != mergeCanonical {
				Library.AddNameMapping(mergeCanonical, name)
				absorbed++
			}
		}

		// DBLP-backed ambiguity resolution: one DBLP key = one person, so any library
		// ambiguity across these name forms is an artifact — merge the duplicates in.
		for _, form := range allForms {
			if form == "" {
				continue
			}
			if ids, ok := Library.AmbiguousNameToContributorIDs[form]; ok {
				for _, ambigID := range ids {
					if ambigID == contribID {
						continue
					}
					if c := Library.ContributorByID[ambigID]; c != nil {
						Library.AddNameMapping(mergeCanonical, c.Name)
					}
				}
			}
		}

		// Link contributor to DBLP key; if the key is already held by a different
		// contributor, DBLP proves they are the same person — merge the unanchored
		// one into the keyed holder so all contributor_roles follow.
		if contrib.DblpKey == "" {
			if existingID, conflict := Library.DblpKeyToContributorID[key]; conflict && existingID != contribID {
				existing := Library.ContributorByID[existingID]
				Library.Progress("DBLP key %s: merging %q into %q.", key, contrib.Name, existing.Name)
				if mergeContributorInDB(contribID, existingID) {
					if existing.ORCID == "" && contrib.ORCID != "" {
						existing.ORCID = contrib.ORCID
						upsertContributorORCIDToDB(existingID, contrib.ORCID, true)
					}
					for name, nid := range Library.NameToContributorID {
						if nid == contribID {
							Library.NameToContributorID[name] = existingID
						}
					}
					// Any ORCID still pointing at the absorbed contributor (not just
					// contrib.ORCID) must be redirected — otherwise a later lookup for
					// that ORCID resolves to a contributor ID that no longer exists in
					// the contributors table, and the next write through it (e.g.
					// applyDblpAuthorORCIDs) hits a FOREIGN KEY violation.
					for orcid, nid := range Library.ORCIDToContributorID {
						if nid == contribID {
							Library.ORCIDToContributorID[orcid] = existingID
						}
					}
					delete(Library.ContributorByID, contribID)
					Library.AddNameMapping(existing.Name, contrib.Name)
					keysLinked++
				}
				continue
			}
			setContributorDblpKey(&Library, contribID, key)
			keysLinked++
		} else if contrib.DblpKey != key {
			// This contributor already has a different DBLP key — the same person in
			// the library was matched by two distinct DBLP persons. Schedule a split.
			splitCandidates = append(splitCandidates, dblpSplitCandidate{
				contribID:    contribID,
				existingKey:  contrib.DblpKey,
				newKey:       key,
				newCanonical: displayCanonical,
				newName:      canonical,
				newForms:     allForms,
			})
		}

		// Set or check ORCID.
		if orcid == "" || contrib.ORCID == orcid {
			continue
		}
		if contrib.ORCID != "" {
			if otherID := Library.ORCIDToContributorID[orcid]; otherID != "" && otherID != contribID {
				makeNameAmbiguous(&Library, contrib.Name, contribID, otherID)
				retroactivelyBackfillDisambiguation(&Library, contrib.Name)
				clearContributorORCIDSeen(contribID, contrib.ORCID)
			} else {
				Library.Warning("ORCID mismatch for %q: stored %s, DBLP suggests %s — use -enrich_contributor_data to resolve",
					contrib.Name, contrib.ORCID, orcid)
				clearContributorORCIDSeen(contribID, contrib.ORCID)
			}
			continue
		}
		contrib.ORCID = orcid
		Library.ORCIDToContributorID[orcid] = contribID
		upsertContributorORCIDToDB(contribID, orcid, true)
		orcidsSet++
	}

	var splitLog *os.File
	if len(splitCandidates) > 0 {
		logPath := bibTeXFolder + bibTeXBaseName + tablesFolderSuffix + "/dblp_contributor_splits.log"
		os.MkdirAll(bibTeXFolder+bibTeXBaseName+tablesFolderSuffix, 0o755) //nolint:errcheck
		if f, err := os.Create(logPath); err == nil {
			splitLog = f
			fmt.Fprintf(splitLog, "DBLP contributor splits on %s\n\n",
				time.Now().Format("2006-01-02 15:04:05"))
		}
	}
	splitsMoved, splitsSkipped := 0, 0
	for _, sc := range splitCandidates {
		if splitContributorByDblpKeys(&Library, sc, splitLog) {
			keysLinked++
			splitsMoved++
		} else {
			splitsSkipped++
		}
	}
	if splitLog != nil {
		splitLog.Close()
	}
	if len(splitCandidates) > 0 {
		Library.Progress("    %d DBLP contributor split candidate(s): %d moved, %d skipped (see dblp_contributor_splits.log).",
			len(splitCandidates), splitsMoved, splitsSkipped)
	}
	Library.Progress("  Linked %d DBLP person key(s), absorbed %d name alias(es), set %d ORCID(s).",
		keysLinked, absorbed, orcidsSet)
	Library.RenormaliseNameFields()
}

type dblpSplitCandidate struct {
	contribID    string
	existingKey  string
	newKey       string
	newCanonical string // DBLP display canonical for readDblpPersonEntries
	newName      string // BibTeX surname-first form for the new contributor name
	newForms     []string
}

// splitMsg writes a DBLP split detail line to the log file when provided,
// or falls back to l.Progress for interactive call sites (log == nil).
func splitMsg(log *os.File, l *TBibTeXLibrary, format string, args ...any) {
	if log != nil {
		fmt.Fprintf(log, "  DBLP split: "+format+"\n", args...)
	} else {
		l.Progress("  DBLP split: "+format, args...)
	}
}

// splitContributorByDblpKeys splits a contributor that was matched by two distinct
// DBLP person keys. Entries whose DBLP key appears in the new person's publication
// index are moved to a freshly-created (or existing) contributor; the original
// contributor retains its existing DBLP key and the remaining entries.
func splitContributorByDblpKeys(l *TBibTeXLibrary, sc dblpSplitCandidate, log *os.File) bool {
	contrib := l.ContributorByID[sc.contribID]
	if contrib == nil {
		return false
	}

	// Build the set of DBLP publication keys belonging to the new person.
	newPubSet := make(map[string]bool)
	for _, k := range readDblpPersonEntries(sc.newCanonical) {
		newPubSet[k] = true
	}
	if len(newPubSet) == 0 {
		splitMsg(log, l, "no publications indexed for %s (%q) — skipped.", sc.newKey, sc.newCanonical)
		return false
	}

	// Find entries attributed to the merged contributor whose DBLP key
	// belongs to the new person.
	rows, err := db.Query(`
		SELECT DISTINCT cr.entry_key, COALESCE(be.value, '')
		FROM contributor_roles cr
		LEFT JOIN bib_entries be ON be.entry_key = cr.entry_key AND be.field = ?
		WHERE cr.contributor_id = ?`, DBLPField, sc.contribID)
	if err != nil {
		l.Warning("splitContributorByDblpKeys: query: %s", err)
		return false
	}
	var toNew []string
	for rows.Next() {
		var entryKey, dblpVal string
		if rows.Scan(&entryKey, &dblpVal) != nil {
			continue
		}
		if newPubSet[normalizeDblpKey(dblpVal)] {
			toNew = append(toNew, entryKey)
		}
	}
	rows.Close()

	if len(toNew) == 0 {
		splitMsg(log, l, "%q linked to %s; 0 entries match %s — skipped.",
			contrib.Name, sc.existingKey, sc.newKey)
		return false
	}

	// Determine the name for the new contributor.
	newName := sc.newName
	if newName == "" {
		newName = sc.newCanonical
	}

	// Create or reuse a contributor for the new DBLP person.
	newID := ""
	creatingNew := false
	if existingNewID, ok := l.NameToContributorID[newName]; ok && existingNewID != sc.contribID {
		newID = existingNewID
		if c := l.ContributorByID[newID]; c != nil && c.DblpKey == "" {
			setContributorDblpKey(l, newID, sc.newKey)
		}
	} else {
		newID = l.NewKey()
		creatingNew = true
	}

	tx, txErr := db.Begin()
	if txErr != nil {
		l.Warning("splitContributorByDblpKeys: begin tx: %s", txErr)
		return false
	}
	if creatingNew {
		if _, err := tx.Exec(
			`INSERT INTO contributors (id, name, orcid, dblp_key, garbled) VALUES (?, ?, '', ?, 0)`,
			newID, newName, sc.newKey); err != nil {
			tx.Rollback() //nolint:errcheck
			l.Warning("splitContributorByDblpKeys: insert contributor: %s", err)
			return false
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO contributor_names (id, name) VALUES (?, ?)`, newID, newName); err != nil {
			l.Warning("splitContributorByDblpKeys: insert contributor_names: %s", err)
			markDbWriteFailed(fmt.Sprintf("splitContributorByDblpKeys: insert contributor_names failed (id=%s, name=%s): %s", newID, newName, err))
		}
	}
	for _, entryKey := range toNew {
		if _, err := tx.Exec(
			`UPDATE contributor_roles SET contributor_id = ? WHERE entry_key = ? AND contributor_id = ?`,
			newID, entryKey, sc.contribID); err != nil {
			tx.Rollback() //nolint:errcheck
			l.Warning("splitContributorByDblpKeys: update roles: %s", err)
			return false
		}
		if _, err := tx.Exec(
			`UPDATE entry_contributor_names SET contributor_id = ? WHERE entry_key = ? AND contributor_id = ?`,
			newID, entryKey, sc.contribID); err != nil {
			l.Warning("splitContributorByDblpKeys: update entry_contributor_names for %s: %s", entryKey, err)
			markDbWriteFailed(fmt.Sprintf("splitContributorByDblpKeys: update entry_contributor_names failed (entry=%s, id=%s): %s", entryKey, newID, err))
		}
	}
	// Remove newName from the old contributor's names. After the split newName is a
	// canonical contributor in its own right; leaving it as an alias of the old
	// contributor creates NameAliasToName cycles on subsequent runs.
	if _, err := tx.Exec(`DELETE FROM contributor_names WHERE id = ? AND name = ?`, sc.contribID, newName); err != nil {
		l.Warning("splitContributorByDblpKeys: remove old alias: %s", err)
		markDbWriteFailed(fmt.Sprintf("splitContributorByDblpKeys: remove old alias failed (id=%s, name=%s): %s", sc.contribID, newName, err))
	}
	if err := tx.Commit(); err != nil {
		l.Warning("splitContributorByDblpKeys: commit: %s", err)
		return false
	}
	if creatingNew {
		l.ContributorByID[newID] = &TContributor{Name: newName, DblpKey: sc.newKey}
		l.NameToContributorID[newName] = newID
		l.DblpKeyToContributorID[sc.newKey] = newID
	}
	// Mirror the DB cleanup in-memory: drop any alias edge that pointed newName → old.
	if l.NameAliasToName[newName] == contrib.Name {
		delete(l.NameAliasToName, newName)
		l.NameToAliases.DeleteValueFromStringSetMap(contrib.Name, newName)
	}

	// Register DBLP name forms for the new contributor as aliases.
	for _, form := range sc.newForms {
		if form != "" && form != newName {
			l.AddNameMapping(newName, form)
		}
	}

	splitMsg(log, l, "moved %d entry/ies from %q (%s) to new contributor %q (%s).",
		len(toNew), contrib.Name, sc.existingKey, newName, sc.newKey)
	return true
}

// accumulateContributorMatchesFromEntry reads the DBLP JSON for dblpKey and
// queries contributor_roles for libraryKey. For each contributor that is either
// unkeyed or has a key not in the entry's expected author set, it tries to
// match the contributor's name against the entry's authors by comparing against
// the bibtex= form (stripped of disambiguation number) and the surname-swapped
// canonical. Matches are accumulated into contribPersonEntries (contribID →
// personKey → set of entry keys) for later batch application.
func accumulateContributorMatchesFromEntry(
	libraryKey, dblpKey string,
	pm dblpPersonMaps,
	contribPersonEntries map[string]map[string]map[string]bool,
	contribExistingKey map[string]string,
) {
	je := readDblpJSONEntry(dblpKey)
	if je == nil {
		return
	}

	type expPerson struct{ key string }
	var expected []expPerson
	expKeySet := make(map[string]bool)
	for _, p := range append(je.Authors, je.Editors...) {
		k := ""
		if p.ORCID != "" {
			k = pm.orcidToKey[p.ORCID]
		}
		if k == "" {
			k = pm.nameToKey[p.Name]
		}
		if k != "" && !expKeySet[k] {
			expKeySet[k] = true
			expected = append(expected, expPerson{k})
		}
	}
	if len(expected) == 0 {
		return
	}

	matchName := func(contribName string) string {
		contribName = strings.TrimSpace(contribName)
		for _, ep := range expected {
			bibtex := strings.TrimSpace(pm.keyToBibtex[ep.key])
			canonical := strings.TrimSpace(pm.keyToCanonical[ep.key])
			if bibtex != "" {
				if bibtex == contribName || strings.TrimSpace(simpleSurnameSwap(bibtex)) == contribName {
					return ep.key
				}
			}
			if canonical != "" {
				if canonical == contribName || strings.TrimSpace(simpleSurnameSwap(canonical)) == contribName {
					return ep.key
				}
			}
		}
		return ""
	}

	rows, err := db.Query(`
		SELECT cr.contributor_id, c.name, COALESCE(c.dblp_key, '')
		FROM contributor_roles cr
		JOIN contributors c ON c.id = cr.contributor_id
		WHERE cr.entry_key = ?`, libraryKey)
	if err != nil {
		return
	}
	for rows.Next() {
		var contribID, contribName, existingKey string
		if rows.Scan(&contribID, &contribName, &existingKey) != nil {
			continue
		}
		if strings.HasPrefix(contribName, "{") {
			continue
		}

		var matchedKey string
		if existingKey != "" {
			if expKeySet[existingKey] {
				continue // correctly attributed
			}
			matchedKey = matchName(contribName)
			if matchedKey == "" || matchedKey == existingKey {
				continue
			}
		} else {
			matchedKey = matchName(contribName)
			if matchedKey == "" {
				continue
			}
		}

		if contribPersonEntries[contribID] == nil {
			contribPersonEntries[contribID] = make(map[string]map[string]bool)
			contribExistingKey[contribID] = existingKey
		}
		if contribPersonEntries[contribID][matchedKey] == nil {
			contribPersonEntries[contribID][matchedKey] = make(map[string]bool)
		}
		contribPersonEntries[contribID][matchedKey][libraryKey] = true
	}
	rows.Close()
}

// applyContributorMatchesFromEntries processes the contributor-to-person matches
// accumulated by accumulateContributorMatchesFromEntry and applies them:
// unkeyed contributors receive the dominant person key directly; contributors
// with a mismatching key get split via splitContributorByDblpKeys. Returns the
// number of assignments and splits executed.
func applyContributorMatchesFromEntries(
	l *TBibTeXLibrary,
	pm dblpPersonMaps,
	keyToNames map[string][]string,
	contribPersonEntries map[string]map[string]map[string]bool,
	contribExistingKey map[string]string,
	splitLog *os.File,
) int {
	done := 0
	for contribID, personEntries := range contribPersonEntries {
		existingKey := contribExistingKey[contribID]

		primaryKey := ""
		maxCount := 0
		for k, entries := range personEntries {
			if len(entries) > maxCount {
				maxCount = len(entries)
				primaryKey = k
			}
		}
		if primaryKey == "" {
			continue
		}

		splitBase := existingKey
		if existingKey == "" {
			setContributorDblpKey(l, contribID, primaryKey)
			done++
			splitBase = primaryKey
		}

		for k := range personEntries {
			if existingKey == "" && k == primaryKey {
				continue
			}
			newCanonical := pm.keyToCanonical[k]
			newName := simpleSurnameSwap(newCanonical)
			if newName == "" {
				newName = newCanonical
			}
			sc := dblpSplitCandidate{
				contribID:    contribID,
				existingKey:  splitBase,
				newKey:       k,
				newCanonical: newCanonical,
				newName:      newName,
				newForms:     keyToNames[k],
			}
			if splitContributorByDblpKeys(l, sc, splitLog) {
				done++
			}
		}
	}
	return done
}

// detectMisattributedDblpEntries scans contributors that have a DBLP key and
// looks for entries attributed to them whose DBLP publication key does not
// appear in that person's publication index. For each such entry it reads the
// DBLP JSON to find the actual author's person key and schedules a split.
// Returns the number of successful splits.
func detectMisattributedDblpEntries(l *TBibTeXLibrary, pm dblpPersonMaps, keyToNames map[string][]string) int {
	type splitTarget struct{ contribID, existingKey, newKey string }
	var splits []splitTarget

	// Pre-fetch only contributor IDs that have entries with a dblp field, to
	// avoid reading person-entry files for the large majority of keyed
	// contributors whose attributed entries carry no dblp value.
	contribsWithDblpEntries := make(map[string]bool)
	{
		rows, err := db.Query(`
			SELECT DISTINCT cr.contributor_id
			FROM contributor_roles cr
			JOIN bib_entries be ON be.entry_key = cr.entry_key AND be.field = ?
			JOIN contributors c ON c.id = cr.contributor_id
			WHERE c.dblp_key IS NOT NULL AND c.dblp_key != ''`, DBLPField)
		if err == nil {
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					contribsWithDblpEntries[id] = true
				}
			}
			rows.Close()
		}
	}

	// Collect the unique DBLP person keys whose publication sets need loading.
	type candidateContrib struct {
		contribID string
		contrib   *TContributor
		canonical string
	}
	var candidates []candidateContrib
	seenDblpKey := make(map[string]bool)
	uniqueKeys := make([]struct{ dblpKey, canonical string }, 0)
	for contribID, contrib := range l.ContributorByID {
		if contrib.DblpKey == "" || !contribsWithDblpEntries[contribID] {
			continue
		}
		canonical := pm.keyToCanonical[contrib.DblpKey]
		if canonical == "" {
			continue
		}
		candidates = append(candidates, candidateContrib{contribID, contrib, canonical})
		if !seenDblpKey[contrib.DblpKey] {
			seenDblpKey[contrib.DblpKey] = true
			uniqueKeys = append(uniqueKeys, struct{ dblpKey, canonical string }{contrib.DblpKey, canonical})
		}
	}
	total := len(candidates)
	if total == 0 {
		return 0
	}

	// Load all publication sets using a fixed worker pool rather than one
	// goroutine per key.
	Library.Progress("Loading publication sets for %d unique DBLP person key(s)...", len(uniqueKeys))
	pubSets := make(map[string]map[string]bool, len(uniqueKeys))
	var pubSetsMu sync.Mutex
	var loadedCount atomic.Int64
	type workItem struct{ dblpKey, canonical string }
	workCh := make(chan workItem, len(uniqueKeys))
	for _, ki := range uniqueKeys {
		workCh <- workItem{ki.dblpKey, ki.canonical}
	}
	close(workCh)
	loadTicker := time.NewTicker(5 * time.Second)
	loadDone := make(chan struct{})
	go func() {
		defer loadTicker.Stop()
		for {
			select {
			case <-loadTicker.C:
				fmt.Fprintf(os.Stderr, "\r  %d/%d person key(s) loaded...", loadedCount.Load(), len(uniqueKeys))
			case <-loadDone:
				fmt.Fprintf(os.Stderr, "\r")
				return
			}
		}
	}()
	const workerCount = 32
	var loadWg sync.WaitGroup
	for range workerCount {
		loadWg.Add(1)
		go func() {
			defer loadWg.Done()
			for item := range workCh {
				entries := readDblpPersonEntries(item.canonical)
				ps := make(map[string]bool, len(entries))
				for _, e := range entries {
					ps[e] = true
				}
				pubSetsMu.Lock()
				pubSets[item.dblpKey] = ps
				pubSetsMu.Unlock()
				loadedCount.Add(1)
			}
		}()
	}
	loadWg.Wait()
	close(loadDone)

	Library.Progress("Scanning %d DBLP-keyed contributor(s) for misattributed entries...", total)

	jsonCache := make(map[string]*TDblpJSONEntry)

	for _, c := range candidates {
		pubSet := pubSets[c.contrib.DblpKey]
		if len(pubSet) == 0 {
			continue
		}
		rows, err := db.Query(`
			SELECT DISTINCT cr.entry_key, be.value
			FROM contributor_roles cr
			JOIN bib_entries be ON be.entry_key = cr.entry_key AND be.field = ?
			WHERE cr.contributor_id = ?`, DBLPField, c.contribID)
		if err != nil {
			continue
		}
		for rows.Next() {
			var entryKey, dblpVal string
			if rows.Scan(&entryKey, &dblpVal) != nil {
				continue
			}
			normKey := normalizeDblpKey(dblpVal)
			if normKey == "" || pubSet[normKey] {
				continue
			}
			if _, seen := jsonCache[normKey]; !seen {
				jsonCache[normKey] = readDblpJSONEntry(normKey)
			}
			je := jsonCache[normKey]
			if je == nil {
				continue
			}
			for _, person := range append(je.Authors, je.Editors...) {
				k2 := pm.nameToKey[person.Name]
				if k2 == "" || k2 == c.contrib.DblpKey {
					continue
				}
				splits = append(splits, splitTarget{c.contribID, c.contrib.DblpKey, k2})
			}
		}
		rows.Close()
	}

	done := 0
	seen := make(map[splitTarget]bool)
	for _, st := range splits {
		if seen[st] {
			continue
		}
		seen[st] = true
		newCanonical := pm.keyToCanonical[st.newKey]
		newName := simpleSurnameSwap(newCanonical)
		if newName == "" {
			newName = newCanonical
		}
		sc := dblpSplitCandidate{
			contribID:    st.contribID,
			existingKey:  st.existingKey,
			newKey:       st.newKey,
			newCanonical: newCanonical,
			newName:      newName,
			newForms:     keyToNames[st.newKey],
		}
		if splitContributorByDblpKeys(l, sc, nil) {
			done++
		}
	}
	return done
}

// detectUnkeyedContributorSplits handles contributors that have no dblp_key but
// whose attributed entries carry a dblp field. For each such entry it reads the
// DBLP JSON to identify which DBLP person authored it (via ORCID or unambiguous
// name lookup combined with a surname match against the contributor). If all
// entries map to a single person key the key is simply assigned to the
// contributor. If entries map to multiple distinct person keys the contributor
// is split: the largest group retains the original record (with its key
// assigned), and each remaining group is split off via splitContributorByDblpKeys.
// Returns the total number of successful splits + key assignments > 0.
func detectUnkeyedContributorSplits(l *TBibTeXLibrary, pm dblpPersonMaps, keyToNames map[string][]string) int {
	type candidateRow struct{ contribID, contribName, entryKey, dblpVal string }

	// dblp_key is NULL (not '') for contributors that have never been keyed.
	rows, err := db.Query(`
		SELECT cr.contributor_id, c.name, cr.entry_key, be.value
		FROM contributor_roles cr
		JOIN bib_entries be ON be.entry_key = cr.entry_key AND be.field = ?
		JOIN contributors c ON c.id = cr.contributor_id
		WHERE COALESCE(c.dblp_key, '') = ''`, DBLPField)
	if err != nil {
		return 0
	}
	var candidates []candidateRow
	for rows.Next() {
		var r candidateRow
		if rows.Scan(&r.contribID, &r.contribName, &r.entryKey, &r.dblpVal) == nil {
			candidates = append(candidates, r)
		}
	}
	rows.Close()
	if len(candidates) == 0 {
		return 0
	}
	Library.Progress("Inferring person keys for %d entry/ies (contributors without DBLP key)...", len(candidates))

	// Read each distinct DBLP entry JSON once and cache it.
	jsonCache := make(map[string]*TDblpJSONEntry)
	for i, c := range candidates {
		if i%500 == 0 {
			fmt.Fprintf(os.Stderr, "\r  reading JSON: %d/%d...", i, len(candidates))
		}
		normKey := normalizeDblpKey(c.dblpVal)
		if normKey == "" {
			continue
		}
		if _, seen := jsonCache[normKey]; !seen {
			jsonCache[normKey] = readDblpJSONEntry(normKey) // nil if not in the DBLP store
		}
	}
	fmt.Fprintf(os.Stderr, "\r")

	// contribPersonEntries: contribID → personKey → deduplicated set of entryKeys
	contribPersonEntries := make(map[string]map[string]map[string]bool)

	for _, c := range candidates {
		// Skip org names (braced) — they are not person contributors.
		if strings.HasPrefix(c.contribName, "{") {
			continue
		}
		normKey := normalizeDblpKey(c.dblpVal)
		if normKey == "" {
			continue
		}
		je := jsonCache[normKey]
		if je == nil {
			continue
		}
		// Find the person key for this contributor's author slot.
		// A candidate key is accepted only when its DBLP canonical name (swapped
		// to Last,First form) exactly matches the contributor name — this prevents
		// assigning the wrong homonym (e.g. "Mark Mulder" ≠ "Mulder, Mark A. T.").
		personKey := ""
		for _, person := range append(je.Authors, je.Editors...) {
			var k string
			if person.ORCID != "" {
				k = pm.orcidToKey[person.ORCID]
			}
			if k == "" {
				k = pm.nameToKey[person.Name]
			}
			if k == "" {
				continue
			}
			if strings.TrimSpace(simpleSurnameSwap(pm.keyToCanonical[k])) == strings.TrimSpace(c.contribName) {
				personKey = k
				break
			}
		}
		if personKey == "" {
			continue
		}
		if contribPersonEntries[c.contribID] == nil {
			contribPersonEntries[c.contribID] = make(map[string]map[string]bool)
		}
		if contribPersonEntries[c.contribID][personKey] == nil {
			contribPersonEntries[c.contribID][personKey] = make(map[string]bool)
		}
		contribPersonEntries[c.contribID][personKey][c.entryKey] = true
	}

	done := 0
	for contribID, personEntries := range contribPersonEntries {
		if len(personEntries) == 0 {
			continue
		}
		// Find the primary key (most entries).
		primaryKey := ""
		maxCount := 0
		for k, entries := range personEntries {
			if len(entries) > maxCount {
				maxCount = len(entries)
				primaryKey = k
			}
		}
		// Assign primary key to the contributor.
		setContributorDblpKey(l, contribID, primaryKey)
		done++
		if len(personEntries) == 1 {
			continue
		}
		// Split off each remaining person key.
		for k := range personEntries {
			if k == primaryKey {
				continue
			}
			newCanonical := pm.keyToCanonical[k]
			newName := simpleSurnameSwap(newCanonical)
			if newName == "" {
				newName = newCanonical
			}
			sc := dblpSplitCandidate{
				contribID:    contribID,
				existingKey:  primaryKey,
				newKey:       k,
				newCanonical: newCanonical,
				newName:      newName,
				newForms:     keyToNames[k],
			}
			if splitContributorByDblpKeys(l, sc, nil) {
				done++
			}
		}
	}
	return done
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
	orcidsSet, keysLinked, merged, checked := 0, 0, 0, 0

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
			if key == "" {
				key = pm.nameToKey[contrib.Name]
			}
			if key == "" {
				if swapped := swapBibTeXNameFormat(contrib.Name); swapped != "" {
					key = pm.nameToKey[swapped]
				}
			}
			if key != "" {
				// If another contributor already holds this DBLP key, DBLP proves
				// they are the same person — merge the key-less contributor into the
				// existing holder so all contributor_roles follow.
				if existingID, conflict := Library.DblpKeyToContributorID[key]; conflict && existingID != id {
					existing := Library.ContributorByID[existingID]
					Library.Progress("DBLP key %s: merging %q into %q.", key, contrib.Name, existing.Name)
					if mergeContributorInDB(id, existingID) {
						if existing.ORCID == "" && contrib.ORCID != "" {
							existing.ORCID = contrib.ORCID
							upsertContributorORCIDToDB(existingID, contrib.ORCID, true)
						}
						for name, nid := range Library.NameToContributorID {
							if nid == id {
								Library.NameToContributorID[name] = existingID
							}
						}
						// See the matching comment at the first DBLP-key merge site above:
						// any ORCID still pointing at the absorbed contributor must be
						// redirected, not just contrib.ORCID, or a later lookup resolves to
						// a deleted contributor ID and the next write through it hits a
						// FOREIGN KEY violation.
						for orcid, nid := range Library.ORCIDToContributorID {
							if nid == id {
								Library.ORCIDToContributorID[orcid] = existingID
							}
						}
						delete(Library.ContributorByID, id)
						Library.AddNameMapping(existing.Name, contrib.Name)
						merged++
					}
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
	if merged > 0 {
		Library.Progress("DBLP key sweep: merged %d duplicate contributor(s) into existing DBLP-keyed records.", merged)
		Library.RenormaliseNameFields()
	}
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
			if mergeContributorInDB(other.id, bestID) {
				// Update in-memory state: redirect all names from other.id to bestID.
				for name, id := range Library.NameToContributorID {
					if id == other.id {
						Library.NameToContributorID[name] = bestID
					}
				}
				// Every member here shares `orcid` by construction, so ORCIDToContributorID[orcid]
				// may currently point at other.id — redirect it, and any other ORCID that
				// somehow still points at other.id, or a later lookup resolves to a deleted
				// contributor ID and the next write through it hits a FOREIGN KEY violation.
				for o, id := range Library.ORCIDToContributorID {
					if id == other.id {
						Library.ORCIDToContributorID[o] = bestID
					}
				}
				delete(Library.ContributorByID, other.id)
				// Register the old canonical as an alias of the new canonical.
				Library.AddNameMapping(bestName, other.name)
				merged++
			}
		}
	}
	return merged
}

// doMergeOrcidDuplicates is the -merge_orcid_duplicates entry point.
func doMergeOrcidDuplicates() {
	if !openLibraryToUpdate() {
		return
	}
	merged := mergeOrcidDuplicatesCore()
	Library.Progress("Merged %d ORCID-duplicate contributor pair(s).", merged)
	if merged > 0 {
		Library.RenormaliseNameFields()
		Library.CheckNameMappingConsistency()
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
	fmt.Fprintf(os.Stderr, "\nEnriching contributor data:\n")

	Library.Progress("  Step 1/4: absorbing DBLP name mappings...")
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
	if merged > 0 {
		Library.RenormaliseNameFields()
		Library.CheckNameMappingConsistency()
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
		fmt.Fprintf(os.Stderr, "Removing old XML: %s\n", path)
		os.Remove(path)
	}
}
