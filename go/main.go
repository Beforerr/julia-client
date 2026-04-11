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

func detectEnv(start string) string {
	if start == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return ""
		}
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "Project.toml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// resolveProject returns an absolute project path: auto-detected if project is empty,
// absolutized if the caller supplied a (possibly relative) path.
func resolveProject(project string) string {
	if project == "" {
		return detectEnv("")
	}
	abs, _ := filepath.Abs(project)
	return abs
}

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

func cmdEval(socketPath, code, project string, timeout float64, juliaCmd string) {
	if code == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		code = string(b)
	}
	payload := map[string]any{"action": "eval", "code": code}
	if p := resolveProject(project); p != "" {
		payload["env_path"] = p
	}
	if timeout != -1 {
		payload["timeout"] = timeout
	}
	if juliaCmd != "" {
		payload["julia_cmd"] = juliaCmd
	}
	run(socketPath, payload, true)
}

func cmdSessions(socketPath string) {
	run(socketPath, map[string]any{"action": "sessions"}, false)
}

func cmdRestart(socketPath, project string) {
	payload := map[string]any{"action": "restart"}
	if p := resolveProject(project); p != "" {
		payload["env_path"] = p
	}
	run(socketPath, payload, false)
}

func cmdStop(socketPath string) {
	run(socketPath, map[string]any{"action": "stop"}, false)
}

func usage() {
	fmt.Fprintf(os.Stderr, `julia-client: Julia REPL client

Usage:
  julia-client [flags] [-e CODE]
  julia-client [--socket PATH] <command> [options]

Eval flags:
  -e, --eval CODE      Evaluate Julia code (omit or use - to read stdin)
  --project PATH       Julia project directory (auto-detected from $PWD)
  --timeout SECS       Timeout in seconds (0 = no timeout, default: 60)
  --julia-cmd CMD      Custom Julia binary, e.g. "julia +1.11"

Commands:
  sessions             List active Julia sessions
  restart              Restart a Julia session, clearing all state
    --project PATH     Project directory
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
	projectFlag := flag.String("project", "", "Julia project directory")
	timeoutFlag := flag.Float64("timeout", -1, "Timeout in seconds")
	juliaCmdFlag := flag.String("julia-cmd", "", "Custom Julia binary")
	flag.Usage = usage
	flag.Parse()

	// -e / --eval: evaluate mode
	code := *evalShort
	if code == "" {
		code = *evalLong
	}
	if code != "" {
		cmdEval(*socketFlag, code, *projectFlag, *timeoutFlag, *juliaCmdFlag)
		return
	}

	args := flag.Args()

	// No subcommand: read stdin only if it's a pipe/redirect, not a terminal
	if len(args) == 0 {
		fi, err := os.Stdin.Stat()
		if err != nil || fi.Mode()&os.ModeCharDevice != 0 {
			usage()
		}
		cmdEval(*socketFlag, "-", *projectFlag, *timeoutFlag, *juliaCmdFlag)
		return
	}

	switch args[0] {
	case "sessions":
		cmdSessions(*socketFlag)

	case "restart":
		fs := flag.NewFlagSet("restart", flag.ExitOnError)
		project := fs.String("project", "", "Project directory")
		fs.Parse(args[1:])
		cmdRestart(*socketFlag, *project)

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
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		usage()
	}
}
