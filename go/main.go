// julia-client: CLI for interacting with the julia-daemon persistent REPL.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

var defaultSocket = filepath.Join(os.Getenv("HOME"), ".local", "share", "julia-client", "julia-daemon.sock")

func startDaemon(socketPath string) {
	// Re-exec ourselves with the daemon subcommand — no external dependency.
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	cmd := exec.Command(self, "--socket", socketPath, "daemon")
	cmd.SysProcAttr = sysProcAttrDetach()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to start daemon: %v\n", err)
	}
}

func connect(socketPath string, startIfNeeded bool) (net.Conn, error) {
	for attempt := range 15 {
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			return conn, nil
		}
		if !startIfNeeded {
			return nil, fmt.Errorf("julia-daemon is not running (no socket at %s)", socketPath)
		}
		if attempt == 0 {
			startDaemon(socketPath)
		}
		time.Sleep(600 * time.Millisecond)
	}
	return nil, fmt.Errorf("could not connect to julia-daemon at %s after startup — try running 'julia-client daemon' manually to see errors", socketPath)
}

type response struct {
	Output string `json:"output"`
	Error  string `json:"error"`
}

func request(socketPath string, payload map[string]any, startIfNeeded bool) (response, error) {
	conn, err := connect(socketPath, startIfNeeded)
	if err != nil {
		return response{}, err
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(payload); err != nil {
		return response{}, err
	}

	var resp response
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	if scanner.Scan() {
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			return response{}, err
		}
	}
	return resp, scanner.Err()
}

func run(socketPath string, payload map[string]any, startIfNeeded bool) {
	resp, err := request(socketPath, payload, startIfNeeded)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintln(os.Stderr, resp.Error)
		os.Exit(1)
	}
	if resp.Output != "" {
		fmt.Print(resp.Output)
	}
}

func mustGetwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: cannot determine working directory:", err)
		os.Exit(1)
	}
	return cwd
}

func cmdEval(socketPath, code, project, session string, timeout float64, juliaCmd string, printResult bool) {
	if code == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		code = string(b)
	}
	projectArg := project
	if project != "@." {
		projectArg, _ = filepath.Abs(project)
	}
	payload := map[string]any{"action": "eval", "code": code, "cwd": mustGetwd(), "project": projectArg}
	if session != "" {
		payload["session"] = session
	}
	if timeout != -1 {
		payload["timeout"] = timeout
	}
	if juliaCmd != "" {
		payload["julia_cmd"] = juliaCmd
	}
	if printResult {
		payload["print_result"] = true
	}
	run(socketPath, payload, true)
}

func cmdSessions(socketPath string) {
	run(socketPath, map[string]any{"action": "sessions"}, false)
}

func cmdRestart(socketPath, project, session string) {
	projectArg := project
	if project != "@." {
		projectArg, _ = filepath.Abs(project)
	}
	payload := map[string]any{"action": "restart", "cwd": mustGetwd(), "project": projectArg}
	if session != "" {
		payload["session"] = session
	}
	run(socketPath, payload, false)
}

func cmdStop(socketPath string) {
	run(socketPath, map[string]any{"action": "stop"}, false)
}

// first returns the first non-empty string.
func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func usage() {
	fmt.Fprintf(os.Stderr, `julia-client: Julia REPL client

Usage:
  julia-client [flags] [-e CODE]
  julia-client script.jl
  julia-client [--socket PATH] <command> [options]

Eval flags:
  -e, --eval CODE      Evaluate Julia code (omit or use - to read stdin)
  -E, --print CODE     Evaluate Julia code and display the result
  --project PATH       Julia project directory (passed as --project to Julia)
  --session LABEL      Named session to create or reuse across directories
  --timeout SECS       Timeout in seconds (0 = no timeout, default: 60)
  --julia-cmd CMD      Custom Julia binary, e.g. "julia +1.11"

Session routing (priority order):
  --session LABEL      Shared by label, regardless of directory
  --project PATH       Keyed by project path
  (default)            Keyed by current working directory; Julia uses --project=@.

Commands:
  sessions             List active Julia sessions
  restart              Restart a Julia session, clearing all state
    --project PATH     Target session by project path
    --session LABEL    Target session by label
  stop                 Stop the daemon
  daemon               Run the daemon in the foreground (normally auto-started)
    --idle-timeout SECS  Shut down after idle (default: 1800)

Global flags:
  --socket PATH        Unix socket path (default: %s)
`, defaultSocket)
	os.Exit(2)
}

func main() {
	socketFlag := flag.String("socket", defaultSocket, "Unix socket path")
	evalShort := flag.String("e", "", "Evaluate Julia code")
	evalLong := flag.String("eval", "", "Evaluate Julia code")
	printShort := flag.String("E", "", "Evaluate and display result")
	printLong := flag.String("print", "", "Evaluate and display result")
	projectFlag := flag.String("project", "@.", "Julia project directory")
	sessionFlag := flag.String("session", "", "Named session label")
	timeoutFlag := flag.Float64("timeout", -1, "Timeout in seconds")
	juliaCmdFlag := flag.String("julia-cmd", "", "Custom Julia binary")
	flag.Usage = usage
	flag.Parse()

	// -E / --print: evaluate and display result
	if code := first(*printShort, *printLong); code != "" {
		cmdEval(*socketFlag, code, *projectFlag, *sessionFlag, *timeoutFlag, *juliaCmdFlag, true)
		return
	}

	// -e / --eval: evaluate mode
	code := first(*evalShort, *evalLong)
	if code != "" {
		cmdEval(*socketFlag, code, *projectFlag, *sessionFlag, *timeoutFlag, *juliaCmdFlag, false)
		return
	}

	args := flag.Args()

	// No subcommand: read stdin only if it's a pipe/redirect, not a terminal
	if len(args) == 0 {
		fi, err := os.Stdin.Stat()
		if err != nil || fi.Mode()&os.ModeCharDevice != 0 {
			usage()
		}
		cmdEval(*socketFlag, "-", *projectFlag, *sessionFlag, *timeoutFlag, *juliaCmdFlag, false)
		return
	}

	switch args[0] {
	case "sessions":
		cmdSessions(*socketFlag)

	case "restart":
		fs := flag.NewFlagSet("restart", flag.ExitOnError)
		project := fs.String("project", "", "Project directory")
		session := fs.String("session", "", "Session label")
		fs.Parse(args[1:])
		cmdRestart(*socketFlag, *project, *session)

	case "stop":
		cmdStop(*socketFlag)

	case "daemon":
		fs := flag.NewFlagSet("daemon", flag.ExitOnError)
		idleTimeout := fs.Float64("idle-timeout", 30*60, "Idle timeout in seconds")
		fs.Parse(args[1:])
		if err := serveDaemon(*socketFlag, time.Duration(float64(time.Second)**idleTimeout)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	default:
		if filepath.Ext(args[0]) == ".jl" {
			b, err := os.ReadFile(args[0])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			cmdEval(*socketFlag, string(b), *projectFlag, *sessionFlag, *timeoutFlag, *juliaCmdFlag, false)
			return
		}
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		usage()
	}
}
