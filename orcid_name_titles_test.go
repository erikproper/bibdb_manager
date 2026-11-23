package main

import "testing"

func TestStripNameTitles(t *testing.T) {
	for in, want := range map[string]string{
		"Dr. Waqar Ali":                 "Waqar Ali",
		"Dr Gaurav Kumar":               "Gaurav Kumar",
		"Ali, Dr. Waqar":                "Ali, Waqar",
		"Nguyen, MD, PhD, Christelle":   "Nguyen, Christelle",
		"Kramer, Ph.D., P.E., Amy":      "Kramer, Amy",
		"Prof. Dr.-Ing. Hans Müller":    "Hans Müller",
		"Ma, Xiaoli":                    "Ma, Xiaoli",
		"Proper, Henderik A.":           "Proper, Henderik A.",
		"Dr.":                           "Dr.",
	} {
		if got := stripNameTitles(in); got != want {
			t.Errorf("stripNameTitles(%q) = %q, want %q", in, got, want)
		}
	}
}
