package main

import (
	"flag"
	"os"
	"regexp"
	"testing"
)

// Every option said to be in the web UI exists, and every action the web UI starts
// is one that the command line still knows.
func TestMovedOptions(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`flag\.\w*Var\(&\w+, "(\w+)"`).FindAllStringSubmatch(string(src), -1) {
		defined[m[1]] = true
	}
	for name := range movedOptions {
		if !defined[name] {
			t.Errorf("-%s is no option", name)
		}
	}
	for action, a := range dialogueActions {
		for _, arg := range a.Args {
			if len(arg) > 1 && arg[0] == '-' && !defined[arg[1:]] {
				t.Errorf("action %s: %s is no option", action, arg)
			}
		}
	}
	for _, keep := range []string{"set_field", "push", "sync", "merge_entries", "harvest"} {
		if _, moved := movedOptions[keep]; moved {
			t.Errorf("-%s is kept on the command line", keep)
		}
	}
}

func TestMovedOptionsGiven(t *testing.T) {
	old := flag.CommandLine
	defer func() { flag.CommandLine = old }()
	flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
	flag.Bool("fix_duplicates", false, "")
	flag.Bool("sync", false, "")
	flag.CommandLine.Parse([]string{"-sync"}) //nolint:errcheck
	if got := movedOptionsGiven(); len(got) != 0 {
		t.Errorf("%v", got)
	}
	flag.CommandLine.Parse([]string{"-fix_duplicates"}) //nolint:errcheck
	if got := movedOptionsGiven(); len(got) != 1 || got[0] != "-fix_duplicates is in the web UI now: Actions, Checks: Fix duplicates" {
		t.Errorf("%v", got)
	}
}
