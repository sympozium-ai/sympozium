package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Actor mode (`agent-runner actor`) is how the runner lives inside an Agent
// Substrate actor (spike 0b, #699).
//
// An actor is restored from a memory snapshot shared by every run of the same
// image, so nothing about a run can arrive by environment or by file at start:
// both are frozen in the snapshot. And an actor has no sidecars, so there is no
// ipc-bridge to hand the task over or carry the result back.
//
// Instead the process parks in a loop asking the turn endpoint for work. While
// the actor is suspended nothing runs; once it is resumed the next request is
// answered with the run's turn. The request carries a placeholder credential
// that Substrate's egress gateway replaces with a JWT naming this actor, so the
// actor never holds anything that identifies or authorises it. The turn then
// runs as a child of this same binary with the environment the controller
// sent, exactly as it would in a pod, and the result is posted back.

const (
	actorTurnPath   = "/v1/actor/turn"
	actorResultPath = "/v1/actor/turn/result"
	// actorPlaceholderCredential is what the egress gateway overwrites. It is
	// not a secret: an endpoint that receives it unreplaced must refuse it.
	actorPlaceholderCredential = "Bearer substrate-actor"
	actorLogTailBytes          = 256 << 10
	actorResultMaxBytes        = 4 << 20
)

// actorTurn is one unit of work handed to the actor.
type actorTurn struct {
	ID  string            `json:"id"`
	Env map[string]string `json:"env"`
}

// actorTurnResult is what the actor reports for a turn.
type actorTurnResult struct {
	ID       string          `json:"id"`
	ExitCode int             `json:"exitCode"`
	Result   json.RawMessage `json:"result,omitempty"`
	Logs     string          `json:"logs,omitempty"`
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

type actorLoop struct {
	base   string
	client *http.Client
	// runTurn executes one turn and returns its result; swapped in tests.
	runTurn func(ctx context.Context, turn actorTurn) actorTurnResult
	idle    time.Duration
}

// newActorHTTPClient never reuses a connection: one that was open when the
// actor was snapshotted is dead after a restore, possibly on another worker.
func newActorHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true, Proxy: nil},
	}
}

func (a *actorLoop) do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", actorPlaceholderCredential)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, actorResultMaxBytes))
	return resp.StatusCode, b, err
}

// next asks for a turn. No turn, a refusal and an unreachable endpoint all
// mean "ask again": the golden snapshot is taken while the actor has no
// egress at all, so failing here would bake a dead process into every run.
func (a *actorLoop) next(ctx context.Context) (actorTurn, bool) {
	status, body, err := a.do(ctx, http.MethodGet, actorTurnPath, nil)
	if err != nil || status != http.StatusOK {
		return actorTurn{}, false
	}
	var turn actorTurn
	if json.Unmarshal(body, &turn) != nil || turn.ID == "" {
		return actorTurn{}, false
	}
	return turn, true
}

// report posts the result until the endpoint accepts it. A result that is
// never delivered leaves the run Running until the controller's own timeout.
func (a *actorLoop) report(ctx context.Context, res actorTurnResult) {
	body, _ := json.Marshal(res)
	for ctx.Err() == nil {
		status, _, err := a.do(ctx, http.MethodPost, actorResultPath, body)
		if err == nil && status/100 == 2 {
			return
		}
		if err == nil && status/100 == 4 && status != http.StatusForbidden && status != http.StatusTooManyRequests {
			log.Printf("actor: result for turn %s refused with %d; dropping it", res.ID, status)
			return
		}
		sleepCtx(ctx, a.idle)
	}
}

func (a *actorLoop) run(ctx context.Context) {
	for ctx.Err() == nil {
		turn, ok := a.next(ctx)
		if !ok {
			sleepCtx(ctx, a.idle)
			continue
		}
		log.Printf("actor: turn %s received", turn.ID)
		res := a.runTurn(ctx, turn)
		a.report(ctx, res)
		log.Printf("actor: turn %s reported (exit %d)", turn.ID, res.ExitCode)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// actorChildEnv builds the child's environment from the turn alone. Nothing
// from the actor's own environment is inherited except what a process needs to
// run at all, so a later turn cannot see an earlier turn's settings.
func actorChildEnv(turn actorTurn) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + getEnv("HOME", "/tmp")}
	// Where the turn's tool calls go. It comes from this actor's own
	// environment, frozen in the snapshot, never from the turn.
	if base := strings.TrimRight(os.Getenv("ACTOR_TURN_URL"), "/"); base != "" {
		env = append(env, actorToolsURLEnv+"="+base)
	}
	for k, v := range turn.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || k == "PATH" || k == "HOME" || k == actorToolsURLEnv {
			continue
		}
		env = append(env, k+"="+v)
	}
	return env
}

