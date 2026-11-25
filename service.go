package main

// The service (plan step 22.2a). "bibtex_check -start" starts a background
// process for one library ("-serve"), listening on a Unix socket in the local DB
// folder. Any other command, given while the service runs, is handed to it: the
// command line becomes a thin client that relays its arguments, its working
// folder, its typed input and Ctrl-C, and shows the job's output. "-stop" ends
// the service. Without a service, a command runs on its own, as before.
//
// A command from the command table runs inside the service, on the library it
// keeps loaded (22.2b, service_jobs.go); the special commands run as a child
// process (the same binary, the same arguments). One job at a time; a second is
// reported as busy.
//
// The socket is only reachable by the user (mode 0600). The web UI (22.3) gets a
// localhost port, from the settings (service_port).

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// envServiceJob marks a child process run by the service as a job.
	envServiceJob = "BIBTEX_CHECK_SERVICE_JOB"
	// envInteractive tells a job whether a person is at the command line (1/0).
	envInteractive = "BIBTEX_CHECK_INTERACTIVE"

	serviceSocketExtension = ".socket"
	serviceLogExtension    = ".service.log"
)

var (
	cmdStartService   bool
	cmdStopService    bool
	cmdRestartService bool
	cmdServiceStatus  bool
	cmdServe          bool
	cmdPush           bool
	cmdLoadLibrary    bool
	cmdOpenUI         bool

	// relayingToService is set while the command line relays a job: Ctrl-C is
	// then passed on to the job instead of handled here.
	relayingToService atomic.Bool
)

// serviceFrame is one message on the socket, one JSON object per line.
//
// Command line → service: "run" (args, cwd, interactive, version, base), "in"
// (data for stdin, when no person is there), "eof" (input ended), "int" (Ctrl-C), "stop", "status".
// Service → command line: "out" (output, to stdout), "err" (log text, to stderr),
// "status" and "result" (the dialogue, ARCHITECTURE.md §15; the questions go to the
// web UI), "open" (bring the web UI up), "exit" (code), and "info" (a reply to stop/status, or
// why a run was refused, then "exit").
type serviceFrame struct {
	T           string     `json:"t"`
	D           string     `json:"d,omitempty"`
	Args        []string   `json:"args,omitempty"`
	Cwd         string     `json:"cwd,omitempty"`
	Interactive bool       `json:"interactive,omitempty"`
	Version     string     `json:"version,omitempty"`
	Base        string     `json:"base,omitempty"`
	Code        int        `json:"code,omitempty"`
	Q           *TQuestion `json:"q,omitempty"` // "question": the question the job asks next
	V           string     `json:"v,omitempty"` // "in": the value that goes with the choice in D
	S           *TStatus   `json:"s,omitempty"` // "status": where the job is
	R           *TMessage  `json:"r,omitempty"` // "result": a part of its result
}

func serviceSocketPath() string { return localDBFolder + bibTeXBaseName + serviceSocketExtension }
func serviceLogPath() string    { return localDBFolder + bibTeXBaseName + serviceLogExtension }

// dialService connects to the running service, or returns nil when none runs.
func dialService() net.Conn {
	conn, err := net.DialTimeout("unix", serviceSocketPath(), 2*time.Second)
	if err != nil {
		return nil
	}
	return conn
}

// handleServiceCommands handles -start, -stop, -restart, -status and -serve, and hands any
// other command to a running service. It returns true when the command was
// dealt with here (the run then ends); false when it runs on its own.
func handleServiceCommands() bool {
	switch {
	case cmdServe:
		runService()
		return true
	case cmdStartService:
		startService()
		openWebUIUnlessOff()
		return true
	case cmdRestartService:
		// -restart is -stop then -start (Erik, 2026-11-25), with its -ui.
		if askService("stop") {
			for i := 0; i < 600 && serviceRuns(); i++ { // the stop may still push (at most a minute)
				time.Sleep(100 * time.Millisecond)
			}
		}
		startService()
		openWebUIUnlessOff()
		return true
	case cmdStopService:
		stopService()
		return true
	case cmdServiceStatus:
		serviceStatus()
		return true
	case cmdOpenUI:
		openWebUI()
		return true
	case cmdSelect:
		doSelect()
		return true
	}
	if os.Getenv(envServiceJob) != "" {
		return false // this is the service's job itself
	}
	conn := dialService()
	if conn == nil {
		// Questions are answered in the web UI (ARCHITECTURE.md §15.5): a command that
		// may ask brings the service up. Recovery commands run on their own, as does
		// one without a person there (a script: a question then fails).
		if !userIO.IsInteractive() || runsWithoutService() {
			return false
		}
		messagef("Starting the service for %s: this command may ask something, in the web UI.\n", bibTeXBaseName)
		startService()
		if conn = dialService(); conn == nil {
			messageln("Cannot reach the service.")
			endRun(1)
		}
	}
	if cmdHarvest && len(flag.Args()) == 0 && userIO.IsInteractive() {
		path, err := saveStdin()
		if err != nil {
			messagef("Harvest: cannot read stdin: %s\n", err)
			endRun(1)
		}
		os.Args = append(os.Args, path)
	}
	endRun(relayToService(conn))
	return true
}

