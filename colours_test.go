package main

import (
	"html/template"
	"strings"
	"testing"
)

func TestColourCSS(t *testing.T) {
	got := string(colourCSS(map[string]string{"ok": "#43a047", "busy": "#f80000", "bad name!": "#000", "x": "red; } body { x"}))
	if got != "--busy: #f80000; --ok: #43a047; " {
		t.Errorf("colourCSS = %q", got)
	}
}

func TestShellColours(t *testing.T) {
	pages := template.Must(template.New("").Funcs(webTemplateFuncs()).Parse(webPages))
	var b strings.Builder
	if err := pages.ExecuteTemplate(&b, "shell", map[string]any{"Version": "x", "Library": "L",
		"ColoursLight": colourCSS(defaultColours().Light), "ColoursDark": colourCSS(defaultColours().Dark)}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "--busy: #f80000;") || !strings.Contains(b.String(), "--bg: #111111;") {
		t.Errorf("the page's style lacks the colours")
	}
}
