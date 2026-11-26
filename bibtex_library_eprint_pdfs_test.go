package main

import "testing"

func TestEprintPDFURL(t *testing.T) {
	saved := eprintPDF
	defer func() { eprintPDF = saved }()
	eprintPDF = map[string]TEprintPDF{"jstor": {URL: "https://www.jstor.org/stable/pdf/$.pdf"}}
	for _, c := range []struct{ typ, value, want string }{
		{"arXiv", "2512.14425", "https://arxiv.org/pdf/2512.14425"},
		{"arXiv", "cs/0603129", "https://arxiv.org/pdf/cs/0603129"},
		{"arXiv", "1011.1669v3", "https://arxiv.org/pdf/1011.1669v3"},
		{"arxiv", "arXiv:2512.14425", "https://arxiv.org/pdf/2512.14425"},
		{"arXiv", "https://arxiv.org/abs/math.GT/0309136", "https://arxiv.org/pdf/math.GT/0309136"},
		{"jstor", "3647812", "https://www.jstor.org/stable/pdf/3647812.pdf"},
		{"jstor", "http://www.jstor.org/stable/3647812", "https://www.jstor.org/stable/pdf/3647812.pdf"},
		{"jstor", "10.2307/25148831", "https://www.jstor.org/stable/pdf/25148831.pdf"},
		{"jstor", "j.ctt1v2xw3s.3", "https://www.jstor.org/stable/pdf/j.ctt1v2xw3s.3.pdf"},
		{"pubmed", "12345", ""},
		{"arXiv", "", ""},
	} {
		if got := eprintPDFURL(c.typ, c.value); got != c.want {
			t.Errorf("eprintPDFURL(%q, %q) = %q, want %q", c.typ, c.value, got, c.want)
		}
	}
}
