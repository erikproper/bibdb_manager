package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// TLoggingIO is a TUserIO that also writes everything the user sees to a log file
// (-log): messages, results, questions and the answers typed. A line that is
// redrawn in place (a progress counter, "\r\033[K…") does not end in a newline, so
// it is dropped from the log; the next "\r" overwrites it there as on screen. Each
// complete line is written at once, so a run that is stopped keeps its log.
type TLoggingIO struct {
	inner TUserIO
	file  *os.File
	mu    sync.Mutex
	line  strings.Builder // the current, not yet ended, line
	input chan string
}

var reANSIEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// logArgs are the arguments the log header shows: the run's own, or a service job's.
var logArgs = os.Args[1:]

// newLoggingIO wraps inner, appending to path under a dated header naming the
// command line.
func newLoggingIO(inner TUserIO, path string) (*TLoggingIO, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(f, "\n# %s  bibtex_check %s  %s\n", time.Now().Format("2006-01-02 15:04:05"), AppVersion, strings.Join(logArgs, " ")) //nolint:errcheck
	l := &TLoggingIO{inner: inner, file: f, input: make(chan string)}
	go func() {
		for s := range inner.Input() {
			l.log(s + "\n")
			l.input <- s
		}
		close(l.input)
	}()
	return l, nil
}

func (l *TLoggingIO) IsInteractive() bool   { return l.inner.IsInteractive() }
func (l *TLoggingIO) Input() <-chan string  { return l.input }
func (l *TLoggingIO) RequestInput()         { l.inner.RequestInput() }
func (l *TLoggingIO) WriteMessage(s string) { l.inner.WriteMessage(s); l.log(s) }
func (l *TLoggingIO) WriteResult(s string)  { l.inner.WriteResult(s); l.log(s) }

// log adds s to the log: "\r" starts the current line afresh, "\n" writes it out.
func (l *TLoggingIO) log(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range reANSIEscape.ReplaceAllString(s, "") {
		switch r {
		case '\r':
			l.line.Reset()
		case '\n':
			l.file.WriteString(l.line.String() + "\n") //nolint:errcheck
			l.line.Reset()
		default:
			l.line.WriteRune(r)
		}
	}
}

// close writes out an unfinished line (not a progress counter: those start with
// "\r" and were reset) and closes the log.
func (l *TLoggingIO) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.line.Len() > 0 {
		l.file.WriteString(l.line.String() + "\n") //nolint:errcheck
		l.line.Reset()
	}
	l.file.Close() //nolint:errcheck
}
