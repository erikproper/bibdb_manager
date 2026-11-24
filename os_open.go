package main

// Calls on the operating system's desktop (Erik, 2026-11-24: keep these OS
// independent): opening a file (a PDF) or an address with the default
// application, opening the web UI in the browser (the settings' web_browser, or
// the default one), running a command line, and the Trash folder.
//
// Covered: macOS (open), Linux and other Unix desktops (xdg-open, the
// freedesktop Trash), Windows (rundll32, cmd). The rest of the programme is not
// built for Windows yet (file locks, the service's Unix session).

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// osOpenCommand is the command opening target (a file or an address) with the
// OS's default application for it.
func osOpenCommand(target string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		return exec.Command("xdg-open", target)
	}
}

// openWithOS opens target with the OS's default application, without waiting
// for it.
func openWithOS(target string) error {
	return startDetached(osOpenCommand(target))
}

// startDetached starts cmd and reaps it in the background.
func startDetached(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() //nolint:errcheck // a viewer; its exit status is of no use
	return nil
}

// shellCommand runs a command line with the OS's shell.
func shellCommand(line string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", line)
	}
	return exec.Command("sh", "-c", line)
}

// openInBrowser opens address in the settings' web_browser, or else in the
// default browser. web_browser is, on macOS, an application name or .app path
// ("Firefox", "Google Chrome", "/Applications/Safari.app"); otherwise, or when
// it holds a "/" but no ".app", a command to which the address is added
// ("firefox", "/usr/bin/chromium --new-window"; "$" in it stands for the
// address instead).
func openInBrowser(address string) error {
	if webBrowser == "" {
		return openWithOS(address)
	}
	if runtime.GOOS == "darwin" && (!strings.Contains(webBrowser, "/") || strings.HasSuffix(strings.TrimSuffix(webBrowser, "/"), ".app")) {
		return startDetached(exec.Command("open", "-a", webBrowser, address))
	}
	args := strings.Fields(webBrowser)
	if len(args) == 0 {
		return openWithOS(address)
	}
	replaced := false
	for i, a := range args {
		if strings.Contains(a, "$") {
			args[i] = strings.ReplaceAll(a, "$", address)
			replaced = true
		}
	}
	if !replaced {
		args = append(args, address)
	}
	return startDetached(exec.Command(args[0], args[1:]...))
}

// webBrowserName is the browser web_browser names, as macOS knows it ("" when
// none is set): used to find a tab already showing the web UI.
func webBrowserName() string {
	return strings.TrimSuffix(filepath.Base(strings.TrimSuffix(webBrowser, "/")), ".app")
}

// moveFileToTrash moves path to the user's Trash: ~/.Trash on macOS; the
// freedesktop Trash (~/.local/share/Trash, with its .trashinfo) elsewhere.
func moveFileToTrash(path string) bool {
	home, _ := os.UserHomeDir()
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	free := func(dir string) string {
		dest := filepath.Join(dir, base)
		if FileExists(dest) {
			dest = filepath.Join(dir, strings.TrimSuffix(base, ext)+"-"+time.Now().Format("20060102-150405")+ext)
		}
		return dest
	}
	if runtime.GOOS == "darwin" {
		return os.Rename(path, free(filepath.Join(home, ".Trash"))) == nil
	}
	trash := filepath.Join(home, ".local", "share", "Trash")
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		trash = filepath.Join(xdg, "Trash")
	}
	if os.MkdirAll(filepath.Join(trash, "files"), 0o700) != nil || os.MkdirAll(filepath.Join(trash, "info"), 0o700) != nil {
		return false
	}
	dest := free(filepath.Join(trash, "files"))
	abs, _ := filepath.Abs(path)
	info := fmt.Sprintf("[Trash Info]\nPath=%s\nDeletionDate=%s\n",
		(&url.URL{Path: abs}).EscapedPath(), time.Now().Format("2006-01-02T15:04:05"))
	if os.WriteFile(filepath.Join(trash, "info", filepath.Base(dest)+".trashinfo"), []byte(info), 0o600) != nil {
		return false
	}
	return os.Rename(path, dest) == nil
}
