/*
 *
 * Module:    bibdb_manager
 * Component:
 * - web
 *   - colours
 *
 * The web UI's colours, from the settings' "colours" (Erik, 2026-11-26): one
 * meaning each, by daylight ("light") and by night ("dark", the OS's dark mode; a
 * name it lacks is the daylight one). Names: bg, card, text, muted, border, hover
 * (the page); primary, primary-text, chosen (the interface, active filters); ok
 * (fine); warn (pending); busy (the LEDs: busy changing data); danger (a dangerous
 * action); edit, editbg (editing).
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 26.11.2026
 *
 */

package main

import (
	"html/template"
	"regexp"
	"sort"
	"strings"
)

// TColours are the colours by daylight and by night: name → CSS colour.
type TColours struct {
	Light map[string]string `json:"light"`
	Dark  map[string]string `json:"dark"`
}

// webColours are the colours in use (set by loadBibTeXFolders).
var webColours = *defaultColours()

// defaultColours are the colours of a library whose settings have none.
func defaultColours() *TColours {
	return &TColours{
		Light: map[string]string{
			"bg": "#fafafa", "card": "#ffffff", "text": "#212121", "muted": "#727272", "border": "#e0e0e0", "hover": "#f0f7fc",
			"primary": "#03a9f4", "primary-text": "#ffffff", "chosen": "#01579b",
			"ok": "#43a047", "warn": "#ffa600", "busy": "#f80000", "danger": "#db4437", "edit": "#e65100", "editbg": "#fff3e0",
		},
		Dark: map[string]string{
			"bg": "#111111", "card": "#1c1c1c", "text": "#e1e1e1", "muted": "#9b9b9b", "border": "#2f2f2f", "hover": "#1f2a33",
			"primary": "#0288d1", "edit": "#ffb74d", "editbg": "#2a1c10",
		},
	}
}

var (
	reColourName  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	reColourValue = regexp.MustCompile(`^[#a-zA-Z0-9(),.% ]+$`)
)

// colourCSS is colours as CSS custom properties ("--ok: #43a047;"), in name order;
// a name or value that is no plain colour is left out.
func colourCSS(colours map[string]string) template.CSS {
	names := make([]string, 0, len(colours))
	for n := range colours {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		if v := strings.TrimSpace(colours[n]); reColourName.MatchString(n) && reColourValue.MatchString(v) {
			b.WriteString("--" + n + ": " + v + "; ")
		}
	}
	return template.CSS(b.String())
}
