package main

import (
	"strings"
	"testing"
)

func TestSummariseAnswer(t *testing.T) {
	candidates := []string{
		"Library entry:", "  @article{EP-2024-05-25-12-25-02,", "    title = {Analysis on Demand},", "  }", "",
		"[1] journals/dke/FahlandFKLVW11 (article)", "    title       : Analysis on demand", "",
		"WARNING:  Entry EP-2024-05-25-12-25-02 has no DBLP key — found 1 candidate(s)",
	}
	got := strings.Join(summariseAnswer("QUESTION: Which DBLP entry matches? (0 = none, k = enter key manually) (0/1/k/q):", candidates, "1"), "\n")
	want := "ISSUE: Entry EP-2024-05-25-12-25-02 has no DBLP key\nACTION: Selected journals/dke/FahlandFKLVW11"
	if got != want {
		t.Errorf("candidate:\n%s\nwant\n%s", got, want)
	}
	names := []string{
		"WARNING:  Name 2 of 6 (difference 2 of 6) for entry EP-2024-05-25-12-25-02 field author:",
		"- Current:    Favre, C", "- Challenger: Favre, C{\\'e}dric",
	}
	got = strings.Join(summariseAnswer("Keep current name? (Y/N = everywhere, y/n = this entry only) (N/Y/n/y/e/d/q):", names, "N"), "\n")
	want = "CHALLENGE: Name 2 of 6 (difference 2 of 6) for entry EP-2024-05-25-12-25-02 field author:\n- Current:    Favre, C\n- Challenger: Favre, C{\\'e}dric\nACTION: Took the challenger's name, everywhere"
	if got != want {
		t.Errorf("names:\n%s\nwant\n%s", got, want)
	}
	field := []string{
		"WARNING:  For entry EP-2025-07-21-15-57-30 and field title:", "- Current   : A", "    (lineage: no known source)",
		"- Challenger: B", "    (source: dblp, priority 100)", "needs to be resolved", "QUESTION: Challenging entry:",
	}
	got = strings.Join(summariseAnswer("Keep the value as is? (n/y/q):", field, "y"), "\n")
	if !strings.HasSuffix(got, "ACTION: Kept the current title") || !strings.HasPrefix(got, "CHALLENGE: For entry") {
		t.Errorf("field challenge:\n%s", got)
	}
	if got := summariseAnswer("Merge these entries (n/y/q):", nil, "n"); got[len(got)-1] != "ACTION: Kept apart" {
		t.Errorf("merge: %v", got)
	}
}