// runsWithoutService tells whether the command runs on its own when no service
// runs: one that only reads, the recovery commands (they may ask a yes/no
// question, at the terminal), and some that never ask.
func runsWithoutService() bool {
	if cmdRestoreFromDump || cmdRestoreBackupName != "" || cmdRestoreKeyHints || cmdRestoreDblp || cmdPushDblp ||
		cmdLoadDblpXml || cmdDeleteGarbage || cmdImport != "" || cmdImportAllCSV || cmdImportBib ||
		cmdNewKey || cmdPush || cmdLoadLibrary {
		return true
	}
	for _, name := range []string{"jobs_file", "version", "v"} {
		if f := flag.Lookup(name); f != nil && f.Value.String() != "" && f.Value.String() != "false" {
			return true
		}
	}
	return selectCommand().kind == commandRead
}

// stdinFilePrefix marks a file holding what -harvest got on stdin: the harvest
// removes it.
const stdinFilePrefix = "harvest_stdin_"

// saveStdin writes stdin to a file in the local DB folder, for the service.
func saveStdin() (string, error) {
	f, err := os.CreateTemp(localDBFolder, stdinFilePrefix+"*.bib")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, os.Stdin)
	if err = errors.Join(err, f.Close()); err != nil {
		os.Remove(f.Name()) //nolint:errcheck
		return "", err
	}
	return f.Name(), nil
}

// ---- the command line side ----

// sendFrames serialises writes to the socket (several goroutines send).
type frameSender struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func newFrameSender(w io.Writer) *frameSender { return &frameSender{enc: json.NewEncoder(w)} }

func (s *frameSender) send(f serviceFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enc.Encode(f)
}

// relayToService runs this command through the service and returns its exit code.
func relayToService(conn net.Conn) int {
	defer conn.Close()
	relayingToService.Store(true)

	cwd, _ := os.Getwd()
	interactive := userIO.IsInteractive()
	out := newFrameSender(conn)
	if err := out.send(serviceFrame{T: "run", Args: os.Args[1:], Cwd: cwd, Interactive: interactive,
		Version: AppVersion, Base: libraryBase()}); err != nil {
		messagef("Cannot reach the service: %s\n", err)
		return 1
	}

	// Ctrl-C goes to the job, which stops gracefully as a run on its own does.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			out.send(serviceFrame{T: "int"}) //nolint:errcheck
		}
	}()

	// Without a person, stdin is passed on as it is (data). With one, nothing is read:
	// questions are answered in the web UI.
	if !interactive {
		go func() {
			buf := make([]byte, 32*1024)
			for {
				n, err := os.Stdin.Read(buf)
				if n > 0 && out.send(serviceFrame{T: "in", D: string(buf[:n])}) != nil {
					return
				}
				if err != nil {
					break
				}
			}
			out.send(serviceFrame{T: "eof"}) //nolint:errcheck
		}()
	}

	dec := json.NewDecoder(bufio.NewReader(conn))
	counter := false // a counter is shown, on the line the cursor is on
	for {
		var f serviceFrame
		if err := dec.Decode(&f); err != nil {
			messageln("The service ended the connection.")
			return 1
		}
		if counter && f.T != "status" && f.T != "result" && f.T != "question" && f.T != "ask" {
			os.Stderr.WriteString("\r\033[K") //nolint:errcheck
			counter = false
		}
		switch f.T {
		case "status": // only a counter: the rest is in the log text
			if interactive && f.S != nil && f.S.Live {
				os.Stderr.WriteString("\r\033[K" + f.S.Progress) //nolint:errcheck
				counter = f.S.Progress != ""
			}
		case "out":
			os.Stdout.WriteString(f.D) //nolint:errcheck
		case "err", "info":
			os.Stderr.WriteString(f.D) //nolint:errcheck
		case "open": // the service started the job in the web UI: bring it up there
			address, hash, _ := strings.Cut(f.D, "#")
			if !reuseBrowserTabAt(address, "#"+hash) {
				openInBrowser(f.D) //nolint:errcheck
			}
		case "exit":
			return f.Code
		}
	}
}

