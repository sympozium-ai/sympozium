package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sympozium-ai/sympozium/internal/ipc"
)

// In-place gate retry. A run with both a response gate and lifecycle.retry
// parks instead of exiting: it writes the attempt's result, holds the live
// LLMProvider, and waits for a verdict under ipc.GateDir. A "continue" verdict
// becomes the next user turn and the agent loop is re-entered, so the model
// corrects work it can still see — its reasoning, its tool calls and results,
// and the files it wrote to /workspace.
//
// Follows the runPromptServiceLoop pattern: poll a directory, act on new
// files. Shares buildLLMProvider with it. The directory is not /ipc but the
// run's gate ConfigMap, which the controller writes and the agent container
// mounts read-only, so no sidecar and no model-chosen command can write a
// verdict.
//
// Unlike the prompt service it uses the ipc types directly rather than local
// mirrors: both messages are signed (ipc.AttemptResult.Sign,
// ipc.GateVerdict.Verify), and a second copy of the signing rule could drift.

// Paths are variables, not constants, so tests can point the loop at a temp
// directory instead of the pod's mounts.
var (
	gateDir      = ipc.GateDir
	ipcOutputDir = "/ipc/output"
	ipcDoneFile  = "/ipc/done"
)

const (
	gatePollEvery = 250 * time.Millisecond

	// defaultParkTimeout bounds one park. The controller is the authority on
	// how long AwaitingGate may last; this is the backstop for a controller
	// that dies mid-chain.
	defaultParkTimeout = 30 * time.Minute
)

// gateInPlaceEnabled reports whether the controller asked this run to park.
// Set only for a Job-backend run with a gate hook and gate retry; everything
// else keeps the single-attempt path and successor-clone retry behind it.
func gateInPlaceEnabled() bool {
	return strings.EqualFold(os.Getenv("GATE_IN_PLACE_ENABLED"), "true")
}

// parkTimeout returns how long one park may last before the runner gives up
// and finishes the run with the attempt it already has.
func parkTimeout() time.Duration {
	if v := os.Getenv("GATE_PARK_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultParkTimeout
}

// inPlaceMaxAttempts mirrors lifecycle.retry.maxAttempts so the runner skips a
// final park it would only time out of. The controller still enforces it.
func inPlaceMaxAttempts() int {
	if n := envInt("GATE_IN_PLACE_MAX_ATTEMPTS", 0); n > 0 {
		return n
	}
	return 0
}

// inPlaceStartAttempt is the attempt this pod runs first. 1 for a new run.
// A successor run created by the clone path starts later in the chain, and
// the controller only accepts a marker for its own attempt number.
func inPlaceStartAttempt() int {
	if n := envInt("GATE_IN_PLACE_START_ATTEMPT", 0); n > 0 {
		return n
	}
	return 1
}

// attemptTally accumulates one attempt's usage into the run totals.
type attemptTally struct {
	inputTokens  int
	outputTokens int
	toolCalls    int
}

// runInPlaceGateLoop drives a gated run across attempts on one live provider.
//
// Returns the final attempt's response plus the run's cumulative token and
// tool-call counts, matching the single-attempt path so main is unchanged.
// Per-attempt figures reach the CR through the markers instead; that is what
// maxChainTokens sums.
//
// ctx must NOT carry the run timeout — parked time is not run time. Each
// attempt gets attemptTimeout of its own.
func runInPlaceGateLoop(
	ctx context.Context, deps promptServiceDeps, attemptTimeout time.Duration,
) (string, int, int, int, error) {
	// The controller mounts the gate ConfigMap here. Without it no verdict
	// can arrive, so parking would only wait out its budget.
	if info, err := os.Stat(gateDir); err != nil || !info.IsDir() {
		return "", 0, 0, 0, fmt.Errorf("%s is not mounted; cannot receive gate verdicts", gateDir)
	}

	// Without the key the controller ignores every attempt marker and the
	// runner ignores every verdict, so parking would only wait out its budget.
	key := []byte(os.Getenv(ipc.GateKeyEnv))
	if len(key) == 0 {
		return "", 0, 0, 0, fmt.Errorf("%s is not set; cannot sign attempts or verify verdicts", ipc.GateKeyEnv)
	}

	p, cleanup, err := buildLLMProvider(deps)
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("build provider: %w", err)
	}
	defer cleanup()

	return driveInPlaceGate(ctx, p, deps.Tools, key, inPlaceStartAttempt(), inPlaceMaxAttempts(), attemptTimeout, parkTimeout())
}

