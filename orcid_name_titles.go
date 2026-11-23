package main

import "strings"

// nameTitles are the academic and courtesy titles ORCID users put in their names
// ("Dr. Waqar Ali", "Begic MD, MA, PhD, Prof. Edin"). They are no part of the name.
// Matched as whole tokens, ignoring case and dots; "MA" is left alone ("Ma" is a
// surname).
var nameTitles = map[string]bool{
	"dr": true, "prof": true, "professor": true, "phd": true, "md": true,
	"pe": true, "mr": true, "mrs": true, "ms": true, "dipling": true, "dring": true,
}

// stripNameTitles drops titles from an ORCID name, and the comma-separated parts
// left empty by that. A name that is nothing but titles is returned unchanged.
func stripNameTitles(name string) string {
	var parts []string
	for _, part := range strings.Split(name, ",") {
		var kept []string
		for _, tok := range strings.Fields(part) {
			key := strings.ToLower(strings.NewReplacer(".", "", "-", "").Replace(tok))
			if !nameTitles[key] {
				kept = append(kept, tok)
			}
		}
		if len(kept) > 0 {
			parts = append(parts, strings.Join(kept, " "))
		}
	}
	if len(parts) == 0 {
		return name
	}
	return strings.Join(parts, ", ")
}
