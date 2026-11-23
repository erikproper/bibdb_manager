package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLookupLocalKey(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bib.config"), []byte(`{"file_names": "other;erik"}`), 0644)
	os.WriteFile(filepath.Join(dir, "erik.keys"), []byte("feltus2021towards;EP-2024-05-22-12-15-12\nbare-key\n"), 0644)

	if got := lookupLocalKey(dir, "feltus2021towards"); got != "EP-2024-05-22-12-15-12" {
		t.Errorf("local key: got %q", got)
	}
	if got := lookupLocalKey(dir, "bare-key"); got != "" {
		t.Errorf("bare line: got %q, want none", got)
	}
	if got := lookupLocalKey(t.TempDir(), "feltus2021towards"); got != "" {
		t.Errorf("no bib.config: got %q, want none", got)
	}
}