// startService starts "-serve" in the background and waits until it listens.
func startService() {
	if conn := dialService(); conn != nil {
		conn.Close()
		messagef("The service for %s runs already.\n", bibTeXBaseName)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		messagef("Cannot find this programme: %s\n", err)
		endRun(1)
	}
	logFile, err := os.OpenFile(serviceLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		messagef("Cannot open %s: %s\n", serviceLogPath(), err)
		endRun(1)
	}
	args := []string{"-base", libraryBase(), "-serve"}
	if keyPrefixArg != "" { // a new library
		args = append(args, "-key_prefix", keyPrefixArg)
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // not stopped by the terminal's Ctrl-C or closing
	if err := cmd.Start(); err != nil {
		messagef("Cannot start the service: %s\n", err)
		endRun(1)
	}
	logFile.Close()
	pid := cmd.Process.Pid
	cmd.Process.Release() //nolint:errcheck

	for i := 0; i < 100; i++ {
		if conn := dialService(); conn != nil {
			conn.Close()
			messagef("Service for %s started (process %d). Log: %s\n", bibTeXBaseName, pid, serviceLogPath())
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	messagef("The service did not come up; see %s\n", serviceLogPath())
	endRun(1)
}

// askService sends one request (stop, status) and shows the reply.
func askService(request string) bool {
	conn := dialService()
	if conn == nil {
		return false
	}
	defer conn.Close()
	if err := newFrameSender(conn).send(serviceFrame{T: request}); err != nil {
		return false
	}
	dec := json.NewDecoder(bufio.NewReader(conn))
	for {
		var f serviceFrame
		if dec.Decode(&f) != nil || f.T == "exit" {
			return true
		}
		os.Stderr.WriteString(f.D) //nolint:errcheck
	}
}

// openWebUIUnlessOff opens the web UI after -start or -restart, which imply -ui
// (Erik, 2026-11-24), unless -ui=false is given.
func openWebUIUnlessOff() {
	uiOff := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "ui" && !cmdOpenUI {
			uiOff = true
		}
	})
	if !uiOff {
		openWebUI()
	}
}

// serviceRuns tells whether a service answers on the library's socket.
func serviceRuns() bool {
	conn := dialService()
	if conn == nil {
		return false
	}
	conn.Close()
	return true
}

// runningJob names the job the service runs (called with s.mu held): its
// command, or the title of a job it gives itself ("Loading the library"; kept in
// jobArgs in parentheses), or "stopping".
func (s *TService) runningJob() string {
	job := strings.Join(s.jobArgs, " ")
	if strings.HasPrefix(job, "(") && strings.HasSuffix(job, ")") {
		job = job[1 : len(job)-1]
	}
	if job == "" {
		job = "stopping"
	}
	return job
}

func stopService() {
	if !askService("stop") {
		messagef("No service runs for %s.\n", bibTeXBaseName)
	}
}

// waitForWebUI waits (at most 6 s) until the web UI answers on its port: a
// service just started may still be waiting for the port (see TWebUI.start).
func waitForWebUI(address string) {
	u, err := url.Parse(address)
	if err != nil {
		return
	}
	for i := 0; i < 60; i++ {
		if conn, err := net.DialTimeout("tcp", u.Host, 200*time.Millisecond); err == nil {
			conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// openWebUI opens the service's web UI in the default browser.
func openWebUI() {
	conn := dialService()
	if conn == nil {
		messagef("No service runs for %s; start it with -start.\n", bibTeXBaseName)
		endRun(1)
	}
	defer conn.Close()
	newFrameSender(conn).send(serviceFrame{T: "ui"}) //nolint:errcheck
	var f serviceFrame
	if json.NewDecoder(bufio.NewReader(conn)).Decode(&f) != nil || f.T != "out" {
		messageln("The service did not give the address of its web UI.")
		endRun(1)
	}
	address := strings.TrimSpace(f.D)
	waitForWebUI(address)
	if reuseBrowserTabAt(address, "") {
		return
	}
	if err := openInBrowser(address); err != nil {
		messagef("Open %s in a browser (%s).\n", address, err)
	}
}

// reuseBrowserTabAt brings a tab already showing the web UI to the front (Erik,
// 2026-11-24; macOS): one of Safari or Google Chrome (when running, and when
// web_browser is set, only that one) with a tab on the
// same localhost port. A tab of the same service keeps its state; one of an
// earlier service (another key) is sent to the new address. A hash ("#sel-…")
// is set on the tab either way (the page then switches tabs). False when there
// is no such tab, or macOS does not allow it (Automation permission): the
// caller then opens a new one.
func reuseBrowserTabAt(address, hash string) bool {
	u, err := url.Parse(address)
	if err != nil || runtime.GOOS != "darwin" {
		return false
	}
	prefix := u.Scheme + "://" + u.Host + "/"
	q := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) }
	// One script per browser: a script naming a browser that is not installed
	// does not compile.
	for _, script := range []string{`
if application "Safari" is running then
	tell application "Safari"
		repeat with w in windows
			repeat with t in tabs of w
				if URL of t starts with "PREFIX" then
					if URL of t does not start with "ADDRESS" or "HASH" is not "" then set URL of t to "ADDRESS" & "HASH"
					set current tab of w to t
					set index of w to 1
					activate
					return "found"
				end if
			end repeat
		end repeat
	end tell
end if
return ""`, `
if application "Google Chrome" is running then
	tell application "Google Chrome"
		repeat with w in windows
			set i to 0
			repeat with t in tabs of w
				set i to i + 1
				if URL of t starts with "PREFIX" then
					if URL of t does not start with "ADDRESS" or "HASH" is not "" then set URL of t to "ADDRESS" & "HASH"
					set active tab index of w to i
					set index of w to 1
					activate
					return "found"
				end if
			end repeat
		end repeat
	end tell
end if
return ""`} {
		browser := "Google Chrome"
		if strings.Contains(script, `"Safari"`) {
			browser = "Safari"
		}
		if webBrowser != "" && !strings.EqualFold(webBrowserName(), browser) {
			continue // web_browser names another browser
		}
		script = strings.NewReplacer("PREFIX", q(prefix), "ADDRESS", q(address), "HASH", q(hash)).Replace(script)
		if out, err := exec.Command("osascript", "-e", script).Output(); err == nil && strings.TrimSpace(string(out)) == "found" {
			return true
		}
	}
	return false
}

func serviceStatus() {
	if !askService("status") {
		messagef("No service runs for %s.\n", bibTeXBaseName)
	}
}

// ---- the service side ----

// TService is the running service: one job at a time.
type TService struct {
	listener net.Listener
	started  time.Time

	mu      sync.Mutex
	busy    bool   // a job runs
	jobStop func() // stops the running job gracefully (as Ctrl-C)
	job     *TJob  // the running job's dialogue
	jobArgs []string
	jobFrom time.Time

	lastActive time.Time // the end of the last job (or the start)
	web        *TWebUI
	ownJob     bool      // the running job is the service's own (loading, the push)
	ownWhat    string    // what that job does, for a command that waits for it
	pushDue    time.Time // when the background push is due (zero: none)

	stopping atomic.Bool
	done     chan struct{}
}

func runService() {
	path := serviceSocketPath()
	if conn := dialService(); conn != nil {
		conn.Close()
		fmt.Fprintf(os.Stderr, "%s  the service runs already\n", time.Now().Format(time.DateTime))
		return
	}
	os.Remove(path) //nolint:errcheck // a socket left by a service that did not stop properly
	listener, err := net.Listen("unix", path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s  cannot listen on %s: %s\n", time.Now().Format(time.DateTime), path, err)
		endRun(1)
	}
	os.Chmod(path, 0o600) //nolint:errcheck

	teeServiceStderr()
	s := &TService{listener: listener, started: time.Now(), lastActive: time.Now(), done: make(chan struct{})}
	// A job's bad flag must end the job, not the service (parseJobArguments).
	flag.CommandLine.Init(os.Args[0], flag.ContinueOnError)
	// Done once for the service, as main does once for a run (jobs: per job).
	maybeMigrateFolderNames()
	maybeMigrateDblpNameFiles()
	fmt.Fprintf(os.Stderr, "%s  service %s started for %s (process %d)\n",
		time.Now().Format(time.DateTime), AppVersion, libraryBase(), os.Getpid())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		s.stop(logSink{})
	}()
	if serviceIdleMinutes > 0 {
		go s.stopWhenIdle(time.Duration(serviceIdleMinutes) * time.Minute)
	}
	s.web = newWebUI(s)
	s.web.start()
	go s.loadAtStart()
	go s.pushWhenDue()
	if readLibraryUnpushed() != nil {
		s.schedulePush() // left by a run before the service
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // closed by stop
			}
			go s.serve(conn)
		}
	}()
	<-s.done
	fmt.Fprintf(os.Stderr, "%s  service stopped\n", time.Now().Format(time.DateTime))
}

