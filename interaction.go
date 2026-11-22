/*
 *
 * Module:    bibtex_check
 * Component:
 * - interaction
 *
 * This module is concerned with the interaction with the user.
 * For the moment, it only involves the reporting of errors and warnings.
 *
 * Creator: Henderik A. Proper (erikproper@gmail.com)
 *
 * Version of: 18.11.2026
 *
 */

package main

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// isTTY is true when a person is there (see TUserIO.IsInteractive).
// Non-interactive sessions (pipes, CI, scripts) get data-only output: all
// messages are silenced and interactive prompts fail fast so nothing blocks.
var isTTY = userIO.IsInteractive()

// readStdinLine blocks until the user types a line and returns it trimmed of
// whitespace (including any stray \r). Closed stdin (end of input, Ctrl-D) quits
// as "q" would: returning "" made the question loops ask again forever.
func readStdinLine() string {
	line, ok := <-userIO.Input()
	if !ok {
		messageln()
		quitNow()
		return "q"
	}
	return strings.TrimSpace(line)
}

// TProgressTicker renders an in-place progress counter in the messages.
// A nil receiver is safe on all methods.
// Create with (r *TInteraction).NewProgressTicker.
type TProgressTicker struct {
	label       string
	total       int // 0 = indeterminate
	current     int
	totalWidth  int // digits in total; 0 when indeterminate
	rendered    bool
	interaction *TInteraction
	lastRender  time.Time
}

// tickerRenderInterval throttles redraws for fast, tight loops (e.g. scanning
// hundreds of thousands of entries where most iterations are a no-op continue).
// Without this, Step()/SetCount() redraw on literally every call — far faster
// than a terminal or a human eye can track, seen live as a line that "keeps
// flashing in/out completely" rather than a normal steadily-updating counter.
const tickerRenderInterval = 80 * time.Millisecond

// dueToRender reports whether enough time has passed since the last redraw to
// draw another one. Always true for the very first frame (t.rendered false)
// so progress appears immediately rather than after an initial delay.
func (t *TProgressTicker) dueToRender() bool {
	return !t.rendered || time.Since(t.lastRender) >= tickerRenderInterval
}

var activeTicker *TProgressTicker

// SpinnerInterrupt clears the current ticker line so a warning/error/progress
// message can print cleanly on its own line. The ticker is not stopped; the next
// Step/Tick call redraws it.
func SpinnerInterrupt() {
	if !isTTY {
		return
	}
	if activeTicker != nil {
		message("\r\033[K")
		activeTicker.rendered = false
	}
}

// SpinnerCommit makes the current ticker line permanently visible by ending it
// with a newline. Use immediately before a multi-line interactive dialog so the
// user can see the progress count above the dialog.
func SpinnerCommit() {
	if !isTTY {
		return
	}
	if activeTicker != nil && activeTicker.rendered {
		message("\n")
		activeTicker.rendered = false
	}
}

// NewProgressTicker creates and activates a ticker with the given label and total.
// Pass total=0 for indeterminate progress (running count without a percentage).
// Returns nil (no-op) when not on a TTY, or when interaction is silenced or progress is suppressed.
// The label is prefixed by progressPrefix (if any).
func (r *TInteraction) NewProgressTicker(label string, total int) *TProgressTicker {
	if !isTTY || r.silenced || r.progressSuppressed {
		return nil
	}
	w := 0
	if total > 0 {
		w = len(fmt.Sprintf("%d", total))
	}
	t := &TProgressTicker{
		label:       r.progressPrefix + label,
		total:       total,
		totalWidth:  w,
		interaction: r,
	}
	activeTicker = t
	return t
}

// SetTotal updates the ticker's total (and the digit-width used to pad
// current/total) for subsequent renders. For a total that's really just a
// snapshot taken before the loop started — e.g. -update_all_dblp_entries,
// where checking a parent's children can create new entries mid-run — call
// this every iteration with a cheap, O(1) count (e.g. len(entryCache), not a
// full re-scan) so the percentage tracks reality instead of drifting past 100%
// and needing to be clamped.
func (t *TProgressTicker) SetTotal(total int) {
	if t == nil || total == t.total {
		return
	}
	t.total = total
	if total > 0 {
		t.totalWidth = len(fmt.Sprintf("%d", total))
	} else {
		t.totalWidth = 0
	}
}

