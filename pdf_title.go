/*
 *
 * Module:    bibdb_manager
 * Component:
 * - pdf
 *   - pdf_title
 *
 * A PDF's own title (its metadata), as a plausibility check that a PDF is the
 * entry's (PROJECT.md step 25.3, Erik 2026-11-26): read from the file itself (the
 * Info dictionary's /Title, else the XMP dc:title), and compared with the entry's
 * title by their words. No title, or a useless one ("Microsoft Word - x.doc"):
 * no check. A wrong one can be waived per entry (MetaPropPdfTitleWaived).
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 26.11.2026
 *
 */

package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"html"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

var (
	rePDFInfoRef  = regexp.MustCompile(`/Info\s+(\d+)\s+(\d+)\s+R`)
	rePDFTitle    = regexp.MustCompile(`/Title\s*(\((?:\\.|[^\\)])*\)|<[0-9A-Fa-f\s]*>)`)
	reXMPTitle    = regexp.MustCompile(`(?s)<dc:title>\s*<rdf:Alt>\s*<rdf:li[^>/]*>([^<]*)</rdf:li>`)
	reUselessName = regexp.MustCompile(`(?i)^(microsoft (word|powerpoint)|untitled|title\b|document\d*$|pii:|lecture notes in|main title|paper\s*-|doi:|slide \d)|\.(docx?|pdf|dvi|tex|ps|rtf|indd|qxd)$|_`)
	// reSeriesPrefix is a series and volume before a PDF's title ("LNBIP 174 - ").
	reSeriesPrefix = regexp.MustCompile(`^(LNCS|LNBIP|LNAI|IFIP AICT|CCIS|AISC|LNISO|LNI)\s*\d+\s*[-–:]\s*`)
)

// pdfTitle is the title in a PDF's metadata, or "" when it has none that can be
// read without decompressing (an Info dictionary inside an object stream).
func pdfTitle(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if refs := rePDFInfoRef.FindAllSubmatch(data, -1); len(refs) > 0 {
		ref := refs[len(refs)-1]
		header := []byte(string(ref[1]) + " " + string(ref[2]) + " obj")
		for from := 0; ; {
			i := bytes.Index(data[from:], header)
			if i < 0 {
				break
			}
			at := from + i
			if at == 0 || !isPDFDigit(data[at-1]) {
				obj := data[at:]
				if end := bytes.Index(obj, []byte("endobj")); end >= 0 {
					obj = obj[:end]
				}
				if m := rePDFTitle.FindSubmatch(obj); m != nil {
					if t := strings.TrimSpace(decodePDFString(m[1])); t != "" {
						return t
					}
				}
			}
			from = at + len(header)
		}
	}
	if m := reXMPTitle.FindSubmatch(data); m != nil {
		return strings.TrimSpace(html.UnescapeString(string(m[1])))
	}
	return ""
}

func isPDFDigit(b byte) bool { return b >= '0' && b <= '9' }

// decodePDFString decodes a PDF string, literal "(…)" or hex "<…>": UTF-16 when it
// starts with a byte order mark, else PDFDocEncoding (taken as Latin-1).
func decodePDFString(s []byte) string {
	var raw []byte
	if s[0] == '<' {
		h := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, string(s[1:len(s)-1]))
		if len(h)%2 == 1 {
			h += "0"
		}
		raw, _ = hex.DecodeString(h)
	} else {
		body := s[1 : len(s)-1]
		for i := 0; i < len(body); i++ {
			c := body[i]
			if c != '\\' || i+1 >= len(body) {
				raw = append(raw, c)
				continue
			}
			i++
			switch e := body[i]; e {
			case 'n':
				raw = append(raw, '\n')
			case 'r':
				raw = append(raw, '\r')
			case 't':
				raw = append(raw, '\t')
			case 'b', 'f':
			case '\n', '\r':
			default:
				if e >= '0' && e <= '7' {
					j := i
					for j < len(body) && j < i+3 && body[j] >= '0' && body[j] <= '7' {
						j++
					}
					v, _ := strconv.ParseUint(string(body[i:j]), 8, 8)
					raw = append(raw, byte(v))
					i = j - 1
				} else {
					raw = append(raw, e)
				}
			}
		}
	}
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		u := make([]uint16, 0, len(raw)/2)
		for i := 2; i+1 < len(raw); i += 2 {
			u = append(u, uint16(raw[i])<<8|uint16(raw[i+1]))
		}
		return string(utf16.Decode(u))
	}
	r := make([]rune, len(raw))
	for i, b := range raw {
		r[i] = rune(b)
	}
	return string(r)
}

