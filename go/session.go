package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultEvalTimeout    = 60.0
	startupTimeout        = 120.0
	tempSessionKey        = "__temp__"
)

// JuliaSession manages a single persistent Julia subprocess.
type JuliaSession struct {
	envDir   string
	sentinel string
	isTemp   bool
	isTest   bool
	juliaCmd string

	proc   *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex

	dead    atomic.Bool
	logFile *os.File
}

func newSentinel() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("__JULIA_CLIENT_%s__", hex.EncodeToString(b))
}

func newJuliaSession(envDir, sentinel string, isTemp, isTest bool, juliaCmd string, logFile *os.File) *JuliaSession {
	return &JuliaSession{
		envDir:   envDir,
		sentinel: sentinel,
		isTemp:   isTemp,
		isTest:   isTest,
		juliaCmd: juliaCmd,
		logFile:  logFile,
	}
}

func (s *JuliaSession) projectPath() string {
	if s.isTest {
		return filepath.Dir(s.envDir)
	}
	return s.envDir
}

func (s *JuliaSession) start() error {
	exe := "julia"
	var channelArgs, extraFlags []string

	if s.juliaCmd != "" {
		parts := strings.Fields(s.juliaCmd)
		exe = parts[0]
		rest := parts[1:]
		if len(rest) > 0 && strings.HasPrefix(rest[0], "+") {
			channelArgs = rest[:1]
			extraFlags = rest[1:]
		} else {
			extraFlags = rest
		}
	}

	if !filepath.IsAbs(exe) {
		resolved, err := exec.LookPath(exe)
		if err != nil {
			return fmt.Errorf("'%s' not found in PATH. Install Julia from https://julialang.org/downloads/", exe)
		}
		exe = resolved
	}

	args := append(channelArgs, "-i", "--threads=auto")
	args = append(args, extraFlags...)
	args = append(args, fmt.Sprintf("--project=%s", s.projectPath()))

	cmd := exec.Command(exe, args...)
	cmd.Dir = s.envDir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	// Merge stdout+stderr into a single pipe (mirrors Python's stderr=subprocess.STDOUT)
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return err
	}
	pw.Close() // parent only reads; close the write end so EOF propagates when process exits

	s.proc = cmd
	s.stdin = stdin
	s.stdout = bufio.NewReaderSize(pr, 64*1024*1024)

	// Wait for Julia's interactive prompt to appear
	if _, err := s.executeRaw("", startupTimeout); err != nil {
		return fmt.Errorf("Julia startup failed: %w", err)
	}
	// TestEnv activation for test/ directories
	if s.isTest {
		if _, err := s.executeRaw("using TestEnv; TestEnv.activate()", 0); err != nil {
			return err
		}
	}
	return nil
}

func (s *JuliaSession) isAlive() bool {
	return !s.dead.Load()
}

type readResult struct {
	output string
	err    error
}

func (s *JuliaSession) executeRaw(code string, timeoutSecs float64) (string, error) {
	// The sentinel command writes an extra "\n" before the marker so it always
	// starts on its own line even when the user code didn't end with a newline.
	// We strip exactly that one "\n" when assembling the result.
	sentinelCmd := fmt.Sprintf(
		"flush(stderr); write(stdout, \"\\n\"); println(stdout, \"%s\"); flush(stdout)\n",
		s.sentinel,
	)
	if _, err := io.WriteString(s.stdin, code+"\n"+sentinelCmd); err != nil {
		return "", err
	}

	ch := make(chan readResult, 1)
	go func() {
		var buf strings.Builder
		for {
			line, err := s.stdout.ReadString('\n')
			if strings.TrimRight(line, "\r\n") == s.sentinel {
				// Strip the one "\n" we injected before the sentinel.
				out := buf.String()
				if len(out) > 0 && out[len(out)-1] == '\n' {
					out = out[:len(out)-1]
				}
				ch <- readResult{out, nil}
				return
			}
			if err != nil {
				s.dead.Store(true)
				ch <- readResult{buf.String(), fmt.Errorf("Julia process died during execution.\nOutput before death:\n%s", buf.String())}
				return
			}
			buf.WriteString(line)
		}
	}()

	if timeoutSecs <= 0 {
		r := <-ch
		return r.output, r.err
	}

	timer := time.NewTimer(time.Duration(float64(time.Second) * timeoutSecs))
	defer timer.Stop()

	select {
	case r := <-ch:
		return r.output, r.err
	case <-timer.C:
		s.proc.Process.Kill()
		s.proc.Wait()
		s.dead.Store(true)
		r := <-ch // goroutine unblocks on EOF after kill
		msg := fmt.Sprintf("Execution timed out after %vs. Session killed; it will restart on next call.", timeoutSecs)
		if r.output != "" {
			msg += "\n\nOutput before timeout:\n" + r.output
		}
		return "", fmt.Errorf("%s", msg)
	}
}

