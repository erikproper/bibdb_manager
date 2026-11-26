package main

import "testing"

func TestDecodePDFString(t *testing.T) {
	for in, want := range map[string]string{
		`(Defining Quality: Alternatives and Implications)`: "Defining Quality: Alternatives and Implications",
		`(A \(nested\) title\\x)`:                         `A (nested) title\x`,
		`(Caf\351)`:                                         "Café",
		`<FEFF00410042>`:                                    "AB",
	} {
		if got := decodePDFString([]byte(in)); got != want {
			t.Errorf("decodePDFString(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestTitlesPlausible(t *testing.T) {
	for _, c := range []struct {
		entry, pdf string
		want       bool
	}{
		{"Defining Quality: {Alternatives} and Implications", "Defining Quality: Alternatives and Implications", true},
		{"Advances in Enterprise Engineering {VIII} -- 4th Working Conference", "LNBIP 174 - Advances in Enterprise Engineering VIII", true},
		{"Design and Evaluation of a Web-based Modeling Platform to Support Learning", "Web-based Modeling Platform", true},
		{"Semantic Smoothing of Document Models for Agglomerative Clustering", "Exploiting Image Contents in Web Search", false},
		{"On the Move to Meaningful Internet Systems: {OTM} 2018 Workshops -- {Confederated} International Workshops: {EI2N}, {FBM}, {ICSP}, and {Meta4eS} 2018", "On the Move to Meaningful Internet Systems: OTM 2018 Workshops", true},
	} {
		if got := titlesPlausible(c.entry, c.pdf); got != c.want {
			t.Errorf("titlesPlausible(%q, %q) = %v", c.entry, c.pdf, got)
		}
	}
	for _, junk := range []string{"psdo_manuscript_R1_unlinked", "jmkg.68.1.001", "Microsoft Word - paper.doc", "Lecture Notes in Computer Science:", "PII: S1467-0895(02)00069-6"} {
		if usablePDFTitle(junk) {
			t.Errorf("usablePDFTitle(%q) = true", junk)
		}
	}
}