// usablePDFTitle tells whether a PDF's title says something: not a file name, not
// a word processor's placeholder, at least two words.
func usablePDFTitle(t string) bool {
	t = strings.TrimSpace(t)
	if !strings.Contains(t, " ") || reUselessName.MatchString(t) || len(titleWords(t)) < 2 {
		return false
	}
	// Garbled bytes (an encrypted PDF's strings): mostly letters and spaces, else no title.
	letters, all := 0, 0
	for _, r := range t {
		all++
		if unicode.IsLetter(r) && r < 0x250 || r == ' ' {
			letters++
		}
		if unicode.IsControl(r) {
			return false
		}
	}
	return letters*10 >= all*8
}

// titleWords are a title's words, lower case, accents and TeX gone, of three
// letters or more.
func titleWords(t string) []string {
	t = foldAccents(strings.ToLower(texToText(t)))
	var out []string
	for _, w := range strings.FieldsFunc(t, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) >= 3 {
			out = append(out, w)
		}
	}
	return out
}

// foldAccents drops the accents of the common Latin letters.
func foldAccents(s string) string {
	return strings.NewReplacer("á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a", "é", "e", "è", "e", "ê", "e", "ë", "e",
		"í", "i", "ì", "i", "î", "i", "ï", "i", "ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o", "ø", "o", "ú", "u", "ù", "u",
		"û", "u", "ü", "u", "ñ", "n", "ç", "c", "ß", "ss", "ý", "y", "ÿ", "y").Replace(s)
}

// titlesPlausible tells whether a PDF titled pdfT can be the entry titled entryT:
// the one starts with the other (a subtitle left out), or most of the entry's
// title words are in the PDF's.
func titlesPlausible(entryT, pdfT string) bool {
	e, p := titleWords(entryT), titleWords(reSeriesPrefix.ReplaceAllString(strings.TrimSpace(pdfT), ""))
	if len(e) == 0 || len(p) == 0 {
		return true
	}
	je, jp := strings.Join(e, " "), strings.Join(p, " ")
	if strings.HasPrefix(je, jp) || strings.HasPrefix(jp, je) {
		return true
	}
	inPDF := map[string]bool{}
	for _, w := range p {
		inPDF[w] = true
	}
	found := 0
	for _, w := range e {
		if inPDF[w] {
			found++
		}
	}
	if found*10 >= len(e)*6 {
		return true
	}
	// A shortened title: nearly all of the PDF's words are in the entry's title.
	inEntry := map[string]bool{}
	for _, w := range e {
		inEntry[w] = true
	}
	shared := 0
	for _, w := range p {
		if inEntry[w] {
			shared++
		}
	}
	return len(p) >= 3 && shared*10 >= len(p)*8
}

// pdfTitleMismatch is the title of the PDF at path when it is usable, the entry's
// title is known, and the two do not plausibly match, unless that very title was
// waived for the entry (MetaPropPdfTitleWaived: its metadata is wrong); "" else.
func (l *TBibTeXLibrary) pdfTitleMismatch(key, path string) string {
	entryT := l.EntryFieldValueity(key, "title")
	if entryT == "" {
		return ""
	}
	t := pdfTitle(path)
	if !usablePDFTitle(t) || titlesPlausible(entryT, t) {
		return ""
	}
	if l.GetMetadata(key, MetaPropPdfTitleWaived) == t {
		return ""
	}
	return t
}

// askPDFTitle asks what to do with a PDF of key whose title (pdfT) does not match
// the entry's. thePDF names it in the question ("The PDF of …", "The new PDF in
// Downloads"). It answers true when the PDF is to be kept as the entry's: then its
// title is waived for the entry (the metadata is wrong). Nobody to ask: a warning,
// and false.
func (l *TBibTeXLibrary) askPDFTitle(key, path, pdfT, thePDF, keep, other string) bool {
	warning := fmt.Sprintf("%s is titled %q; entry %s is titled %q.", thePDF, pdfT, key, texToText(l.EntryFieldValueity(key, "title")))
	if !isTTY {
		l.Warning("Entry %s: %s", key, warning)
		return false
	}
	if answerOf(&TQuestion{Kind: "choice", Key: key, Text: "Is it the entry's PDF?", Warning: warning, PDF: path,
		Options: []TQuestionOption{{Key: "w", Label: keep}, {Key: "s", Label: other}, {Key: "q", Label: "stop"}}}) == "w" {
		l.SetMetadata(key, MetaPropPdfTitleWaived, pdfT)
		return true
	}
	return false
}
