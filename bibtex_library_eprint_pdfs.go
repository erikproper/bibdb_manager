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
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
var builtinEprintPDF = map[string]TEprintPDF{
	"arxiv": {URL: "https://arxiv.org/pdf/$"},
	"jstor": {URL: "https://www.jstor.org/stable/pdf/$.pdf", Browser: "Firefox", NoAuto: true},
}

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
	if v, ok := eprintPDFSetting(eprintType); !ok {
		messagef("No download URL for eprinttype %q (settings: eprint_pdf).\n", eprintType)
		return
	} else if v.Browser != "" {
		messagef("The PDFs of %s eprints are opened in %s: one entry at a time (its PDF icon, or Download PDF).\n", eprintType, v.Browser)
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

// pdfSourceURL is where an entry's PDF can be downloaded, from its fields (field
// gives a field's value; ignored tells a Bad URL): a url ending in ".pdf", else its
// eprint's download URL; "" when there is none. browser is the browser to open it
// in, when its eprinttype is opened in one (step 25.3); else "".
func pdfSourceURL(field func(string) string, ignored func(string) bool) (u, browser string) {
	if u := field("url"); u != "" && strings.HasSuffix(strings.ToLower(u), ".pdf") && !ignored(u) {
		return u, ""
	}
	typ := field("eprinttype")
	if v, ok := eprintPDFSetting(typ); ok {
		if u := eprintPDFURL(typ, field("eprint")); u != "" && !ignored(u) {
			return u, v.Browser
		}
	}
	return "", ""
}

// GetEntryPDF downloads the PDF of one entry, when it has none and a source
// (pdfSourceURL): a click on its faded PDF icon, or its pop-up's Download PDF.
func (l *TBibTeXLibrary) GetEntryPDF(key string) bool {
	filePath := l.FilesRoot + l.FilesFolder + key + ".pdf"
	if FileExists(filePath) {
		messagef("%s has a PDF already.\n", key)
		return true
	}
	u, browser := pdfSourceURL(func(f string) string { return l.EntryFieldValueity(key, f) },
		func(u string) bool { return l.URLsIgnore.Contains(u) })
	if u == "" {
		messagef("No source to download a PDF of %s from.\n", key)
		return false
	}
	if browser != "" {
		return l.getPDFInBrowser(key, l.EntryFieldValueity(key, "eprinttype"), u, browser, filePath)
	}
	l.Progress(ProgressDownloadingPDF, key, u)
	if err := downloadPDF(u, filePath); err != nil {
		l.Warning(WarningPDFDownloadFailed, key, u, err)
		return false
	}
	messagef("Downloaded the PDF of %s.\n", key)
	return l.CheckNewPDFTitle(key)
}

// loggedInAt are the eprinttypes whose site the user said to be logged in to, in
// this process (the service: until it stops).
var loggedInAt = map[string]bool{}

// browserPDFWait is how long getPDFInBrowser waits for the PDF before asking.
var browserPDFWait = 2 * time.Minute

// getPDFInBrowser gets key's PDF through a browser (step 25.3): a site that needs a
// login and forbids automated downloading (JSTOR). After the user said to be
// logged in there, it opens u in browser, which saves the PDF in Downloads (Firefox:
// Settings, Applications, PDF: Save File); the next new, finished PDF there is
// taken as this entry's (Firefox names it after the paper, not the id), checked,
// and moved to filePath. One entry at a time.
func (l *TBibTeXLibrary) getPDFInBrowser(key, eprintType, u, browser, filePath string) bool {
	site := eprintType
	if !loggedInAt[strings.ToLower(eprintType)] {
		if answerOf(&TQuestion{Kind: "choice", Key: key, Text: fmt.Sprintf("Logged in to %s in %s?", site, browser),
			Warning: fmt.Sprintf("The PDF of %s comes from %s, opened in %s, which needs you to be logged in there.", key, site, browser),
			Options: []TQuestionOption{{Key: "y", Label: "yes: open it"}, {Key: "n", Label: "not now"}, {Key: "q", Label: "stop"}}}) != "y" {
			messagef("Not downloaded: log in to %s in %s first.\n", site, browser)
			return false
		}
		loggedInAt[strings.ToLower(eprintType)] = true
	}
	dir := downloadsFolder()
	before := pdfsIn(dir)
	if err := openInNamedBrowser(browser, u); err != nil {
		l.Warning(WarningPDFDownloadFailed, key, u, err)
		return false
	}
	l.Progress("Opened %s in %s; waiting for the PDF in %s", u, browser, dir)
	deadline := time.Now().Add(browserPDFWait)
	sizes := map[string]int64{}
	for !l.QuitWasRequested() {
		if name := newFinishedPDF(dir, before, sizes); name != "" {
			from := filepath.Join(dir, name)
			if !isValidPDF(from) {
				l.Warning("Entry %s: %s in Downloads is no PDF; left there.", key, name)
				return false
			}
			if t := l.pdfTitleMismatch(key, from); t != "" &&
				!l.askPDFTitle(key, from, t, "The new PDF in Downloads ("+name+")", "yes: its metadata is wrong (take it)", "no: leave it in Downloads") {
				messagef("Not downloaded: %s (%s left in Downloads).\n", key, name)
				return false
			}
			if err := moveFile(from, filePath); err != nil {
				l.Warning("Entry %s: could not move %s: %v", key, name, err)
				return false
			}
			messagef("Downloaded the PDF of %s (%s, through %s).\n", key, name, browser)
			return true
		}
		if time.Now().After(deadline) {
			if answerOf(&TQuestion{Kind: "choice", Key: key, Text: "Wait longer?",
				Warning: fmt.Sprintf("No new PDF in %s after %s (%s, opened in %s).", dir, browserPDFWait, u, browser),
				Options: []TQuestionOption{{Key: "w", Label: "wait longer"}, {Key: "s", Label: "skip"}, {Key: "q", Label: "stop"}}}) != "w" {
				messagef("Not downloaded: %s.\n", key)
				return false
			}
			deadline = time.Now().Add(browserPDFWait)
		}
		time.Sleep(time.Second)
	}
	return false
}

// answerOf puts q to the user and answers the option chosen ("" when nobody can
// be asked, or the job stops: "q", Stop, which also stops the job's loop).
func answerOf(q *TQuestion) string {
	if !isTTY {
		return ""
	}
	valid := map[string]bool{}
	for _, o := range q.Options {
		valid[o.Key] = true
	}
	for {
		sendQuestion(q)
		a := readStdinLine()
		if a == "q" { // before the options, which offer "q" too (v30.178 returned it as an answer)
			// Stop: as Ctrl-C, which the job's loop checks (QuitWasRequested); quitNow
			// would do nothing for a job of the service after its first stop.
			interrupted.Store(true)
			return ""
		}
		if valid[a] {
			return a
		}
		q.Again = true
	}
}

// openInNamedBrowser opens address in browser: on macOS an application name
// ("Firefox"), otherwise a command.
func openInNamedBrowser(browser, address string) error {
	if runtime.GOOS == "darwin" && !strings.Contains(browser, "/") {
		return startDetached(exec.Command("open", "-a", browser, address))
	}
	return startDetached(exec.Command(browser, address))
}

// pdfsIn are the PDFs in dir, with their modification times.
func pdfsIn(dir string) map[string]time.Time {
	out := map[string]time.Time{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(strings.ToLower(e.Name()), ".pdf") {
			if info, err := e.Info(); err == nil {
				out[e.Name()] = info.ModTime()
			}
		}
	}
	return out
}

// newFinishedPDF is a PDF in dir that is new since before (or changed) and
// finished: no ".part" file beside it (Firefox's download in progress) and the
// same size as at the last look (sizes); "" when there is none yet.
func newFinishedPDF(dir string, before map[string]time.Time, sizes map[string]int64) string {
	for name, mod := range pdfsIn(dir) {
		if old, ok := before[name]; ok && !mod.After(old) {
			continue
		}
		if FileExists(filepath.Join(dir, name+".part")) {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Size() == 0 {
			continue
		}
		if sizes[name] == info.Size() {
			return name
		}
		sizes[name] = info.Size()
	}
	return ""
}

// moveFile moves from to to, also across volumes (copy, then remove).
func moveFile(from, to string) error {
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err := os.WriteFile(to, data, 0644); err != nil {
		return err
	}
	return os.Remove(from)
}