// stopWhenIdle stops the service once no job ran for limit.
func (s *TService) stopWhenIdle(limit time.Duration) {
	for range time.Tick(time.Minute) {
		s.mu.Lock()
		idle := !s.busy && time.Since(s.lastActive) >= limit
		s.mu.Unlock()
		if idle {
			fmt.Fprintf(os.Stderr, "%s  no job for %s: stopping\n", time.Now().Format(time.DateTime), limit)
			s.stop(logSink{})
			return
		}
	}
}

// stop ends the service: a running job is stopped as with Ctrl-C, and waited
// for; then what is not pushed yet is pushed (output to sink).
func (s *TService) stop(sink frameSink) {
	if s.stopping.Swap(true) {
		return
	}
	s.listener.Close()
	os.Remove(serviceSocketPath()) //nolint:errcheck
	for {
		s.mu.Lock()
		busy, jobStop := s.busy, s.jobStop
		s.mu.Unlock()
		if !busy {
			break
		}
		if jobStop != nil {
			jobStop()
		}
		time.Sleep(500 * time.Millisecond)
	}
	if readLibraryUnpushed() != nil {
		s.mu.Lock()
		s.busy, s.jobArgs, s.jobFrom = true, []string{"-push"}, time.Now()
		s.mu.Unlock()
		fmt.Fprintf(os.Stderr, "%s  pushing what is not pushed yet\n", time.Now().Format(time.DateTime))
		sink.send(serviceFrame{T: "info", D: fmt.Sprintf("Pushing this machine's changes to %s first...\n", sharedDBFolder)}) //nolint:errcheck
		if s.runOwnJob([]string{"-push"}, sink) == 0 && readLibraryUnpushed() == nil {
			sink.send(serviceFrame{T: "info", D: "Pushed.\n"}) //nolint:errcheck
		} else {
			sink.send(serviceFrame{T: "info", D: "Not pushed; the next run pushes it.\n"}) //nolint:errcheck
		}
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}
	s.web.stop()
	close(s.done)
}

