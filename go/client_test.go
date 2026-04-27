package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestMain allows the test binary to act as the CLI when TEST_CLI=1,
// enabling subprocess-based end-to-end tests of main().
func TestMain(m *testing.M) {
	if os.Getenv("TEST_CLI") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// ---- pkgPattern ----

func TestPkgPattern(t *testing.T) {
	hits := []string{
		"Pkg.add(\"Example\")",
		"using Pkg; Pkg.update()",
		"Pkg.resolve()",
	}
	misses := []string{
		"println(\"hello\")",
		"x = 1 + 2",
		"# no package ops here",
	}
	for _, s := range hits {
		if !pkgPattern.MatchString(s) {
			t.Errorf("pkgPattern should match %q", s)
		}
	}
	for _, s := range misses {
		if pkgPattern.MatchString(s) {
			t.Errorf("pkgPattern should not match %q", s)
		}
	}
}

// ---- handleRequest (no Julia needed) ----

func newTestState() *daemonState {
	s := &daemonState{
		manager: newSessionManager(),
		stopCh:  make(chan struct{}),
	}
	s.lastRequest.Store(time.Now().UnixNano())
	return s
}

func TestHandleRequest_Ping(t *testing.T) {
	state := newTestState()
	resp := handleRequest(state, protocolRequest{Action: "ping"})
	if resp.Output != "pong" {
		t.Errorf("ping response = %v, want pong", resp.Output)
	}
}

func TestHandleRequest_SessionsEmpty(t *testing.T) {
	state := newTestState()
	resp := handleRequest(state, protocolRequest{Action: "sessions"})
	if resp.Output != "No active Julia sessions." {
		t.Errorf("sessions response = %q", resp.Output)
	}
}

func TestHandleRequest_UnknownAction(t *testing.T) {
	state := newTestState()
	resp := handleRequest(state, protocolRequest{Action: "bogus"})
	if resp.Error == "" {
		t.Error("expected error for unknown action")
	}
}

func TestHandleRequest_Stop(t *testing.T) {
	state := newTestState()
	resp := handleRequest(state, protocolRequest{Action: "stop"})
	if resp.Output != "Daemon stopping." {
		t.Errorf("stop response = %v", resp.Output)
	}
	select {
	case <-state.stopCh:
		// closed as expected
	default:
		t.Error("stopCh not closed after stop action")
	}
}

// ---- helpers ----

// startTestDaemon launches serveDaemon in a goroutine and returns a stop func and the socket path.
// The returned WaitGroup is done when the daemon exits.
func startTestDaemon(t *testing.T) (socketPath string, stop func(), wg *sync.WaitGroup) {
	t.Helper()
	socketPath = filepath.Join(t.TempDir(), "test.sock")
	wg = &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		serveDaemon(socketPath, time.Hour)
	}()
	waitForSocket(t, socketPath)
	stop = func() {
		conn, _ := net.Dial("unix", socketPath)
		if conn != nil {
			json.NewEncoder(conn).Encode(protocolRequest{Action: "stop"})
			conn.Close()
		}
		wg.Wait()
	}
	return
}

func waitForSocket(t *testing.T, socketPath string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("daemon socket did not appear in time")
}

func sendRequest(t *testing.T, socketPath string, req protocolRequest) response {
	t.Helper()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	json.NewEncoder(conn).Encode(req)
	var resp response
	json.NewDecoder(conn).Decode(&resp)
	return resp
}

// ---- daemon socket integration (no Julia) ----

func TestDaemonPingOverSocket(t *testing.T) {
	socketPath, stop, _ := startTestDaemon(t)
	defer stop()

	resp := sendRequest(t, socketPath, protocolRequest{Action: "ping"})
	if resp.Output != "pong" {
		t.Errorf("ping over socket = %v, want pong", resp.Output)
	}
}

// ---- Julia integration ----

func TestEvalBasic(t *testing.T) {
	socketPath, stop, _ := startTestDaemon(t)
	defer stop()

	cwd, _ := os.Getwd()
	send := func(req protocolRequest) response {
		if req.Cwd == "" {
			req.Cwd = cwd
		}
		return sendRequest(t, socketPath, req)
	}

	// Eval basic expression
	resp := send(protocolRequest{Action: "eval", Code: `println("hello world")`})
	if resp.Error != "" {
		t.Fatalf("eval error: %v", resp.Error)
	}
	out := resp.Output
	if out != "hello world\n" {
		t.Errorf("eval output = %q, want %q", out, "hello world\n")
	}

	// State persists across calls
	send(protocolRequest{Action: "eval", Code: "x = 42"})
	resp2 := send(protocolRequest{Action: "eval", Code: "println(x)"})
	out2 := resp2.Output
	if out2 != "42\n" {
		t.Errorf("state not persisted: x = %q, want %q", out2, "42\n")
	}

	// Fresh eval clears state before running code.
	resp3 := send(protocolRequest{Action: "eval", Code: "println(isdefined(Main, :x))", Fresh: true})
	out3 := resp3.Output
	if out3 != "false\n" {
		t.Errorf("after fresh eval x should be undefined, got %q", out3)
	}

	// println adds trailing newline; print does not
	resp4 := send(protocolRequest{Action: "eval", Code: `print("no-nl")`})
	if resp4.Output != "no-nl" {
		t.Errorf("print output = %q, want %q", resp4.Output, "no-nl")
	}
	resp5 := send(protocolRequest{Action: "eval", Code: `println("with-nl")`})
	if resp5.Output != "with-nl\n" {
		t.Errorf("println output = %q, want %q", resp5.Output, "with-nl\n")
	}
}

// TestScriptFile exercises the full main() routing: julia-client script.jl
// The test binary re-invokes itself as the CLI via the TestMain/TEST_CLI mechanism.
func TestScriptFile(t *testing.T) {
	socketPath, stop, _ := startTestDaemon(t)
	defer stop()

	cmd := exec.Command(os.Args[0], "--socket", socketPath, "testdata/compute.jl")
	cmd.Env = append(os.Environ(), "TEST_CLI=1")
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if e, ok := err.(*exec.ExitError); ok {
			stderr = string(e.Stderr)
		}
		t.Fatalf("script run failed: %v\n%s", err, stderr)
	}
	if got := string(out); got != "42\n" {
		t.Errorf("script output = %q, want %q", got, "42\n")
	}
}

func TestPrintResult(t *testing.T) {
	socketPath, stop, _ := startTestDaemon(t)
	defer stop()

	cwd, _ := os.Getwd()
	resp := sendRequest(t, socketPath, protocolRequest{
		Action:      "eval",
		Code:        "1 + 1",
		Cwd:         cwd,
		PrintResult: true,
	})
	if resp.Error != "" {
		t.Fatalf("print_result error: %v", resp.Error)
	}
	if resp.Output != "2\n" {
		t.Errorf("print_result output = %q, want %q", resp.Output, "2\n")
	}
}