// driveInPlaceGate is runInPlaceGateLoop with the provider already built: one
// provider, many attempts, a park between each.
func driveInPlaceGate(
	ctx context.Context, p LLMProvider, tools []ToolDef, key []byte,
	firstAttempt, maxAttempts int, attemptTimeout, park time.Duration,
) (string, int, int, int, error) {
	log.Printf("gate retry: parking between attempts (maxAttempts=%d attemptTimeout=%s parkTimeout=%s)",
		maxAttempts, attemptTimeout, park)

	var total attemptTally
	var lastText string
	budget := loadTokenBudget()

	for attempt := firstAttempt; ; attempt++ {
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, attemptTimeout)
		text, inTok, outTok, calls, loopErr := runAgentLoop(attemptCtx, p, tools)
		cancelAttempt()

		total.inputTokens += inTok
		total.outputTokens += outTok
		total.toolCalls += calls
		if text != "" {
			lastText = text
		}

		// A failed attempt ends the chain rather than parking: the gate judges
		// agent output, and there is none to judge.
		if loopErr != nil {
			return lastText, total.inputTokens, total.outputTokens, total.toolCalls, loopErr
		}

		// A budget halt ends the chain too. Its last assistant turn may hold
		// tool calls with no results, and a user turn after those is rejected
		// by the provider. The budget counts this pod's tokens, so this
		// conversation cannot go on in any case.
		if budget.halts(inTok, outTok) {
			log.Printf("gate retry: attempt %d hit the token budget; not parking", attempt)
			return lastText, total.inputTokens, total.outputTokens, total.toolCalls, nil
		}

		// The last permitted attempt has nothing to wait for — the controller
		// resolves the gate terminally either way.
		if maxAttempts > 0 && attempt >= maxAttempts {
			log.Printf("gate retry: attempt %d is the last permitted; not parking", attempt)
			return lastText, total.inputTokens, total.outputTokens, total.toolCalls, nil
		}

		res := ipc.AttemptResult{Attempt: attempt, Status: "success", Response: text}
		res.Metrics.InputTokens = inTok
		res.Metrics.OutputTokens = outTok
		res.Metrics.ToolCalls = calls
		if err := publishAttemptResult(res, key); err != nil {
			return lastText, total.inputTokens, total.outputTokens, total.toolCalls, err
		}

		verdict, err := waitForGateVerdict(ctx, attempt, park, key)
		if err != nil {
			// Finish with what we have rather than hold the pod. The
			// controller already has this attempt from the marker.
			log.Printf("gate retry: no verdict for attempt %d (%v); finishing with this attempt", attempt, err)
			return lastText, total.inputTokens, total.outputTokens, total.toolCalls, nil
		}

		// Anything but "continue", including an action this runner does not
		// know, ends the chain, so a verdict it cannot interpret releases the pod.
		if verdict.Action != ipc.GateVerdictActionContinue {
			log.Printf("gate retry: verdict %q for attempt %d ends the chain", verdict.Action, attempt)
			return lastText, total.inputTokens, total.outputTokens, total.toolCalls, nil
		}

		card := buildRetryCard(verdict, attempt+1)
		p.AddUserMessage(card)
		log.Printf("gate retry: injected verdict for attempt %d, starting attempt %d (cardLen=%d)",
			attempt, attempt+1, len(card))
	}
}

