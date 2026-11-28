package main

import (
	"encoding/json"
	"testing"
)

func TestFieldURLJSON(t *testing.T) {
	var m map[string]TFieldURL
	in := `{"repositum": "https://doi.org/$", "researchgate": {"url": "https://www.researchgate.net/publication/$", "icon": "https://www.researchgate.net/favicon.ico"}}`
	if err := json.Unmarshal([]byte(in), &m); err != nil {
		t.Fatal(err)
	}
	if m["repositum"].URL != "https://doi.org/$" || m["repositum"].Icon != "" || m["researchgate"].Icon != "https://www.researchgate.net/favicon.ico" {
		t.Errorf("read %+v", m)
	}
	out, _ := json.Marshal(m)
	if string(out) != `{"repositum":"https://doi.org/$","researchgate":{"url":"https://www.researchgate.net/publication/$","icon":"https://www.researchgate.net/favicon.ico"}}` {
		t.Errorf("written %s", out)
	}
}

func TestAddQuestionLinks(t *testing.T) {
	q := &TQuestion{Field: "doi", Values: []TQuestionValue{{Role: "current", Value: "10.1007/x"}},
		Entries: []TQuestionEntry{{Key: "k", Fields: []TQuestionPair{{Name: "doi", Value: "10.2307/1"}, {Name: "url", Value: "https://example.org/p"}, {Name: "year", Value: "2001"}},
			Parent: &TQuestionEntry{Fields: []TQuestionPair{{Name: "doi", Value: "10.1/p"}}}}}}
	addQuestionLinks(q)
	e := q.Entries[0]
	if q.Values[0].Link != "https://doi.org/10.1007/x" || e.Fields[0].Link != "https://doi.org/10.2307/1" || e.Fields[1].Link != "https://example.org/p" ||
		e.Fields[2].Link != "" || e.Parent.Fields[0].Link != "https://doi.org/10.1/p" {
		t.Errorf("links: %+v %+v %+v", q.Values, e.Fields, e.Parent.Fields)
	}
}
