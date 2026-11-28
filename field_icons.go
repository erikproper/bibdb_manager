/*
 *
 * Module:    bibdb_manager
 * Component:
 * - web
 *   - field_icons
 *
 * A field's link with an icon (Erik, 2026-11-27): the settings' field_urls give a
 * field a URL ("$": its value) and, optionally, an icon. In the lists of entries, an
 * entry without a PDF and without a source to download one from shows the icon of
 * such a field it has, a link to the computed URL (ResearchGate: not every document
 * there has a PDF to download, so it is a place to look, not a download).
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 27.11.2026
 *
 */

package main

import (
	"database/sql"
	"encoding/json"
	"html/template"
	"sort"
	"strings"
)

// TFieldURL is a field's link: its URL ("$": the value) and an optional icon (an
// image's URL, or a few characters). In the settings it is a string (the URL) or an
// object {"url": …, "icon": …}.
type TFieldURL struct {
	URL  string `json:"url"`
	Icon string `json:"icon,omitempty"`
}

func (u *TFieldURL) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		*u = TFieldURL{URL: s}
		return nil
	}
	type plain TFieldURL
	return json.Unmarshal(data, (*plain)(u))
}

func (u TFieldURL) MarshalJSON() ([]byte, error) {
	if u.Icon == "" {
		return json.Marshal(u.URL)
	}
	type plain TFieldURL
	return json.Marshal(plain(u))
}

// TFieldIconLink is an entry's link with an icon, in a list of entries.
type TFieldIconLink struct {
	URL   string       `json:"u"`
	Icon  string       `json:"i"`
	Field string       `json:"f"`
	Image template.URL `json:"-"` // Icon as an image URL, for a template ("" when it is text)
}

// webFieldIconLink is the link with an icon of key, from the first (by name) field with
// an icon that it has; nil when none.
func webFieldIconLink(ro *sql.DB, key string) *TFieldIconLink {
	if len(fieldIcons) == 0 {
		return nil
	}
	fields := make([]string, 0, len(fieldIcons))
	for f := range fieldIcons {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(fields)), ", ")
	args := []any{key}
	for _, f := range fields {
		args = append(args, f)
	}
	values := map[string]string{}
	if rows, err := ro.Query(`SELECT field, value FROM bib_entries WHERE entry_key = ? AND field IN (`+marks+`)`, args...); err == nil {
		for rows.Next() {
			var f, v string
			if rows.Scan(&f, &v) == nil {
				values[f] = v
			}
		}
		rows.Close()
	}
	for _, f := range fields {
		if v := values[f]; v != "" {
			if u := webFieldLink(f, v); u != "" {
				l := &TFieldIconLink{URL: u, Icon: fieldIcons[f], Field: f}
				if iconIsImage(l.Icon) {
					l.Image = template.URL(l.Icon)
				}
				return l
			}
		}
	}
	return nil
}

// iconIsImage tells whether an icon is an image's URL (else it is shown as text).
func iconIsImage(icon string) bool {
	return strings.HasPrefix(icon, "https://") || strings.HasPrefix(icon, "http://") || strings.HasPrefix(icon, "data:image/")
}