// render redraws the progress line in place via \r\033[K (clears to EOL before
// writing, so any character the user typed on the same line is erased).
// Determined mode:   label:  N/Total (XX%)
// Indeterminate:     label: N
// Indeterminate/idle: label: ...
func (t *TProgressTicker) render() {
	if t == nil {
		return
	}
	if t.total > 0 {
		// total is a snapshot taken before the loop starts, not a true ceiling —
		// callers that also drain newly-created entries or count nested sub-items
		// (e.g. -update_all_dblp_entries counting DBLP children checked along the
		// way) can legitimately push current past it. The raw counts are still
		// useful there, but the percentage must never claim more than 100% done.
		pct := float64(t.current) * 100.0 / float64(t.total)
		if pct > 100 {
			pct = 100
		}
		messagef("\r\033[K%s:  %*d/%d (%.0f%%)",
			t.label, t.totalWidth, t.current, t.total, pct)
	} else if t.current > 0 {
		messagef("\r\033[K%s: %d", t.label, t.current)
	} else {
		messagef("\r\033[K%s: ...", t.label)
	}
	t.rendered = true
	t.lastRender = time.Now()
}

// Step increments the count by one, redraws the line, and non-blockingly checks
// whether the user typed 'q' to request a quit.
// Returns true when a graceful quit has been requested (caller should break its loop).
func (t *TProgressTicker) Step() bool {
	if t == nil {
		return false
	}
	t.current++
	t.pollForQuit()
	if t.dueToRender() {
		t.render()
	}
	return t.interaction != nil && t.interaction.QuitWasRequested()
}

// SetCount sets the count to n (without incrementing), polls for a 'q' quit
// request, redraws the line, and returns true when a graceful quit has been
// requested (caller should break its loop).
func (t *TProgressTicker) SetCount(n int) bool {
	if t == nil {
		return false
	}
	t.current = n
	t.pollForQuit()
	if t.dueToRender() {
		t.render()
	}
	return t.interaction != nil && t.interaction.QuitWasRequested()
}

// Tick redraws without incrementing, polls for a 'q' quit request, and returns
// true when a graceful quit has been requested.
// Use in time-based polling loops where no per-iteration count is available.
func (t *TProgressTicker) Tick() bool {
	if t == nil {
		return false
	}
	t.pollForQuit()
	if t.dueToRender() {
		t.render()
	}
	return t.interaction != nil && t.interaction.QuitWasRequested()
}

// pollForQuit checks, without blocking, whether the user typed "q", and quits
// gracefully (committing the work done so far) when so.
func (t *TProgressTicker) pollForQuit() {
	line, ok := pollLine()
	if ok && strings.TrimSpace(line) == "q" && t.interaction != nil {
		t.interaction.quitRequested = true
		stderrPrintf("\nInterrupted — committing work done so far before exiting...\n")
		quitNow()
	}
}

// WasAborted reports whether a graceful quit has been requested on the ticker's
// associated interaction. Use when Step()/SetCount() is called inside a
// conditional and cannot return a bool directly to the loop header.
func (t *TProgressTicker) WasAborted() bool {
	if t == nil {
		return false
	}
	return t.interaction != nil && t.interaction.QuitWasRequested()
}

// Done prints "label: done", advances to a new line, and deactivates the ticker.
func (t *TProgressTicker) Done() {
	if t == nil {
		return
	}
	if t.rendered {
		message("\r\033[K")
	}
	messagef("%s: done\n", t.label)
	t.rendered = false
	activeTicker = nil
}

// DoneQuiet clears the ticker's line (no "done" record left behind) and
// deactivates it. Use instead of Done() for a ticker nested inside a larger
// batch operation (e.g. one per parent within a bulk multi-parent pass) —
// leaving a permanent completed-line per item defeats the point of a ticker
// when there can be hundreds of items; whatever runs next just overwrites the
// same line, exactly like an in-progress Step() would.
func (t *TProgressTicker) DoneQuiet() {
	if t == nil {
		return
	}
	if t.rendered {
		message("\r\033[K")
	}
	t.rendered = false
	activeTicker = nil
}

