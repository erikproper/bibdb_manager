package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The .keys files have two columns, or three (the preferred alias).
func TestKeysColumns(t *testing.T) {
	oldDelimiter := csvDelimiter
	csvDelimiter = ";"
	defer func() { csvDelimiter = oldDelimiter }()
	for line, want := range map[string][3]string{
		"a;EP-1":                 {"a", "EP-1", ""},
		" a ; EP-1 ; pref2020x ": {"a", "EP-1", "pref2020x"},
		"a;EP-1;":                {"a", "EP-1", ""},
	} {
		if l, c, p, paired := splitKeysLine(line); !paired || [3]string{l, c, p} != want {
			t.Errorf("%q: %q %q %q %v", line, l, c, p, paired)
		}
	}
	if l, _, _, paired := splitKeysLine("EP-9"); paired || l != "EP-9" {
		t.Error("a bare key")
	}

	dir := t.TempDir()
	base := filepath.Join(dir, "refs")
	write := func(s string) { os.WriteFile(base+".keys", []byte(s), 0o644) }               //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "bib.config"), []byte(`{"file_names":"refs"}`), 0o644) //nolint:errcheck
	write("a;EP-1;x2020\nb;EP-2\nc;EP-1;x2020\n")
	if got := lookupLocalKey(dir, "a"); got != "EP-1" {
		t.Errorf("lookup: %q", got)
	}
	pairs, modified, ok := readKeysFile(base)
	if !ok || !modified || len(pairs) != 2 || pairs[0] != (TBibGetPair{"a", "EP-1"}) || pairs[1] != (TBibGetPair{"b", "EP-2"}) {
		t.Errorf("read: %v modified %v", pairs, modified) // modified: EP-1 is in it twice
	}
	write("a;EP-1;x2020\nb;EP-2\n")
	if _, modified, _ := readKeysFile(base); modified {
		t.Error("a third column alone is no reason to rewrite the file")
	}
	appendPairToKeysFile(base+".keys", "z", "EP-1")
	if data, _ := os.ReadFile(base + ".keys"); string(data) != "a;EP-1;x2020\nb;EP-2\n" {
		t.Errorf("an entry listed already was added: %q", data)
	}
	appendPairToKeysFile(base+".keys", "z", "EP-3")
	if data, _ := os.ReadFile(base + ".keys"); string(data) != "a;EP-1;x2020\nb;EP-2\nz;EP-3\n" {
		t.Errorf("not added: %q", data)
	}
}
