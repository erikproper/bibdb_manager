package main

import "testing"

// TestSameSurname: the surname test of autoResolveByORCIDSide, on the pairs of the
// triage of 2026-11-22.
func TestSameSurname(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Gao, Jerry Zeyu", "Gao, Jerry", true},
		{"Wimmer, Maria A.", "Maria Wimmer", true},
		{"Liu, Ivy L. B.", "Libo Liu", true},
		{"Navarro-Arribas, Guillermo", "Navarro, Guillermo", true},
		{"D{\\'\\i}az-Fr{\\'\\i}as, Juan Francisco", "D{\\'\\i}az, Juan Francisco", true},
		{"Luis {Quesada Torres}", "Quesada Torres, Luis", true},
		{"Sabegh, Mohammad A. J.", "Mohammad Jabbari", false},
		{"Wolf, Karsten", "Karsten Schmidt", false},
		{"Christian Krause", "Christian Koehler", false},
		{"Li, Yi", "Lin, Yi", false},
		{"James L. Parrish Jr.", "Parrish, James", true},
		{"James L. Parrish, Jr.", "Parrish, Jr., James", true},
		{"King, Jr., Martin Luther", "Martin Luther King", true},
	}
	for _, c := range cases {
		if got := sameSurname(c.a, c.b); got != c.want {
			t.Errorf("sameSurname(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestGivenNamesFit: the given-name test of autoResolveByORCIDSide.
func TestGivenNamesFit(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Gao, Jerry Zeyu", "Gao, Jerry", true},
		{"Anderson, Kenneth Mark", "Anderson, Ken", true},
		{"Zh. B. Kalpeyeva", "Zhuldyz B. Kalpeyeva", true},
		{"Zhang, Yan-Qing", "Yanqing Zhang", true},
		{"Deng, Xuefei (Nancy)", "Xuefei Deng", true},
		{"Wimmer, Maria A.", "Maria Wimmer", true},
		{"James L. Parrish Jr.", "Parrish, James", true},
		{"Campbell, Linda E.", "Campbell, Linda J.", false},
		{"Liu, Ivy L. B.", "Libo Liu", false},
		{"Michael Barnett", "Mike Barnett", false},
	}
	for _, c := range cases {
		if got := givenNamesFit(c.a, c.b); got != c.want {
			t.Errorf("givenNamesFit(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestAutoResolveByORCIDSide: exactly one name with an ORCID and the same surname →
// that name wins; otherwise the question stays.
func TestAutoResolveByORCIDSide(t *testing.T) {
	identityFixture(t)
	Library.ContributorByID["gao"] = &TContributor{Name: "Gao, Jerry Zeyu", ORCID: "0000-0002-1051-5839"}
	Library.ContributorByID["gaoJ"] = &TContributor{Name: "Gao, Jerry"}
	Library.ContributorByID["sab"] = &TContributor{Name: "Sabegh, Mohammad A. J."}
	Library.ContributorByID["jab"] = &TContributor{Name: "Mohammad Jabbari", ORCID: "0000-0002-2172-0600"}
	Library.ContributorByID["lindaE"] = &TContributor{Name: "Campbell, Linda E.", ORCID: "0000-0002-8872-9626"}
	Library.ContributorByID["lindaJ"] = &TContributor{Name: "Campbell, Linda J."}
	Library.ContributorByID["kr"] = &TContributor{Name: "Christian Krause"}
	Library.ContributorByID["ko"] = &TContributor{Name: "Christian Koehler"}
	names := func() {
		for id, c := range Library.ContributorByID {
			Library.NameToContributorID[c.Name] = id
		}
	}
	cases := []struct {
		current, challenger, want string
		ok                        bool
	}{
		{"Gao, Jerry Zeyu", "Gao, Jerry", "Gao, Jerry Zeyu", true},
		{"Gao, Jerry", "Gao, Jerry Zeyu", "Gao, Jerry Zeyu", true},
		{"Sabegh, Mohammad A. J.", "Mohammad Jabbari", "", false},
		{"Christian Krause", "Christian Koehler", "", false},
		{"Campbell, Linda E.", "Campbell, Linda J.", "", false},
	}
	for _, c := range cases {
		names() // a global answer maps the names; each case starts afresh
		Library.orcidSideAutoResolves = nil
		got, ok := Library.autoResolveByORCIDSide("k", "author", c.current, c.challenger)
		if got != c.want || ok != c.ok {
			t.Errorf("%s / %s: got (%q, %v), want (%q, %v)", c.current, c.challenger, got, ok, c.want, c.ok)
		}
	}
}
