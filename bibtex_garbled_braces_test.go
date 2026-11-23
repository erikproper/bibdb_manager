package main

import "testing"

func TestGarbledNameIgnoresCommasInBraces(t *testing.T) {
	for _, name := range []string{"{SofTech, Inc.}", "{University of Arizona, Tucson}", "{Last}, {First}", "Proper, Jr., Henderik A."} {
		if isGarbledContributorName(name) {
			t.Errorf("%q: flagged as garbled", name)
		}
	}
	for _, name := range []string{"Proper, Erik, Henderik, A.", "Jr. J. A.} {Bubenko", "{}Proper", "Proper, Erik and Kelsen, Pierre"} {
		if !isGarbledContributorName(name) {
			t.Errorf("%q: not flagged as garbled", name)
		}
	}
}