func (s *TService) serve(conn net.Conn) {
	defer conn.Close()
	out := newFrameSender(conn)
	dec := json.NewDecoder(bufio.NewReader(conn))
	var first serviceFrame
	if dec.Decode(&first) != nil {
		return
	}
	switch first.T {
	case "status":
		out.send(serviceFrame{T: "info", D: s.statusText()}) //nolint:errcheck
		out.send(serviceFrame{T: "exit"})                    //nolint:errcheck
	case "ui":
		out.send(serviceFrame{T: "out", D: s.web.URL() + "\n"}) //nolint:errcheck
		out.send(serviceFrame{T: "exit"})                       //nolint:errcheck
	case "select":
		var files []TSelectFile
		if json.Unmarshal([]byte(first.D), &files) != nil || len(files) == 0 {
			out.send(serviceFrame{T: "err", D: "No files to select from.\n"}) //nolint:errcheck
			return
		}
		id := openSelectSessions(files)
		out.send(serviceFrame{T: "out", D: s.web.URL() + "#sel-" + id + "\n"}) //nolint:errcheck
		out.send(serviceFrame{T: "exit"})                                      //nolint:errcheck
	case "stop":
		s.mu.Lock()
		running := s.busy
		s.mu.Unlock()
		if running {
			out.send(serviceFrame{T: "info", D: "Stopping the running job first...\n"}) //nolint:errcheck
		}
		s.stop(out)
		out.send(serviceFrame{T: "info", D: fmt.Sprintf("Service for %s stopped.\n", bibTeXBaseName)}) //nolint:errcheck
		out.send(serviceFrame{T: "exit"})                                                              //nolint:errcheck
	case "run":
		s.runJob(first, dec, out)
	}
}

func (s *TService) statusText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Service %s for %s, process %d, since %s.\n",
		AppVersion, bibTeXBaseName, os.Getpid(), s.started.Format("2006-01-02 15:04"))
	if serviceIdleMinutes > 0 {
		fmt.Fprintf(&b, "Stops after %d min without a job (service_idle_minutes).\n", serviceIdleMinutes)
	}
	if s.web != nil {
		fmt.Fprintf(&b, "Web UI: %s (open it with -ui).\n", s.web.URL())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.pushDue.IsZero() {
		fmt.Fprintf(&b, "Push to %s pending (due %s).\n", sharedDBFolder, s.pushDue.Format("15:04:05"))
	}
	if s.busy {
		fmt.Fprintf(&b, "Running since %s: %s\n", s.jobFrom.Format("15:04:05"), s.runningJob())
	} else {
		b.WriteString("No job running.\n")
	}
	return b.String()
}

