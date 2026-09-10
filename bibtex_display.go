/*
 *
 * Module:    bibtex_check
 * Component:
 * - bibtex_display
 *
 * Live external preview channel: displayContent writes text to display_file
 * (see the .settings file) and launches display_command to open that file in
 * an external viewer. By default (display_reopen = true) display_command runs
 * again on every call — for an app opened via `open -a`, this just brings its
 * window to the front rather than spawning a second instance, so each update
 * is both shown and surfaced. Setting display_reopen to false launches
 * display_command only once per run; later calls just overwrite display_file
 * and rely on the viewer to notice the change on its own.
 *
 * All three settings are optional; when display_file is unset, displayContent
 * is a silent no-op so callers never need to guard the call themselves.
 *
 * Creator: Henderik A. Proper (e.proper@acm.org), Luxembourg, in collaboration with Claude.ai
 *
 * Version of: 04.08.2026
 *
 */

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var displayCommandLaunched = false

// displayContent writes content to display_file, then launches display_command
// to open it — on every call when display_reopen is true (the default, so the
// viewer window comes to front each time), or only on the first call otherwise.
// No-op when display_file is not configured.
func displayContent(content string) {
	if displayFile == "" {
		return
	}
	if dir := filepath.Dir(displayFile); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not create display_file directory %s: %s\n", dir, err)
			return
		}
	}
	if err := os.WriteFile(displayFile, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write display_file %s: %s\n", displayFile, err)
		return
	}
	if displayReopen || !displayCommandLaunched {
		launchDisplayCommand()
		displayCommandLaunched = true
	}
}

// launchDisplayCommand starts display_command (a shell command line) detached from
// this process, without waiting for it to finish. Failures are non-fatal — the
// display_file is still written, just without a viewer open on it.
func launchDisplayCommand() {
	if displayCommand == "" {
		return
	}
	cmd := exec.Command("sh", "-c", displayCommand)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not run display_command %q: %s\n", displayCommand, err)
		return
	}
	go cmd.Wait() //nolint:errcheck // detached viewer process; nothing to do with its exit status
}