func (s *JuliaSession) execute(code string, timeoutSecs float64, printResult bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dead.Load() {
		return "", fmt.Errorf("Julia session has died unexpectedly")
	}

	hexCode := hex.EncodeToString([]byte(code))
	var wrapped string
	if printResult {
		wrapped = fmt.Sprintf(
			`show(stdout, MIME("text/plain"), include_string(Main, String(hex2bytes("%s"))));println(stdout)`,
			hexCode,
		)
	} else {
		wrapped = fmt.Sprintf(
			`include_string(Main, String(hex2bytes("%s")));nothing`,
			hexCode,
		)
	}

	if s.logFile != nil {
		fmt.Fprintf(s.logFile, "[%s] julia> %s\n", time.Now().Format("15:04:05"), code)
	}

	output, err := s.executeRaw(wrapped, timeoutSecs)
	if err != nil {
		return "", err
	}
	if s.logFile != nil && output != "" {
		fmt.Fprintf(s.logFile, "%s\n\n", output)
	}
	return output, nil
}

func (s *JuliaSession) kill() {
	s.dead.Store(true)
	if s.proc != nil && s.proc.Process != nil {
		s.proc.Process.Kill()
		s.proc.Wait()
	}
	if s.isTemp {
		os.RemoveAll(s.envDir)
	}
}

// SessionManager tracks multiple named Julia sessions.
type SessionManager struct {
	mu          sync.Mutex
	sessions    map[string]*JuliaSession
	createLocks map[string]*sync.Mutex
	logDir      string
	logFiles    map[string]*os.File
}

func newSessionManager() *SessionManager {
	logDir, _ := os.MkdirTemp("", "julia-client-logs-")
	return &SessionManager{
		sessions:    make(map[string]*JuliaSession),
		createLocks: make(map[string]*sync.Mutex),
		logDir:      logDir,
		logFiles:    make(map[string]*os.File),
	}
}

func (m *SessionManager) key(envPath string) string {
	if envPath == "" {
		return tempSessionKey
	}
	abs, _ := filepath.Abs(envPath)
	return abs
}

func (m *SessionManager) logFile(key string) *os.File {
	if f, ok := m.logFiles[key]; ok {
		return f
	}
	safe := strings.NewReplacer("/", "_", "\\", "_").Replace(strings.Trim(key, "/"))
	if safe == "" {
		safe = "temp"
	}
	f, err := os.OpenFile(filepath.Join(m.logDir, safe+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil
	}
	m.logFiles[key] = f
	return f
}

func (m *SessionManager) createLock(key string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createLocks[key] == nil {
		m.createLocks[key] = &sync.Mutex{}
	}
	return m.createLocks[key]
}

func (m *SessionManager) getOrCreate(envPath, juliaCmd string) (*JuliaSession, error) {
	key := m.key(envPath)

	// Fast path
	m.mu.Lock()
	sess := m.sessions[key]
	m.mu.Unlock()
	if sess != nil && sess.isAlive() && sess.juliaCmd == juliaCmd {
		return sess, nil
	}

	// Slow path: serialize creation per key
	mu := m.createLock(key)
	mu.Lock()
	defer mu.Unlock()

	m.mu.Lock()
	sess = m.sessions[key]
	m.mu.Unlock()
	if sess != nil && sess.isAlive() && sess.juliaCmd == juliaCmd {
		return sess, nil
	}
	if sess != nil {
		sess.kill()
		m.mu.Lock()
		delete(m.sessions, key)
		m.mu.Unlock()
	}

	isTemp := envPath == ""
	var envDir string
	var isTest bool
	if isTemp {
		var err error
		envDir, err = os.MkdirTemp("", "julia-client-")
		if err != nil {
			return nil, err
		}
	} else {
		abs, _ := filepath.Abs(envPath)
		envDir = abs
		isTest = filepath.Base(envDir) == "test"
	}

	m.mu.Lock()
	lf := m.logFile(key)
	m.mu.Unlock()

	sess = newJuliaSession(envDir, newSentinel(), isTemp, isTest, juliaCmd, lf)
	if err := sess.start(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.sessions[key] = sess
	m.mu.Unlock()
	return sess, nil
}

func (m *SessionManager) remove(envPath string) {
	key := m.key(envPath)
	m.mu.Lock()
	delete(m.sessions, key)
	m.mu.Unlock()
}

func (m *SessionManager) restart(envPath string) {
	key := m.key(envPath)
	m.mu.Lock()
	sess := m.sessions[key]
	delete(m.sessions, key)
	m.mu.Unlock()
	if sess != nil {
		sess.kill()
	}
}

type sessionInfo struct {
	envPath  string
	alive    bool
	isTemp   bool
	juliaCmd string
	logFile  string
}

func (m *SessionManager) list() []sessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]sessionInfo, 0, len(m.sessions))
	for key, sess := range m.sessions {
		info := sessionInfo{
			envPath:  sess.envDir,
			alive:    sess.isAlive(),
			isTemp:   sess.isTemp,
			juliaCmd: sess.juliaCmd,
		}
		if f := m.logFiles[key]; f != nil {
			info.logFile = f.Name()
		}
		result = append(result, info)
	}
	return result
}

func (m *SessionManager) shutdown() {
	m.mu.Lock()
	sessions := make([]*JuliaSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = make(map[string]*JuliaSession)
	m.mu.Unlock()

	for _, s := range sessions {
		s.kill()
	}
	for _, f := range m.logFiles {
		f.Close()
	}
	os.RemoveAll(m.logDir)
}
