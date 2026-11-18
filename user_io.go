/*
 *
 * Module:    bibtex_check
 * Component:
 * - interaction
 *   - user_io
 *
 * The seam between the programme and its user (plan step 22.0). All text for the
 * user and all answers from the user pass through TUserIO: messages (progress,
 * warnings, questions; on stderr), results (what a command produces; on stdout),
 * and lines typed in. Today the only implementation is the terminal. In the
 * service (step 22.2) a job talks to the client that started it instead.
 *
 * TInteraction keeps the logic (silencing, deferral, question loops); TUserIO
 * only moves text.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 18.11.2026
 *
 */

package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/mattn/go-isatty"
)

// TUserIO moves text between the programme and its user.
type TUserIO interface {
	// IsInteractive reports whether a person is there to see progress and answer
	// questions (for the terminal: stderr is a terminal).
	IsInteractive() bool
	// WriteMessage writes text meant for the person: progress, warnings, questions.
	WriteMessage(s string)
	// WriteResult writes what a command produces (entry lists, rendered BibTeX),
	// which scripts may capture.
	WriteResult(s string)
	// Input delivers the lines typed, one per receive; it is closed once input
	// has ended (or at once when there is no person). Everything that reads
	// input receives from this one channel, so no two readers compete.
	Input() <-chan string
}

// TTerminalIO is the TUserIO of a command run in a terminal: stderr, stdout, and
// stdin read by a single pump goroutine, so no two readers compete for input.
type TTerminalIO struct {
	interactive bool
	lines       chan string
}

func newTerminalIO() *TTerminalIO {
	t := &TTerminalIO{
		interactive: isatty.IsTerminal(os.Stderr.Fd()),
		lines:       make(chan string, 1),
	}
	if !t.interactive {
		close(t.lines) // no person there: no stdin reads, questions fail fast
		return t
	}
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			t.lines <- scanner.Text()
		}
		close(t.lines)
	}()
	return t
}

func (t *TTerminalIO) IsInteractive() bool   { return t.interactive }
func (t *TTerminalIO) WriteMessage(s string) { os.Stderr.WriteString(s) } //nolint:errcheck
func (t *TTerminalIO) WriteResult(s string)  { os.Stdout.WriteString(s) } //nolint:errcheck

func (t *TTerminalIO) Input() <-chan string { return t.lines }

// pollLine returns a line only when one has been typed already; it never blocks.
func pollLine() (string, bool) {
	select {
	case line, ok := <-userIO.Input():
		return line, ok
	default:
		return "", false
	}
}

// userIO is the current session's TUserIO.
var userIO TUserIO = newTerminalIO()

// message, messagef and messageln write to the user, like fmt.Fprint(os.Stderr, ...)
// and friends did. They do not filter: whether a message is wanted (interactive
// session, not silenced) is up to the caller, as before.
func message(args ...any)                 { userIO.WriteMessage(fmt.Sprint(args...)) }
func messagef(format string, args ...any) { userIO.WriteMessage(fmt.Sprintf(format, args...)) }
func messageln(args ...any)               { userIO.WriteMessage(fmt.Sprintln(args...)) }

// result, resultf and resultln write command output, like fmt.Print and friends did.
func result(args ...any)                 { userIO.WriteResult(fmt.Sprint(args...)) }
func resultf(format string, args ...any) { userIO.WriteResult(fmt.Sprintf(format, args...)) }
func resultln(args ...any)               { userIO.WriteResult(fmt.Sprintln(args...)) }
