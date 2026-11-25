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

// splitKeysLine splits a line of a .keys file into its columns,
// local;canonical or local;canonical;preferred (Erik, 2026-11-25: the preferred alias
// as a third column, for a later program that brings the keys in the tex files up to
// date). paired is false for a line without a delimiter, a bare key (in local).
func splitKeysLine(line string) (local, canonical, preferred string, paired bool) {
	local, rest, paired := strings.Cut(strings.TrimSpace(line), csvDelimiter)
	canonical, preferred, _ = strings.Cut(rest, csvDelimiter)
	return strings.TrimSpace(local), strings.TrimSpace(canonical), strings.TrimSpace(preferred), paired
}

// keysLine is a line of a .keys file: local;canonical;preferred, without the last
// when the entry has no preferred alias.
func keysLine(local, canonical, preferred string) string {
	line := local + csvDelimiter + canonical
	if preferred != "" {
		line += csvDelimiter + preferred
	}
	return line
}

// keysFilePreferred are the preferred aliases a .keys file has, by canonical key.
func keysFilePreferred(path string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		if _, canonical, preferred, paired := splitKeysLine(line); paired && preferred != "" {
			out[canonical] = preferred
		}
	}
	return out
}

// preferredOutOfDate tells whether a preferred alias in the .keys file differs from
// the library's, or is missing from it.
func preferredOutOfDate(path string, pairs []TBibGetPair) bool {
	have := keysFilePreferred(path)
	for _, p := range pairs {
		if now := Library.PreferredKey(p.canonicalKey); now != "" && now != have[p.canonicalKey] {
			return true
		}
	}
	return false
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
			if local, canonical, _, paired := splitKeysLine(line); paired && local == localKey && canonical != "" {
				return canonical
			}
		}
	}
	return ""
}
