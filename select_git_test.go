package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectGitPush(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip()
	}
	root := t.TempDir()
	remote, work := filepath.Join(root, "remote.git"), filepath.Join(root, "work")
	run := func(dir string, args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return string(out)
	}
	os.MkdirAll(work, 0755)
	run(root, "init", "-q", "--bare", remote)
	run(work, "init", "-q")
	run(work, "config", "user.email", "t@example.org")
	run(work, "config", "user.name", "t")
	os.WriteFile(filepath.Join(work, "other.txt"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(work, "refs.bib"), []byte("@misc{a,}\n"), 0644)
	os.WriteFile(filepath.Join(work, "refs.keys"), []byte("a\n"), 0644)
	run(work, "add", "other.txt")
	run(work, "commit", "-q", "-m", "first")
	run(work, "remote", "add", "origin", remote)
	run(work, "push", "-q", "-u", "origin", "HEAD")
	os.WriteFile(filepath.Join(work, "other.txt"), []byte("changed"), 0644)
	run(work, "add", "other.txt") // staged, but not one of the two files
	s := &TSelectSession{File: TSelectFile{Dir: work, Name: "refs"}}
	msg, err := s.gitPush()
	if err != nil || !strings.HasPrefix(msg, "Committed and pushed refs.bib and refs.keys") {
		t.Fatalf("gitPush = %q, %v", msg, err)
	}
	if files := run(work, "show", "--name-only", "--format=", "HEAD"); strings.Contains(files, "other.txt") || !strings.Contains(files, "refs.keys") {
		t.Errorf("the commit holds %q", files)
	}
	if staged := run(work, "diff", "--cached", "--name-only"); !strings.Contains(staged, "other.txt") {
		t.Errorf("other.txt is no longer staged: %q", staged)
	}
	if msg, _ := s.gitPush(); !strings.HasPrefix(msg, "Nothing new to commit") {
		t.Errorf("second gitPush = %q", msg)
	}
}

func TestSelectGitPushSubfolder(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip()
	}
	root := t.TempDir()
	remote, work := filepath.Join(root, "remote.git"), filepath.Join(root, "work")
	sub := filepath.Join(work, "paper", "refs")
	run := func(dir string, args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return string(out)
	}
	os.MkdirAll(sub, 0755)
	run(root, "init", "-q", "--bare", remote)
	run(work, "init", "-q")
	run(work, "config", "user.email", "t@example.org")
	run(work, "config", "user.name", "t")
	os.WriteFile(filepath.Join(work, "main.tex"), []byte("x"), 0644)
	run(work, "add", "main.tex")
	run(work, "commit", "-q", "-m", "first")
	run(work, "remote", "add", "origin", remote)
	run(work, "push", "-q", "-u", "origin", "HEAD")
	os.WriteFile(filepath.Join(sub, "refs.bib"), []byte("@misc{a,}\n"), 0644)
	os.WriteFile(filepath.Join(sub, "refs.keys"), []byte("a\n"), 0644)
	s := &TSelectSession{File: TSelectFile{Dir: sub, Name: "refs"}}
	if !s.info().Git {
		t.Fatal("a subfolder of a repository is not seen as one")
	}
	if msg, err := s.gitPush(); err != nil || !strings.HasPrefix(msg, "Committed and pushed") {
		t.Fatalf("gitPush = %q, %v", msg, err)
	}
	if files := run(work, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(files, "paper/refs/refs.bib") {
		t.Errorf("the commit holds %q", files)
	}
}
