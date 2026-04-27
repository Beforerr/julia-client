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
	resp := handleRequest(state, map[string]any{"action": "ping"})
	if resp["output"] != "pong" {
		t.Errorf("ping response = %v, want pong", resp["output"])
	}
}

func TestHandleRequest_SessionsEmpty(t *testing.T) {
	state := newTestState()
	resp := handleRequest(state, map[string]any{"action": "sessions"})
	out, _ := resp["output"].(string)
	if out != "No active Julia sessions." {
		t.Errorf("sessions response = %q", out)
	}
}

func TestHandleRequest_UnknownAction(t *testing.T) {
	state := newTestState()
	resp := handleRequest(state, map[string]any{"action": "bogus"})
	if resp["error"] == nil {
		t.Error("expected error for unknown action")
	}
}

func TestHandleRequest_Stop(t *testing.T) {
	state := newTestState()
	resp := handleRequest(state, map[string]any{"action": "stop"})
	if resp["output"] != "Daemon stopping." {
		t.Errorf("stop response = %v", resp["output"])
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
			json.NewEncoder(conn).Encode(map[string]any{"action": "stop"})
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

func sendRequest(t *testing.T, socketPath string, payload map[string]any) map[string]any {
	t.Helper()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	json.NewEncoder(conn).Encode(payload)
	var resp map[string]any
	json.NewDecoder(conn).Decode(&resp)
	return resp
}

// ---- daemon socket integration (no Julia) ----

func TestDaemonPingOverSocket(t *testing.T) {
	socketPath, stop, _ := startTestDaemon(t)
	defer stop()

	resp := sendRequest(t, socketPath, map[string]any{"action": "ping"})
	if resp["output"] != "pong" {
		t.Errorf("ping over socket = %v, want pong", resp["output"])
	}
}

// ---- Julia integration ----

func TestEvalBasic(t *testing.T) {
	socketPath, stop, _ := startTestDaemon(t)
	defer stop()

	cwd, _ := os.Getwd()
	send := func(payload map[string]any) map[string]any {
		if _, ok := payload["cwd"]; !ok {
			payload["cwd"] = cwd
		}
		return sendRequest(t, socketPath, payload)
	}

	// Eval basic expression
	resp := send(map[string]any{"action": "eval", "code": `println("hello world")`})
	if resp["error"] != nil {
		t.Fatalf("eval error: %v", resp["error"])
	}
	out, _ := resp["output"].(string)
	if out != "hello world\n" {
		t.Errorf("eval output = %q, want %q", out, "hello world\n")
	}

	// State persists across calls
	send(map[string]any{"action": "eval", "code": "x = 42"})
	resp2 := send(map[string]any{"action": "eval", "code": "println(x)"})
	out2, _ := resp2["output"].(string)
	if out2 != "42\n" {
		t.Errorf("state not persisted: x = %q, want %q", out2, "42\n")
	}

	// Fresh eval clears state before running code.
	resp3 := send(map[string]any{"action": "eval", "code": "println(isdefined(Main, :x))", "fresh": true})
	out3, _ := resp3["output"].(string)
	if out3 != "false\n" {
		t.Errorf("after fresh eval x should be undefined, got %q", out3)
	}

	// println adds trailing newline; print does not
	resp4 := send(map[string]any{"action": "eval", "code": `print("no-nl")`})
	if out4, _ := resp4["output"].(string); out4 != "no-nl" {
		t.Errorf("print output = %q, want %q", out4, "no-nl")
	}
	resp5 := send(map[string]any{"action": "eval", "code": `println("with-nl")`})
	if out5, _ := resp5["output"].(string); out5 != "with-nl\n" {
		t.Errorf("println output = %q, want %q", out5, "with-nl\n")
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
	resp := sendRequest(t, socketPath, map[string]any{
		"action":       "eval",
		"code":         "1 + 1",
		"cwd":          cwd,
		"print_result": true,
	})
	if resp["error"] != nil {
		t.Fatalf("print_result error: %v", resp["error"])
	}
	if out, _ := resp["output"].(string); out != "2\n" {
		t.Errorf("print_result output = %q, want %q", out, "2\n")
	}
}
