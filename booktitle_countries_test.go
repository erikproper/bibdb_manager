package main

import "testing"

// Country names in a title: the table's form, the braces of a braced one kept.
func TestBooktitleCountriesKeepBraces(t *testing.T) {
	l := &TBibTeXLibrary{}
	l.BooktitleCountryAliasToCanonical = TStringMap{"USA": "USA", "{USA}": "USA", "U.S.A.": "USA", "Germany": "Germany", "{Deutschland}": "Germany", "UK": "{UK}", "{UK}": "{UK}"}
	for in, want := range map[string]string{
		"{RE} 2012, Chicago, {IL}, {USA}, September 24--28": "{RE} 2012, Chicago, {IL}, {USA}, September 24--28",
		"X, Austin, U.S.A., 1996":                           "X, Austin, USA, 1996",
		"X, {U.S.A.}, 1996":                                 "X, {USA}, 1996",
		"X, {Germany}":                                      "X, {Germany}",
		"X, {Deutschland}":                                  "X, {Germany}",
		"X, UK":                                             "X, {UK}",
	} {
		if got := l.NormaliseBooktitleLocationNames(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