// refuse tells the command line why its run is not done.
func refuse(out *frameSender, format string, args ...any) {
	out.send(serviceFrame{T: "info", D: fmt.Sprintf(format, args...)}) //nolint:errcheck
	out.send(serviceFrame{T: "exit", Code: 1})                         //nolint:errcheck
}

// runJob runs one command for a command line: inside the service when the
// command table has it (service_jobs.go), otherwise as a child process.
func (s *TService) runJob(req serviceFrame, dec *json.Decoder, out *frameSender) {
	if req.Version != AppVersion {
		refuse(out, "The service runs version %s, this command line is %s. Restart the service: -stop, then -start.\n",
			AppVersion, req.Version)
		return
	}
	if req.Base != libraryBase() {
		refuse(out, "The service runs for %s, not %s.\n", libraryBase(), req.Base)
		return
	}

	s.mu.Lock()
	// The service's own push: wait for it, rather than refuse.
	toldAsking := false
	for waited := false; s.busy && s.ownJob && !s.stopping.Load(); waited = true {
		s.mu.Unlock()
		if !waited {
			out.send(serviceFrame{T: "info", D: "Waiting for the service: " + s.ownWhat + "...\n"}) //nolint:errcheck
		}
		if !toldAsking && dialogue.Asking() {
			toldAsking = true
			out.send(serviceFrame{T: "info", D: "It waits for an answer in the web UI's Actions tab: " + s.web.URL() + "#dialogue\n"}) //nolint:errcheck
			if req.Interactive {
				out.send(serviceFrame{T: "open", D: s.web.URL() + "#dialogue"}) //nolint:errcheck
			}
		}
		time.Sleep(300 * time.Millisecond)
		s.mu.Lock()
	}
	if s.stopping.Load() {
		s.mu.Unlock()
		refuse(out, "The service is stopping.\n")
		return
	}
	if s.busy {
		running := s.runningJob()
		s.mu.Unlock()
		refuse(out, "The service is running another job (%s); try again when it is done.\n", running)
		return
	}
	s.busy, s.jobStop, s.jobArgs, s.jobFrom = true, nil, req.Args, time.Now()
	s.mu.Unlock()
	libraryNeedsAnswer.Store(false) // a person runs a job: the question reaches them
	reloadNotBefore = time.Time{}
	routed := false // handed to the Actions tab: that job holds the slot now
	defer func() {
		if routed {
			return
		}
		s.mu.Lock()
		s.busy, s.jobStop, s.job, s.lastActive = false, nil, nil, time.Now()
		s.mu.Unlock()
	}()

	sio := &TSocketIO{out: out, interactive: req.Interactive, lines: make(chan string, 16)}
	if !req.Interactive {
		sio.endInput() // no person there: questions fail fast, as on a run on its own
	}
	flag.CommandLine.SetOutput(flagOutput{sio})
	positional, ok := parseJobArguments(req.Args)
	if !ok {
		out.send(serviceFrame{T: "exit", Code: 2}) //nolint:errcheck
		return
	}

	// An interactive check or fix asked for at a terminal runs in the web UI's Actions
	// tab (Erik, 2026-11-24): its questions as pop-ups there, its summaries in the log.
	// The command line says so and is free again. A run without a person (a script)
	// stays on its terminal: it waits for the result.
	// -merge_entries at a terminal (Erik, 2026-11-24: two keys from a LaTeX source on the
	// clipboard) opens the Merge tab with those entries side by side, the last one kept:
	// checked there before the merge.
	if req.Interactive && selectCommand().name == "merge_entries" && len(positional) >= 2 {
		var keys []string
		for _, raw := range positional {
			if k := resolveKeyForUI(raw, req.Cwd); k != "" {
				keys = append(keys, k)
			} else {
				refuse(out, "Not an entry: %s\n", raw)
				return
			}
		}
		out.send(serviceFrame{T: "info", D: "Opened in the web UI's Merge tab: " + strings.Join(keys, ", ") + " (the last one kept); merge there.\n"}) //nolint:errcheck
		out.send(serviceFrame{T: "open", D: s.web.URL() + "#merge-entries=" + strings.Join(keys, ",")})                                                //nolint:errcheck
		out.send(serviceFrame{T: "exit"})                                                                                                              //nolint:errcheck
		return
	}
	childAction := selectCommand().name == "harvest" && len(positional) > 0 // a file (stdin stays on the terminal)
	if req.Interactive && (runsInService() || childAction) && actionFromCommandLine[selectCommand().name] != "" {
		name := selectCommand().name
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
		args := req.Args
		if len(args) >= 2 && args[0] == "-base" {
			args = args[2:]
		}
		if err := s.runDialogueJob(actionFromCommandLine[name], [][]string{args}, false, nil, req.Cwd); err != nil {
			refuse(out, "%s\n", err)
			return
		}
		routed = true
		out.send(serviceFrame{T: "info", D: fmt.Sprintf("Started in the web UI's Actions tab (%s): answer its questions there; its output is in the Log tab.\n",
			actionFromCommandLine[name])}) //nolint:errcheck
		out.send(serviceFrame{T: "open", D: s.web.URL() + "#dialogue"}) //nolint:errcheck
		out.send(serviceFrame{T: "exit"})                               //nolint:errcheck
		return
	}

	job := newJob(jobTitle(req.Args), "command line")
	s.mu.Lock()
	s.job = job
	s.mu.Unlock()
	sio.job = job
	code := 1
	defer func() {
		job.finish(code)
		dialogue.finishIf(job, code)
	}()
	if req.Interactive {
		job.setReply(sio.answer)
		job.onAsk = func() { s.bringQuestionUp(job, out) }
	}
	inService := runsInService()
	where := "child process"
	if inService {
		where = "in the service"
	}
	fmt.Fprintf(os.Stderr, "%s  job (%s): %s (in %s)\n", time.Now().Format(time.DateTime), where,
		strings.Join(req.Args, " "), req.Cwd)

	if inService {
		s.mu.Lock()
		s.jobStop = func() { job.withdraw(); sio.interrupt() }
		s.mu.Unlock()
		deferServicePush = !cmdPush // -push pushes now
		go relayInput(dec, sio, job)
		cwd, _ := os.Getwd()
		if err := os.Chdir(req.Cwd); err != nil {
			refuse(out, "The service cannot use the folder %s: %s\n", req.Cwd, err)
			return
		}
		code = s.runJobInService(req, positional, sio)
		sio.finished.Store(true)
		if readLibraryUnpushed() != nil {
			s.schedulePush()
		}
		os.Chdir(cwd) //nolint:errcheck
	} else {
		code = s.runChildJob(req, dec, out, job)
		serviceLibraryLevel = libraryNotLoaded // the child may have changed the database
	}
	fmt.Fprintf(os.Stderr, "%s  job done (exit %d)\n", time.Now().Format(time.DateTime), code)
	out.send(serviceFrame{T: "exit", Code: code}) //nolint:errcheck
}

