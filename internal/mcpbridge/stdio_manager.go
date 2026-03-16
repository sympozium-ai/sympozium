package mcpbridge

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// stdioResponse is a single line read from the child stdout.
type stdioResponse struct {
	data []byte
	err  error
}

// StdioManager manages the lifecycle of a stdio-based MCP server child process.
type StdioManager struct {
	cmdPath string
	args    []string
	env     []string

	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
	alive bool

	// responseCh delivers lines read from stdout by a single dedicated goroutine.
	// This avoids spawning a new goroutine per Send() call and eliminates
	// thread-unsafe concurrent access to bufio.Scanner.
	responseCh chan stdioResponse

	stopCh chan struct{}
	waitCh chan struct{} // closed when cmd.Wait() returns
}

// NewStdioManager creates a new StdioManager.
func NewStdioManager(cmdPath string, args []string, env []string) *StdioManager {
	return &StdioManager{
		cmdPath: cmdPath,
		args:    args,
		env:     env,
		stopCh:  make(chan struct{}),
	}
}

// Start spawns the child process and sets up pipes.
func (m *StdioManager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startLocked()
}

func (m *StdioManager) startLocked() error {
	cmd := exec.Command(m.cmdPath, m.args...)
	// Build a clean environment for the child process:
	// 1. Essential system vars from parent (PATH, HOME, etc.)
	// 2. Stripped STDIO_ENV_* vars (DT_ENVIRONMENT, etc.)
	// Do NOT inherit the full parent env — vars like
	// OTEL_EXPORTER_OTLP_ENDPOINT or MCP_STDIO_ADAPTER can
	// interfere with the child process.
	essentialPrefixes := []string{
		"PATH=", "HOME=", "USER=", "LANG=", "LC_",
		"NODE_", "NPM_", "HOSTNAME=", "TERM=",
		"SSL_CERT", "CA_CERT", "CURL_CA",
	}
	var childEnv []string
	for _, e := range os.Environ() {
		for _, prefix := range essentialPrefixes {
			if strings.HasPrefix(e, prefix) {
				childEnv = append(childEnv, e)
				break
			}
		}
	}
	cmd.Env = append(childEnv, m.env...)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start process: %w", err)
	}

	// FIX #3: Drain stderr with a large buffer to prevent the child
	// from blocking when it writes a lot to stderr before reading stdin.
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024) // 1MB max line
		for scanner.Scan() {
			log.Printf("[stdio-child stderr] %s", scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			log.Printf("stderr scanner error: %v", err)
		}
	}()

	// FIX #2: Single dedicated goroutine reads stdout lines into a channel.
	// No concurrent Scanner access — one goroutine owns the Scanner for its
	// entire lifetime. Send() simply receives from the channel.
	responseCh := make(chan stdioResponse, 1)
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max line
		for scanner.Scan() {
			line := make([]byte, len(scanner.Bytes()))
			copy(line, scanner.Bytes())
			responseCh <- stdioResponse{data: line}
		}
		scanErr := scanner.Err()
		if scanErr == nil {
			scanErr = fmt.Errorf("stdout closed")
		}
		responseCh <- stdioResponse{err: scanErr}
	}()

	m.cmd = cmd
	m.stdin = stdinPipe
	m.responseCh = responseCh
	m.alive = true

	// waitCh is closed when cmd.Wait() returns — single waiter, no races.
	waitCh := make(chan struct{})
	m.waitCh = waitCh
	go m.monitor(cmd, waitCh)

	return nil
}

// monitor waits for the process to exit and handles auto-restart.
// It is the ONLY goroutine that calls cmd.Wait() for this process instance.
func (m *StdioManager) monitor(cmd *exec.Cmd, waitCh chan struct{}) {
	err := cmd.Wait()
	close(waitCh)

	m.mu.Lock()
	m.alive = false
	m.mu.Unlock()

	select {
	case <-m.stopCh:
		// Intentional stop, don't restart
		return
	default:
	}

	if err != nil {
		log.Printf("stdio process exited: %v, restarting with backoff", err)
	} else {
		log.Printf("stdio process exited normally, restarting with backoff")
	}

	// Exponential backoff restart
	backoff := time.Second
	maxBackoff := 30 * time.Second
	for {
		select {
		case <-m.stopCh:
			return
		case <-time.After(backoff):
		}

		m.mu.Lock()
		err := m.startLocked()
		m.mu.Unlock()

		if err == nil {
			log.Printf("stdio process restarted successfully")
			return
		}

		log.Printf("stdio restart failed: %v (backoff %v)", err, backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// Send writes a JSON-RPC request to stdin and reads a line-delimited JSON
// response from the dedicated stdout reader goroutine via responseCh.
func (m *StdioManager) Send(ctx context.Context, request []byte) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.alive {
		return nil, fmt.Errorf("stdio process is not alive")
	}

	// Write request + newline. Use a copy to avoid mutating the caller's slice.
	msg := make([]byte, len(request)+1)
	copy(msg, request)
	msg[len(request)] = '\n'
	log.Printf("stdio Send: writing %d bytes to stdin", len(msg))
	if _, err := m.stdin.Write(msg); err != nil {
		return nil, fmt.Errorf("write to stdin: %w", err)
	}

	// Wait for the dedicated reader goroutine to deliver one line.
	select {
	case resp := <-m.responseCh:
		if resp.err != nil {
			return nil, fmt.Errorf("read from stdout: %w", resp.err)
		}
		log.Printf("stdio Send: received %d bytes response", len(resp.data))
		return resp.data, nil
	case <-m.waitCh:
		return nil, fmt.Errorf("stdio process exited while waiting for response")
	case <-time.After(120 * time.Second):
		return nil, fmt.Errorf("stdio response timeout after 120s")
	}
}

// Stop gracefully stops the child process (SIGTERM → 5s → SIGKILL).
func (m *StdioManager) Stop() {
	close(m.stopCh)

	m.mu.Lock()
	cmd := m.cmd
	waitCh := m.waitCh
	m.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}

	// SIGTERM
	_ = cmd.Process.Signal(syscall.SIGTERM)

	// Wait for monitor goroutine to finish (it's the sole cmd.Wait() caller)
	select {
	case <-waitCh:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-waitCh
	}

	m.mu.Lock()
	m.alive = false
	m.mu.Unlock()
}

// IsAlive returns true if the child process is running.
func (m *StdioManager) IsAlive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.alive
}

// WriteOnly writes a message to stdin without waiting for a response.
// Used for MCP notifications which have no id and expect no reply.
func (m *StdioManager) WriteOnly(ctx context.Context, request []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.alive {
		return fmt.Errorf("stdio process is not alive")
	}

	msg := make([]byte, len(request)+1)
	copy(msg, request)
	msg[len(request)] = '\n'
	log.Printf("stdio WriteOnly: writing %d bytes to stdin (notification)", len(msg))
	if _, err := m.stdin.Write(msg); err != nil {
		return fmt.Errorf("write to stdin: %w", err)
	}
	return nil
}
