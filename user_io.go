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
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

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
	// RequestInput asks for the next line on Input. Input is read only when
	// asked (Erik, 2026-11-23): what is typed ahead while a command runs stays
	// for the shell, and cannot answer a question not yet shown. Asking again
	// before the line came is the same request.
	RequestInput()
}

// TTerminalIO is the TUserIO of a command run in a terminal: stderr, stdout, and
// stdin read by a single pump goroutine, so no two readers compete for input.
type TTerminalIO struct {
	interactive bool
	lines       chan string
	demand      chan struct{} // a line is asked for (RequestInput)
	mu          sync.Mutex
	pending     bool // a request not yet answered
}

func newTerminalIO() *TTerminalIO {
	t := &TTerminalIO{
		interactive: isatty.IsTerminal(os.Stderr.Fd()),
		lines:       make(chan string, 1),
	}
	// A job run by the service (service.go) reads and writes pipes; whether a
	// person is there is what the command line that sent it says.
	switch os.Getenv(envInteractive) {
	case "1":
		t.interactive = true
	case "0":
		t.interactive = false
	}
	if !t.interactive {
		close(t.lines) // no person there: no stdin reads, questions fail fast
		return t
	}
	t.demand = make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for range t.demand {
			if !scanner.Scan() {
				close(t.lines)
				return
			}
			t.mu.Lock()
			t.pending = false
			t.mu.Unlock()
			t.lines <- scanner.Text()
		}
	}()
	return t
}

// RequestInput lets the pump read one line from stdin.
func (t *TTerminalIO) RequestInput() {
	if !t.interactive {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.pending {
		t.pending = true
		t.demand <- struct{}{}
	}
}

func (t *TTerminalIO) IsInteractive() bool   { return t.interactive }
func (t *TTerminalIO) WriteMessage(s string) { os.Stderr.WriteString(s) } //nolint:errcheck
func (t *TTerminalIO) WriteResult(s string)  { os.Stdout.WriteString(s) } //nolint:errcheck

func (t *TTerminalIO) Input() <-chan string { return t.lines }

// TFrameIO is the TUserIO of a job the service runs as a child process: it speaks
// the service's protocol (the frames of service.go) as a job in the service does
// (Erik, 2026-11-25: a question/answer protocol between client and server, not
// text the other side interprets). Frames go out on fd 3 (out, err, ask,
// question), answers come in on fd 4 ("in", "eof"); stdin, stdout and stderr
// stay what they are (data from a pipe, stray output).
type TFrameIO struct {
	interactive bool
	out         *frameSender
	lines       chan string
	asked       atomic.Bool
}

// envFrames tells a child process of the service to speak frames on fd 3 and 4.
const envFrames = "BIBTEX_CHECK_FRAMES"

func newFrameIO() *TFrameIO {
	t := &TFrameIO{
		interactive: os.Getenv(envInteractive) == "1",
		out:         newFrameSender(os.NewFile(3, "frames-out")),
		lines:       make(chan string, 1),
	}
	if !t.interactive {
		close(t.lines)
		return t
	}
	go func() {
		defer close(t.lines)
		dec := json.NewDecoder(bufio.NewReader(os.NewFile(4, "frames-in")))
		for {
			var f serviceFrame
			if dec.Decode(&f) != nil || f.T == "eof" {
				return
			}
			if f.T == "in" {
				t.asked.Store(false)
				setAnswerValue(f.V)
				t.lines <- strings.TrimSuffix(f.D, "\n")
			}
		}
	}()
	return t
}

func (t *TFrameIO) IsInteractive() bool   { return t.interactive }
func (t *TFrameIO) WriteMessage(s string) { t.out.send(serviceFrame{T: "err", D: s}) } //nolint:errcheck
func (t *TFrameIO) WriteResult(s string)  { t.out.send(serviceFrame{T: "out", D: s}) } //nolint:errcheck
func (t *TFrameIO) Input() <-chan string  { return t.lines }

// RequestInput asks the job's client (a command line, the Actions tab) for a line.
func (t *TFrameIO) RequestInput() {
	if t.interactive && t.asked.CompareAndSwap(false, true) {
		t.out.send(serviceFrame{T: "ask"}) //nolint:errcheck
	}
}

// newUserIO is the terminal, or the frames of a child job of the service.
func newUserIO() TUserIO {
	if os.Getenv(envFrames) == "1" {
		return newFrameIO()
	}
	return newTerminalIO()
}

// userIO is the current session's TUserIO.
var userIO TUserIO = newUserIO()

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