// runChildJob runs one command as a child process (the same binary, the same
// arguments), relaying its frames, input and output, and returns its exit code.
func (s *TService) runChildJob(req serviceFrame, dec *json.Decoder, out *frameSender, job *TJob) int {
	c, err := startFrameChild(req.Args, req.Cwd, req.Interactive, func(kind, d string) {
		webLog.write(d)
		out.send(serviceFrame{T: kind, D: d}) //nolint:errcheck
	})
	if err != nil {
		out.send(serviceFrame{T: "err", D: fmt.Sprintf("The service cannot run the job: %s\n", err)}) //nolint:errcheck
		return 1
	}
	stop := func() {
		c.interrupt()
		if job.withdraw() {
			c.answer("q\n", "")
		}
	}
	s.mu.Lock()
	s.jobStop = stop
	s.mu.Unlock()
	if req.Interactive {
		job.setReply(func(a TAnswer) { c.answer(a.line()+"\n", a.value()) })
	}

	// From the command line: data for its stdin (when no person is there), end of
	// input, Ctrl-C. When the command line goes away (terminal
	// closed), the job is stopped as with Ctrl-C.
	go func() {
		for {
			var f serviceFrame
			if err := dec.Decode(&f); err != nil {
				job.withdraw()
				c.interrupt()
				c.endInput()
				return
			}
			switch f.T {
			case "in":
				if !req.Interactive {
					io.WriteString(c.stdin, f.D) //nolint:errcheck
				}
			case "eof":
				c.endInput()
			case "int":
				stop()
			}
		}
	}()
	return c.run(func(f serviceFrame) {
		if f.T == "out" || f.T == "err" {
			webLog.write(f.D)
		}
		if f.T != "question" && f.T != "ask" {
			out.send(f) //nolint:errcheck
		}
		job.take(f)
	})
}