type TInteraction struct {
	silenced           bool
	progressSuppressed bool // suppress Progress() output without silencing warnings/questions
	quitRequested      bool // set when user answers "q" to any question or ticker
	questionsAnswered  int  // total questions answered; snapshotted before/after runHarvestEntry
	deferMessages      bool
	deferredWarnings   []string
	deferredErrors     []string
	progressPrefix     string // extra indent prepended to all Progress() messages and ticker labels
}

// interrupted is set by Ctrl-C (SIGINT/SIGTERM, see main). It is one flag for every
// TInteraction: the library keeps a copy of Reporting (Initialise), so setting
// Reporting.quitRequested was not seen by the loops that ask the library or its
// tickers, and Ctrl-C did not stop e.g. the watch (2026-11-22).
var interrupted atomic.Bool

// QuitWasRequested reports whether the user asked to stop at any prompt or ticker,
// or by Ctrl-C.
func (r *TInteraction) QuitWasRequested() bool {
	return r.quitRequested || interrupted.Load()
}

// QuestionsAnswered returns the running total of user answers (WarningQuestion + ConfirmAction).
func (r *TInteraction) QuestionsAnswered() int {
	return r.questionsAnswered
}

// ResetQuestionFlag is a no-op retained for call-site compatibility; to be removed in 17.2 cleanup.
func (r *TInteraction) ResetQuestionFlag() {}

// AskForInput prints prompt and returns the trimmed line the user types.
// Typing "q" quits immediately (see quitNow) and never returns.
// In non-TTY sessions, quits immediately (see quitNow) and never returns.
func (r *TInteraction) AskForInput(prompt string) (string, error) {
	if !isTTY {
		r.quitRequested = true
		quitNow()
		return "", nil
	}
	SpinnerInterrupt()
	messagef("\nQUESTION: %s (q=quit): ", prompt)
	line := readStdinLine()
	if line == "q" {
		r.quitRequested = true
		messageln()
		quitNow()
		return "", nil
	}
	messageln()
	return line, nil
}

// Disable any output to standard out.
func (r *TInteraction) SetInteractionOff() {
	r.silenced = true
}

// Enable any output to standard out.
func (r *TInteraction) SetInteractionOn() {
	r.silenced = false
}

// Status of interaction.
func (r *TInteraction) InteractionIsOff() bool {
	return r.silenced
}

// Set the interaction status to the specified state
func (r *TInteraction) SetInteraction(status bool) {
	r.silenced = status
}

// printWarningLine prints a WARNING line immediately, bypassing any deferral.
// Clears any active ticker first and inserts a blank line before the message.
func (r *TInteraction) printWarningLine(warning string, context ...any) {
	if !isTTY {
		return
	}
	SpinnerInterrupt()
	messagef("\nWARNING:  "+warning+"\n", context...)
}

// printWarningLineGrouped is printWarningLine without the leading blank line — for a
// warning that directly follows its own related Progress line and should stay visually
// grouped with it (e.g. "Downloading PDF for X: url" / "WARNING: Download failed..."),
// rather than having the leading blank line wedge itself between the two.
func (r *TInteraction) printWarningLineGrouped(warning string, context ...any) {
	if !isTTY {
		return
	}
	SpinnerInterrupt()
	messagef("WARNING:  "+warning+"\n", context...)
}

// printErrorLine prints an ERROR line immediately, bypassing any deferral.
func (r *TInteraction) printErrorLine(errorMessage string, context ...any) {
	if !isTTY {
		return
	}
	SpinnerInterrupt()
	messagef("\nERROR:    "+errorMessage+"\n", context...)
}

// BeginDeferringMessages enables warning/error deferral and clears any buffered messages.
func (r *TInteraction) BeginDeferringMessages() {
	if !isTTY {
		return
	}
	r.deferMessages = true
	r.deferredWarnings = nil
	r.deferredErrors = nil
}

