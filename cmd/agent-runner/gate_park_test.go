package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sympozium-ai/sympozium/internal/ipc"
)

// testKey stands in for the per-run gate key the controller injects.
var testKey = []byte("test-gate-key")

// useTempIPC points the gate loop at temp directories so tests never touch
// the pod's real /gate and /ipc mounts.
func useTempIPC(t *testing.T) (gate, output string) {
	t.Helper()
	root := t.TempDir()
	gate = filepath.Join(root, "gate")
	output = filepath.Join(root, "output")
	for _, d := range []string{gate, output} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	origGate, origOutput, origDone := gateDir, ipcOutputDir, ipcDoneFile
	gateDir, ipcOutputDir, ipcDoneFile = gate, output, filepath.Join(root, "done")
	t.Cleanup(func() {
		gateDir, ipcOutputDir, ipcDoneFile = origGate, origOutput, origDone
	})
	return gate, output
}

// writeVerdict writes v the way the controller delivers a verdict:
// signed with the run's key.
func writeVerdict(t *testing.T, dir string, v ipc.GateVerdict) {
	t.Helper()
	if err := v.Sign(testKey); err != nil {
		t.Fatalf("sign verdict: %v", err)
	}
	writeRawVerdict(t, dir, v)
}

// writeRawVerdict writes v as-is: what anyone who can edit the gate
// ConfigMap can put there.
func writeRawVerdict(t *testing.T, dir string, v ipc.GateVerdict) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal verdict: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("verdict-%d.json", v.Attempt))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ── waitForGateVerdict ─────────────────────────────────────────────────────

func TestWaitForGateVerdict_ReturnsTheMatchingVerdict(t *testing.T) {
	gate, _ := useTempIPC(t)
	writeVerdict(t, gate, ipc.GateVerdict{Attempt: 2, Action: "continue", Reason: "nope"})

	got, err := waitForGateVerdict(context.Background(), 2, time.Second, testKey)
	if err != nil {
		t.Fatalf("waitForGateVerdict: %v", err)
	}
	if got.Action != "continue" || got.Reason != "nope" {
		t.Errorf("got %+v, want the attempt-2 retry verdict", got)
	}
}

// A verdict for an attempt that is already resolved must not be consumed: the
// runner is parked on a later attempt and acting on the older decision would
// run an attempt nobody asked for.
func TestWaitForGateVerdict_IgnoresAnAlreadyConsumedAttempt(t *testing.T) {
	gate, _ := useTempIPC(t)
	writeVerdict(t, gate, ipc.GateVerdict{Attempt: 1, Action: "continue", Reason: "stale"})

	_, err := waitForGateVerdict(context.Background(), 2, 400*time.Millisecond, testKey)
	if err == nil {
		t.Fatal("attempt 2 consumed attempt 1's verdict")
	}
	if !strings.Contains(err.Error(), "no verdict within") {
		t.Errorf("err = %v, want the park budget to expire", err)
	}
}

// Anyone who can edit the gate ConfigMap can put a verdict file in front of
// the runner. It must not start an attempt the gate never asked for; the
// controller's signed verdict, when it lands, still counts.
func TestWaitForGateVerdict_IgnoresAnUnsignedVerdict(t *testing.T) {
	gate, _ := useTempIPC(t)
	writeRawVerdict(t, gate, ipc.GateVerdict{Attempt: 1, Action: "continue", Reason: "self-approved"})

	if _, err := waitForGateVerdict(context.Background(), 1, 400*time.Millisecond, testKey); err == nil {
		t.Fatal("an unsigned verdict was accepted")
	}

	go func() {
		time.Sleep(300 * time.Millisecond)
		writeVerdict(t, gate, ipc.GateVerdict{Attempt: 1, Action: "continue", Reason: "from the gate"})
	}()
	got, err := waitForGateVerdict(context.Background(), 1, 5*time.Second, testKey)
	if err != nil {
		t.Fatalf("waitForGateVerdict: %v", err)
	}
	if got.Reason != "from the gate" {
		t.Errorf("reason = %q, want the signed verdict's", got.Reason)
	}
}

func TestWaitForGateVerdict_DoneSentinelReleasesThePark(t *testing.T) {
	useTempIPC(t)
	if err := os.WriteFile(ipcDoneFile, []byte("done"), 0o644); err != nil {
		t.Fatalf("write done: %v", err)
	}

	if _, err := waitForGateVerdict(context.Background(), 1, 10*time.Second, testKey); err == nil {
		t.Fatal("/ipc/done did not release the park")
	}
}