// runTurnAsChild runs the ordinary one-shot runner as a child process. The
// runner's file protocol stays as it is; /ipc is simply a local directory here
// and this process plays the part the ipc-bridge sidecar plays in a pod.
func runTurnAsChild(ctx context.Context, turn actorTurn) actorTurnResult {
	res := actorTurnResult{ID: turn.ID}
	for _, dir := range []string{"/ipc/output", "/ipc/input", "/ipc/tools", "/ipc/prompts"} {
		_ = os.RemoveAll(dir)
	}
	_ = os.Remove("/ipc/done")
	if err := os.MkdirAll("/ipc/output", 0o755); err != nil {
		res.ExitCode, res.Logs = -1, "actor: cannot prepare /ipc: "+err.Error()
		return res
	}
	self, err := os.Executable()
	if err != nil {
		res.ExitCode, res.Logs = -1, "actor: cannot find own binary: "+err.Error()
		return res
	}
	logs := &tailBuffer{max: actorLogTailBytes}
	cmd := exec.CommandContext(ctx, self)
	cmd.Env = actorChildEnv(turn)
	cmd.Stdout = io.MultiWriter(os.Stdout, logs)
	cmd.Stderr = io.MultiWriter(os.Stderr, logs)
	if err := cmd.Run(); err != nil {
		res.ExitCode = -1
		if exit, ok := err.(*exec.ExitError); ok {
			res.ExitCode = exit.ExitCode()
		} else {
			fmt.Fprintf(logs, "actor: runner did not start: %v\n", err)
		}
	}
	if b, err := os.ReadFile("/ipc/output/result.json"); err == nil && len(b) <= actorResultMaxBytes && json.Valid(b) {
		res.Result = b
	}
	res.Logs = logs.String()
	return res
}

// runActor is the entry point for `agent-runner actor`.
func runActor() {
	base := strings.TrimRight(getEnv("ACTOR_TURN_URL", ""), "/")
	if base == "" {
		fatal("ACTOR_TURN_URL is required in actor mode")
	}
	addr := getEnv("ACTOR_HEALTH_ADDR", ":8089")

	// The wakeup probe: Substrate's ResumeActor returns once this answers, and
	// the golden snapshot is taken only after it does.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			fatal("actor health listener: " + err.Error())
		}
	}()

	// SIGTERM here is not "stop": Substrate sends it when the actor's worker
	// is draining, and the actor then has 30 minutes to finish or be
	// suspended. Exiting would crash the actor and lose the turn, so the
	// signal is only noted and a one-shot turn runs to its end. (Passing the
	// notice on, so an enduring run can be suspended in time, is not done yet.)
	evicting := make(chan os.Signal, 1)
	signal.Notify(evicting, syscall.SIGTERM)
	go func() {
		for range evicting {
			log.Printf("actor: worker is draining (SIGTERM); finishing the current turn")
		}
	}()
	ctx := context.Background()
	log.Printf("actor mode: waiting for turns from %s", base)
	loop := &actorLoop{base: base, client: newActorHTTPClient(), runTurn: runTurnAsChild, idle: 100 * time.Millisecond}
	loop.run(ctx)
}

// Tools in an actor.
//
// A tool that acts outside the sandbox cannot be a file handed to a sidecar:
// there is none. It is a request to the control plane, carrying the same
// placeholder credential as the turn requests, which Substrate's egress
// gateway replaces with this actor's identity. The control plane decides
// whether the run may make the call; this process only asks.

const (
	// actorToolsURLEnv is set for a turn's runner when it is inside an actor.
	actorToolsURLEnv = "ACTOR_TOOLS_URL"
	actorToolPath    = "/v1/actor/tools/"
)

// inActor reports whether this runner is a turn inside a Substrate actor.
func inActor() bool { return os.Getenv(actorToolsURLEnv) != "" }

// actorOfferedTools narrows the runner's tools to the ones the control plane
// serves this run. Every other built-in tool needs a sidecar or the node
// network, and an actor has neither.
func actorOfferedTools(tools []ToolDef) []ToolDef {
	offered := map[string]bool{}
	for _, name := range strings.Split(os.Getenv("ACTOR_TOOLS"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			offered[name] = true
		}
	}
	kept := tools[:0:0]
	for _, t := range tools {
		if offered[t.Name] {
			kept = append(kept, t)
		}
	}
	return kept
}

// callActorTool asks the control plane to make a tool call for this run and
// returns what to tell the model.
func callActorTool(ctx context.Context, tool string, args any) string {
	body, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	loop := &actorLoop{base: os.Getenv(actorToolsURLEnv), client: newActorHTTPClient()}
	status, resp, err := loop.do(ctx, http.MethodPost, actorToolPath+tool, body)
	if err != nil {
		return fmt.Sprintf("Error: tool %s could not be reached: %v", tool, err)
	}
	var out struct {
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal(resp, &out)
	switch {
	case status == http.StatusOK:
		return out.Result
	case status == http.StatusForbidden && out.Error != "":
		return "Error: " + out.Error
	default:
		return fmt.Sprintf("Error: tool %s is unavailable (status %d)", tool, status)
	}
}
