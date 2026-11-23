package main

import "testing"

// TestSameNameOtherOrderAliases: the pairs of the "Ambiguous alias" warnings of
// 2026-11-23 are one name in two orders; a real difference is not.
func TestSameNameOtherOrderAliases(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"Wang, Qi", "Qi Wang", true},
		{"Wang, Chen", "Chen Wang", true},
		{"Zhang, Rui", "Rui Zhang", true},
		{"Wang, Qi", "Wang, Chen", false},
		{"Zhang, Rui", "Rui Zheng", false},
	} {
		if got := sameNameOtherOrder(c.a, c.b); got != c.want {
			t.Errorf("sameNameOtherOrder(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
