/*
 *
 * Module:    bibdb_manager
 * Component:
 * - bibtex_library
 *   - bibtex_library_eprint_pdfs
 *
 * Implements -get_eprint_pdfs <eprinttype> (PROJECT.md step 25): downloads the
 * missing PDFs of the entries with that eprinttype from the eprint's site, by a
 * download URL per eprinttype ("$": the id). The stored eprint values stay as
 * they are; the id is taken from them when downloading (eprintID).
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 26.11.2026
 *
 */

package main

import (
	"net/url"
	"strings"
	"time"
)

// TEprintPDF says where the PDF of an eprinttype's eprints comes from.
type TEprintPDF struct {
	// URL is the download URL, "$" standing for the id.
	URL string `json:"url"`
	// Browser, when set, has the PDF opened in that browser (a login there; step
	// 25.3) rather than downloaded directly.
	Browser string `json:"browser,omitempty"`
	// NoAuto leaves the eprinttype out of the bulk downloads of the Actions tab: only
	// an entry's own download.
	NoAuto bool `json:"noauto,omitempty"`
}

// builtinEprintPDF are the built-in sources per eprinttype (lower case); the
// settings' eprint_pdf add to them or override them.
var builtinEprintPDF = map[string]TEprintPDF{"arxiv": {URL: "https://arxiv.org/pdf/$"}}

// eprintDownloadPause is the time between two downloads from an eprint's site
// (arXiv asks for 3 s between requests).
var eprintDownloadPause = 3 * time.Second

// eprintPDFSetting is where eprinttype's PDFs come from, if known.
func eprintPDFSetting(eprintType string) (TEprintPDF, bool) {
	t := strings.ToLower(eprintType)
	for k, v := range eprintPDF {
		if strings.ToLower(k) == t {
			return v, v.URL != ""
		}
	}
	v, ok := builtinEprintPDF[t]
	return v, ok
}

// eprintPDFURLTemplate is the download URL of eprinttype ("$": the id), or "".
func eprintPDFURLTemplate(eprintType string) string {
	v, _ := eprintPDFSetting(eprintType)
	return v.URL
}

// eprintIDPrefixes are what may stand before an id in an eprint value, per
// eprinttype (matched regardless of case): a URL of the site, a DOI.
var eprintIDPrefixes = map[string][]string{
	"arxiv": {"https://arxiv.org/abs/", "http://arxiv.org/abs/", "https://doi.org/10.48550/arxiv.", "10.48550/arxiv.", "arxiv:"},
	"jstor": {"https://www.jstor.org/stable/", "http://www.jstor.org/stable/", "https://doi.org/10.2307/", "10.2307/"},
}

// eprintID is the id in an eprint value of eprinttype: the value without a site's
// URL or DOI before it ("http://www.jstor.org/stable/3647812" → "3647812").
func eprintID(eprintType, value string) string {
	id := strings.TrimSpace(value)
	for _, p := range eprintIDPrefixes[strings.ToLower(eprintType)] {
		id = trimPrefixFold(id, p)
	}
	return id
}

// eprintPDFURL is the download URL of an entry's eprint, or "".
func eprintPDFURL(eprintType, value string) string {
	template, id := eprintPDFURLTemplate(eprintType), eprintID(eprintType, value)
	if template == "" || id == "" {
		return ""
	}
	return strings.ReplaceAll(template, "$", strings.ReplaceAll(url.PathEscape(id), "%2F", "/"))
}

// GetEprintPDFs downloads the missing PDFs of the entries with eprinttype, from the
// eprint's site, one every eprintDownloadPause; Bad URLs are skipped, and failed
// downloads go to urls_failed.csv, as for GetPDFs.
func (l *TBibTeXLibrary) GetEprintPDFs(eprintType string) {
	if !Online {
		return
	}
	if eprintPDFURLTemplate(eprintType) == "" {
		messagef("No download URL for eprinttype %q (settings: eprint_pdf).\n", eprintType)
		return
	}
	filesDir := l.FilesRoot + l.FilesFolder
	var failedLines []string
	downloaded, failed, first := 0, 0, true
	forEachBibEntryKey(func(key string) bool {
		if l.QuitWasRequested() || !strings.EqualFold(l.EntryFieldValueity(key, "eprinttype"), eprintType) {
			return !l.QuitWasRequested()
		}
		filePath := filesDir + key + ".pdf"
		if FileExists(filePath) {
			return true
		}
		u := eprintPDFURL(eprintType, l.EntryFieldValueity(key, "eprint"))
		if u == "" || l.URLsIgnore.Contains(u) {
			return true
		}
		if !first {
			time.Sleep(eprintDownloadPause)
		}
		first = false
		l.Progress(ProgressDownloadingPDF, key, u)
		if err := downloadPDF(u, filePath); err != nil {
			l.Warning(WarningPDFDownloadFailed, key, u, err)
			failedLines = append(failedLines, csvLine(u, err.Error(), time.Now().Format("2006-01-02")))
			failed++
		} else {
			l.Progress(ProgressPDFDownloaded, key, filePath)
			downloaded++
		}
		return true
	})
	if len(failedLines) > 0 {
		writeURLsFailedFile(l.FilesRoot+URLsFailedFilePath, failedLines)
	}
	messagef("%d PDF(s) downloaded, %d failed.\n", downloaded, failed)
}

// pdfSourceURL is where an entry's PDF can be downloaded directly, from its fields
// (field gives a field's value; ignored tells a Bad URL): a url ending in ".pdf",
// else its eprint's download URL; "" when there is none. An eprinttype opened in a
// browser (step 25.3) does not count yet.
func pdfSourceURL(field func(string) string, ignored func(string) bool) string {
	if u := field("url"); u != "" && strings.HasSuffix(strings.ToLower(u), ".pdf") && !ignored(u) {
		return u
	}
	typ := field("eprinttype")
	if v, ok := eprintPDFSetting(typ); ok && v.Browser == "" {
		if u := eprintPDFURL(typ, field("eprint")); u != "" && !ignored(u) {
			return u
		}
	}
	return ""
}

// GetEntryPDF downloads the PDF of one entry, when it has none and a source
// (pdfSourceURL): a click on its faded PDF icon, or its pop-up's Download PDF.
func (l *TBibTeXLibrary) GetEntryPDF(key string) bool {
	filePath := l.FilesRoot + l.FilesFolder + key + ".pdf"
	if FileExists(filePath) {
		messagef("%s has a PDF already.\n", key)
		return true
	}
	u := pdfSourceURL(func(f string) string { return l.EntryFieldValueity(key, f) },
		func(u string) bool { return l.URLsIgnore.Contains(u) })
	if u == "" {
		messagef("No source to download a PDF of %s from.\n", key)
		return false
	}
	l.Progress(ProgressDownloadingPDF, key, u)
	if err := downloadPDF(u, filePath); err != nil {
		l.Warning(WarningPDFDownloadFailed, key, u, err)
		return false
	}
	messagef("Downloaded the PDF of %s.\n", key)
	return true
}
