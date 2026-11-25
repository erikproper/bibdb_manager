package main

import (
	"strings"
	"testing"
)

func TestSummariseAnswer(t *testing.T) {
	cand := newChoiceQuestion(&TQuestion{Topic: "dblp_candidate", Options: []TQuestionOption{
		{Key: "1", Entry: &TQuestionEntry{Key: "journals/dke/FahlandFKLVW11", Type: "article"}}}},
		"Which DBLP entry matches? (0 = none, k = enter key manually)", []string{"0", "1", "k", "q"},
		"Entry EP-2024-05-25-12-25-02 has no DBLP key — found 1 candidate(s)")
	if cand.Text != "Which DBLP entry matches?" || cand.Options[0].Label != "none" || cand.Options[2].Label != "enter key manually" {
		t.Errorf("question: %+v", cand)
	}
	got := strings.Join(summariseAnswer(cand, TAnswer{Choice: "1"}), "\n")
	want := "ISSUE: Entry EP-2024-05-25-12-25-02 has no DBLP key\nACTION: Selected journals/dke/FahlandFKLVW11"
	if got != want {
		t.Errorf("candidate:\n%s\nwant\n%s", got, want)
	}
	if got := summariseAnswer(cand, TAnswer{Choice: "0"}); got[len(got)-1] != "ACTION: No DBLP entry matches" {
		t.Errorf("none: %v", got)
	}
	names := newChoiceQuestion(&TQuestion{Topic: "name_pair", Key: "EP-2024-05-25-12-25-02", Field: "author",
		Values: []TQuestionValue{{Role: "current", Value: "Favre, C"}, {Role: "challenger", Value: "Favre, C{\\'e}dric"}}},
		"Keep current name? (Y/N = everywhere, y/n = this entry only)", []string{"N", "Y", "n", "y", "e", "d", "q"},
		"Name 2 of 6 (difference 2 of 6) for entry EP-2024-05-25-12-25-02 field author:\n- Current:    Favre, C")
	if names.Options[0].Label != "everywhere" || names.Options[3].Label != "this entry only" {
		t.Errorf("labels: %+v", names.Options)
	}
	got = strings.Join(summariseAnswer(names, TAnswer{Choice: "N"}), "\n")
	want = "CHALLENGE: Name 2 of 6 (difference 2 of 6) for entry EP-2024-05-25-12-25-02 field author\n- Current   : Favre, C\n- Challenger: Favre, C{\\'e}dric\nACTION: Took the challenger's name, everywhere"
	if got != want {
		t.Errorf("names:\n%s\nwant\n%s", got, want)
	}
	field := newChoiceQuestion(&TQuestion{Topic: "field_challenge", Key: "EP-2025-07-21-15-57-30", Field: "title",
		Values: []TQuestionValue{{Role: "current", Value: "A"}, {Role: "challenger", Value: "B"}}},
		"Challenging entry:\n  @article{x,\n  }\nKeep the value as is? (e = edit)", []string{"n", "y", "e", "q"}, "")
	if field.Text != "Keep the value as is?" {
		t.Errorf("text: %q", field.Text)
	}
	got = strings.Join(summariseAnswer(field, TAnswer{Choice: "y"}), "\n")
	if got != "CHALLENGE: For entry EP-2025-07-21-15-57-30 and field title\n- Current   : A\n- Challenger: B\nACTION: Kept the current title" {
		t.Errorf("field challenge:\n%s", got)
	}
	merge := newChoiceQuestion(&TQuestion{Topic: "merge"}, "Merge these entries", []string{"n", "y", "q"}, "")
	if got := summariseAnswer(merge, TAnswer{Choice: "n"}); got[len(got)-1] != "ACTION: Kept apart" {
		t.Errorf("merge: %v", got)
	}
	if got := summariseAnswer(&TQuestion{Kind: "text", Text: "DBLP key"}, TAnswer{Text: "conf/er/X21"}); got[0] != "ACTION: Entered DBLP key conf/er/X21" {
		t.Errorf("text: %v", got)
	}
}

func TestActionCommand(t *testing.T) {
	split := dialogueActions["split_position"]
	if got, err := split.command([]string{"EP-1", "author", "2"}); err != nil || strings.Join(got, " ") != "-split_position EP-1 author 2" {
		t.Errorf("%v %v", got, err)
	}
	for _, bad := range [][]string{nil, {"EP-1"}, {"EP-1", "", "2"}, {"-stop", "author", "2"}, {"EP-1", "author", "2\n3"}} {
		if got, err := split.command(bad); err == nil {
			t.Errorf("%q taken: %v", bad, got)
		}
	}
	if _, err := dialogueActions["check"].command([]string{"x"}); err == nil {
		t.Error("an argument for an action without any")
	}
	if got := givenArguments("EP-1", "editor", 3); got != `["EP-1","editor","3"]` {
		t.Error(got)
	}
}
