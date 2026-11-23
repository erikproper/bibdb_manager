package main

import "testing"

func TestAuthorEditorNamesEquivalentAnyOrder(t *testing.T) {
	l := &TBibTeXLibrary{}
	l.NameAliasToName = map[string]string{}
	if !authorEditorNamesEquivalentAnyOrder(l, "Liu, Kun and Li, Min and Chu He", "Liu, Kun and Min Li and Chu He") {
		t.Error("Li, Min / Min Li at one position should be equivalent")
	}
	if authorEditorNamesEquivalentAnyOrder(l, "Liu, Kun and Li, Min", "Liu, Kun and Li, Ming") {
		t.Error("different names must not be equivalent")
	}
	if authorEditorNamesEquivalentAnyOrder(l, "Liu, Kun and Li, Min", "Li, Min and Liu, Kun") {
		t.Error("a different order of authors must not be equivalent")
	}
}