// FlushDeferredMessages stops deferral and prints buffered warnings and errors
// as indented blocks under "Warnings:" and "Errors:" section headers.
// Empty sections are omitted.
func (r *TInteraction) FlushDeferredMessages() {
	r.deferMessages = false
	if !isTTY {
		r.deferredWarnings = nil
		r.deferredErrors = nil
		return
	}
	if len(r.deferredWarnings) > 0 {
		messagef("\nWarnings:\n")
		for _, w := range r.deferredWarnings {
			messagef("  - %s\n", w)
		}
	}
	if len(r.deferredErrors) > 0 {
		messagef("\nErrors:\n")
		for _, e := range r.deferredErrors {
			messagef("  - %s\n", e)
		}
	}
	if len(r.deferredWarnings) > 0 || len(r.deferredErrors) > 0 {
		messagef("\n")
	}
	r.deferredWarnings = nil
	r.deferredErrors = nil
}

// Reporting errors.
// The error message should provide the formatting.
func (r *TInteraction) Error(errorMessage string, context ...any) bool {
	if isTTY && !r.silenced {
		if r.deferMessages {
			r.deferredErrors = append(r.deferredErrors, fmt.Sprintf(errorMessage, context...))
		} else {
			r.printErrorLine(errorMessage, context...)
		}
	}

	return true
}

// Reporting warnings.
// The warning message should provide the formatting.
func (r *TInteraction) Warning(warning string, context ...any) bool {
	if isTTY && !r.silenced {
		if r.deferMessages {
			r.deferredWarnings = append(r.deferredWarnings, fmt.Sprintf(warning, context...))
		} else {
			r.printWarningLine(warning, context...)
		}
	}

	return true
}

// WarningGrouped behaves like Warning but omits the leading blank line, so the
// warning stays visually grouped with a Progress line immediately preceding it
// instead of having a blank line wedge itself in between. Callers are responsible
// for printing their own trailing separator (e.g. stderrPrintf("\n")) once the
// group (progress line + warning) is complete.
func (r *TInteraction) WarningGrouped(warning string, context ...any) bool {
	if isTTY && !r.silenced {
		if r.deferMessages {
			r.deferredWarnings = append(r.deferredWarnings, fmt.Sprintf(warning, context...))
		} else {
			r.printWarningLineGrouped(warning, context...)
		}
	}

	return true
}

// WarningQuestion presents a question and waits for one of the listed options.
// "q" is always accepted (in addition to the named options) and quits
// immediately (see quitNow) — it never returns to the caller.
// When warning is non-empty it is printed immediately (bypassing any deferral)
// as inline context for the question.
// In non-TTY sessions, quits immediately (see quitNow) and never returns.
func (r *TInteraction) WarningQuestion(question string, options TStringSet, warning string, context ...any) string {
	return r.warningQuestionCore(question, options, warning, false, context...)
}

// WarningQuestionGrouped behaves like WarningQuestion but omits the leading blank
// line before the warning, so it stays visually grouped with whatever Progress
// output the caller printed immediately before it (e.g. an "Entry: ... / Current:
// ... / Superseded: ..." block) instead of having a blank line wedge itself in
// between.
func (r *TInteraction) WarningQuestionGrouped(question string, options TStringSet, warning string, context ...any) string {
	return r.warningQuestionCore(question, options, warning, true, context...)
}

func (r *TInteraction) warningQuestionCore(question string, options TStringSet, warning string, grouped bool, context ...any) string {
	if !isTTY {
		r.quitRequested = true
		quitNow()
		return "q"
	}
	r.questionsAnswered++

	optionSet := "("
	separator := ""
	for _, option := range options.ElementsSorted() {
		optionSet += separator + option
		separator = "/"
	}
	if !options.Contains("q") {
		optionSet += separator + "q"
	}
	optionSet += "): "

	if warning != "" {
		if grouped {
			r.printWarningLineGrouped(warning, context...)
		} else {
			r.printWarningLine(warning, context...)
		}
		message("QUESTION: " + question + " " + optionSet)
	} else {
		SpinnerInterrupt()
		message("\nQUESTION: " + question + " " + optionSet)
	}

	for {
		option := readStdinLine()
		if option == "q" {
			r.quitRequested = true
			messageln()
			quitNow()
			return "q"
		}
		if options.Contains(option) {
			messageln()
			return option
		}
		message(optionSet)
	}
}