// bringQuestionUp brings a question of a job of a command line up in the web UI:
// shown in the Actions tab, the browser opened for the first one.
func (s *TService) bringQuestionUp(job *TJob, out *frameSender) {
	if s.web == nil || s.web.server == nil {
		out.send(serviceFrame{T: "info", D: "The job asks something, but the web UI is not running (see the service log): the job is stopped.\n"}) //nolint:errcheck
		s.mu.Lock()
		stop := s.jobStop
		s.mu.Unlock()
		if stop != nil {
			go stop()
		}
		return
	}
	first := dialogue.show(job)
	out.send(serviceFrame{T: "info", D: "A question waits for your answer in the web UI's Actions tab: " + s.web.URL() + "#dialogue\n"}) //nolint:errcheck
	if first {
		out.send(serviceFrame{T: "open", D: s.web.URL() + "#dialogue"}) //nolint:errcheck
	}
}

// jobTitle is a command line as a job's title (without its -base).
func jobTitle(args []string) string {
	if len(args) >= 2 && args[0] == "-base" {
		args = args[2:]
	}
	if len(args) == 0 {
		return "check"
	}
	return strings.Join(args, " ")
}

// TFrameChild is a job run as a child process that speaks the service's frames
// (TFrameIO): its frames on one pipe (its fd 3), answers to it on another (its
// fd 4), its stdin, stdout and stderr as they are.
type TFrameChild struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	answers  *frameSender
	answersW *os.File
	frames   *os.File
	raw      sync.WaitGroup
	once     sync.Once
}

// startFrameChild starts the command; raw gets what it writes to stdout ("out")
// and stderr ("err") outside the frames.
func startFrameChild(args []string, dir string, interactive bool, raw func(kind, d string)) (*TFrameChild, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	framesR, framesW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	answersR, answersW, err := os.Pipe()
	if err != nil {
		framesR.Close()
		framesW.Close()
		return nil, err
	}
	person := "0"
	if interactive {
		person = "1"
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), envServiceJob+"=1", envInteractive+"="+person, envFrames+"=1")
	cmd.ExtraFiles = []*os.File{framesW, answersR} // fd 3, fd 4
	stdin, err1 := cmd.StdinPipe()
	stdout, err2 := cmd.StdoutPipe()
	stderr, err3 := cmd.StderrPipe()
	err = errors.Join(err1, err2, err3)
	if err == nil {
		err = cmd.Start()
	}
	framesW.Close()
	answersR.Close()
	if err != nil {
		framesR.Close()
		answersW.Close()
		return nil, err
	}
	c := &TFrameChild{cmd: cmd, stdin: stdin, answers: newFrameSender(answersW), answersW: answersW, frames: framesR}
	copyRaw := func(r io.Reader, kind string) {
		defer c.raw.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				raw(kind, string(buf[:n]))
			}
			if err != nil {
				return
			}
		}
	}
	c.raw.Add(2)
	go copyRaw(stdout, "out")
	go copyRaw(stderr, "err")
	return c, nil
}

// answer gives the child a line (with its newline) for the question it asks, and
// the value that goes with it, if any.
func (c *TFrameChild) answer(text, value string) {
	c.answers.send(serviceFrame{T: "in", D: text, V: value}) //nolint:errcheck
}

// endInput ends the child's input: no more answers, end of its stdin.
func (c *TFrameChild) endInput() {
	c.once.Do(func() {
		c.answers.send(serviceFrame{T: "eof"}) //nolint:errcheck
		c.answersW.Close()
		c.stdin.Close()
	})
}

// interrupt stops the child gracefully, as Ctrl-C.
func (c *TFrameChild) interrupt() { c.cmd.Process.Signal(syscall.SIGINT) } //nolint:errcheck

// run hands each frame of the child to handle until it ends, and returns its exit code.
func (c *TFrameChild) run(handle func(serviceFrame)) int {
	dec := json.NewDecoder(bufio.NewReader(c.frames))
	for {
		var f serviceFrame
		if dec.Decode(&f) != nil {
			break
		}
		handle(f)
	}
	c.frames.Close()
	c.raw.Wait()
	err := c.cmd.Wait()
	c.endInput()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code > 0 {
			return code
		}
		return 1
	}
	if err != nil {
		return 1
	}
	return 0
}
