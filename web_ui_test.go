package main

import "testing"

func TestWebFieldLink(t *testing.T) {
	saved := fieldURLs
	defer func() { fieldURLs = saved }()
	fieldURLs = map[string]string{
		"repositum":    "https://doi.org/$",
		"researchgate": "https://www.researchgate.net/publication/$",
		"dblp":         "https://example.org/$",
	}
	for _, c := range []struct{ field, value, want string }{
		{"repositum", "20.500.12708/208699", "https://doi.org/20.500.12708/208699"},
		{"researchgate", "399341806", "https://www.researchgate.net/publication/399341806"},
		{"doi", "10.1007/978-3-031-1 x", "https://doi.org/10.1007/978-3-031-1%20x"},
		{"dblp", "conf/er/X21", "https://example.org/conf/er/X21"}, // the settings override
		{"url", "https://a.b/c", "https://a.b/c"},
		{"urloriginal", "https://a.b/c", "https://a.b/c"},
		{"url", "not a link", ""},
		{"note", "anything", ""},
	} {
		if got := webFieldLink(c.field, c.value); got != c.want {
			t.Errorf("webFieldLink(%q, %q) = %q, want %q", c.field, c.value, got, c.want)
		}
	}
}