// WarningQuestionOrdered is like WarningQuestion but uses the caller-supplied slice
// to control the display order of options (useful when ASCII sort would mis-group them).
// In non-TTY sessions, quits immediately (see quitNow) and never returns.
func (r *TInteraction) WarningQuestionOrdered(question string, ordered []string, warning string, context ...any) string {
	if !isTTY {
		r.quitRequested = true
		quitNow()
		return "q"
	}
	valid := TStringSetNew()
	hasQ := false
	optionSet := "("
	separator := ""
	for _, opt := range ordered {
		valid.Add(opt)
		if opt == "q" {
			hasQ = true
		}
		optionSet += separator + opt
		separator = "/"
	}
	if !hasQ {
		optionSet += separator + "q"
	}
	optionSet += "): "

	r.questionsAnswered++

	if warning != "" {
		r.printWarningLine(warning, context...)
		message("QUESTION: " + question + " " + optionSet)
	} else {
		SpinnerInterrupt()
		message("\nQUESTION: " + question + " " + optionSet)
	}

	for {
		option := readStdinLine()
		if option == "q" {
			r.quitRequested = true
			messageln()
			quitNow()
			return "q"
		}
		if valid.Contains(option) {
			messageln()
			return option
		}
		message(optionSet)
	}
}

// WarningYesNoQuestion presents a yes/no question; returns true for "y".
// "q" is also accepted and triggers a graceful quit (returns false).
func (r *TInteraction) WarningYesNoQuestion(question, warning string, context ...any) bool {
	options := TStringSetNew()
	options.Add("y", "n")

	answer := r.WarningQuestion(question, options, warning, context...)

	return answer == "y"
}

// statRow is a label/value/comment triple for printStatBlock.
// comment is optional; when non-empty it is printed after the value.
type statRow struct {
	label   string
	value   string
	comment string
}

// printStatBlock prints header then rows with labels left-aligned and values
// right-aligned so all columns line up regardless of label/value widths.
// An optional comment is appended after two spaces.
// No-op in non-TTY sessions.
func printStatBlock(header string, rows []statRow, trailingBlank bool) {
	if !isTTY {
		return
	}
	maxLabelLen := 0
	maxValLen := 0
	for _, r := range rows {
		if n := len(r.label) + 1; n > maxLabelLen { // +1 for the colon
			maxLabelLen = n
		}
		if n := len(r.value); n > maxValLen {
			maxValLen = n
		}
	}
	messagef("\n%s\n", header)
	for _, r := range rows {
		if r.comment != "" {
			messagef("  %-*s  %*s  %s\n", maxLabelLen, r.label+":", maxValLen, r.value, r.comment)
		} else {
			messagef("  %-*s  %*s\n", maxLabelLen, r.label+":", maxValLen, r.value)
		}
	}
	if trailingBlank {
		messagef("\n")
	}
}

// ConfirmAction always prompts the user for y/n/q confirmation, even when the
// interaction is silenced. Use for safety gates that must not be skipped
// by batch-mode callers. "q" quits immediately (see quitNow) and never returns.
// In non-TTY sessions, quits immediately (see quitNow) and never returns.
func (r *TInteraction) ConfirmAction(prompt string) bool {
	if !isTTY {
		r.quitRequested = true
		quitNow()
		return false
	}
	r.questionsAnswered++
	SpinnerInterrupt()
	messagef("\nCONFIRM:  %s (y/n/q): ", prompt)
	for {
		answer := readStdinLine()
		if answer == "y" || answer == "n" {
			messageln()
			return answer == "y"
		}
		if answer == "q" {
			r.quitRequested = true
			messageln()
			quitNow()
			return false
		}
		message("(y/n/q): ")
	}
}

// Reporting progres.
// The progress message should provide the formatting.
func (r *TInteraction) Progress(progress string, context ...any) bool {
	if isTTY && !r.silenced && !r.progressSuppressed {
		SpinnerInterrupt()
		messagef(r.progressPrefix+progress+"\n", context...)
	}
	return true
}
