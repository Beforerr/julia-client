package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var pkgPattern = regexp.MustCompile(`\bPkg\.`)

type daemonState struct {
	manager     *SessionManager
	mu          sync.Mutex
	lastRequest time.Time
	stopOnce    sync.Once
	stopCh      chan struct{}
}

func handleRequest(state *daemonState, req map[string]any) map[string]any {
	state.mu.Lock()
	state.lastRequest = time.Now()
	state.mu.Unlock()

	action, _ := req["action"].(string)

	switch action {
	case "eval":
		code, _ := req["code"].(string)
		envPath, _ := req["env_path"].(string)
		juliaCmd, _ := req["julia_cmd"].(string)

		var timeoutSecs float64
		if t, ok := req["timeout"]; ok {
			if v, ok := t.(float64); ok && v > 0 {
				timeoutSecs = v
			}
			// v <= 0 means no timeout; timeoutSecs stays 0
		} else if pkgPattern.MatchString(code) {
			timeoutSecs = 0 // Pkg operations: no timeout
		} else {
			timeoutSecs = defaultEvalTimeout
		}

		printResult, _ := req["print_result"].(bool)
		sess, err := state.manager.getOrCreate(envPath, juliaCmd)
		if err != nil {
			return errResp(err.Error())
		}
		output, err := sess.execute(code, timeoutSecs, printResult)
		if err != nil {
			if !sess.isAlive() {
				state.manager.remove(envPath)
			}
			return errResp(err.Error())
		}
		return map[string]any{"output": output, "error": nil}

	case "restart":
		envPath, _ := req["env_path"].(string)
		state.manager.restart(envPath)
		return map[string]any{"output": "Session restarted.", "error": nil}

	case "sessions":
		sessions := state.manager.list()
		if len(sessions) == 0 {
			return map[string]any{"output": "No active Julia sessions.", "error": nil}
		}
		lines := []string{"Active Julia sessions:"}
		for _, s := range sessions {
			status := "alive"
			if !s.alive {
				status = "dead"
			}
			label := s.envPath
			if s.isTemp {
				label += " (temp)"
			}
			line := fmt.Sprintf("  %s: %s", label, status)
			if s.juliaCmd != "" {
				line += " julia_cmd=" + s.juliaCmd
			}
			if s.logFile != "" {
				line += " log=" + s.logFile
			}
			lines = append(lines, line)
		}
		return map[string]any{"output": strings.Join(lines, "\n"), "error": nil}

	case "stop":
		state.stopOnce.Do(func() { close(state.stopCh) })
		return map[string]any{"output": "Daemon stopping.", "error": nil}

	case "ping":
		return map[string]any{"output": "pong", "error": nil}

	default:
		return errResp(fmt.Sprintf("Unknown action: %q", action))
	}
}

func errResp(msg string) map[string]any {
	return map[string]any{"output": nil, "error": msg}
}

func handleConn(conn net.Conn, state *daemonState) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	if !scanner.Scan() {
		return
	}

	var req map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		json.NewEncoder(conn).Encode(errResp(fmt.Sprintf("invalid JSON: %v", err)))
		return
	}

	json.NewEncoder(conn).Encode(handleRequest(state, req))
}

func serveDaemon(socketPath string, idleTimeout time.Duration) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0755); err != nil {
		return err
	}
	os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}

	pidPath := filepath.Join(filepath.Dir(socketPath), "daemon.pid")
	os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644)
	fmt.Fprintf(os.Stderr, "julia-daemon listening on %s\n", socketPath)

	state := &daemonState{
		manager:     newSessionManager(),
		lastRequest: time.Now(),
		stopCh:      make(chan struct{}),
	}

	// Idle watchdog: closes listener when idle or stop requested
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-state.stopCh:
				ln.Close()
				return
			case <-ticker.C:
				state.mu.Lock()
				idle := time.Since(state.lastRequest)
				state.mu.Unlock()
				if idle > idleTimeout {
					ln.Close()
					return
				}
			}
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			break
		}
		go handleConn(conn, state)
	}

	state.manager.shutdown()
	os.Remove(socketPath)
	os.Remove(pidPath)
	return nil
}
