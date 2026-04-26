package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ---- detectEnv / resolveProject ----

func TestDetectEnv_FindsProjectToml(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Project.toml"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	got := detectEnv(sub)
	if got != root {
		t.Errorf("detectEnv(%q) = %q, want %q", sub, got, root)
	}
}

func TestDetectEnv_NoneFound(t *testing.T) {
	dir := t.TempDir()
	// Make sure there's no Project.toml anywhere up the tree
	// (TempDir is under /tmp which never has one)
	got := detectEnv(dir)
	if got != "" {
		t.Errorf("detectEnv(%q) = %q, want empty", dir, got)
	}
}

func TestResolveProject_Empty(t *testing.T) {
	// When empty, result is either a detected env or "".
	// Just ensure it doesn't panic and returns a valid absolute path or "".
	got := resolveProject("")
	if got != "" {
		if !filepath.IsAbs(got) {
			t.Errorf("resolveProject(\"\") = %q, want absolute path or empty", got)
		}
	}
}

func TestResolveProject_Relative(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "proj")
	os.Mkdir(sub, 0755)

	orig, _ := os.Getwd()
	os.Chdir(root)
	defer os.Chdir(orig)

	got := resolveProject("proj")
	// Resolve symlinks on both sides (macOS /var → /private/var)
	gotR, _ := filepath.EvalSymlinks(got)
	subR, _ := filepath.EvalSymlinks(sub)
	if gotR != subR {
		t.Errorf("resolveProject(\"proj\") = %q, want %q", got, sub)
	}
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

// ---- daemon socket integration (no Julia) ----

func TestDaemonPingOverSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "test.sock")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		serveDaemon(socketPath, time.Hour)
	}()

	// Wait for socket to appear
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(map[string]any{"action": "ping"}); err != nil {
		t.Fatal(err)
	}
	var resp map[string]any
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["output"] != "pong" {
		t.Errorf("ping over socket = %v, want pong", resp["output"])
	}

	// Stop the daemon so the goroutine exits
	conn2, _ := net.Dial("unix", socketPath)
	json.NewEncoder(conn2).Encode(map[string]any{"action": "stop"})
	conn2.Close()
	wg.Wait()
}

// ---- Julia integration ----

func TestEvalBasic(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "test.sock")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		serveDaemon(socketPath, time.Hour)
	}()

	// Wait for socket
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	send := func(payload map[string]any) map[string]any {
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

	// Restart clears state
	send(map[string]any{"action": "restart"})
	resp3 := send(map[string]any{"action": "eval", "code": "println(isdefined(Main, :x))"})
	out3, _ := resp3["output"].(string)
	if out3 != "false\n" {
		t.Errorf("after restart x should be undefined, got %q", out3)
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

	// Stop daemon
	conn, _ := net.Dial("unix", socketPath)
	json.NewEncoder(conn).Encode(map[string]any{"action": "stop"})
	conn.Close()
	wg.Wait()
}
