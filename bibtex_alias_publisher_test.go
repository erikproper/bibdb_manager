package main

import "testing"

// TestDeriveAliasBasePublisher: without author or editor, the alias base is the
// publisher's first word (the watch-added books of 2026-11-21).
func TestDeriveAliasBasePublisher(t *testing.T) {
	cases := []struct{ publisher, author, want string }{
		{"Springer Nature Switzerland", "", "springer2025"},
		{"Cambridge University Press", "", "cambridge2025"},
		{"IEEE", "", "ieee2025"},
		{"The MIT Press", "", "mit2025"},
		{"Springer", "Proper, Henderik A.", "proper2025"},
	}
	for _, c := range cases {
		e := &TBibTeXEntry{Key: "k", Fields: map[string]string{"publisher": c.publisher, "year": "2025"}}
		if c.author != "" {
			e.Fields["author"] = c.author
		}
		if got := Library.deriveAliasBase(e); got != c.want {
			t.Errorf("%q / %q: got %q, want %q", c.publisher, c.author, got, c.want)
		}
	}
}