// publishAttemptResult signs the attempt result, writes it to /ipc/output (for
// the bridge) and prints it to stdout (for the controller). The controller
// re-reads the log on every reconcile and ignores a marker it cannot verify.
func publishAttemptResult(res ipc.AttemptResult, key []byte) error {
	if err := res.Sign(key); err != nil {
		return fmt.Errorf("signing attempt %d: %w", res.Attempt, err)
	}
	writeJSON(filepath.Join(ipcOutputDir, fmt.Sprintf("result-%d.json", res.Attempt)), res)
	marker, err := ipc.FormatAttemptMarker(res)
	if err != nil {
		return fmt.Errorf("encoding attempt %d: %w", res.Attempt, err)
	}
	fmt.Fprintf(os.Stdout, "\n%s\n", marker)
	log.Printf("gate retry: parked after attempt %d (in=%d out=%d tools=%d)",
		res.Attempt, res.Metrics.InputTokens, res.Metrics.OutputTokens, res.Metrics.ToolCalls)
	return nil
}

// waitForGateVerdict blocks until the controller writes this attempt's
// verdict, the pod is told to finish, or the park budget runs out.
//
// It only reads this attempt's file, so a stale or duplicated verdict for
// another attempt is never consumed. That is the runner's half of the
// idempotency guarantee; the controller refuses to publish the other half.
//
// A file that is not a verdict signed with key is ignored, and the wait goes
// on. Nothing in the pod can write gateDir, but anyone who can edit the gate
// ConfigMap can; the controller's next write overwrites theirs.
func waitForGateVerdict(ctx context.Context, attempt int, budget time.Duration, key []byte) (*ipc.GateVerdict, error) {
	path := filepath.Join(gateDir, ipc.GateVerdictFile(attempt))
	deadline := time.Now().Add(budget)
	var rejected []byte

	poll := time.NewTicker(gatePollEvery)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-poll.C:
			if data, err := os.ReadFile(path); err == nil && !bytes.Equal(data, rejected) {
				var v ipc.GateVerdict
				if err := json.Unmarshal(data, &v); err != nil || !v.Verify(key) {
					log.Printf("gate retry: ignoring %s: not a verdict signed for this run", path)
					rejected = data
					continue
				}
				if v.Attempt != attempt {
					// The controller names the file, so this should not
					// happen. Treat it as unusable rather than guess.
					return nil, fmt.Errorf("verdict file for attempt %d carries attempt %d", attempt, v.Attempt)
				}
				return &v, nil
			}
			// The controller ends a chain with a "stop" verdict. /ipc/done,
			// the sentinel every sidecar already writes, also releases the pod.
			if _, err := os.Stat(ipcDoneFile); err == nil {
				return nil, fmt.Errorf("/ipc/done observed while parked on attempt %d", attempt)
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("no verdict within %s", budget)
			}
		}
	}
}

// buildRetryCard renders the verdict as the user turn that starts the next
// attempt. Much shorter than buildRetryTask: a successor has to be told the
// original task and shown its predecessor's output, this conversation holds
// both already.
//
// The controller bounds the gate output before publishing, so nothing is
// truncated here.
func buildRetryCard(v *ipc.GateVerdict, nextAttempt int) string {
	reason := v.Reason
	if reason == "" {
		reason = "The response gate rejected that attempt without giving a reason."
	}

	header := fmt.Sprintf("## Attempt %d", nextAttempt)
	if v.MaxAttempts > 0 {
		header = fmt.Sprintf("## Attempt %d of %d", nextAttempt, v.MaxAttempts)
	}

	card := header + "\n\nYour previous response was rejected by the response gate. " +
		"Your earlier work is still here — the files you wrote and the tool results above are unchanged, " +
		"so correct that work rather than starting over.\n\n### Why It Was Rejected\n" + reason
	if v.Output != "" {
		card += "\n\n### Gate Output\n" + v.Output
	}
	return card
}
