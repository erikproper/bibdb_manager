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
	cmdStartService  bool
	cmdStopService   bool
	cmdServiceStatus bool
	cmdServe         bool
	cmdPush          bool
	cmdLoadLibrary   bool
	cmdOpenUI        bool

	// relayingToService is set while the command line relays a job: Ctrl-C is
	// then passed on to the job instead of handled here.
	relayingToService atomic.Bool
)

// serviceFrame is one message on the socket, one JSON object per line.
//
// Command line → service: "run" (args, cwd, interactive, version, base), "in"
// (typed input), "eof" (input ended), "int" (Ctrl-C), "stop", "status".
// Service → command line: "out" (stdout), "err" (stderr), "exit" (code), and
// "info" (a reply to stop/status, or why a run was refused, then "exit").
type serviceFrame struct {
	T           string   `json:"t"`
	D           string   `json:"d,omitempty"`
	Args        []string `json:"args,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	Interactive bool     `json:"interactive,omitempty"`
	Version     string   `json:"version,omitempty"`
	Base        string   `json:"base,omitempty"`
	Code        int      `json:"code,omitempty"`
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

// handleServiceCommands handles -start, -stop, -status and -serve, and hands any
// other command to a running service. It returns true when the command was
// dealt with here (the run then ends); false when it runs on its own.
func handleServiceCommands() bool {
	switch {
	case cmdServe:
		runService()
		return true
	case cmdStartService:
		startService()
		// -start implies -ui (Erik, 2026-11-24), unless -ui=false is given.
		uiOff := false
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "ui" && !cmdOpenUI {
				uiOff = true
			}
		})
		if !uiOff {
			openWebUI()
		}
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
	// -harvest reading its bib data from stdin opens /dev/tty for its questions,
	// which a job of the service has not: it runs on its own (the service holds
	// the instance lock only while it runs a job).
	if cmdHarvest && len(flag.Args()) == 0 {
		return false
	}
	conn := dialService()
	if conn == nil {
		return false
	}
	endRun(relayToService(conn))
	return true
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

	// Typed lines come from the one input pump (user_io.go), and only when the
	// job asks ("ask"; a child-process job, which cannot say, gets "askall":
	// read all along). Without a person, stdin is passed on as it is.
	asks := make(chan bool, 16) // true: from now on read all along
	go func() {
		if interactive {
			continuous := false
			for {
				if !continuous {
					all, ok := <-asks
					if !ok {
						return
					}
					continuous = all
				}
				userIO.RequestInput()
				line, ok := <-userIO.Input()
				if !ok {
					break
				}
				if out.send(serviceFrame{T: "in", D: line + "\n"}) != nil {
					return
				}
			}
		} else {
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
		}
		out.send(serviceFrame{T: "eof"}) //nolint:errcheck
	}()

	dec := json.NewDecoder(bufio.NewReader(conn))
	for {
		var f serviceFrame
		if err := dec.Decode(&f); err != nil {
			messageln("The service ended the connection.")
			return 1
		}
		switch f.T {
		case "ask", "askall":
			select {
			case asks <- f.T == "askall":
			default:
			}
		case "out":
			os.Stdout.WriteString(f.D) //nolint:errcheck
		case "err", "info":
			os.Stderr.WriteString(f.D) //nolint:errcheck
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
	cmd := exec.Command(exe, "-base", libraryBase(), "-serve")
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
		fmt.Fprintf(&b, "Running since %s: %s\n", s.jobFrom.Format("15:04:05"), strings.Join(s.jobArgs, " "))
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
	for waited := false; s.busy && s.ownJob && !s.stopping.Load(); waited = true {
		s.mu.Unlock()
		if !waited {
			out.send(serviceFrame{T: "info", D: "Waiting for the service: " + s.ownWhat + "...\n"}) //nolint:errcheck
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
		running := strings.Join(s.jobArgs, " ")
		s.mu.Unlock()
		refuse(out, "The service is running another job (%s); try again when it is done.\n", running)
		return
	}
	s.busy, s.jobStop, s.jobArgs, s.jobFrom = true, nil, req.Args, time.Now()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy, s.jobStop, s.lastActive = false, nil, time.Now()
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

	inService := runsInService()
	where := "child process"
	if inService {
		where = "in the service"
	}
	fmt.Fprintf(os.Stderr, "%s  job (%s): %s (in %s)\n", time.Now().Format(time.DateTime), where,
		strings.Join(req.Args, " "), req.Cwd)

	var code int
	if inService {
		s.mu.Lock()
		s.jobStop = sio.interrupt
		s.mu.Unlock()
		deferServicePush = !cmdPush // -push pushes now
		go relayInput(dec, sio)
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
		code = s.runChildJob(req, dec, out)
		serviceLibraryLevel = libraryNotLoaded // the child may have changed the database
	}
	fmt.Fprintf(os.Stderr, "%s  job done (exit %d)\n", time.Now().Format(time.DateTime), code)
	out.send(serviceFrame{T: "exit", Code: code}) //nolint:errcheck
}

// runChildJob runs one command as a child process (the same binary, the same
// arguments), relaying its input and output, and returns its exit code.
func (s *TService) runChildJob(req serviceFrame, dec *json.Decoder, out *frameSender) int {
	exe, err := os.Executable()
	if err != nil {
		out.send(serviceFrame{T: "err", D: fmt.Sprintf("The service cannot find its programme: %s\n", err)}) //nolint:errcheck
		return 1
	}
	interactive := "0"
	if req.Interactive {
		interactive = "1"
	}
	cmd := exec.Command(exe, req.Args...)
	cmd.Dir = req.Cwd
	cmd.Env = append(os.Environ(), envServiceJob+"=1", envInteractive+"="+interactive)
	stdin, err1 := cmd.StdinPipe()
	stdout, err2 := cmd.StdoutPipe()
	stderr, err3 := cmd.StderrPipe()
	if err := errors.Join(err1, err2, err3); err != nil {
		out.send(serviceFrame{T: "err", D: fmt.Sprintf("The service cannot run the job: %s\n", err)}) //nolint:errcheck
		return 1
	}
	if err := cmd.Start(); err != nil {
		out.send(serviceFrame{T: "err", D: fmt.Sprintf("The service cannot start the job: %s\n", err)}) //nolint:errcheck
		return 1
	}
	s.mu.Lock()
	s.jobStop = func() { cmd.Process.Signal(syscall.SIGINT) } //nolint:errcheck
	s.mu.Unlock()
	if req.Interactive {
		out.send(serviceFrame{T: "askall"}) //nolint:errcheck // the child reads its stdin itself
	}

	// From the command line: input, end of input, Ctrl-C. When the command line
	// goes away (terminal closed), the job is stopped as with Ctrl-C.
	go func() {
		for {
			var f serviceFrame
			if err := dec.Decode(&f); err != nil {
				cmd.Process.Signal(syscall.SIGINT) //nolint:errcheck
				stdin.Close()
				return
			}
			switch f.T {
			case "in":
				io.WriteString(stdin, f.D) //nolint:errcheck
			case "eof":
				stdin.Close()
			case "int":
				cmd.Process.Signal(syscall.SIGINT) //nolint:errcheck
			}
		}
	}()

	var copies sync.WaitGroup
	relay := func(r io.Reader, kind string) {
		defer copies.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				webLog.write(string(buf[:n]))
				out.send(serviceFrame{T: kind, D: string(buf[:n])}) //nolint:errcheck
			}
			if err != nil {
				return
			}
		}
	}
	copies.Add(2)
	go relay(stdout, "out")
	go relay(stderr, "err")
	copies.Wait()

	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}
