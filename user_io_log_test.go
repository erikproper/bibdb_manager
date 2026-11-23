package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tSilentIO struct{ in chan string }

func (t *tSilentIO) IsInteractive() bool  { return true }
func (t *tSilentIO) WriteMessage(string)  {}
func (t *tSilentIO) WriteResult(string)   {}
func (t *tSilentIO) Input() <-chan string { return t.in }

// TestLoggingIO: progress lines redrawn in place stay out of the log; messages,
// a question and its answer go in.
func TestLoggingIO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	in := make(chan string)
	l, err := newLoggingIO(&tSilentIO{in: in}, path)
	if err != nil {
		t.Fatal(err)
	}
	l.WriteMessage("Checking entries.\n")
	l.WriteMessage("\r\033[KEnriching:  10/100 (10%)")
	l.WriteMessage("\r\033[KEnriching:  20/100 (20%)")
	l.WriteMessage("\r\033[K")
	l.WriteMessage("Enriched 100 contributors.\n")
	l.WriteMessage("QUESTION: Keep? (y/n): ")
	in <- "y"
	if got := <-l.Input(); got != "y" {
		t.Errorf("input %q, want y", got)
	}
	l.WriteResult("done\n")
	l.close()
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{"Checking entries.", "Enriched 100 contributors.", "QUESTION: Keep? (y/n): y", "done"}
	if len(lines) != len(want)+1 || !strings.HasPrefix(lines[0], "# ") {
		t.Fatalf("log:\n%s", data)
	}
	for i, w := range want {
		if lines[i+1] != w {
			t.Errorf("line %d = %q, want %q", i+1, lines[i+1], w)
		}
	}
}
