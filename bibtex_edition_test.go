package main

import "testing"

// TestNormaliseEditionValue: a first edition goes; other editions stay.
func TestNormaliseEditionValue(t *testing.T) {
	for in, want := range map[string]string{
		"1st": "", "1": "", "1.": "", "1e": "", "First": "", "First Edition": "",
		"1st ed.": "", "{1st}": "", " first edition ": "",
		"2nd": "2nd", "3.": "3.", "Revised": "Revised", "10th": "10th", "11th": "11th",
		"Sixth Edition": "Sixth Edition",
	} {
		if got := NormaliseEditionValue(&Library, in); got != want {
			t.Errorf("NormaliseEditionValue(%q) = %q, want %q", in, got, want)
		}
	}
}