func TestWaitForGateVerdict_CancelledContextReturns(t *testing.T) {
	useTempIPC(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := waitForGateVerdict(ctx, 1, time.Minute, testKey); err == nil {
		t.Fatal("a cancelled context must not park")
	}
}

// ── driveInPlaceGate: the park/continue state machine ─────────────────────────

// The load-bearing property: the verdict arrives as a turn on the *existing*
// conversation, so the model corrects work it can still see. A successor run
// would have neither the tool results nor the reasoning that produced them.
func TestDriveInPlaceGate_InjectsTheVerdictOnTheLiveConversation(t *testing.T) {
	gate, output := useTempIPC(t)

	p := &mockProvider{
		name:  "mock",
		model: "m",
		turns: []ChatResult{
			{Text: "first draft", InputTokens: 10, OutputTokens: 3},
			{Text: "corrected", InputTokens: 12, OutputTokens: 4},
		},
	}

	// Deliver the verdict as soon as the runner parks: it publishes the
	// attempt result first, so the result file is the signal that it is
	// waiting.
	go func() {
		waitForFile(t, filepath.Join(output, "result-1.json"), 2*time.Second)
		writeVerdict(t, gate, ipc.GateVerdict{
			Attempt: 1, Action: "continue", Reason: "missing the edge case",
			Output: "FAIL: empty input", MaxAttempts: 3,
		})
		// Approve the correction, so the chain ends on a verdict rather than
		// on the park budget expiring.
		waitForFile(t, filepath.Join(output, "result-2.json"), 2*time.Second)
		writeVerdict(t, gate, ipc.GateVerdict{Attempt: 2, Action: "stop"})
	}()

	text, in, out, _, err := driveInPlaceGate(context.Background(), p, nil, testKey, 1, 3, time.Minute, 5*time.Second)
	if err != nil {
		t.Fatalf("driveInPlaceGate: %v", err)
	}
	if text != "corrected" {
		t.Errorf("text = %q, want the second attempt's answer", text)
	}
	if in != 22 || out != 7 {
		t.Errorf("tokens in=%d out=%d, want the sum across both attempts (22/7)", in, out)
	}
	if p.chatCalls != 2 {
		t.Errorf("chat calls = %d, want 2: one provider, two attempts", p.chatCalls)
	}
	if len(p.userMessages) != 1 {
		t.Fatalf("injected %d user messages, want 1", len(p.userMessages))
	}
	card := p.userMessages[0]
	for _, want := range []string{"missing the edge case", "FAIL: empty input", "Attempt 2 of 3"} {
		if !strings.Contains(card, want) {
			t.Errorf("retry card is missing %q:\n%s", want, card)
		}
	}
}

// A successor of an in-place run (one that halted on the token budget and
// was retried as a new AgentRun) starts at its own attempt number. The
// controller only accepts a marker for that attempt, so counting from 1 would
// leave the successor parked until its timeout, never gated.
func TestDriveInPlaceGate_ASuccessorCountsFromItsOwnAttempt(t *testing.T) {
	gate, output := useTempIPC(t)
	p := &mockProvider{name: "mock", model: "m", turns: []ChatResult{
		{Text: "second try", InputTokens: 1},
		{Text: "third try", InputTokens: 1},
	}}

	go func() {
		waitForFile(t, filepath.Join(output, "result-2.json"), 2*time.Second)
		writeVerdict(t, gate, ipc.GateVerdict{Attempt: 2, Action: "continue", MaxAttempts: 3})
	}()

	text, _, _, _, err := driveInPlaceGate(context.Background(), p, nil, testKey, 2, 3, time.Minute, 5*time.Second)
	if err != nil {
		t.Fatalf("driveInPlaceGate: %v", err)
	}
	if text != "third try" {
		t.Errorf("text = %q, want attempt 3's answer", text)
	}
	if _, err := os.Stat(filepath.Join(output, "result-1.json")); err == nil {
		t.Error("published attempt 1 from a run that is attempt 2")
	}
	if len(p.userMessages) != 1 || !strings.Contains(p.userMessages[0], "Attempt 3 of 3") {
		t.Errorf("retry cards = %q, want one for attempt 3 of 3", p.userMessages)
	}
}

func TestInPlaceStartAttempt_ReadsTheEnv(t *testing.T) {
	t.Setenv("GATE_IN_PLACE_START_ATTEMPT", "2")
	if got := inPlaceStartAttempt(); got != 2 {
		t.Errorf("inPlaceStartAttempt = %d, want 2", got)
	}
	for _, v := range []string{"", "0", "-1", "two"} {
		t.Setenv("GATE_IN_PLACE_START_ATTEMPT", v)
		if got := inPlaceStartAttempt(); got != 1 {
			t.Errorf("inPlaceStartAttempt(%q) = %d, want 1", v, got)
		}
	}
}

// The last permitted attempt has nothing to wait for, so it must not park —
// the controller resolves the gate terminally either way.
func TestDriveInPlaceGate_DoesNotParkOnTheLastAttempt(t *testing.T) {
	_, output := useTempIPC(t)
	p := &mockProvider{name: "mock", model: "m", turns: []ChatResult{{Text: "only", InputTokens: 1}}}

	start := time.Now()
	text, _, _, _, err := driveInPlaceGate(context.Background(), p, nil, testKey, 1, 1, time.Minute, 5*time.Second)
	if err != nil {
		t.Fatalf("driveInPlaceGate: %v", err)
	}
	if text != "only" {
		t.Errorf("text = %q, want %q", text, "only")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("parked for %s on the last permitted attempt", elapsed)
	}
	if _, err := os.Stat(filepath.Join(output, "result-1.json")); err == nil {
		t.Error("published an attempt result for an attempt nothing will judge")
	}
}

// A park that never receives a verdict has to end: a pod that holds forever is
// the failure mode this design exists to avoid.
func TestDriveInPlaceGate_ParkTimeoutFinishesWithWhatItHas(t *testing.T) {
	useTempIPC(t)
	p := &mockProvider{name: "mock", model: "m", turns: []ChatResult{{Text: "unjudged", InputTokens: 1}}}

	text, _, _, _, err := driveInPlaceGate(context.Background(), p, nil, testKey, 1, 5, time.Minute, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("driveInPlaceGate: %v", err)
	}
	if text != "unjudged" {
		t.Errorf("text = %q, want the attempt already produced", text)
	}
}

// A "stop" verdict ends the chain on the attempt that was judged.
func TestDriveInPlaceGate_StopVerdictEndsTheChain(t *testing.T) {
	gate, output := useTempIPC(t)
	p := &mockProvider{name: "mock", model: "m", turns: []ChatResult{{Text: "approved answer", InputTokens: 5}}}

	go func() {
		waitForFile(t, filepath.Join(output, "result-1.json"), 2*time.Second)
		writeVerdict(t, gate, ipc.GateVerdict{Attempt: 1, Action: "stop"})
	}()

	text, _, _, _, err := driveInPlaceGate(context.Background(), p, nil, testKey, 1, 5, time.Minute, 5*time.Second)
	if err != nil {
		t.Fatalf("driveInPlaceGate: %v", err)
	}
	if text != "approved answer" {
		t.Errorf("text = %q, want the judged attempt's answer", text)
	}
	if len(p.userMessages) != 0 {
		t.Errorf("a stop verdict injected %d turns, want 0", len(p.userMessages))
	}
}

// A failed attempt ends the chain rather than parking: the gate judges agent
// output and there is none to judge.
func TestDriveInPlaceGate_LoopErrorEndsTheChain(t *testing.T) {
	_, output := useTempIPC(t)
	p := &mockProvider{
		name:    "mock",
		model:   "m",
		turns:   []ChatResult{{}},
		turnErr: []error{context.DeadlineExceeded},
	}

	if _, _, _, _, err := driveInPlaceGate(context.Background(), p, nil, testKey, 1, 5, time.Minute, 5*time.Second); err == nil {
		t.Fatal("a failed attempt must surface its error, not park")
	}
	if _, err := os.Stat(filepath.Join(output, "result-1.json")); err == nil {
		t.Error("published an attempt result for an attempt that failed")
	}
}

// A token-budget halt can stop on a turn whose tool calls were never answered.
// A user turn after it would be rejected, so the chain ends instead of parking.
func TestDriveInPlaceGate_TokenBudgetHaltEndsTheChain(t *testing.T) {
	t.Setenv("WORKFLOW_MEMBRANE_MAX_TOKENS_PER_RUN", "100")
	t.Setenv("WORKFLOW_MEMBRANE_TOKEN_BUDGET_ACTION", "halt")
	_, output := useTempIPC(t)
	p := &mockProvider{name: "mock", model: "m", turns: []ChatResult{{
		Text:         "halfway",
		InputTokens:  60,
		OutputTokens: 50,
		ToolCalls:    []ToolCall{{ID: "c1", Name: "read_file", Input: `{"path":"/tmp/x"}`}},
		FinishReason: "tool_calls",
	}}}

	start := time.Now()
	text, _, _, _, err := driveInPlaceGate(context.Background(), p, nil, testKey, 1, 5, time.Minute, 5*time.Second)
	if err != nil {
		t.Fatalf("driveInPlaceGate: %v", err)
	}
	if text != "halfway" {
		t.Errorf("text = %q, want the halted attempt's text", text)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("parked for %s after a budget halt", elapsed)
	}
	if _, err := os.Stat(filepath.Join(output, "result-1.json")); err == nil {
		t.Error("published an attempt result after a budget halt")
	}
	if len(p.userMessages) != 0 {
		t.Errorf("injected %d user turns after a budget halt, want 0", len(p.userMessages))
	}
}

// ── the attempt marker the controller reads ──────────────────────────────────

func TestPublishAttemptResult_WritesTheResultFile(t *testing.T) {
	_, output := useTempIPC(t)

	res := ipc.AttemptResult{Attempt: 3, Status: "success", Response: "hello"}
	res.Metrics.InputTokens = 7
	if err := publishAttemptResult(res, testKey); err != nil {
		t.Fatalf("publishAttemptResult: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(output, "result-3.json"))
	if err != nil {
		t.Fatalf("read attempt result: %v", err)
	}
	var got ipc.AttemptResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Attempt != 3 || got.Response != "hello" || got.Metrics.InputTokens != 7 {
		t.Errorf("got %+v, want the published attempt verbatim", got)
	}
	// The controller ignores an attempt it cannot verify.
	if !got.Verify(testKey) {
		t.Error("the published attempt is not signed with the run's key")
	}
}

// ── the retry card ──────────────────────────────────────────────────────────

func TestBuildRetryCard_FallsBackWhenTheGateGaveNoReason(t *testing.T) {
	card := buildRetryCard(&ipc.GateVerdict{Attempt: 1, Action: "continue"}, 2)
	if !strings.Contains(card, "without giving a reason") {
		t.Errorf("card leaves the model guessing:\n%s", card)
	}
	if strings.Contains(card, "### Gate Output") {
		t.Error("an empty gate output must not get a heading")
	}
	if !strings.Contains(card, "## Attempt 2") {
		t.Errorf("card does not say which attempt this is:\n%s", card)
	}
}

func TestGateInPlaceEnv_ReadsTheEnv(t *testing.T) {
	t.Setenv("GATE_IN_PLACE_ENABLED", "true")
	if !gateInPlaceEnabled() {
		t.Error("GATE_IN_PLACE_ENABLED=true must enable parking")
	}
	t.Setenv("GATE_IN_PLACE_ENABLED", "")
	if gateInPlaceEnabled() {
		t.Error("an unset env must keep the single-attempt path")
	}
}

func TestParkTimeout_HonoursTheOverride(t *testing.T) {
	t.Setenv("GATE_PARK_TIMEOUT", "90s")
	if got := parkTimeout(); got != 90*time.Second {
		t.Errorf("parkTimeout = %s, want 90s", got)
	}
	t.Setenv("GATE_PARK_TIMEOUT", "not-a-duration")
	if got := parkTimeout(); got != defaultParkTimeout {
		t.Errorf("parkTimeout = %s, want the default on a malformed value", got)
	}
}

func waitForFile(t *testing.T, path string, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("timed out waiting for %s", path)
}

// Without the key every marker is ignored and every verdict rejected, so
// parking would only wait out its budget. Fail before the first attempt.
func TestRunInPlaceGateLoop_RefusesToParkWithoutAKey(t *testing.T) {
	useTempIPC(t)
	t.Setenv(ipc.GateKeyEnv, "")

	_, _, _, _, err := runInPlaceGateLoop(context.Background(), promptServiceDeps{}, time.Minute)
	if err == nil || !strings.Contains(err.Error(), ipc.GateKeyEnv) {
		t.Fatalf("err = %v, want a missing %s error", err, ipc.GateKeyEnv)
	}
}

// Without the gate mount no verdict can arrive, so parking would only wait
// out its budget. Fail before the first attempt.
func TestRunInPlaceGateLoop_RefusesToParkWithoutTheGateMount(t *testing.T) {
	gate, _ := useTempIPC(t)
	if err := os.Remove(gate); err != nil {
		t.Fatalf("remove gate dir: %v", err)
	}
	t.Setenv(ipc.GateKeyEnv, "k")

	_, _, _, _, err := runInPlaceGateLoop(context.Background(), promptServiceDeps{}, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "not mounted") {
		t.Fatalf("err = %v, want a missing mount error", err)
	}
}
