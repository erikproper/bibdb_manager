package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// resolveUserKey turns a key given on the command line into a library key. It
// first maps it through the key aliases. When the library does not know the
// result, and the current directory is a project with a bib.config, it looks
// the key up as a local key in that project's .keys files: with key_mapping, a
// project cites its own keys (feltus2021towards), which only its .keys file
// links to the library key.
func resolveUserKey(rawKey string) string {
	key := Library.MapEntryKey(cleanKey(rawKey))
	if Library.EntryExists(key) {
		return key
	}
	if canonical := lookupLocalKey(".", key); canonical != "" {
		return Library.MapEntryKey(canonical)
	}
	return key
}

// lookupLocalKey returns the library key that a .keys file in dir maps
// localKey to, or "" when there is none. It reads only file_names from
// dir/bib.config and does not write anything back.
func lookupLocalKey(dir, localKey string) string {
	data, err := os.ReadFile(filepath.Join(dir, "bib.config"))
	if err != nil {
		return ""
	}
	var cfg struct {
		FileNames string `json:"file_names"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return ""
	}
	for _, name := range strings.Split(cfg.FileNames, ";") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		keys, err := os.ReadFile(filepath.Join(dir, name+KeysFileExtension))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(keys), "\n") {
			parts := strings.SplitN(line, csvDelimiter, 2)
			if len(parts) == 2 && strings.TrimSpace(parts[0]) == localKey {
				if canonical := strings.TrimSpace(parts[1]); canonical != "" {
					return canonical
				}
			}
		}
	}
	return ""
}
