# Parent/turn implementation inventory — epic #464

Repository inspection, 2026-09-08. This is evidence about the current code and
the parser and real-guest mailbox slices, not proof of a working multi-turn Harness.

## Existing primitives and constraints

- `celln-warden/src/vmm/boot.rs`: `LinuxCell::run` retains the VM object and
  `stop_when_guest_prints` can stop at an exit boundary. This is a useful
  experimental primitive, not a bounded production mailbox or lease controller.
- `set_invocation` delivers exactly one bounded envelope over port `0x510` and
  refuses repeated delivery. Preserve that ABI for one-shot execution.
- `park` explicitly refuses cells with invocation data or HTTP authority. Do not
  remove that guard to manufacture parent cloning. The existing snapshot is a
  warm mote substrate, not a qualified checkpoint of an authority-bearing parent.
- `LinuxCell::fork_from` restores a mote with fresh per-cell invocation/broker
  state. Independently admitted warm motes are the initial child source.
- HTTPS uses its existing four dedicated PIO ports. Parent/child messages need
  a separate versioned bounded interface, not an HTTP endpoint masquerading as
  a guest-accessible generic host command API.
- The native JSON worker currently caps task input at 2,048 bytes in both guest
  validation and host spec validation; Sympozium's runtime policy also caps it.
  A practical context envelope requires a coordinated, explicit extension and
  new signed artifacts. Do not silently truncate history or relax only one side.

## First implementation slice

`celln-warden/src/parent_protocol.rs` introduces a strict data-only
`celln.parent-turn/v1` request parser. It accepts a parent-scoped turn ID and
bounded task. It rejects caller-provided executable/credential/endpoint/grant
fields, duplicate fields, trailing JSON, unknown versions, unsafe identifiers
and encoded/UTF-8 byte overflow. Four focused tests passed locally.

This parser is deliberately not wired as a public spawning endpoint. Host-side
lease/parent binding, durable idempotency, budget reservation, authority
intersection and child teardown must precede invocation. Passing these tests
does not prove a parent exists or any child has run.

## Next implementation and proof

1. Build an explicit native stateful parent supervisor and versioned mailbox.
   Parent keeps live Harness context; child runs the admitted turn worker.
2. Prove guest-held state survives at least two mailbox exchanges in the same
   parent VM, with bounded waits and no reset of one-shot invocation semantics.
3. Wire host-owned child authorization and accounting before enabling requests.
4. Prove distinct real KVM children, parent survival, result handoff, failed-turn
   state handling and whole-tree teardown with guest-generated observations.
5. Bind the resulting lifecycle to enduring AgentRun; preserve existing OCI
   HarnessSession compatibility rather than extending it for this path.

The abandoned, uncommitted HarnessSession/host-transcript scaffolding was removed.
The working one-shot demo and its evidence remain separate and intact.

## Real guest mailbox proof — 2026-09-08

Celln now has an optional, separately enabled transport at PIO ports `0x520`
(host message read), `0x521` (guest response write), and `0x522` (commit/yield).
The existing invocation and HTTPS interfaces are unchanged. Frames are bounded
to 8,192 bytes; only one exchange may be outstanding. Busy host deliveries are
rejected without overwriting data. Guest overflow, premature response, malformed
or repeated commit permanently poison the mailbox. No response prefix can be
interpreted as a successful truncated request. This transport carries no spawn
authority and is not itself an authenticated public API.

Mailbox-enabled cells cannot be captured into reusable motes. Commit returns
control to the owner, which must inspect the response and resume the same VM;
KVM completes the pending output instruction on re-entry. This is live process
continuation, not a checkpoint/recovery claim.

A separate Rust hardware-test init (`guest/probes/parent-mailbox.rs`) remembers
a value in a heap allocation. One warm-forked guest accepts `remember:...`, then
two `recall` messages that do not contain the value. Responses contain guest
turn counters 1, 2, 3 and the remembered value. A second independent warm fork
receives only `recall` and returns counter 1 with empty context. All assertions
passed on real KVM; the test completed in 2.76 seconds including template boot.
That duration is not a spawn-latency measurement.

Reproduce from the Celln worktree (absolute fixture path required):

```sh
bash scripts/mkparent-probe.sh "$PWD/target/parent-probe.cpio"
CELLN_PARENT_PROBE_INITRD="$PWD/target/parent-probe.cpio" \
  CARGO_TARGET_DIR=target/validation cargo test -p celln-warden --all-features \
  parent_guest_retains_heap -- --nocapture
```

Without the explicit fixture environment variable, this hardware test reports
a skip. Do not interpret a default CI pass as this guest proof running.
The probe is never packed into normal guest assets. It is not an admitted
Harness and does not prove tool sealing, child authorization, leases, model
work, or UI integration. Those M1/M2 requirements remain open. The next step is
the admitted native parent/worker protocol and host-owned lifecycle, not exposing
this test init as a runtime or presenting it as the finished parent.

## Host turn ledger — implementation in progress

`celln-warden/src/parent_lease.rs` now implements the in-memory reservation
logic: host-bound parent incarnation, collision-safe parent/turn-derived child
identity, one outstanding child, permanent turn-ID replay refusal, bounded
turn registry (at most 1,024), and full per-turn model ceiling reservations
against aggregate budgets. No refund is issued on failure or cancellation.
The child timeout is capped by the parent's remaining monotonic lease.
Stop closes admission but retains the child owner until destruction is confirmed.
Zero-model tool work is supported without inventing model authority.

Five focused tests and all-feature warden Clippy passed. This is not yet wired
to serving requests or VM destruction. The caller must persist the reservation
before spawning, authenticate the parent, independently recheck the admitted
worker/tool/model grants, reserve actual node RAM, enforce child ceilings, and
run the expiry watchdog. The ledger does not replace any of those mechanisms.
Do not serialize/recreate it as if that recovered a lost live parent.

Two concrete integration constraints found in the current code:

- `dispatch_warm.rs` retains only one template and may prepare/boot on a cache
  miss. The enduring owner must pin an admitted worker mote before accepting
  turns and use a fork-only path. Alternating parent and worker through the
  current single-entry cache would not establish a no-hot-boot guarantee.
- Pilot's agent seccomp/capability path currently grants only the four HTTPS
  ports when fetch is enabled. The dedicated parent mailbox needs an explicit
  separately admitted capability and guest refusal tests. Do not broaden the
  HTTPS range, give a Harness arbitrary raw I/O, or reuse the privileged test
  init as the production supervisor.

## Pinned worker substrate — real KVM proof

The warm dispatcher now separates `pin` (preparation allowed) from
`PinnedMote::fork` (no cache lookup, preparation callback, or boot path).
Existing one-shot `fork` delegates through these primitives, preserving its
behaviour. An enduring owner can retain the immutable mote independently of
cache eviction, but must account for that retained memory and revalidate live
grants before creating each child. A pin is not execution authorization.

The explicit Rust hardware fixture passed `pinned_mote_forks_after_eviction_while_parent_retains_context`:
one parent stores context, the global cache is evicted, two separate child VMs
fork from the retained mote and each reports fresh guest state, each child is
dropped, and the original parent still reports its retained value and advancing
turn counter. The focused test ran in 2.92 seconds including template preparation;
this is not a spawn-latency measurement. Reproduce with the same fixture above:

```sh
CELLN_PARENT_PROBE_INITRD="$PWD/target/parent-probe.cpio" \
  CARGO_TARGET_DIR=target/validation cargo test -p celln-cli \
  pinned_mote_forks -- --nocapture
```

This resolves the fork-only primitive identified above, not the complete
enduring owner integration. The probe remains a hardware fixture, not an
admitted parent/worker Harness. Durable turn reservations, narrowed authority,
signed execution, guest-requested spawn/result handoff, watchdog teardown and
Sympozium UX are still required before the MLP can be marked complete.

## Pilot parent mailbox confinement — implementation, not yet hardware proof

Pilot now parses an explicit host invocation flag `allow_parent_mailbox`, off
by default. It requires forced agent-lane confinement, workspace access `none`,
an expected content hash and closure membership data, and refuses simultaneous
fetch permission. Existing hash/manifest checks still determine execution.
In the forked workload process, Pilot grants only ports `0x520`–`0x522` before
the existing sandbox drops every Linux capability. The port bitmap survives
exec; the parent does not retain `CAP_SYS_RAWIO`. The existing network/seccomp
filter is unchanged. Warden must separately enable its mailbox or those ports
provide no host service.

Configuration tests cover opt-in propagation, incompatible combinations and
legacy fetch compatibility. This is not yet proof that an admitted parent can
use the mailbox after exec, nor that hostile guest code cannot widen access.
The next hardware proof must execute a hash-checked, sealed parent under the
real Pilot path and attempt out-of-range I/O acquisition, raw device access and
network use. Do not enable a public parent runtime until those tests pass.

## Sealed Pilot-path proof — passed locally 2026-09-08

`celln-parent-proof` now builds a static Rust hostile parent fixture, seals it
in toolfs, creates/verifies its closure with an explicit local test signing key,
and admits its hash into the test manifest. The production Pilot binary runs
the fixture through its existing exec-by-hash and agent-lane sandbox path.
This uses test trust roots, not production catalogue admission.

The real-KVM run at `target/parent-confined-proof-1313371` passed:

- Three exchanges after exec retain the value in guest memory; only the first
  host message contains the remembered value.
- Guest `capget` observes all effective, permitted and inheritable bits zero.
- Guest attempts wider/other `ioperm` grants, `iopl`, a network socket, raw
  device creation/access and writing its executable all fail.
- A separate warm fork with the mailbox flag removed, but the host mailbox
  enabled, faults with guest SIGSEGV on its port access and sends no response.

Reproduce from Celln (no model credential or billable request involved):

```sh
cargo build --release --target x86_64-unknown-linux-musl \
  -p celln-pilot --bin celln-pilot --bin pilot-fetch
CELLN_PILOT_DIR="$PWD/target/x86_64-unknown-linux-musl/release" \
  CARGO_TARGET_DIR=target/validation cargo run -p celln-pilot --features kvm \
  --bin celln-parent-proof
```

This advances the sandbox proof above, but is still a fixture rather than the
native stateful Harness. It does not prove signed production admission, durable
parent/turn ownership, child authority intersection, model work, or UI support.

## Native parent state machine

`celln-harness-parent` is now a static native guest adapter using the pre-granted
mailbox, with no capability acquisition or model connection. Its
`celln.parent-context/v1` host envelope accepts `turn` (turnId/message) and
`result` (turnId/succeeded/answer). A turn produces a `spawn` reply containing
the existing data-only `celln.parent-turn/v1` request. Only a matching successful
result commits the user/assistant exchange into guest-held context. Failure
does not enter history; IDs are not reusable. The completed reply includes the
turn identity, success flag and answer. Unknown fields and mismatched results
are refused, and the binary exits on protocol failure rather than resetting.

Three focused state-machine tests passed, including context carried into the
next worker request, busy/replay/wrong-result refusal, failed-turn non-commit,
and explicit context overflow. This is not yet a real worker-spawn proof.
The native parent must still be exercised through Pilot and connected to
host-owned reservations plus independently admitted child execution.

The worker request currently encodes structured `history` and `message` data
inside its task string, under the existing 2 KiB worker limit. There is no
silent truncation or implicit context compaction. A practical MLP requires
the previously identified coordinated worker/spec/runtime bound extension and
structured model-message mapping before claiming general multi-turn usability.

## Native parent → real worker → parent handoff proof

`celln-parent-proof --native-turns` passed on real KVM, with artifacts under
`target/parent-confined-proof-1318129`. Unlike the earlier mailbox fixture,
this executes the actual `celln-harness-parent` binary through Pilot. The
parent requests two turns over its mailbox; the host ledger reserves each
identity; each child is a new VM forked from the authority-free warm mote and
executes a sealed, hash-checked deterministic echo worker with no egress or
workspace access. The host validates Pilot's execution grant and zero exit,
drops the child VM, and returns its real output to the parent for commit.

The second worker request contains the first user message and first child's
actual answer from retained parent state; the host second-turn input does not
replay either. Evidence includes distinct child identities, common parent,
worker hash, requests/results and framed guest consoles. The parent is dropped
after both turns. Reproduce after building `celln-harness-parent` for musl:

```sh
CELLN_PILOT_DIR="$PWD/target/x86_64-unknown-linux-musl/release" \
  CARGO_TARGET_DIR=target/validation cargo run -p celln-pilot --features kvm \
  --bin celln-parent-proof -- --native-turns
```

This is deterministic execution, not AI. The proof uses test signing roots,
an in-memory owner, and a shared prepared substrate with per-invocation closure
allowlists. It is not the production catalogue/router path, durable ownership,
independent worker-mote admission, or whole-tree cancellation/expiry proof.
Those remain required, alongside practical context bounds and real-model/UI E2E.

## Structured model context and separate turn-worker artifact

The worker library now has an explicit `run_with_history` entry point using the
same user/assistant exchange type as the native parent. Host persona remains
the system message; committed exchanges become alternating user/assistant
messages, followed by the current task. Context cannot provide system messages,
tool definitions, endpoints or credentials. Invalid text/count/byte bounds fail
before model I/O, and the existing complete 8 KiB broker-envelope cap remains.
Nineteen Pilot library tests passed, including role mapping and pre-call refusal.

The separate `celln-harness-turn` executable accepts host model/tool config as
argument one and the parent's structured context as argument two, requiring
the current message to match the configured task. The old `celln-harness-json`
entry point still passes empty history and shares the unchanged tool/broker
execution helper. Both static musl artifacts build. The new artifact is not
yet accepted by production grant/catalogue resolution or included in AI proof.

This corrects structured model-message mapping but does not yet expand the
2 KiB parent task transport. That limit remains a documented MLP usability gap;
larger history also competes with tool schemas under the unchanged broker cap.

## Real DeepSeek multi-turn parent/child proof — passed

The explicit `--real-model` parent proof passed at
`target/parent-confined-proof-1332047/native-turns.json`. The actual native
parent ran through Pilot and retained context while two distinct sealed child
VMs executed `celln-harness-turn`. Each made exactly one host-brokered DeepSeek
request. Parent identity was unchanged; children were destroyed before result
commit. The host second-turn message was only `repeat my value`.

- Turn one: user `my value is violet`; model `Noted. Your value is violet.`
- Turn two: model `Violet.` using the parent-supplied structured history.

The proof caps each child at one model request/512 output tokens, reserving
two requests/1,024 output tokens across the parent. The user-authorized key was
read literally from `.zshrc`, not executed or printed. A temporary host-only
0600 credential file was removed on runner exit; no key was delivered into a
guest, toolfs, evidence file or Kubernetes resource.

Reproduce after building the static Pilot, fetch, parent and turn binaries:

```sh
CELLN_PILOT_DIR="$PWD/target/x86_64-unknown-linux-musl/release" \
  CARGO_TARGET_DIR=target/validation cargo run -p celln-pilot --features kvm \
  --bin celln-parent-proof -- --real-model --key-from-zshrc /path/to/.zshrc
```

This command is explicitly billable. Alternatively provide an existing
`CELLN_MODEL_TOKEN_FILE` and omit `--key-from-zshrc`. The default proof makes
no model call. This passing test uses local signing roots and a bounded
in-memory proof owner, not production catalogue admission or durable lifecycle.
No borrowed tool was selected in this run. Borrowed-tool multi-turn proof,
larger context, cancellation/expiry/restart and production UI remain open.

## Real-model borrowed-tool turns — passed

The `--real-model --borrowed-tool` proof passed under
`target/parent-confined-proof-1335268`. Both distinct child VMs ran the native
turn Harness, each selected the sealed `uppercase` fixture once, and each
completed with `VIOLET`. The second user message named no value: its worker
used the original `violet` value from the persistent parent's committed
context. The framed guest tool event records input `{"text":"violet"}` and
result `{"text":"VIOLET"}`, with executable hash
`blake3:2696e782ed2d35ec85cb07057b27a4ed1068862edb5ce8b40510d87e8dd58d0c`.

Four actual model requests were made (two per child), within a pre-reserved
six-request/3,072-output-token ceiling. Each child allows one tool call and
at most three model rounds. Only the selected tool and broker client are in
the worker's execution allowlist. The parent has its separate parent-only
allowlist and no fetch permission. The temporary host credential was removed.

Reproduce with the previous real-model command plus `--borrowed-tool`. This
remains a local test-root proof with an in-memory host owner, not production
catalogue composition, durable lifecycle, cross-tenant isolation, or UI proof.
The remaining focus is serving-path integration and lifecycle/recovery gates,
not interpreting these functional proofs as completion of M1–M3.

## Durable turn stages — first journal integration

`warden::parent_journal` now publishes immutable, bounded records using
temporary files, file fsync, no-clobber publication and directory fsync.
Creating an existing parent incarnation refuses, including after owner loss
or partial creation. It does not reconstruct guest context or replenish a
lease. The caller must treat missing live ownership as context loss.

The native parent/child proof now orders operations as:
reserve host budget → persist reservation → fork/run child → destroy child →
persist child result → deliver result to parent → persist parent acknowledgement.
Child identity, parent identity, request and reserved ceilings are recorded.
No duplicate or changed stage is allowed to overwrite the existing record.

Two journal tests passed for recreation/replay refusal and distinct immutable
teardown/commit stages. The deterministic real-KVM proof also passed with this
ordering at `target/parent-confined-proof-1338901`, including on-disk journal
records. No new model request was needed for this check.

This is still proof-runner integration, not the dispatcher HTTP serving path.
Public recovery/status APIs, live-owner authentication, crash fault injection,
expiry/cancellation watchdogs, node reservation and AgentRun reconciliation
remain required. In particular, recording a caller's destruction observation
is not itself a mechanism that kills a VM.

## Bounded live owner and real guest cancellation

`warden::parent_owner` now owns a runtime on one serving thread. Initialization
and every exchange share a single monotonic `celln-control` lease. At most one
exchange is accepted; idle expiry drops the retained runtime without needing
another request. Handler failures close the owner rather than recreate context.
Cancellation is a request; `stop_and_join` separately confirms thread exit and
release of handler-owned resources. Handlers must use the control-aware KVM
and broker paths; this does not forcibly kill arbitrary uncooperative callbacks.

Three owner tests pass for idle expiry, active cancellation/backpressure and
handler failure. A real-KVM test also passes: the guest reports entering a busy
loop, the owner re-enters KVM, cancellation produces the required `TimedOut`
run report, and the VM-owning thread joins. The test was tightened to reject
generic errors/repeated diagnostic pauses; it passed in 2.93 seconds including
template boot. Reproduce with the explicit parent probe initrd and
`cargo test -p celln-warden --all-features parent_owner -- --nocapture`.

This tests one actual guest owner. The full parent/child turn handler, durable
journal, HTTP authentication and AgentRun reconciler are not yet composed into
this serving thread. Whole-tree cancellation remains an acceptance gate.

## AgentRun schema groundwork

Sympozium now defines `spec.executionLifecycle` (`one-shot` or `enduring`),
with omission retaining existing task/server semantics. The existing
`spec.lifecycle` pre/post-run hook API is unchanged. Enduring runs carry
`spec.enduring` ceilings for lease seconds, turns, model requests and output
tokens; runtime/operator grants may narrow these, not replenish them.
The initial enduring combination requires backend `celln`, catalogue selection,
and task mode. It is a native parent/worker lifecycle, not OCI server mode.

Example of the new fields only (not a deployable complete run):

```yaml
spec:
  executionLifecycle: enduring
  backend: celln
  mode: task
  cellnSelection:
    runtimeRef: approved-native-parent
    toolRefs: []
  enduring:
    leaseSeconds: 3600
    maxTurns: 20
    maxModelRequests: 60
    maxOutputTokens: 30720
```

Generated deepcopy/CRD/chart schemas are synchronized. Focused race tests pass
for legacy compatibility, limits, invalid combinations and refusal before
one-shot execution. The generated CRD passed Kubernetes server-side dry-run
validation against `kind-celln-deployed`; it was not installed.

Until serving integration exists, the controller explicitly fails enduring
requests as unavailable instead of silently launching a one-shot. This guard
must be replaced by the authenticated enduring reconciler, not removed to
fall through. No UI selector is enabled and this is not a usability handoff.

## Composed host session handler

`pilot::parent_session::ParentSession` now composes the parent transport,
independently bound child executor, host lease/turn ledger and durable journal.
Its caller-facing operation accepts user turns only; external child-result
injection is rejected before reaching either transport. It binds the parent's
spawn request to the submitted turn, persists the reservation before worker
execution, checks the destroyed child's identity, persists its result, then
validates and persists the actual parent acknowledgement. Failure closes the
session rather than rebuilding context or continuing uncertain work.

The proof runner now uses this shared handler instead of manually sequencing
those stages. Three focused tests pass for normal context sequencing/replay,
caller result injection and changed child ownership. The deterministic real-KVM
two-child proof passed at `target/parent-confined-proof-1366943` with this handler.

The executor callback remains trusted host code responsible for actual teardown
and independent admission; a struct named `DestroyedChild` is not a hardware
guarantee. Next integration must construct these transports in `ParentOwner`,
apply the existing signed runtime/model/tool checks, and expose authenticated
serving operations. The controller's enduring-unavailable guard remains intact.

## Live-owner/session composition and active-child cancellation

`parent_session::spawn_owner` now constructs the full session on the serving
thread and uses a single control scope for parent mailbox exchanges, child
KVM execution and broker work. The proof runner submits individual user turns
through the bounded owner queue and confirms shutdown with `stop_and_join`.
The deterministic two-turn path passed at `target/parent-confined-proof-1375019`.

The new non-billable `celln-parent-proof --cancel-child` mode passed at
`target/parent-confined-proof-1376739`. A sealed worker reports
`CHILD_BUSY_ENTERED` through Pilot's protected output framing and spins. The
test cancels the owner, requires the actual KVM interruption report plus the
guest marker, drops the child, and joins the owner (releasing the parent).
No child result is committed to the guest. Evidence is in
`cancelled-child.console` and `cancelled-tree.json`.

The durable reservation remains unresolved on this failure path, conservatively
preventing replay. A production teardown/status reconciler must record confirmed
cancellation; it must not interpret an unresolved reservation as permission to
retry. The HTTP/catalogue/AgentRun serving path is still not enabled. These tests
prove composed local owner mechanics, not production admission or recovery UX.

## Declared dispatcher preparation separated from execution

The production declared launcher now uses a private `PreparedDeclared` handle:
existing admission, artifact resolution, hash/closure checks and authority-free
warm preparation produce a retained mote plus bounded invocation/provenance.
Execution then rechecks live substrate/closure/Harness policy, forks only from
that handle, claims the model grant where applicable, and runs the cell.
The handle remains private until enduring admission binds it to an owner.

Thirty focused dispatcher tests passed (four explicitly ignored), followed by
an explicit complete `declared_substrate_on_real_kvm` run. The first attempt
stopped at the test's missing default-path dispatcher binary prerequisite;
after building it, the full test passed in 17.95 seconds. It covered warm
private scratch/args, workspace restrictions, broker refusal, immutable inputs,
actual HTTP dispatcher conformance, deadline/cancel teardown, invalid tool and
kernel refusal. HTTP evidence is at
`target/dispatch-conformance/1788885113562932588-1386350`. `make ci` also passed.

This validates the refactored one-shot production path, not an enduring HTTP
endpoint. Persistent-owner admission and authenticated lifecycle routes remain
the next integration requirement; no public parent capability is advertised.

## Durable turn inspection after live-owner loss

The journal now exposes read-only `inspect_turn`: reserved, child-destroyed,
or parent-committed. It never returns a reusable execution owner, and no stage
proves live context survived. The caller must consult the actual owner registry;
without that owner, context is lost even for a previously committed turn.
Concurrent inspection may lag publication and is not a linearizable serving
status endpoint. Missing/corrupt records are errors, never permission to replay.

All stages are decoded as strict, bounded records. Reservation reads rederive
the child identity; acknowledgement requires a valid versioned teardown record.
Five focused journal tests pass, including corruption and orphan-commit refusal.
The real-KVM proof now inspects records after joining the owner: two committed
turns passed at `target/parent-confined-proof-1399097`; busy-child cancellation
passed at `target/parent-confined-proof-1399268` with only a reservation remaining.
Both evidence files explicitly label live context lost after shutdown. These
runs used deterministic workers and no model credentials.
The final `CARGO_TARGET_DIR=target/validation make ci` rerun passed after
the proof integration (format, all-feature Clippy, build and tests).

This is a recovery-reporting prerequisite, not the authenticated persistent
serving API. Operator permits, node owner registry/routes, controller integration
and enduring UI remain outstanding. Direct one-shot tool execution remains
independent of optional Harness/model configuration.

## Explicit parent permit and one-use incarnation claim

`warden::parent_permit` now reads bounded, hash-pinned operator files from
`trusted-parent-permits`, separate from artifact stores. The v1 binding includes
the independently authenticated principal, unique incarnation, full admitted
parent/worker configuration hashes, parent/child memory, lifetime and turn/model
ceilings. The serving layer must compute those configuration hashes over all
closure, tool, input, model-policy and execution restrictions; executable hashes
alone are insufficient. The primitive does not perform that artifact admission.

Admission expiry is exclusive, at most five minutes and bound to Linux boot ID
and CLOCK_BOOTTIME. Unsupported hosts refuse. It is distinct from the admitted
parent lifetime (up to one day), which the live owner must enforce. `claim`
rechecks the operator file then creates the durable incarnation tombstone before
VM launch. Failed authentication consumes nothing; a successful claim cannot be
recreated even after dropping the returned lease/journal or failing startup.
Removing or changing the permit blocks subsequent admission checks; it does not
itself revoke an already-running owner.

Five focused tests pass for every binding-field substitution, caller mismatch,
expiry/boot mismatch, invalid budgets, model-free bounded work, missing/modified
operator files and replay after claim. The final full `make ci` passed.
This is a local admission primitive, not an enabled serving API: the node still
needs canonical configuration binding, tenant authentication, RAM reservation,
owner registry and authenticated routes before the controller guard can change.

## Versioned full-request configuration binding

`celln_spec::ExecutionRequest::configuration_binding` now fingerprints the full
normalized request with domain `celln.execution-configuration/v1` and a distinct
one-shot, parent or worker role. Nested object keys sort lexically, arrays retain
order, and scalars use serde_json encoding (not a claim of RFC 8785/JCS). No
identity, argument, input, closure, borrowed-tool, model-grant or authority field
is stripped. Transport limits remain the caller's responsibility; no new size
restriction is imposed on the existing local execution path.

The declared launcher pins and checks this request binding alongside its warm
handle. Five configuration tests cover role separation, normalized JSON,
identity/authority changes, immutable inputs, model grants, closure and borrowed
tool changes. Thirty dispatcher tests passed (four explicitly ignored), full
`make ci` passed, and the explicit real-KVM declared-substrate suite passed in
18.74 seconds. HTTP evidence: `target/dispatch-conformance/1788886224617544694-1416508`.

This binds existing complete execution requests, not a permission to substitute
per-turn data under an enduring worker template. A versioned native worker
template must still define host-derived turn identity, context arguments and
fresh model grants while holding executable/tool/model authority fixed. Parent
permit construction and serving must use that contract rather than stripping
fields from a one-shot request hash. No persistent route/controller guard change
or deployment was made in this slice.

## Native worker policy template and fresh real-model proof

`pilot::turn_worker::Template` now fixes model URL/name, persona, tools and their
schema bytes/limits before turns. Its versioned binding excludes only the empty
task slot by construction; supplying a task during template creation is refused.
Host-generated arguments fill that task from strict parent context, check the
reserved model/token ceilings, and validate the initial complete broker request
including history before child fork. The same context decoder is used by the
actual `celln-harness-turn` guest binary. Parent data cannot inject policy keys.
Two focused tests and final full `make ci` passed.

The proof now constructs one template before the owner and retains it across
turns. Rebuilt static guest binaries passed the real DeepSeek + borrowed uppercase
two-turn proof at `target/parent-confined-proof-1425896`. Both turns returned
`VIOLET`, used two model requests and distinct child VMs, and persisted parent
acknowledgements. Both recorded template
`blake3:4e8d763e1d499d2e6257bf62395afdaa3251b9386058d07317335597eb8430cc`.
The second turn required the original value from live parent context. The
temporary key file was removed after the run; no credential was put in evidence.

This supplies the native model/tool policy portion of the worker contract, not
the full production worker admission: executable/closure identity, template hash
and broker-grant issuance must still be composed under the parent permit and
authenticated serving owner. No HTTP route, controller guard or UI was enabled.

## Caller-scoped live-owner registry

`warden::parent_registry` now routes submit/status/cancel/stop by independently
authenticated principal and incarnation. It accepts only already-admitted
owners; it does not turn caller strings from request bodies into authentication.
Initialization runs on the owner thread. The bounded registry reserves logical
memory capacity before thread creation and keeps incarnation entries after stop
or context loss, preventing replacement. The serving layer must separately
account for actual parent, child and retained-mote memory; this is not a physical
RAM guarantee. Entries are capped at 1024, including stopped tombstones; durable
eviction/long-running service capacity management remains future integration.

Cancellation does not reclaim capacity. Stop releases the registry lock before
joining, so slow teardown does not block unrelated owners. Only a successful
join releases the reservation; a panic leaves teardown uncertain and charged.
Finished threads report context loss, not resumable context. Idle finished owners
still need a serving reconciler to call stop/join and reclaim capacity.

Four registry tests cover caller isolation, capacity, no incarnation reuse,
concurrent teardown and panic uncertainty. CI first caught an MSRV-incompatible
convenience method; it was replaced and full `make ci` passed. The native proof
now routes through `parent_session::spawn_registered`: deterministic two-turn
real-KVM execution passed at `target/parent-confined-proof-1435326`; active-child
cancellation and joined owner teardown passed at
`target/parent-confined-proof-1435465`. No model calls were made in these runs.

This is the live routing component, not an HTTP endpoint. Production tenant
authentication, permit/configuration composition, child model-grant issuance,
controller reconciliation and UI remain outstanding; no deployment changed.

## Experimental authenticated lifecycle HTTP routes

The dispatcher now owns a parent registry and routes `/v1/parents/...` through
separate operator-managed authentication. `trusted-parent-clients.json` uses
`celln.parent-clients/v1` and bounded entries `{principal, tokenHash}`, where
`tokenHash` is the BLAKE3 hash of a high-entropy bearer credential. The file is
reopened for each operation; missing/corrupt policies fail closed, duplicate
credential hashes are refused, and shared dispatcher authority is not inherited.
This is still the local dispatcher's existing non-TLS transport: external use
requires the existing TLS/proxy deployment safeguards.

Implemented operations on an already registered incarnation:

- `GET /v1/parents/<hash>`: live-owner observation, not checkpoint recovery.
- `POST .../turns`: bounded user turn submission; a 30-second observation timeout
  returns pending with `retryAuthorized: false`, without cancelling the turn.
- `GET .../turns/<turnId>`: strict durable journal stages, caller-scoped through
  the owner registry, including after a confirmed stop.
- `POST .../cancel`: cancellation requested, explicitly not teardown confirmed.
- `POST .../stop`: success only after joined teardown.

The TCP tests use a real composed ParentSession and journal with in-process
fixture transports (not VMs), verify two committed turns/read-after-stop, caller
isolation, credential rotation, malformed policies, and creation refusal. All
three focused tests passed. Prior separate real-KVM registry proofs remain the
hardware evidence; this slice does not claim HTTP-to-VM E2E coverage.

`/v1/parents` creation still returns unavailable and no enduring capability is
advertised. A new dispatcher therefore has no parents to serve. Before enabling
creation, compose permit/artifact/template/broker admission and atomically share
node capacity with one-shot/prewarm reservations. Recovery after process restart
also needs durable principal binding: the current empty registry does not expose
historical owners. Controller, UI and deployment remain unchanged.

## Parent capacity visibility and shared admission gate

Dispatcher node availability now subtracts the registry's retained memory
reservations and two cell slots per charged owner (parent plus possible active
child), including idle, stopping and teardown-uncertain owners. Cancellation or
thread exit alone does not restore capacity; a confirmed join does. Until exact
parent broker charges are tracked, any charged parent conservatively leaves no
advertised egress slots. This may underutilize a node, but cannot manufacture
spare broker capacity. Poisoned registry state advertises no capacity.

The new internal `dispatch_parents::spawn_admitted` gate checks and installs the
parent reservation while holding the same execution-registry lock as one-shot
and prewarm admission. Future creation must use this wrapper after independent
artifact/permit admission, not call the raw parent registry directly. It remains
intentionally unused outside tests while public creation is disabled.

Two focused tests passed: parent charges remain visible until joined teardown;
and simultaneous parent admissions compete atomically for the two available
slots while an existing prewarm reservation blocks parent admission. The panic
test also verifies that teardown uncertainty retains its memory charge. This is
reservation accounting evidence, not a physical-memory benchmark. Production
creation still needs the declared parent/worker launcher and broker-grant checks.

## Declared persistent-parent launcher

The declared substrate path now has an internal prepared-parent handle. It
requires a full parent-role configuration binding, one signed closure, agent
lane, hardware isolation, no workspace/input/egress/model authority and no caller
arguments. Memory, lifetime and principal must match the operator permit. The
permit is checked before warm preparation and rechecked/claimed at launch.
Live mote/closure policy and local member revocation are also rechecked. Only
after the durable incarnation claim does launch fork the pinned mote, install
the host-owned mailbox invocation and enable the warden mailbox. No worker or
model authority is inferred from this parent launch.

A focused contract test passed. The new explicit
`declared_parent_launcher_on_real_kvm` initially failed because its fixture
omitted the closure sandbox's required `/tmp` mount point; Pilot correctly
refused before parent execution. After correcting the fixture, the test passed
in 3.34 seconds: signed native parent execution, permit removal between prepare
and launch refused, guest context retained over mailbox exchanges, no hot boot,
and repeat incarnation launch refused. The result envelope in this test is a
host fixture, not an actual child/model run; earlier separate two-child proofs
are not relabelled as coverage of this new composed launcher.

The launcher remains internal and not wired to public creation. Paired worker
admission, child-bound broker grants, shared node reservation and the actual
session/HTTP/controller integration must be composed before enabling creation.

## Parent-bound child broker issuance

`dispatch_parent_model` adds the host-only `celln.parent-model-profile/v1`
operator profile under `trusted-parent-models/<hash>.json`. The full worker
binding now combines the worker-role execution request, immutable native policy
template and operator model-profile hash under
`celln.native-worker-admission/v1`. The profile binds principal, request/template
fingerprints, exact DeepSeek URL/model, an absolute host credential path, request
count and token ceilings. Reading the profile does not open the credential file.

An owner-local `ChildBrokers` issuer checks the parent incarnation, derived child
identity and reserved ceilings, claims each child's broker once, rereads the
operator profile, and returns a fresh bounded warden HTTP policy. Missing or
changed profiles refuse issuance; failures do not make the child claim reusable.
The parent ledger/journal still provide aggregate accounting and durable replay
protection. This in-memory issuer is explicitly not a recovery mechanism or an
independent artifact admission decision.

Three focused tests pass for bounded one-use child brokers, missing credential
files remaining unopened, live profile revocation, retry refusal, changed child
identity and widened reservations. No provider request was made. The issuer is
not yet connected to the declared worker executor; signed closure/tool selection
verification and actual per-turn fork/run/teardown must be composed with it next.
Public creation remains disabled and nothing was deployed.

Verification caveat for this slice: the first full CI run failed the existing
`a_state_root_has_one_dispatcher_owner_and_releases_on_drop` immediate-reacquire
assertion. It passed in isolation, then the complete `make ci` rerun passed.
No lock implementation or assertion was changed; the intermittent failure's
cause remains unverified and should be investigated if it recurs.

## Declared worker executor and acknowledgement ordering

`dispatch_parent_worker` now combines the pinned declared substrate, fixed native
policy template and owner-local broker issuer. It checks that the closure contains
exactly the selected static members, or validates the selected composition roots
and their dependencies. Every turn rechecks live mote/closure/member policy,
constructs bounded context arguments, obtains its one-use broker, forks from the
retained mote and consumes the cell through the declared runner. Only successful,
hash-validated execution and a unique bounded completion event produce a
`DestroyedChild` result after teardown. Failure closes the session path rather
than inventing a successful result. Sub-millisecond remaining time is refused,
not widened to a one-millisecond execution allowance.

The runner has an explicit internal broker-policy path so the legacy direct-exec
GET policy cannot overwrite a newly authorized model broker. Existing one-shot
callers keep their original path. Two worker tests cover tool substitution/extra
members and completion parsing. The first CI attempt caught a malformed schema
in the new test fixture, which was corrected.

CI also exposed a timing-sensitive HTTP session failure. Inspection found that
ParentOwner sent a completion before publishing readiness for another turn.
Readiness now precedes acknowledgement (failed handlers cancel first), with a
1,000-turn immediate-resubmission regression test. Final full `make ci` passed.
The explicit existing declared-substrate real-KVM suite passed in 17.58 seconds;
HTTP evidence: `target/dispatch-conformance/1788888428298180910-1480641`.

The new worker executor itself still needs an explicit paired-parent/model KVM
proof; the existing one-shot hardware regression does not establish that. Paired
session construction and public creation remain disabled pending that integration.
No controller, UI or deployment was changed.

## Paired declared parent/worker proof with real inference

Prepared parent and worker handles now compose into the shared ParentSession
only when their complete permit bindings match. Each parent mailbox exchange
checks Pilot's protected execution-grant record (exact runtime hash, agent lane,
no fetch and no workspace) and refuses unexpected parent exit/failure.

The first combined hardware attempt uncovered a real startup scheduling issue:
the workload can commit mailbox output before the guest supervisor gets CPU time
to emit its successful-exec record. Parent launch now runs to the exact protected
Pilot Started record before delivering any turn, then clears that stop marker.
This adds explicit startup acknowledgement rather than relaxing grant checks.

`declared_parent_worker_pair_real_model` then passed in 9.74 seconds, using two
separate signed images, operator parent/model profiles, retained warm handles,
the declared worker executor and real DeepSeek. Turn one recorded the value;
turn two answered `violet` using the parent's retained context. Distinct child
identities and both durable parent commits were checked after joining the owner.
The temporary key was removed. Evidence:
`target/declared-parent-pair-1490288-1788888920994901557/pair-results.json`.

This new composed proof is model-only (no borrowed tool selection); the earlier
borrowed-uppercase proofs are separate evidence and do not establish that case
through this new launcher. HTTP creation, registry/capacity integration of this
pair, controller, UI and deployed E2E are still outstanding. Nothing was deployed.

Follow-up hardware regression caught an additional idle-startup defect in the
native parent: it treated an empty RX mailbox as a malformed frame. Empty RX is
`0xff`; the adapter now polls the low length word atomically, then reads the high
word only after a real frame arrives. This avoids splitting an idle byte-read
across host delivery and uses only the existing three-port permission range
(a 32-bit IN would require the ungranted fourth port). A mailbox unit test covers
idle word polling followed by a complete frame. The declared parent regression
then passed in 3.40 seconds; the corrected paired real-model proof passed in
9.90 seconds at `target/declared-parent-pair-1497628-1788889169127446500`.

## Finished-owner reclamation

The dispatcher now runs a 100 ms maintenance sweep that joins only owner threads
already observed finished. Successful join releases the logical reservation but
retains the incarnation tombstone and `ContextLost` status. A later stop can
acknowledge confirmed teardown without changing that status or permitting replay.
Panicked joins remain `TeardownUncertain` and retain their capacity charge.
The sweep takes ownership under the registry lock and joins outside it; active
handlers are neither cancelled nor waited on. Its weak reference does not keep
the serving state alive after shutdown.

Two new registry tests cover expiry, capacity release, continued operation of an
unrelated live owner, caller isolation, replay refusal, idempotent reconciliation,
and retention after panic. All six registry tests and full `make ci` passed;
CI log: `target/validation/parent-reaper-ci.log` in the Celln implementation
worktree. This is host lifecycle evidence, not a new hardware or deployed proof.
Authenticated parent creation, controller integration, UI and deployed E2E remain
outstanding. No services were deployed or restarted for this change.

The execution model also explicitly preserves harness-free one-shots: a declared
tool can run directly in a disposable cell and return its output without model
credentials or harness configuration. Determinism, where required, needs explicit
constraints on inputs, time, randomness and external effects; lack of a harness
alone does not establish it.

## Authenticated creation and real-model HTTP proof

`POST /v1/parents` now accepts only a bounded (1 KiB), strict selection envelope:

```json
{"apiVersion":"celln.parent-create/v1","launchProfile":"blake3:<64 lowercase hex digits>"}
```

The separately authenticated principal selects an operator-owned, content-hashed
`trusted-parent-launches/<hex>.json` file (64 KiB maximum), not an uploadable store
object. Its `celln.parent-launch/v1` profile fixes `parent`, `worker`, `template`,
`modelProfile`, `permit`, `binding` and `reservedMemoryBytes`. Parent configuration,
principal/permit and worker/template/model bindings are checked before creation.
The capacity gate serializes against one-shot/prewarm admission, then claims the
durable incarnation before starting the owner thread or preparing either runtime.
Preparation independently verifies declared artifacts and closures. Parent launch
rechecks the permit but retains the original claimed lease rather than resetting
its elapsed lifetime. Failure after claim burns the incarnation.

The operator reservation must exceed twice the sum of parent/child guest RAM,
to account at least for retained warm motes and live guests plus host overhead.
This necessary floor is not measured physical memory enforcement or sufficient
automatic sizing; the operator must budget artifact buffers and host overhead.
One broker slot is required; existing conservative parent accounting remains.
Creation returns 202 with `initializationPending:true` and no retry permission,
not a false ready/healthy acknowledgement. Registry `Owned` still includes startup.
Creation failure returns a non-retryable reconciliation response. Restart recovery
of caller-scoped journal status and explicit readiness remain unfinished.

The explicit real DeepSeek/KVM paired test now traverses the authenticated HTTP
handler, creation/capacity/claim path, registry turn delivery and HTTP joined stop.
It passed in 8.84 seconds. The second turn answered `violet`; both distinct child
identities and durable parent commits were verified, stop released the reservation,
and repeat creation of the same incarnation was refused. The temporary model key
was removed. Evidence: `target/declared-parent-pair-1516790-1788890039452154205/`.
This is model-only, local HTTP-handler evidence, not a deployed server/controller/UI
proof or a borrowed-tool proof through the new route. No capability advertisement
or controller guard was enabled and no services were deployed.

## Explicit live readiness

The ambiguous experimental `Owned` status is replaced by `Initializing`, `Ready`
and `TurnActive`, alongside `Stopping`, `ContextLost`, `Stopped` and
`TeardownUncertain`. The owner publishes initialization completion only after its
admitted runtime constructor returns (the declared parent constructor checks
Pilot's protected execution acknowledgement). Cancellation/deadline and observed
thread exit override readiness. Status is an observation, not a promise that a
later submission will succeed; capacity and one-active-turn checks still apply.
The existing bounded queue can accept one turn during initialization, but the
controller should wait for `Ready` before submitting its first turn.

Two synchronized owner tests cover blocked initialization, readiness, active work,
cancellation, and failed initialization never reporting ready. Full `make ci`
passed (`target/validation/parent-readiness-ci.log`). The real-model HTTP proof now
polls authenticated status until `Ready` before submitting any turn, and passed
in 9.69 seconds with retained `violet` context, separate children and joined stop.
Evidence: `target/declared-parent-pair-1524926-1788890259281677638/`.
Restart reconciliation, borrowed tools through the creation route, controller/UI
integration and deployed E2E remain unfinished; nothing was deployed.

## Borrowed tool through authenticated HTTP creation

The paired proof now has a separate explicit, billable borrowed-tool variant.
It compiles the static Rust uppercase fixture, includes its hash in the signed
worker closure and selected schema-bound template, and budgets up to three model
requests and one tool call per child. Test-only worker evidence records actual
native events and host broker counts after child teardown; production does not
persist conversation output at this boundary. Assertions require one uppercase
event with the selected executable hash, input `violet`, result `VIOLET`, one
completed tool call and two actual model requests per child, with no broker denial.
Creation, readiness polling, turns and joined stop all traverse authenticated HTTP.

An initial borrowed proof passed in 12.18 seconds. A subsequent run failed when
the second model response denied knowing the value and made no tool call. Its
durable reservation contains the original user message and prior `VIOLET` answer,
so context reached the child; that evidence alone does not capture the outbound
wire. The proof instructions were clarified to use earlier conversation messages
and execute the tool anew, without adding the value to the second message or
weakening any execution assertion. No automatic model retry was introduced.

Both borrowed-tool and model-only variants then passed together in 20.41 seconds;
final borrowed evidence: `target/declared-parent-pair-1533358-1788890659561601068/`;
model-only: `target/declared-parent-pair-1533358-1788890670726858134/`.
Full `make ci` passed (`target/validation/parent-borrowed-ci.log`). Temporary keys
were removed. This establishes successful local HTTP executions, not statistical
model reliability, general tool compatibility, deployed controller/UI operation,
or restart reconciliation. Those remaining requirements are not closed.

## Caller-scoped historical reconciliation

Permit claim now durably publishes an immutable `owner.json` alongside the parent
tombstone before runtime preparation. It binds the independently authenticated
principal and incarnation, contains no credential, and grants historical reads
only. Missing, partial, corrupt or mismatched ownership records grant no access;
even incomplete creation retains its replay-prevention tombstone.

When no caller-scoped registry owner is available, authenticated GET routes consult
that binding and can return `ContextLost` with `statusIsLiveOwnerObservation:false`
and the immutable turn stage. All mutation routes refuse with no retry permission
and no teardown confirmation. Historical reads do not require a still-valid launch
permit but do require current client authentication, so credential revocation still
applies. Existing legacy journals without ownership records remain inaccessible
through this fallback rather than inferring ownership from user input.

New journal/HTTP tests cover immutable binding, invalid/missing records, wrong
principals, replay refusal and read-only recovery. Full `make ci` passed. The real
model HTTP proof passed in 9.18 seconds, then constructed fresh serving state with
an empty registry and verified both committed turns remained readable only by the
bound caller, context was reported lost, and all mutations were refused. Evidence:
`target/declared-parent-pair-1541196-1788890846606524987/`; CI log:
`target/validation/parent-recovery-ci.log`.

This tests fresh-registry reconciliation after confirmed stop, not abrupt process
death during a live child. Crash/teardown-uncertain E2E, controller/UI integration,
deployment and remaining resource-accounting work are still outstanding.

## Sympozium parent lifecycle client

`internal/cellnparent` now provides the controller-side creation, status, turn,
journal, cancel and joined-stop client. The operator supplies an exact frozen
owner origin and separate absolute credential-file path; the client does not use
the one-shot dispatcher token or infer routing from task data. Credentials are
bounded and reread per request. HTTPS is required except literal loopback test
origins; TLS uses a supplied trust pool, ambient proxies are disabled, redirects
are refused, and mutations are never automatically retried. Transport failures,
ambiguous statuses, invalid responses and mismatched identities require preserving
the original incarnation/turn and reconciliation, not issuing replacement work.

Responses are size-bounded and decoded against explicit envelopes. The client
distinguishes accepted initialization, pending turns, completed answers,
cancellation requests and confirmed teardown. Historical turn evidence preserves
owner status and checks stage, parent and turn identities; the future reconciler
must also bind the child/request/budgets to its frozen admission before applying
evidence. A committed answer cannot turn `ContextLost` into ready state.

HTTP fixture tests cover lifecycle calls, credential rotation, redirected or
ambiguous creation with exactly one request, pending/oversized responses, historical
status, mismatched journal identity and invalid authority/path input. Race-enabled
client and focused Celln controller tests passed; `go vet ./internal/cellnparent`
and diff whitespace checks passed. These are Go fixture/compatibility tests, not
yet a Go-to-live-Celln interoperability proof. The enduring controller guard remains
enabled pending frozen admission and lifecycle reconciliation wiring. No cluster
or UI was changed in this slice.

## Frozen parent admission and durable creation attempt

AgentRun status now has an optional `cellnParent` record containing immutable
owner origin, principal, run UID, complete typed-spec SHA-256, launch-profile hash
and incarnation. It contains no credentials and is not independent execution
authority. CRD transition rules prevent removing the record, changing its binding,
or clearing its `createAttempted` bit. Deepcopy, base CRD and both chart copies
were regenerated.

The parent admission helper requires an independently operator-approved binding,
valid enduring intent and exact UID/spec match. Changes to task, limits, runtime
selection or owner refuse, as does replacing an existing one-shot/Job execution.
`Prepare` persists the binding without network effects. `ClaimCreate` uses a fresh
read and resource-version-checked status update to persist the attempt before a
caller may POST; already-attempted creation returns reconciliation-only. A crash
between that write and dispatch sacrifices automatic retry rather than risking a
duplicate parent. The future reconciler must report that uncertainty honestly.

Race-enabled tests cover UID reuse, spec/owner/principal changes, preparation before
claim, two concurrent claimants with exactly one winner, and durable repeat refusal.
Focused API/Celln controller regressions and vet passed after correcting a test
fixture to use the actual TaskSpec type. The generated CRD passed a server-side
dry run on `kind-celln-deployed`; kubectl reported a non-fatal legacy last-applied
annotation migration conflict. The schema was not installed. Operator approval
loading, parent lifecycle reconciliation and UI delivery remain unwired, and the
enduring guard remains enabled. No readiness or deployment completion is claimed.

## Operator approval loader and startup coordination

The parent package now loads bounded, strict operator deployment JSON:
`sympozium.ai/celln-parent-controller-v1`, with `approvals` entries containing
`namespace`, `name`, `binding`, `tokenFile` and optional `caFile`. Selection requires
the exact namespaced run and UID. Duplicate UIDs/incarnations are refused; complete
intent is revalidated against the approved spec digest and persisted binding.
Configuration is reread per reconciliation, so removal is not hidden by a cache.
Token/CA paths must be absolute; CA files are bounded and bearer contents are read
only when making a request. Loading performs no network, issuance or status write.
This is an initial per-run operator-provisioned bridge, not automatic catalogue
parent issuance.

`ReconcileStart` performs one step: persist preparation without dispatch; claim
the attempt before the first POST; thereafter query only the frozen incarnation.
HTTP-fixture tests verify an ambiguous creation reply produces exactly one POST
and subsequent GETs, with the durable attempt visible before creation reaches the
server. Loader tests cover UID reuse, removal, malformed/oversized files, duplicate
authority and no credential read during selection. Race-enabled parent and focused
Celln controller tests, vet and whitespace checks passed.

This coordinator is not yet called by the AgentRun reconciler. Turn persistence,
enduring phase/feed integration, cancellation/deletion reconciliation, automatic
approval provisioning and live Go-to-Celln/deployed E2E remain unfinished. The
controller guard still prevents exposing this as a complete user-facing feature.

## Recovery-only joined stop

`ReconcileStop` now reads the existing run and targets only its frozen incarnation.
A private recovery binding loader allows cleanup after spec drift or deletion,
while still requiring the exact saved operator binding and current credential
configuration. It does not invoke startup admission or authorize changed intent.
There is no replacement creation, turn submission or implicit retry in this path.
Only HTTP 200 with explicit joined-teardown confirmation succeeds; missing owner,
historical context loss, conflict and ambiguous transport responses remain errors.
No attempted parent is treated as a separate local-admission reconciliation case,
not proof of a remote teardown. Removing operator configuration still prevents
remote access; this helper cannot invent credentials for cleanup.

Race-enabled tests exercise deleting/spec-mutated runs, refusal of new admission,
the exact original stop URL, 200 versus 404/409/502 handling, one HTTP attempt, and
rejection of a changed principal before network access. Parent-package and focused
Celln controller tests, vet and whitespace checks passed. The helper itself does
not remove finalizers or update lifecycle status. Wiring startup/stop and durable
turn handling into the AgentRun reconciler remains outstanding; nothing was deployed.

## Opt-in AgentRun parent startup and cleanup wiring

The controller now accepts the explicit deployment setting `CELLN_PARENT_CONFIG`.
With that operator path configured, enduring Pending runs use the startup coordinator
and Running parent runs poll the same owner rather than the one-shot execution path.
An existing frozen parent binding also prevents Pending intent changes from falling
through to another backend. Without configuration, new enduring intent is still
refused; already-owned parents retain their recovery identity.

Readiness is recorded as the `CellnParentReady` condition. A live initialized owner
moves the run to Running, not Succeeded; no initial-task completion is invented.
Status is freshly read after startup's durable writes, and unchanged observations
avoid unnecessary status updates. Terminal/unrelated phases cannot acquire a new
creation claim. Context loss produces failure without reconstructing a parent.
Completed/deleting runs invoke joined stop before generic cleanup/finalizer removal;
missing configuration or uncertain teardown keeps cleanup pending.

The configured controller fixture verifies one creation, native Running/readiness,
no Job or one-shot action, no recreation on Running reconciliation, and preservation
of the finalizer/attempt on teardown conflict. Race-enabled parent and focused Celln
controller tests and controller build passed; whitespace checks passed. These are
HTTP-fixture tests, not a deployed controller-to-KVM proof. No controller was restarted
or config enabled on the cluster. Initial-task delivery, durable subsequent turns,
results/feed/UI and complete crash/deployed E2E remain required before user handoff.

## Durable initial turn in the controller

Parent status now includes an optional immutable initial-turn identity, exact
bounded message, derived child identity, monotonic attempt bit and immutable result.
Transition rules prevent removing the turn/result or clearing an attempt. The
initial child ID uses the host's BLAKE3 hash of the serialized parent/turn tuple.
Preparation is persisted without submission; readiness is checked and the attempt
is persisted before POST. A pending/lost response is reconciled by GET of the same
turn. Only a parent-committed record with matching child and bounded result is
accepted from the journal. No result write authorizes a retry.

The configured AgentRun reconciler now drives this initial turn after native parent
readiness, rereads status after its writes and records `CellnInitialTurnComplete`.
A committed answer remains within a Running enduring run rather than completing
the parent. HTTP fixture tests cover durable attempt-before-POST, an ambiguous
response followed by journal recovery with exactly one submission, and a direct
controller completion. Parent-package and focused Celln controller race tests,
vet and whitespace checks passed. Deepcopy and all CRD copies were regenerated;
the Kind server-side schema dry run passed with the same non-fatal legacy annotation
warning. Nothing was installed or deployed.

This slice handles only the initial task. Subsequent interactive turn admission,
historical feed/results, UI and deployed multi-turn proof remain outstanding.
Recovery of initial evidence after parent context loss also needs controller-level
coverage; the lower-level historical-read transport already exists. No broad MLP
completion is claimed from the initial-turn fixture tests.

## Committed answer recovery after parent context loss

The controller now uses an explicit recovery-only initial-turn path when the owner
reports ContextLost, Stopped or TeardownUncertain. This path requires a previously
attempted turn and cannot prepare or submit new work. A valid committed journal
answer is persisted independently of the parent lifecycle, so the run can report
the initial turn committed while correctly marking live parent context unavailable.

The new controller fixture simulates a lost turn reply followed by historical
ContextLost status and a committed journal entry. It exposed a real observation
loss: the shared failure helper rereads status and did not preserve in-memory
parent/turn conditions. Those observations are now saved before terminal failure
handling. The final test verifies exactly one turn submission, retained answer,
initial-turn completion, parent readiness false, parent lifecycle failed, and
finalizer retention on uncertain teardown. A lower-level test refuses recovery
of an unattempted turn without POST. Race-enabled parent/Celln controller tests,
vet and whitespace checks passed. This is fixture-based failure evidence, not a
real dispatcher crash test. Subsequent interactive turns and deployed/UI E2E remain.

## Durable subsequent-turn resource

Added the namespaced `AgentRunTurn` message/result resource. AgentRun remains the
lifecycle owner; this child record is not a second agent or an AgentHarness CR.
Separate bounded resources avoid putting up to 1,024 full messages/results into
one parent object and exceeding Kubernetes object-size limits. The immutable spec
contains only run name, run UID and message. There are no executable, tool, model,
credential or endpoint fields. Status reuses the immutable identity/attempt/result
contract and fixes the parent incarnation before future dispatch.

The construction helper assigns a same-namespace AgentRun controller owner
reference. Binding requires the persisted turn UID, exact original parent UID,
owner reference and frozen incarnation; its host turn ID is derived from that UID
and its child ID from the parent/turn tuple. Name reuse, input replacement,
cross-namespace targeting, missing UID and oversized/NUL input are refused.
These helpers do not replace API authentication, operator approval, readiness or
the still-required one-active-turn serialization gate.

Deepcopy/base CRD/both chart copies were generated. Race-enabled parent, API and
focused Celln controller tests passed, as did vet and whitespace checks. The new
CRD passed a Kind server-side dry run and was not installed. Queueing, turn dispatch,
API endpoints and UI creation of these records are not yet wired. This adds durable
data structure and identity validation, not a completed interactive turn path.

## One active subsequent turn and durable budget claims

Parent status now holds one active turn name/UID and a nondecreasing count of
accepted subsequent turns. `ClaimTurnSlot` fresh-reads the run/turn, revalidates
operator approval and requires the initial turn's committed success before a
resource-version-checked parent status update. Competing turn claims cannot both
win. Repeated claims for the same UID do not consume another budget unit. The
initial turn counts separately toward the configured total; claimed budget is
not refunded after uncertainty.

`ReleaseTurnSlot` requires the exact turn's durable attempted/result record. A
missing/deleted/uncertain record cannot release the slot, and a late completion
cannot clear another turn's active reference. These are controller serialization
and accounting rules, not substitutes for host-enforced authority/turn budgets.
The helpers make no HTTP calls and do not yet dispatch subsequent turns.

Race-enabled tests prove one winner, idempotent repeated claim, retention without
a result, correct release, refusal of late cross-turn release and budget exhaustion
including the initial turn. Parent and focused controller tests, vet and whitespace
checks passed. Generated deepcopy/CRDs were refreshed; the Kind schema dry run
passed with the existing non-fatal legacy annotation warning. Nothing was installed.
Subsequent-turn execution, API/UI and deployed E2E remain to be connected.

## Subsequent-turn dispatch coordinator and controller registration

`ReconcileTurn` now connects the saved AgentRunTurn to its parent's serialization
slot, frozen execution identity, readiness, attempt-before-POST rule and journal
recovery. An already-attempted turn uses the exact recovery binding and GETs its
evidence rather than resubmitting. Only a matching parent-committed result is
accepted from the journal. The result is saved to the child resource before the
parent slot can be released; reconciliation after that save does not execute again.

An AgentRunTurn reconciler is registered when `CELLN_PARENT_CONFIG` is configured.
It watches queued records and invokes this coordinator; it cannot create parent
VMs, Kubernetes workloads, tool selections or model grants. The Helm manager role
gains turn read/watch and status-update permissions only. Uncertain or busy turns
are requeued with their original identity. No API endpoint or UI writes these
records yet, and deleting an uncertain record still conservatively retains its
parent slot for reconciliation rather than guessing completion.

The two-subsequent-turn HTTP fixture covers a lost first reply recovered from the
journal, direct completion of the second, repeated result observation, exactly two
submissions, durable results, two consumed budget units, released slots and a still
Running parent. Race-enabled parent and focused controller tests, controller build,
vet and whitespace checks passed. This is not live Go-to-Celln or deployed E2E
evidence; API/UI and that integration proof remain outstanding. No deployment changed.

## Run turn API and replay-stable request identity

Added POST/GET `/api/v1/runs/{name}/turns` under the existing API authentication
middleware and namespace selection convention. POST accepts only `runUID`,
`requestId` (bounded alphanumeric/underscore/hyphen) and `message`; it cannot accept
runtime/model/tool authority. The run must be enduring with a successful initial
turn and remaining declared turn capacity. The API creates a data-only child and
returns 202, not execution success. Repeating an existing request observes that
same record; changed input or a stale run UID is refused. No turn deletion/status
mutation endpoint is exposed. The apiserver Helm role gains turn get/list/create,
not turn-status write permission.

History is namespace-paginated (256 resources per page) and filtered by immutable
run UID/name, so a reused run name does not inherit prior history. Consumers must
follow continuation tokens even when a filtered page is empty. Existing global API
authentication/namespace semantics are preserved; this is not new per-user RBAC.

The API review changed host turn identity derivation from child UID to the tuple
of parent run UID, record name and exact message. A persisted Kubernetes UID is
still required and checked for slot ownership, but deleting/recreating the same
request cannot mint another host execution identity. Binding the exact message
also prevents a changed input from inheriting an earlier committed answer. While
an original record exists, changed input with the same request ID conflicts; after
operator deletion a different message is a different host identity, still bounded
by the parent's host-enforced budgets. Host journals remain necessary for replay
exclusion independent of Kubernetes record retention.

API tests cover accepted creation, existing-request observation, conflicting input,
stale UID, unknown authority fields, oversized messages and namespace/run-UID history
isolation. Race-enabled API/parent/focused controller tests, vet and whitespace
checks passed. UI and live Go-to-Celln/deployed multi-turn proof remain outstanding;
no service was deployed or restarted.

## Persistent conversation browser contract

The enduring run detail now renders the initial exchange and paginated
AgentRunTurn history, checks the returned parent UID, and permits a bounded next
message only while the parent reports ready and has available turn capacity.
The panel does not reinterpret an old answer as a live parent. One-shot runs keep
their existing result presentation; direct tool execution remains independent of
optional Harness configuration, as specified in the overlay epic's product model.

Submission saves its request identity in session storage before POST. This API
operation opts out of the shared client's network retry loop. An unconfirmed
submission remains disabled across reload and is reconciled through history;
neither a network error nor reload creates a fresh request identity. This is
application-level retry exclusion, not a guarantee that browsers or intermediaries
never retransmit a request: API identity checks and the host journal remain vital.

The initial socket-reset Cypress fixture observed seven intercepted requests, so
it was unsuitable for asserting the number of application fetch calls. The test
now rejects at the fetch boundary and counts application calls explicitly; the
four browser checks passed (committed answer, one application submission
across failure/reload, context-loss send refusal, and run-UID mismatch refusal).
The existing eight one-shot selection/result checks also passed: 12/12 combined.
The production web build and whitespace check passed. These are intercepted UI
contracts, not live model, controller-to-Celln, or deployed multi-turn evidence.
Run creation opt-in and operator admission provisioning still need integration.

## Explicit enduring creation intent

The run-create API now accepts and preserves `executionLifecycle` and `enduring`
ceilings. It validates lifecycle combinations before Kubernetes lookup and rejects
an initial enduring message exceeding 2048 UTF-8 bytes or containing NUL. Neither
resource creation nor the 201 response fabricates parent admission or readiness.
Missing operator approval still cannot fall back to one-shot execution.

Race-enabled creation tests cover preserved limits, legacy/default and explicit
one-shot compatibility, missing/stray limits, unknown lifecycle, exceeded budgets,
message bounds and rejection of non-catalogue enduring intent. Focused API, parent
and controller race suites and the production web build passed. The browser API
type exposes these fields and disables automatic network retries for enduring
creation, which currently has no idempotency key. The run-create form does not
expose an enduring option yet; automatic parent approval provisioning and live
deployed proof remain outstanding. No deployment changed.

`docs/guides/celln-enduring-run.md` records the YAML/API contract, the required
operator binding, conversation behavior and explicit development limitations.

## Enduring request form

The native Harness + Celln creation form now exposes an explicit development-only
enduring opt-in with editable lease, turn, model-request and output-token ceilings.
It validates bounded integers and the initial-message byte/NUL limits before
submission. Opting out omits both lifecycle and enduring fields, preserving the
one-shot request. The form keeps Harness selection separate from this lifecycle
choice; it does not advertise arbitrary runtime compatibility.

The enduring choice hides the one-shot permission preview and automatic issuance
copy, replacing them with an explicit exact-run operator approval requirement.
Catalogue runtime metadata and node preflight do not attest to persistent parent
support. An unconfirmed parent creation locks further form submission while the
page remains mounted; this is not durable creation idempotency across reload.
The guide describes this limitation and the need to check the run list.

Browser coverage adds exact enduring request fields, invalid-budget refusal,
opt-out preservation of one-shot requests, and uncertain-create submission lock.
The first combined run passed 14/15 checks; the failure-message assertion needed
to scroll within the existing bounded-height dialog. The corrected combined suite
passed 15/15, and the production web build and whitespace check passed.
Automatic approval provisioning and deployed controller/Celln/model proof
remain unfinished; this form is a development request path, not a ready service.

## Issuance lifecycle boundary review

Reviewing the automatic one-shot issuer before parent integration found that its
run-provisioning guard and catalogue reader did not explicitly reject enduring
intent or saved parent ownership. Controller routing separated these paths, but
direct helper callers could still enter one-shot planning/provisioning. This was
not evidence of a deployed exploit or a parent being launched as a one-shot.

Both boundaries now reject any lifecycle other than omitted/one-shot, any enduring
limits, and any saved CellnParent status. A parent-bound run cannot regain one-shot
authority merely by changing its spec lifecycle. Catalogue revalidation uses the
same guarded reader, so an old frozen selection cannot bypass this check. Explicit
and legacy one-shots remain supported. A future parent issuer must have a separate
parent-specific protocol rather than weakening these one-shot checks.

Focused race tests passed for these refusal cases, frozen selection, execution
candidate construction, run issuance and controller Celln/enduring behavior.
The first new fake-client test incorrectly attempted a status-subresource update
on a fixture without status-subresource support; using that fixture's ordinary
update corrected the test, with the saved-parent refusal assertion retained.
Full authority/issuer package race tests, vet and whitespace checks passed. No Celln code or
deployment was changed in this slice; live cross-language parent proof and
automatic approval provisioning remain outstanding.

## Real Go client → Celln parent interoperability

Added an opt-in `TestLiveCellnParentClient`, launched as a separately compiled Go
test executable by Celln's existing declared parent/worker hardware proof. The
test-only Celln bridge binds a literal-loopback listener and serves the actual
authenticated HTTP handler. It passes only the launch/parent identities, a
private proof bearer-file path and result path. Model credentials remain with
the host proof, never in Go request payloads or the guest.

The real Go client creates the parent, polls initialized readiness, submits two
turns without POST retries, reads parent-committed journals, checks exact derived
child identities and distinct children, and confirms the parent remains ready.
Celln retains its independent borrowed-tool/broker evidence assertions, joined
stop, replay refusal and historical caller-isolation checks after the Go client
exits. This is not a fake HTTP response test or a deployed controller/UI proof.

The borrowed-tool run passed on real KVM with DeepSeek in 10.20 seconds (Go client
9.41 seconds). Both answers were `VIOLET`; the second prompt did not repeat the
value. Each child performed exactly one approved uppercase call on `violet`,
returned `VIOLET`, used two model requests and recorded zero broker denials.
Evidence lives in the Celln integration worktree at
`target/declared-parent-pair-1603189-1788894661376224238/`, including
`interop-results.json`, two worker proofs and the parent journal.

Reproduce after building the native guest prerequisites:

```sh
# In the Sympozium integration worktree:
go test -c -o target/celln-parent-interop.test ./internal/cellnparent
# In the Celln integration worktree:
CELLN_PARENT_INTEROP_BINARY=/absolute/sympozium-worktree/target/celln-parent-interop.test \
CELLN_PARENT_MODEL_KEY_FROM_ZSHRC=/absolute/path/to/.zshrc \
CARGO_TARGET_DIR=target/validation cargo test -p celln-cli \
  declared_parent_worker_pair_borrowed_tool -- --ignored --nocapture --test-threads=1
```

The key-source reader does not source the shell profile or print the secret.
The temporary proof bearer file is removed after the subprocess exits. The
external-client hook is test-only and requires an explicit absolute executable
path. Ordinary Go tests skip this live test unless the launcher supplies its
environment; a skip is not hardware evidence. Parent package race tests passed.
Celln `make ci` also passed; its log is `target/parent-interop-ci.log`.
Automatic catalogue-to-parent provisioning and the actual controller/deployed
browser journey remain outstanding. No service deployment changed.

## Real Celln execution through the Go coordinators

The live interop launcher now also supports `CELLN_INTEROP_COORDINATOR=1`.
Instead of direct creation/submission calls, this invokes `ReconcileStart`,
`ReconcileInitialTurn` and `ReconcileTurn` against the real Celln HTTP owner.
Kubernetes storage is explicitly a fake client with status subresources; the
proof constructs an exact per-run operator approval rather than exercising
automatic issuance. It does not prove API-server persistence, CRD admission,
controller watches, restart recovery or a deployed browser journey.

The coordinator proof checks saved initial and subsequent results, exact parent
and turn binding, distinct child identities, one consumed subsequent-turn slot,
slot release, a still-Running parent and repeated completed reconciliation.
Celln's existing independent hardware checks now follow the returned turn IDs
(`initial` and the coordinator-derived hash rather than hard-coded `one`/`two`),
while still requiring exactly two results, distinct children, exact borrowed-tool
arguments/results, model counts, joined teardown and historical isolation.

An initial successful run exposed a fixture-description mismatch on inspection:
the synthetic AgentRun requested lower ceilings than the independently prepared
host profile. The fixture was corrected to match the actual host profile: 180s
lease, two turns, six aggregate model requests and 3072 aggregate output tokens.
This correction does not establish automatic budget-to-grant provisioning.

The corrected proof passed in 10.16s (Go coordinator path 9.35s), with both answers
`VIOLET`, one uppercase call and two actual DeepSeek requests per child.
Evidence: Celln worktree
`target/declared-parent-pair-1609476-1788894892542623909/`.
Reproduce with the previous interop command plus `CELLN_INTEROP_COORDINATOR=1`.
Celln `make ci` passed (`target/parent-coordinator-ci.log`), as did the parent Go
package race tests, vet and whitespace checks. Nothing was deployed.

## Real Kubernetes persistence with the live coordinators

The coordinator proof now accepts explicit
`CELLN_INTEROP_KUBE_CONTEXT=kind-celln-deployed`. This uses the actual Kubernetes
API server, real assigned run/turn UIDs, generated schema validation and status
subresources instead of the fake client. It creates a uniquely named namespace
and removes only that namespace with a UID precondition after archiving results.
No other context is accepted by this cleanup-capable proof.

The existing active catalogue controller was verified to watch only its own
namespace; the older cluster-wide proof controller had zero replicas. After
reviewing the additive schema diff and successful server dry runs, the AgentRun
CRD was updated and AgentRunTurn CRD installed on Kind. Server-side apply first
reported ownership of `spec.versions` by client-side apply; the existing
client-side workflow was used without forcing field ownership. Both CRDs remain
installed. No controller/UI deployment or replica count was changed.

The final proof passed in 11.43s (Go path 10.63s), preserving both `VIOLET` answers
in actual API resources. It additionally proved that the API server rejects a
committed answer mutation and rollback of the parent creation-attempt flag.
The archived run stayed Running, acceptedTurns was one and activeTurn was absent.
Celln's independent tool/model/child/teardown assertions also passed.

Evidence: Celln worktree
`target/declared-parent-pair-1612754-1788895078069242964/`, especially
`interop-results.json.resources.json`. Run UID:
`43d2ca20-72ab-408a-893f-96d9c8c63fde`; turn UID:
`59f06a48-866d-4eb7-b5c8-62f7cef3c12d`. Temporary namespace
`celln-parent-interop-86rhc` was confirmed removed. The earlier first successful
real-persistence run's namespace `celln-parent-interop-c5lz8` was also removed;
the unrelated catalogue controller remained available.

Reproduce with the previous coordinator command plus
`CELLN_INTEROP_KUBE_CONTEXT=kind-celln-deployed` and the new CRDs installed.
This still drives coordinator functions directly, supplies operator approval
from the proof, and manually sets Running after observing owner readiness. It
does not prove controller-manager watches, automatic issuance or deployed UI
interaction. Those remain required next integration steps.

## Live controller-manager watch proof

Added `internal/controller/celln_parent_live_test.go`, built as a separate test
executable for the same Celln hardware launcher. It starts a real controller
manager with a namespace-scoped cache and the production AgentRun and
AgentRunTurn reconcilers. The test creates resources and observes status; it does
not call coordinator functions or set Running itself. The controller establishes
readiness, saves the initial result, dispatches the follow-up, releases its slot,
and retains the parent as Running. Deleting the run exercises the actual parent
stop/finalizer path before namespace cleanup.

The first run executed both turns but failed test cleanup: the test's deferred
context cancellation stopped its manager before testing.T cleanup callbacks.
The manager now has a separately controlled lifetime and is stopped only after
cleanup observes run deletion. The first run's namespace was conservatively
retained. After confirming its Celln proof process (PID 1615658) had exited and
the resources were archived at
`target/declared-parent-pair-1615658-1788895264285120924/interop-results.json.resources.json`,
the test-owned leftover finalizer was removed using exact UID/finalizer JSON
preconditions and namespace `celln-parent-manager-ftctm` deleted. This manual
failed-test cleanup is not evidence of production finalizer success.

The corrected run passed naturally in 13.41s (Go manager portion 12.62s), including
controller-driven joined stop and finalizer removal. Both answers were `VIOLET`;
the independent Celln checks retained distinct-child/tool/model/replay assertions.
Evidence: Celln worktree
`target/declared-parent-pair-1617366-1788895326091792846/`.
Its archived run UID is `c2385972-c201-4e7c-a53e-d23a215049c7`; driver is
`real-controller-manager`. Namespace `celln-parent-manager-wlmm5` was confirmed gone.

Build with `go test -c -o target/celln-parent-manager.test ./internal/controller`,
then use the hardware interop command with that executable and
`CELLN_INTEROP_KUBE_CONTEXT=kind-celln-deployed`. This manager runs locally against
Kind using the test kubeconfig, not under deployed controller service-account
RBAC. Approval remains provisioned by the proof. Automatic parent admission,
deployment/RBAC qualification and the browser-driven journey remain unfinished.

## Authenticated API → manager → real Celln proof

The manager proof now lives in the external controller test package so it can
also instantiate the production API server without an import cycle. Its local
HTTP listener serves the real API handler with a distinct random seeded bearer
reader. It explicitly verifies unauthenticated run-history access receives 401.
No API routes or Celln replies are intercepted or fabricated.

The proof creates an Agent in its isolated namespace, creates the enduring run
via POST `/api/v1/runs`, and binds operator approval to the returned persisted
UID/spec. Once controller watches establish readiness and the initial result,
POST `/api/v1/runs/{name}/turns` creates the follow-up. Repeating that same request
returns 200 with the same turn UID, not another execution. GET turn history must
return the matching run UID and single committed turn. Manager-driven deletion
and joined parent teardown remain required before namespace removal.

The final run passed in 14.82s (Go API/manager portion 14.04s). Evidence lives in
the Celln worktree at
`target/declared-parent-pair-1621992-1788895558713138389/`, with resource archive
driver `real-api-controller-manager`. Namespace `celln-parent-manager-x99qc` was
cleaned up. Focused API/controller race tests, vet and whitespace checks passed.
An earlier API/manager run also passed in 14.59s; its namespace
`celln-parent-manager-w2hzl` was confirmed gone. A cancellation-time watch warning
in that earlier run did not prevent finalizer cleanup or manager shutdown.

The test also handles namespace cleanup if setup fails before the manager starts;
after manager startup, uncertain finalizer cleanup still retains evidence/resources
rather than forcing success. This remains a local API and manager against Kind,
not a deployed service-account/RBAC or browser rendering proof. Exact parent
approval is prepared by the test; automatic admission remains unfinished.

## Live browser conversation proof

The API server now exposes `HandlerWithUI` so its real authenticated API/SPA
handler can run on a listener owned by the integration proof. `StartWithUI` uses
that same handler. With `CELLN_INTEROP_WEB_DIR` set to an absolute built web
directory, the manager proof serves `dist` and launches Cypress against that
actual server. No HTTP routes are intercepted. The browser opens the existing
ready enduring run, submits the follow-up, observes its committed answer, reloads,
and checks retained history and the exhausted two-turn ceiling.

The first live browser run failed correctly: the second model response was
lowercase `violet`, with one model request and zero borrowed-tool calls according
to the host worker trace. The UI faithfully displayed that answer. This was not
a missing UI update. Evidence is retained at Celln worktree
`target/declared-parent-pair-1624063-1788895705296395296/`. The follow-up instruction
was strengthened to require a new tool invocation rather than answering from
memory/reusing an earlier result; it still does not repeat the original value.
The browser's result assertion timeout was also made explicitly 45s. Tool/model
evidence assertions were not relaxed, and no execution was automatically retried.

The revised proof passed in 19.55s (Go/API/manager/browser portion 18.74s). Both
answers were `VIOLET`, with distinct disposable children, one actual uppercase
call and two actual DeepSeek requests per child. Results persisted through a
browser reload, and controller cleanup joined the parent and removed the run.
Evidence: Celln worktree
`target/declared-parent-pair-1626565-1788895788084736514/`, whose resource archive
has `browser:true`. Run UID: `cc3fadba-2b02-42da-852b-28e6cd782ed7`.
Namespace `celln-parent-manager-z7s57` was confirmed removed; the failed browser
proof namespace `celln-parent-manager-glrz2` was also cleaned up normally.

Reproduce after `npm run build` and rebuilding the manager test executable:
add `CELLN_INTEROP_WEB_DIR=/absolute/sympozium-worktree/web` to the existing
Kind/manager hardware command. Focused API/controller race tests, vet, web build
and whitespace checks passed. A model can still ignore tool-use instructions;
one passing rerun is not a statistical tool-use reliability guarantee.

This proves interaction on an existing admitted run, not creation through the
selection form or automatic parent provisioning. The proof has no NATS event bus;
the global connection badge can report Offline while polled run/turn APIs work.
It is not yet the coherent event-feed-enabled user environment or deployed-RBAC
qualification required for handoff. Those gaps remain open.

## Live browser with authenticated event-bus connection

The manager/browser proof can now start its own loopback NATS JetStream process
using an explicit absolute `CELLN_INTEROP_NATS_BINARY`. It provisions a private
temporary config with fresh username/password credentials and storage, connects
the real event-bus client, and supplies that bus to the API and manager. It never
connects to the existing demo bus or publishes synthetic model-output events.
Cleanup closes the client and terminates/joins only its own NATS process.

With this option, Cypress requires the actual `Stream Connected` indicator before
the follow-up and again after reload. The built UI, real authenticated API, Kind
resources, controller watches, Celln owner, DeepSeek and borrowed uppercase tool
were all used in the passing proof. It completed in 19.00s (Go/browser path
18.25s); both answers were `VIOLET`, and all independent child/tool/model/teardown
assertions remained enabled. Evidence: Celln worktree
`target/declared-parent-pair-1629571-1788895979027076106/`; the resource archive
records `browser:true` and `eventBus:true`. Namespace
`celln-parent-manager-86vjc` was confirmed removed. Normal NATS disconnection
messages occurred only during proof cleanup.

Reproduce with the prior browser command plus
`CELLN_INTEROP_NATS_BINARY=/absolute/path/to/nats-server`. The binary used here was
`/home/axjns/Code/sympozium-m0-celln/target/nats-proof/bin/nats-server`.
Focused event-bus/API/controller race tests, vet and whitespace checks passed.
This checks a live authenticated stream connection; it does not establish new
per-turn lifecycle event delivery or token streaming. Answers still use history
polling. Creation-form-to-admission provisioning, deployed RBAC qualification,
operational gates and a persistent hands-on environment remain unfinished.

## Parent-specific catalogue selection planning

Added `cellnauthority.Loader.FreezeParentRun` and
`RevalidateParentSelection` as the read-only first stage for parent issuance.
Unlike the existing one-shot planner, this path never calls `Prepare` or emits
a disposable one-shot execution composition. It pins the live run UID/generation
and complete spec digest, independently resolves the Agent/runtime references,
intersects the explicit ordered tool selection against operator/runtime/Agent
grant sources, and records their revisions and exact resolved tool identities.
The run is reread after multi-object resolution; revalidation requires the same
intent, subjects, source revisions and resolved grants.

The versioned ParentSelection is not dispatch authority, a model permit or a
parent-compatible-runtime assertion. Reusing the JSON tool resolver only proves
the selected worker tool contract/grant intersection. A registered parent/worker
architecture and independently bounded host launch/permit must still be bound
to this selection. The new planner is not wired to automatic issuance yet.

Tests cover pinned subjects/three grant layers, refusal by the separate one-shot
planner, unchanged selection, changed task/budgets/revisions, withdrawn grants,
one-shot lifecycle substitution, implicit and duplicate tool selection. Full
authority-package race tests, vet and whitespace checks passed. This is planning
and revalidation evidence, not new live issuance or deployment evidence.

## Binding a parent selection to a prepared operator registration

Added `cellnparent.ParentLaunchRegistration` and `BindRegisteredParent`.
The trusted registration references an already prepared host launch/permit by
launch-profile hash and incarnation, with exact owner/principal, credential-file
locations, model/persona and declared host ceilings. Its selection digest binds
the entire resolved Agent/runtime/tool/grant-source snapshot, not runtime/tool
names alone. The binder revalidates that snapshot, rereads the run identity and
returns an exact run-UID/spec-bound approval only if the registration matches.

All aggregate host ceilings must be no larger than the requested run ceilings,
and the registered lease must fit an explicit timeout. Unsupported pod-only,
hook, environment, context or dry-run semantics are refused rather than ignored.
Model/persona must match exactly. No secret is read, HTTP called, host allocation
made or approval persisted by this helper.

This registration is one prepared incarnation, not a renewable reusable parent
template. A caller must durably claim it before publishing the returned approval;
the host journal remains the final replay barrier. Actual bytes, architecture,
permit validity and correspondence of declared registration ceilings to the host
profile still require independent host verification. This helper alone does not
prove those properties or complete automatic issuance. Effective narrowed limits
also still need to be represented in user-facing readiness/accounting.

Tests cover exact binding, narrowing, every aggregate ceiling widening, changed
model/persona/selection, relative credential paths, changed live task, withdrawn
grant and unsupported pod environment. Full parent and authority race tests, vet
and whitespace checks passed. Automatic assignment/publication, actual registered
profile provisioning and live integration of this binder remain unfinished.

## Durable one-use registration assignment

`ClaimRegisteredParent` now joins the live registration binder to an operator-owned
filesystem journal. Records are keyed by parent incarnation and bind the complete
returned approval plus the exact registration digest. A synced temporary file is
published using a non-replacing hard link, followed by directory sync. Existing
records are accepted only for byte-identical claims; a competing run, changed
registration or credential path is refused. Missing journals are not created,
and unsupported publication/sync semantics return errors rather than inventing
durability. The helper performs no dispatch or controller-config publication.

All issuers for the registration pool must use the same trusted journal. Deleting
records is not a supported replenishment mechanism; host journals remain the final
replay barrier. Live grants are revalidated after assignment sync. If that check
fails, the incarnation remains consumed rather than being reassigned during a
revocation race. A fresh successful return is still not an execution retry grant.

Tests prove one winner between concurrent different-run claims, identical-claim
recovery, refusal of changed registration/credential binding, private file mode,
temporary-file cleanup, preservation of corrupt records and refusal of absent or
relative journals. Positive registration tests also exercise binding through
durable claim twice. Parent/authority race tests, vet and whitespace checks passed.
Filesystem crash/power-loss testing, automatic approval publication and live
registration-assignment integration remain outstanding; nothing was deployed.

## Non-replacing per-run approval publication

`PublishRegisteredParent` now performs live binding/revalidation and durable
one-use assignment before publishing a controller-readable approval. Approval
records use a SHA-256-derived filename for the exact Kubernetes run UID. The
same synced, non-replacing publication primitive is used for assignment and
approval records; it never rewrites a shared multi-run file or replaces an
existing differing approval. Both directories must already exist and be
operator-owned. Publication failure does not refund the assigned incarnation.

`CELLN_PARENT_CONFIG` may now point to that approval directory. The loader reads
only the current run UID's record, requires exactly one binding, and retains its
namespace/name/UID/spec checks. The existing single-file configuration remains
supported. Directory mode depends on the shared assignment journal to prevent
automatic cross-run incarnation reuse; arbitrary operator-written records are
still trusted input, not self-authenticating grants.

Publication is an explicit approval action, not a watcher or execution call.
Revocation must withdraw the underlying registration/grants before automation
publishes again. Removing a published file prevents controller loading, but does
not by itself revoke an otherwise valid registration that an operator explicitly
asks the publisher to approve again. No authority renewal or automatic deletion
of journal records is provided.

Positive tests now run bind → claim → publish → controller load twice and require
the same binding. Directory tests cover run-name reuse with a different UID,
ambiguous records and immediate observation of file removal. Parent/authority
race tests and focused controller regressions, vet and whitespace checks passed.
This is not yet a registration-selection watcher, host template issuer or live
automatic admission proof; that integration remains outstanding.

## Exact prepared-registration selection and recovery pin

`SelectRegisteredParent` now resolves current enduring run intent and selects
exactly one operator-prepared registration by the complete selection snapshot,
model and persona. Missing or ambiguous candidates refuse before journal writes;
the binder still validates limits, unsupported execution options and live grants.
The selector does not fall through to a second candidate when binding fails.

Before claiming the host incarnation, it durably pins the complete approval and
registration digest under the run UID in the shared operator journal. This closes
the recovery gap where approval publication fails and a changed registration
list might otherwise assign another parent on retry. A differing choice refuses
before consuming the replacement incarnation. Identical retries can complete the
original publication, with fresh grant validation and no host execution call.
The choice record is not refunded on subsequent failure.

Tests cover empty/unmatched/ambiguous selection without journal writes, failure
after choice and incarnation assignment, refusal of a replacement without
consuming it, and repeated recovery of the original approval. Parent and authority
race suites pass. This is a callable admission selector, not yet controller
startup wiring, automatic host preparation, or new live E2E evidence. The product
model remains unchanged: direct tool one-shots need neither Harness nor model;
only the enduring Harness path needs a retained parent and disposable turn cells.

## Prepared-registration controller admission wiring

The controller now optionally loads `CELLN_PARENT_REGISTRATIONS` through an
uncached reader, and requires its approval directory to exactly match
`CELLN_PARENT_CONFIG`. The strict bounded JSON configuration identifies three
distinct trusted grant sources, the shared durable journal and prepared launch
registrations. Missing directories, malformed configuration and inconsistent
approval routing fail startup. Registration contents are reread for each unbound
run; source/directory routing changes are refused in a running dispatcher.

Before parent startup, the reconciler invokes admission only when no parent
binding is present. The dispatcher independently rereads the run and skips
already-bound parents. Admission refusal returns before startup, without host
requests or fallback execution. Existing recovery and stop paths do not depend
on registration availability, and never use this dispatcher to replace a parent.
Registration withdrawal is new-admission control, not live-parent cancellation.

Tests exercise repeated real dispatcher publication, withdrawal observation,
journal-route replacement refusal, strict configuration failures, controller
refusal without an execution identity, and exactly one admission invocation
across startup/running/context-loss tests. Focused race tests and controller
entrypoint tests pass. Live registration-based startup and fresh host preparation
remain unproven; earlier live tests used explicit per-run approvals.

## Live prepared-registration admission proof (2026-09-08)

The real API/controller-manager proof now creates an AgentRuntime and CellnTool
from public metadata exported by the hardware fixture's actual executable,
signed closure and warm-mote artifacts. The uppercase tool gets its own signed
closure; the worker still contains the tool selected by the prepared host
template. Three independent grant documents bind real Kubernetes Agent/runtime/
tool UIDs and specs. The operator registration pins that resolved snapshot and
the prepared host launch. No per-run approval is written by the test: the
production registration dispatcher selects, claims and publishes it during
controller reconciliation.

This integration exposed that the create-run API did not carry an explicit
`systemPrompt` into the run. It now maps that optional field to the existing
spec field, so parent registration can bind the actual requested persona rather
than silently comparing an empty intent to a nonempty host template. Regression
tests cover preservation for enduring and existing one-shot requests. This is
explicit request data, not a new inherited Agent persona or model credential.

Successful evidence is in the Celln validation worktree at
`target/declared-parent-pair-1652954-1788897570978024359/`:

- `interop-results.json.resources.json` records `admission=prepared-registration`,
  `driver=real-api-controller-manager`, `browser=true` and `eventBus=true`.
- Real run UID `f6e7c776-756c-4720-a163-96a6ef61f654` remained Running after two
  committed `VIOLET` answers, with distinct initial/follow-up child identities.
- The browser sent the follow-up without repeating the original value, showed
  both answers after reload, and disabled sending at the two-turn ceiling.
- Host proof assertions checked a fresh uppercase call and two brokered model
  requests per child, original parent context, joined stop and reclaimed capacity.
- The test passed in 24.44 seconds (Go/browser portion 23.43 seconds). Namespace
  `celln-parent-manager-cvt7q` was removed through normal run finalization/cleanup.

An earlier launch ran the previous Go binary after compilation failed; its
`1651153-1788897505840079944` evidence is only the old explicit-approval path,
not registration proof. That run and namespace `celln-parent-manager-t4gwd`
also completed and cleaned up. Neither is counted as a replay of uncertain work.

The current proof still uses an isolated local manager with admin credentials,
operator-prepared host launch/permit and an explicitly unused placeholder OCI
image in runtime metadata. It does not prove OCI compatibility, general parent
template issuance, dynamic tool delivery, deployed service-account RBAC, continuous
Kubernetes grant revocation, or browser creation-form selection. The API creates
the initial run; the browser proves the live follow-up conversation. No persistent
hands-on environment was left running.

Post-change verification: full race suites for `internal/apiserver`,
`internal/cellnparent`, `internal/controller` and `internal/cellnauthority`,
targeted vet and whitespace checks passed. Celln `make ci` passed; its log is
`target/parent-registration-ci.log` in the validation worktree.

The enduring creation form now exposes the explicit system prompt and explains
prepared-registration admission accurately. It omits that field after opting out
and resets it after successful creation. Production web build and all 11 focused
selection/result Cypress tests pass, including prompt preservation and opt-out.
These are intercepted UI contract tests, not a new live creation-form proof.
The first Cypress attempt lacked the fixture auth token and was stopped; the
successful run supplied a test-only token. The owned Vite server was stopped.

## Visible admission waiting state

Prepared-registration refusal now records `CellnParentReady=False` with reason
`AdmissionPending` and a fixed user-safe explanation. Raw operator errors and
paths are not copied into resource status. The observation rereads UID,
generation, deletion state, lifecycle phase and parent binding before writing;
it cannot overwrite a concurrently bound parent's observation. Repeated identical
refusals do not rewrite status. No parent/execution identity is created by this
reporting path, and the original admission error still reaches controller logs.

The conversation panel renders this waiting explanation only for an unbound
parent and a condition observing the current resource generation. Sending stays
disabled. Tests cover no execution on refusal, stable repeated status, refusal
of a stale write after parent binding, visible admission guidance and hiding a
stale-generation message. Controller/parent race suites, vet, production web build
and all five intercepted conversation Cypress tests pass. The owned local test
server was stopped. This improves pending-run feedback; it does not qualify
automatic host preparation or the live browser creation-form journey.

## Current-intent readiness for subsequent turns

The conversation UI now enables sending only when `CellnParentReady=True`
observes the run's current generation. A historical answer or older readiness
condition cannot enable the composer for changed intent. The API independently
requires current-generation readiness and validates the complete run spec against
its frozen parent binding before creating a new turn. Existing exact request
identities remain observable through the idempotent response path even after
readiness or intent changes; they are never recreated as new executions.

API tests independently reject stale-generation readiness, changed spec with a
current readiness condition, and explicit not-ready status, while checking that
no refused turn is stored and the original request remains queryable. Full API,
controller and parent race suites, API vet and production web build pass. All six
intercepted conversation browser tests pass, including stale readiness with
historical answers retained. This does not replace the host's per-turn authority
checks or claim that Kubernetes reads are a transaction with host execution.

## Readiness regression run exposed tool-use reliability (2026-09-08)

After rebuilding the Go manager binary, the full real browser/Kind/DeepSeek proof
was rerun with current-generation readiness enforcement. Evidence:
`target/declared-parent-pair-1678791-1788898536401829083/` in the Celln worktree.
The Go/browser portion completed in 21.94 seconds: both answers were `VIOLET`,
distinct children committed, and the parent remained Running until normal joined
cleanup. Namespace `celln-parent-manager-n2jq6` was confirmed removed.

The overall Rust proof FAILED in 22.95 seconds. The follow-up worker made only
one model request and zero tool calls, despite its prompt explicitly requiring a
fresh uppercase invocation. The initial worker made two model requests and one
uppercase call. Thus the UI/API readiness path worked, but this is not a passing
borrowed-tool end-to-end regression. The existing host-side assertion correctly
rejected the superficially correct final answer. No assertion was weakened and
no uncertain turn was replayed.

Inspection shows the native JSON harness currently sends `tool_choice=auto` and
has a maximum-call ceiling but no explicit minimum/required-call policy. An
optional harness contract for required tool use needs evaluation before claiming
reliable compliance with tool-mandatory tasks. Prompt strengthening alone has
already proved insufficient. Such a contract must preserve optional tool use by
default and cannot expand selected tool authority or silently rerun a completed
turn. Earlier successful proofs remain evidence of capability, not statistical
tool-use reliability.

## Opt-in native required-tool contract

Celln's native JSON harness now accepts optional `require_tool_call: true`.
It requests provider `tool_choice=required` until a tool executes, then permits
normal final-answer generation. Independently of provider compliance, an early
final answer with zero executed tools fails locally without retry or a completed
event. Invalid configurations (no selected tool, no call budget or no model
result-turn budget) are refused before contacting the broker. Existing name,
schema, hash and call-ceiling checks still apply; this cannot lend new tools.

Omitted/false policy is not serialized, preserving original template bytes and
optional-tool behaviour. Enabling it changes the immutable native template hash,
requiring corresponding host admission. It cannot be supplied in parent turn
message data. Tests prove provider request transitions, refusal of skipped tools
without retry, no fabricated completion, invalid-budget refusal, non-lent tool
refusal, backward serialization and distinct required/optional template bindings.
All 11 native JSON harness tests and Celln `make ci` pass
(`target/required-tool-ci.log` in the validation worktree).

This remains native host configuration: no Sympozium YAML/UI policy field or
catalogue issuer mapping has been added, the live fixture has not been switched,
and rebuilt guest execution with this policy is not yet qualified. The previous
failed regression remains failed. The next step is explicit prepared-policy
integration and a fresh live proof, not rerunning the same optional policy until
the model happens to comply.

## Required-tool intent integrated and live-proven (2026-09-08)

Optional `enduring.requireToolCall` is now carried by AgentRun YAML/API and the
enduring creation form. Validation requires at least one selected tool and model
request/output budgets. Parent registration requires exact equality of the
requirement, unlike numeric ceilings which may shrink. Its value is included in
the complete run intent digest. Defaults remain optional tool use; direct and
Harness one-shot issuer behaviour is unchanged. The generated CRD addition was
reviewed, server-dry-run checked and installed on explicit `kind-celln-deployed`.

The native worker/parent/JSON binaries were rebuilt for musl. The borrowed-tool
fixture now explicitly prepares a required-call template and matching enduring
run, rather than repeating the previous optional policy. Full live proof passed
in 23.08 seconds (Go/browser 22.08 seconds). Evidence in the Celln worktree:
`target/declared-parent-pair-1692840-1788898925598399686/`.

The saved run UID is `cc8c7398-60c1-4672-8ab1-d216e5d10041` and records
`requireToolCall=true` and `admission=prepared-registration`. The exact trusted
launch also records `template.require_tool_call=true`. Both answers are `VIOLET`;
the host assertions verified a fresh uppercase call and two model requests per
distinct child, persistent parent context and joined teardown. Namespace
`celln-parent-manager-t4p9f` was confirmed removed. This is one successful real
model proof of the explicit contract, not a universal provider reliability claim.

API/type/parent/controller race suites and the production web build passed.
All 12 intercepted selection/result Cypress tests passed, including the new
opt-in and refusal without selected tools. The owned Vite server was stopped.
The live initial run is still API-created; browser creation-form end-to-end and
fresh host template provisioning remain outstanding.

Final regression checks: Celln `make ci` passed, logged at
`target/required-tool-integration-ci.log`; targeted Go vet and whitespace checks
also passed.

## Full browser creation → persistent conversation proof (2026-09-08)

The owned live hardware test now starts through the real New Run form when web
testing is enabled. `celln-parent-create-live.cy.ts` uses no API intercepts and
no API substitute for creation: it selects the Agent, explicitly overrides the
Harness to `worker`, chooses Celln, lends uppercase, enables enduring lifecycle
and required-tool execution, enters the exact system prompt and bounded budgets,
then submits once. The Go driver reads Kubernetes afterwards and requires
exactly one run with preserved intent before preparing its matching registration.
The production controller then admits and starts the parent. A second browser
journey sends the follow-up and verifies the committed conversation after reload.

The full proof passed in 31.26 seconds (Go/browser portion 30.18 seconds).
Evidence in the Celln validation worktree:
`target/declared-parent-pair-1698573-1788899113067745856/`.
The resource archive records `browserCreation=true`, `browser=true`,
`eventBus=true`, `admission=prepared-registration`, explicit `worker` runtime,
uppercase revision `v1`, and `requireToolCall=true`. Both turns answered `VIOLET`
in distinct child cells with fresh uppercase execution and two model requests
each, checked by the host-side evidence assertions. The original value was not
repeated in the follow-up. Joined teardown completed and namespace
`celln-parent-manager-6m7jd` was confirmed removed.

Controller/API/parent race suites, controller vet and whitespace checks passed.
This closes the missing real browser entry-point proof for the prepared native
pair. It still relies on operator-prepared host artifacts/permit and a local
admin-credential manager; general fresh-host provisioning, deployed RBAC and a
persistent hands-on environment are separate remaining work. No test services
were left running.

## Host-local permit construction for provisioning

Celln now provides `Permit::issue` for explicitly trusted local operator callers.
It reads the current host boot ID/monotonic clock, validates the complete binding
and requires a whole-millisecond admission window of 1–300000 ms with checked
expiry arithmetic. It neither selects authority nor publishes a permit, claims
an incarnation, prepares motes or launches a guest. A provisioning service must
still independently authorize its binding and durably pin issuance to a run.
The live-pair fixture now uses this constructor instead of hand-assembling time
fields; this fixture change has compiled but has not yet been live-rerun.

Tests cover binding preservation, window limits/submillisecond rejection, bad
boot IDs, clock overflow and invalid lease limits. The explicit KVM-feature suite
also checks actual host-clock sourcing and demonstrates that issuing/publishing
a different valid permit for an already consumed incarnation does not bypass its
existing claim journal. All seven permit tests pass with `--features kvm`.
Celln `make ci` passed (`target/parent-permit-issuance-ci.log`); the subsequent
test-only replay assertion also passed in the focused KVM-feature suite.
Automatic durable permit/profile publication and fresh-incarnation allocation
remain outstanding.

## Durable local permit publication

`Permit::publish` now writes an already-issued permit into the pre-existing
operator-owned `trusted-parent-permits` directory. It validates against the real
host clock, serializes bounded content, syncs a private temporary file, publishes
without replacing an existing record and syncs the directory. An existing record
is accepted only for identical bytes. Corrupt/partial content is preserved and
refused, never repaired silently. The public method rechecks expiry after sync;
expiry during publication returns an error without deleting or renewing the
record. Absolute operator roots are required and missing directories are not
created by the publishing primitive.

This is not allocation or execution: successful publication does not claim a
parent, and failure does not permit changing an already-pinned incarnation. The
live-pair fixture now calls issue → publish rather than writing the permit file
directly. That updated fixture has compiled, but has not yet been live-rerun.

Nine explicit KVM-feature permit tests pass, including actual host-clock
issue/publish/authorise, identical repeated publication, concurrent publication,
private file mode, absent/relative directory refusal, corrupt-record preservation
and consumed-incarnation replay refusal. Celln `make ci` passes with log
`target/parent-permit-publication-ci.log`; a subsequent test-only expansion of
the public publication check also passes. These are filesystem protocol tests,
not power-loss qualification. Durable run-to-incarnation allocation and launch
profile publication remain the next provisioning steps.

## Durable run-to-incarnation issuance

Celln now exposes `run_incarnation(scope, run_uid)` and `issue_for_run` for trusted
local provisioning. A versioned hash of the stable operator-selected cluster/
issuer scope and immutable run UID determines the incarnation. Neither intent
changes nor new timestamps create another incarnation for the same identity.
The caller must independently authorize the complete intent hash and binding;
these helpers do not interpret tenant requests or grant tools.

Issuance pins the intent hash, complete parent/worker binding, admission window
and original permit in a synced, non-replacing record under a pre-existing
operator-owned `parent-issuance` directory. Reconciliation reads the winning
record rather than returning this attempt's candidate timestamps. Different
intent, limits or admission window refuse; expired/previous-boot permits cannot
be renewed through recovery. Corrupt records remain untouched. All issuers and
restarts for a scope must retain and share this directory. No journal deletion
or fallback identity is a supported repair operation.

Eleven explicit KVM-feature permit tests pass. New cases cover stable identity,
scope/UID separation, recovery of identical original permit bytes, no expiry
extension, changed-input refusal, corrupt-record preservation and exactly one
winning intent between concurrent issuers. This is tested local issuance,
not yet a deployed Sympozium provisioner or a fresh-parent live proof. Connecting
the authorized Kubernetes selection to this issuer and publishing the launch
profile remain outstanding.

Celln `make ci` and whitespace checks passed. CI log:
`target/parent-run-issuance-ci.log` in the validation worktree.

### Validated local launch publication and live regression

Celln's local `parent_create::publish` now shares validation with parent startup:
exact parent/worker/template/model bindings, current permit and principal, and
the logical memory reservation floor. It accepts bounded operator-local JSON,
not tenant HTTP uploads. Publication requires an absolute operator root and a
pre-existing protected launch directory, syncs a private temporary file, and
publishes without replacing an existing record. Exact retries recover the same
hash; corrupt records refuse and remain untouched. A final admission check
refuses authority expired or withdrawn during publication. No incarnation is
claimed, credential read, mote prepared or VM created by this helper.

Two focused tests pass for publication/recovery, private permissions, corruption
preservation, size/path checks and mismatched principal, persona, parent/worker
authority or limits. Celln `make ci` passes after fixing a needless-borrow lint;
the production web build also passes. The real signed parent/worker fixture now
uses this publisher rather than manually writing its launch profile.

The browser/Kind/KVM/DeepSeek regression passed in 32.40 seconds (Go/browser
31.28 seconds), including initial browser harness/Celln/tool selection,
follow-up and reload, real tool calls in both distinct child cells, retained
parent context, and joined teardown. Evidence in the Celln validation worktree:
`target/declared-parent-pair-1729347-1788900051154815024/`.
Logs: `target/parent-launch-publication-ci.log` and
`target/parent-launch-publication-live.log`. The isolated
`celln-parent-manager-pzpvx` namespace was confirmed removed.

This proves the publication helper in the existing prepared-registration path,
not automatic run-to-host provisioning. The authorized Kubernetes selection
still needs bridging to `issue_for_run`, fresh host template preparation and
registration. No persistent user demo was left running and no release/epic
completion is claimed.

### Operator-local per-run provisioning command

Celln now exposes `parent-provision PLAN --principal PRINCIPAL` with an absolute
operator state root. Its strict, bounded `celln.parent-provision-plan/v1` input
contains the authorized intent SHA256 identity, stable issuer scope/run UID,
exact parent/worker requests, native harness template, host model-profile hash
and turn/aggregate ceilings. The command computes configuration bindings and
the run incarnation itself, validates model/parent policy, pins issuance,
publishes the permit, and publishes the launch profile. It returns their launch
and incarnation hashes without claiming or starting the parent.

The durable issuance intent is a domain-separated hash of the entire typed
plan, including the opaque upstream SHA256 digest and logical reservation.
Semantic retries recover the same launch even with different JSON formatting;
changed intent or reservation refuses. Tests also show a distinct run UID gets
a distinct incarnation and no parent journal is claimed. The CLI help path
builds and runs. Full Celln CI log:
`target/parent-provision-command-ci.log`.

The input is trusted operator authority, not proof of Kubernetes authorization.
The controller still needs to construct the complete authorized intent/plan,
invoke this host-side primitive through a trusted integration, and publish the
run registration. This command does not prepare artifacts, warm motes, or
qualify deployment RBAC/TLS. Detailed wire contract and recovery rules are in
Celln `docs/PARENT_PROVISIONING.md`.

### Kubernetes provisioning-intent identity

`cellnparent.PrepareProvisionIntent` now produces the upstream intent identity
needed by Celln `parent-provision`. It binds the complete typed AgentRun spec
and the frozen Agent/runtime/tool/three-source grant snapshot. It requires a
live, unbound pending run, rejects unsupported pod-only semantics using the
same check as registration, and revalidates catalogue sources. Its versioned
snapshot has a bounded SHA256 digest for the host plan's `intentSHA256` field.
The hash is an opaque identity at the host, not a transferable authorization.

`ProvisionIntent.Revalidate` recomputes the live snapshot and compares every
field. Call it immediately before host issuance and again before publishing a
registration; it is not continuous revocation or an atomic cross-system lock.
The host's stable run incarnation and durable issuance record still prevent
retrying a run under a replacement intent.

Tests cover stable repeated digests, changed task/digest refusal, changed live
run and withdrawn grant refusal, unsupported native semantics, and rejection
of already-bound, running or terminal runs. Race-enabled tests pass for
`internal/cellnparent`, `internal/cellnauthority`, `internal/controller` and
`internal/apiserver`. These are unit/controller tests, not a new live-model
measurement. Building the host plan from an operator-approved template and
invoking the local host provisioning command remain unwired.

### Operator-template to host-plan builder

`BuildHostProvisionPlan` now maps a revalidated `ProvisionIntent` and trusted
`sympozium.ai/celln-parent-host-template-v1` operator template into Celln's
`celln.parent-provision-plan/v1` wire format. It supplies the stable scope, real
run UID and complete intent SHA256; native parent/worker/harness JSON is kept
intact for Celln's strict parser and independent host admission.

The template must match the exact catalogue snapshot digest. Shared validation
with registration checks model/persona, required-tool policy, requested upper
bounds and explicit run timeout. Additional checks tie the native plan to its
declared parent lease, aggregate budgets, caller principal and model hash.
The native template cannot supply an initial task: turns remain the durable
AgentRun/AgentRunTurn path. Operator scope/principal are bounded and control
characters are refused.

Stable plan serialization and twelve mismatch cases pass, along with the
race-enabled authority, parent, controller and API suites and focused Go vet.
These test operator-template consistency, not signed artifact compatibility or
hardware execution. The operator template asserts its architecture implements
the exact selection; Celln must still admit the actual artifacts/model policy.

There is still no automatic host invocation: the integration must durably pin
template/host choice, invoke `parent-provision` through a trusted local boundary,
revalidate the intent, and publish registration. No remote tenant upload route
or deployment authority has been added by this builder.

### Local host invocation wired into admission

`RegistrationConfig` now has an exclusive local-provisioning mode:
`localProvisioner` plus `hostTemplates`, with no prepared `registrations`.
The existing `CELLN_PARENT_REGISTRATIONS` admission wiring selects exactly one
template matching the live catalogue snapshot, model and persona. Ambiguous or
withdrawn templates refuse. Changes to the local host routing configuration
require restart; each run's durable choice still prevents switching on retry.

`LocalProvisioner` contains absolute operator `binary`, `root`, `journal` and
`approvals` paths plus the exact owner `target`, `tokenFile` and optional `caFile`.
Its journal/approval directories must equal the containing registration config.
All replicas and restarts must retain/share the same journal. The operator must
ensure the configured owner target actually serves this local root; this mode
does not establish remote host placement or infer root ownership.

Before execution it publishes a synced non-replacing choice record binding the
entire host configuration and plan SHA256. It revalidates intent, writes a
private temporary plan, and directly invokes the configured Celln binary with
explicit root/principal, a minimal environment, a 15-second deadline and bounded
output. There is no shell or inherited model credential. A failed or uncertain
invocation retains issuance/choice state; it does not retry internally or refund.

The result must be strict versioned JSON with valid hashes and the exact
scope/run-UID incarnation computed using Celln's domain-separated tuple format.
After another live-intent check, the existing durable registration assignment
and per-run approval publication completes. No VM is started by this helper;
normal parent reconciliation consumes the resulting approval.

Synthetic process-boundary tests cover identical retry recovery, host/root/plan
switch refusal, no inherited test secret, private staging cleanup, malformed or
oversized output, and dispatcher template ambiguity/withdrawal. Race-enabled
parent/authority/controller/API suites pass. These tests do not constitute a
real Celln command or KVM proof. The next qualification is a real browser-created
run through this local-provisioning mode, replacing the fixture's pre-issued
launch. Deployment RBAC/TLS and a lasting user environment remain open.

### Real automatic-provisioning browser proof

The live proof now supports `CELLN_INTEROP_PROVISION_BINARY` pointing at the
built Celln CLI. In this mode the Rust fixture prepares signed artifacts and
independent model policy but creates **no** permit or launch profile. Go checks
that issuance/permit/launch directories are empty, then installs an operator
host template after the browser creates the real run. The production admission
dispatcher invokes the actual CLI, publishes approval and lets the normal
manager reconcilers create the parent and execute turns.

This passed with real browser creation/follow-up/reload, Kind, KVM, NATS and
DeepSeek: 38.46 seconds overall, 37.45 seconds in Go/browser. Archive admission
mode is `local-provisioner`, with browser/browserCreation/eventBus all true.
Run UID: `147fb643-af4e-43db-a26d-04ef2053ddc4`. Rust independently recomputed
the scope/UID incarnation and verified exactly one issuance, permit and launch
record. Both turns made real uppercase tool calls and two broker requests,
returned `VIOLET`, and used distinct child identities. Joined shutdown, capacity
reclamation, replay refusal and historical read-only access checks also passed.
The isolated `celln-parent-manager-jt5xd` namespace was confirmed removed.

Evidence in the Celln validation worktree:
`target/declared-parent-pair-1758381-1788901255321525365/`.
Logs: `target/parent-auto-provision-live.log` and
`target/parent-auto-provision-ci.log`. Full Celln CI, the production web build,
and race-enabled parent/authority/controller/API tests passed.

Reproduction adds this environment setting to the documented live proof:
`CELLN_INTEROP_PROVISION_BINARY=/home/axjns/Code/celln-worktrees/merged-epic-validation/target/validation/debug/celln`.
Build that CLI and the Go manager test binary first. Omitting the variable still
exercises the older prepared-registration path and must not be reported as
automatic provisioning. The operator's signed architecture/model template is
still prepared in advance; arbitrary runtime composition, deployed RBAC/TLS,
multi-run reuse qualification and a lasting hands-on environment remain open.

### Unchanged template reused across two real runs

The live proof now optionally sets `CELLN_INTEROP_REUSE_TEMPLATE=true` alongside
automatic provisioning. After the browser-created run completes both turns, it
deletes that exact run and waits for finalization. It then creates a second
AgentRun directly through Kubernetes with the same spec and executes its initial
and follow-up turns. The manager and host keep running; operator template/config
bytes must remain identical. The second run uses a new UID, permit, launch and
parent incarnation with independent turn accounting. The second run is an API/
Kubernetes path, not a second browser creation claim.

This passed with real Kind/KVM/DeepSeek in 63.41 seconds (Go/browser 62.33).
Rust verified two issuance records, two permits, two launch profiles, four
distinct child identities and a real uppercase call/two broker requests in each
turn. Both parents confirmed joined teardown and refused replay. The namespace
`celln-parent-manager-tkbfk` was confirmed removed. This is sequential reuse after
reclaim, not simultaneous multi-parent capacity or restart-recovery proof.

Evidence: Celln validation worktree
`target/declared-parent-pair-1764457-1788901518648954218/`.
The second run's public archive is `interop-reused.json`; the first run retains
its separate archive. `pair-results.json` includes all four child identities.
Logs: `target/parent-template-reuse-live.log` and
`target/parent-template-reuse-ci.log`. Full Celln CI and race-enabled parent,
authority, controller and API suites pass. The live log includes the test
manager's unset-logger diagnostic stack, not a controller panic or test failure.

The remaining deployment qualification and lasting hands-on environment are not
replaced by this isolated, automatically cleaned-up proof.

### Scoped controller identity proved against Kind

Read-only inspection found the installed general controller ServiceAccount
could not read `CellnTool` or `AgentRunTurn` resources in the catalogue demo
namespace. The existing admin-based proof therefore did not qualify deployed
controller permissions. No installed general-controller grants were widened.

Added opt-in `config/samples/celln-parent-controller-rbac.yaml`: a namespaced
ServiceAccount/Role/RoleBinding for the native parent controller. It reads
catalogue resources and turns, updates run/finalizer/turn status, and reads only
the three named grant ConfigMaps. Owned Job/Deployment/Service watches are
read-only. It cannot mutate grants/tools, read secrets, create pods or read runs
in another namespace. This role is not sufficient for the general multi-purpose
controller manager; use a namespace-scoped parent-only manager. The account has
automount disabled: a deployed process needs deliberately configured projected
credentials and rotation, not an indefinitely retained test token.

With `CELLN_INTEROP_SCOPED_CONTROLLER=true` and
`CELLN_INTEROP_PARENT_RBAC` pointing to that manifest, the live fixture installs
it only in its isolated namespace and obtains a real 600-second TokenRequest.
The controller REST config contains this token and cluster CA, with no admin
certificate, exec auth or credential files. Ten SelfSubjectAccessReviews check
required permissions and negative boundaries before starting the manager.
The API browser driver/setup/cleanup still use admin credentials: this is a
controller boundary qualification, not a production API-server RBAC proof.

The full automatic-provisioning + sequential-template-reuse test passed with
that controller identity in 61.89 seconds (Go/browser 60.83): two parents, four
actual tool-using DeepSeek turns, four distinct sub-cells, joined teardown and
replay refusal. Evidence in Celln validation:
`target/declared-parent-pair-1773076-1788901892962195882/`;
log `target/parent-scoped-controller-live.log`. The namespace
`celln-parent-manager-srm8l` and its scoped roles/account were confirmed removed.
Race-enabled parent/authority/controller/API suites passed. This does not yet
provide a deployed parent-only manager, production owner TLS or lasting demo.

### Dedicated parent-only controller startup

The existing controller binary now supports `--celln-parent-only`, requiring
an explicit watch namespace plus both parent config paths. This branch registers
only AgentRun/AgentRunTurn controllers and optional NATS, without a pod builder,
general controllers or owned Job/Deployment/Service watches. The opt-in RBAC
sample consequently no longer grants those owned-resource reads.

`AgentRunReconciler.ParentOnly` ignores unrelated new runs before status or
finalizer writes. Tests cover pod runs, disposable Celln runs and existing
Job/Deployment/one-shot execution identities. Already-bound parents remain
eligible for original-owner recovery even when their spec changes. Race-enabled
controller command, controller, parent and API tests pass; focused vet and
whitespace checks pass. The compiled command refuses missing configuration with
exit 1 before accessing Kubernetes.

The scoped live manager now uses this exact ParentOnly reconcile/watch path.
Automatic provisioning, sequential template reuse and four real DeepSeek/tool
turns passed again in 61.17 seconds (Go/browser 60.15) with the reduced Role.
Evidence: Celln validation
`target/declared-parent-pair-1780827-1788902209774085567/`;
log `target/parent-only-manager-live.log`. Namespace
`celln-parent-manager-2t9k8` was confirmed removed. This uses the manager test
entrypoint, not a deployed controller process/image or production owner TLS.

Usage, credential rotation and co-located host requirements are documented in
`docs/guides/celln-parent-only-controller.md`. A lasting deployment/environment
and protection of the owner transport remain outstanding.

### TLS parent edge and live qualification

Added `cmd/celln-parent-proxy` and its reusable parent-edge handler. The command
requires operator certificate/key files and TLS 1.3. It forwards only validated
parent routes to a fixed literal loopback dispatcher; no provisioning/general
execution endpoints, remote plaintext backend, DNS-selected backend, query
overrides or upgrades are accepted. Requests are bounded before forwarding.
Bearer authorization remains the host's responsibility; caller idempotency
headers are removed so POSTs do not acquire transport retry semantics. No
ambient proxy, model key or operator token is read by the edge.

Race-enabled tests cover HTTPS forwarding, certificate trust refusal, forbidden
routes and oversized chunked bodies not reaching the dispatcher. The command
builds; usage/rotation/host-boundary requirements are in the parent-only guide.

With `CELLN_INTEROP_OWNER_TLS=true`, the live proof uses the production edge
handler and a test-only certificate, explicitly trusted by the real controller
client. Combined with parent-only mode, scoped ServiceAccount and template reuse,
the full browser/Kind/KVM/DeepSeek proof passed in 62.18 seconds (Go/browser
61.08). Both parent bindings use HTTPS; four distinct tool-using child turns and
joined teardown/replay refusal passed. Namespace `celln-parent-manager-fq286`
was confirmed removed. Evidence: Celln validation
`target/declared-parent-pair-1787117-1788902563447257387/`;
log `target/parent-owner-tls-live.log`.

The proxy request-body close was made explicit after that live binary was built;
the updated proxy race tests pass. Controller/parent/API/command race suites and
focused vet passed. This qualifies the TLS handler/client path, not a deployed
proxy executable, external certificate lifecycle or lasting user environment.

### Actual parent-only controller process qualification

`CELLN_INTEROP_CONTROLLER_BINARY` now starts the built controller command in
the live proof instead of starting an in-process manager. It receives only an
explicit namespace, parent config paths and a private kubeconfig referencing the
real scoped ServiceAccount token file. No admin certificate, ambient credentials
or model key is inherited. Cleanup first finalizes runs, then sends SIGTERM and
waits for the process; a bounded fallback kills a stuck process rather than
leaving an untracked controller.

The browser/TLS/scoped-identity/automatic-provisioning/template-reuse proof passed
with this executable in 41.17 seconds (Go/browser 40.11): two parents, four real
tool-using DeepSeek turns and joined owner teardown/replay refusal. Evidence in
Celln validation: `target/declared-parent-pair-1793337-1788902875760609196/`;
log `target/parent-controller-process-live.log`. Controller PID 1794868 exited
and namespace `celln-parent-manager-twk82` was confirmed removed. Race-enabled
controller command, controller, parent and proxy tests pass.

The test API/NATS driver remains separate; this external controller does not
inherit its NATS credentials. The TLS edge and dispatcher still use their test
hosting entrypoints in this proof. An older, independently running catalogue
demo was found and deliberately left untouched. No new lasting demo is claimed.

### Standalone TLS proxy process qualification

`CELLN_INTEROP_PROXY_BINARY` now runs the built TLS-edge command with private
test certificate/key files, rather than serving its handler in the Go process.
Readiness verifies the certificate and TLS 1.3; a TLS 1.2-only client is refused
by the executable. Only its fixed loopback backend is configured. The process
receives a minimal environment, then exits via SIGTERM after parent/controller
cleanup, before its key material is removed.

Combined with the separate scoped parent-only controller executable, the real
browser/Kind/KVM/DeepSeek two-parent/four-turn proof passed in 40.38 seconds
(Go/browser 39.31). Evidence: Celln validation
`target/declared-parent-pair-1797994-1788903072543351657/`;
log `target/parent-proxy-process-live.log`. Proxy PID 1799405 and controller
PID 1799429 exited; namespace `celln-parent-manager-m6zd9` was confirmed removed.
Controller/proxy race tests and focused vet passed.

The dispatcher and API still use test hosting entrypoints; this does not yet
establish an all-standalone or lasting user environment. The older catalogue
environment remains untouched.

### Standalone dispatcher with retained execution evidence

`CELLN_INTEROP_DISPATCHER_BINARY` now starts the actual Celln dispatcher command
on an isolated loopback port/root. Parent and general dispatcher credentials are
distinct. The proof uses public HTTP responses and journal/artifact evidence,
not an in-process owner registry. After both parents join it verifies replay
refusal and full logical capacity reclamation, stops the owner process, restarts
it on the same root, and verifies historical-only ContextLost access with no
turn/stop/cancel authority. This is restart after joined teardown, not recovery
of live context after a mid-turn crash.

The first attempt completed the Go/browser flow but failed the outer Rust proof
because production did not emit test-only worker evidence. That is not counted
as a full pass (`target/parent-dispatcher-process-live.log`). Rather than omit
actual-tool checks, Celln now supports explicit private host audit retention:
an operator-created mode-0700 `parent-audit` directory enables bounded, synced,
non-replacing mode-0600 guest-event/broker records. Production defaults to no
raw-output retention. Audit files contain sensitive conversation/tool data and
are not replay authority; retention requirements are in Celln's provisioning
guide. Tests cover default-off, unsafe/symlink directory refusal, private files,
bounds and non-overwrite behavior.

The complete audited proof passed in 47.12 seconds (Go/browser 45.90), with
standalone dispatcher, TLS proxy and scoped parent-only controller, browser
creation, two parents, four actual tool-using DeepSeek turns and four distinct
sub-cells. Evidence: Celln validation
`target/declared-parent-pair-1811646-1788903579265720206/`;
logs `target/parent-dispatcher-process-audited-live.log` and
`target/parent-dispatcher-process-ci.log`. Full Celln CI passed. Namespace
`celln-parent-manager-ztv9j` and the owned processes were confirmed removed.

The dispatcher lacks a shutdown API, so the proof stops its process only after
parent teardown and zero live reservations are confirmed. The API/web host is
still test-owned and this proof still cleans up: no lasting environment is yet
claimed. The older independent catalogue environment remains untouched.

### Standalone API/web and all-process proof

`CELLN_INTEROP_API_BINARY` now runs the built API command with its embedded UI,
explicit loopback listener, private bearer-token file and owned NATS connection.
It uses the setup/operator Kubernetes identity in a private kubeconfig, not the
controller's scoped identity; this is explicitly not API-ServiceAccount RBAC
qualification. The controller continues using its restricted token-file identity.

Process testing exposed that the API command cancelled its background manager
on SIGTERM but did not stop its HTTP listener. Added `ServeContext` and wired the
command to join bounded HTTP shutdown. Both headless and UI shutdown tests pass,
as do race-enabled API/controller/command tests and focused vet. Existing legacy
Start methods remain available for callers owning their own lifecycle.

With dispatcher, controller, TLS proxy, API/web and NATS all in separate owned
processes, the browser/Kind/KVM/DeepSeek two-parent/four-turn proof passed in
40.88 seconds (Go/browser 39.72). Evidence: Celln validation
`target/declared-parent-pair-1820020-1788903937761264930/`;
log `target/parent-all-process-live.log`. Namespace
`celln-parent-manager-8l8jf` and dispatcher/API/proxy/controller PIDs
1820266/1820375/1821106/1821128 were confirmed gone after cleanup. Restart remained
historical-only and all four tool calls were checked from private native audits.

This is an all-process proof, still automatically cleaned up. A supervised
lasting local environment, token rotation and explicit user stop/cleanup remain
the next handoff work; the older catalogue environment was not modified.

### Scoped controller credential renewal

The standalone proof's operator supervisor now requests renewed ten-minute
ServiceAccount tokens for its isolated namespace and atomically publishes them
to the controller's existing mode-0600 token file. Only the supervisor holds
the operator credential; controller privileges and kubeconfig remain scoped.
Renewal uses the returned expiration, refreshes halfway through its lifetime,
retries temporary failures every five seconds, and stops the controller if it
cannot renew before a one-minute credential safety margin. Requests are bounded
to ten seconds; shutdown cancels and joins renewal. Provider error content is
not included in renewal failure messages. Private-file checks refuse symlinks,
public permissions, empty/whitespace/oversized credentials; writes are synced
and renamed without exposing a partially written token to client-go.

Race-enabled tests cover normal renewal cadence, transient recovery, sustained
failure before expiration, too-short returned lifetime, private publication and
invalid-file refusal. These use simulated time, not a claim of a ten-minute live
soak. Controller, parent and API race suites and controller vet pass.

The all-process browser/Kind/KVM/DeepSeek proof with this supervisor passed in
40.01 seconds (Go/browser 38.98), retaining its two-parent/four-child real-tool
and joined-cleanup checks. Evidence: Celln validation
`target/declared-parent-pair-1831089-1788904718990735261/` and
`target/parent-token-renewal-live.log`. Namespace `celln-parent-manager-pjkfb`
and dispatcher/API/proxy/controller PIDs 1831327/1831390/1832063/1832086 were
confirmed absent afterward. This short proof does not verify client-go refresh
after the original token expires; that requires the planned longer hands-on
session. No lasting environment or production credential distribution is yet
claimed. Supervised hold, fresh larger-budget run, stop signal and whole-namespace
owned-parent cleanup remain before the interactive handoff.

### Supervised hands-on parent

Added `test/integration/test-celln-parent-hands-on.sh` and
`docs/guides/celln-parent-hands-on.md`. The opt-in hold runs all standalone
components, completes the original proof, confirms those parents removed,
atomically updates its operator template limits, and creates a fresh native
parent with 12 total turns. Persona/model/tools remain explicitly admitted;
changing the lease does not widen tool grants. A private handoff JSON identifies
URL, namespace, run, token-file path, matching generated-name YAML, deadline and
stop-file path without embedding token values. The supervisor owns the expiring
credential and processes until the bounded hold ends or a regular stop file is
created. Cleanup first closes API admission, then deletes all runs only in its
isolated namespace using exact UID preconditions, retaining uncertain finalizers
instead of deleting authority. Tests cover namespace isolation and refusal to
count unconfirmed parent teardown as successful cleanup.

The three-second hold smoke passed in 53.66 seconds (Go 52.46), including a fresh
third parent and its initial real-model turn, confirmed teardown of all three,
zero live reservations and historical-only owner restart. Evidence: Celln
`target/declared-parent-pair-1837300-1788905002200189967/` and
`target/parent-hold-smoke.log`. Namespace `celln-parent-manager-gq2dn` and owned
PIDs 1837622/1837687/1838721/1838745 were confirmed gone. Full Celln CI passed
(`target/parent-hold-ci.log`), as did Go controller/parent/API race suites,
focused cleanup/renewal race tests, controller vet and shell syntax validation.

The reproducible launcher then started an actual twelve-hour hold, verified
Ready with initial committed result: namespace `celln-parent-manager-hd8xh`,
run `hands-on-h2ms4`, URL `http://127.0.0.1:37877`. Private handoff:
Celln `target/declared-parent-pair-1844881-1788905152695803240/hands-on.json`;
Sympozium log `target/parent-hands-on-live.log`. Automatic stop is
2026-09-09T10:06:45Z (11:06 UK). This is a time-stamped observation, not a durable
claim that the environment remains live; check the supervisor and run before
reuse. Its long-duration token refresh, stop-file path and final cleanup are
not yet qualified by this in-progress hold. Do not count the running outer
proof as a completed test. The earlier short hold remains the completed evidence.

### Conversation lifecycle explanations

The conversation UI now explains current-generation ContextLost, Stopped,
TeardownUncertain and ReconciliationRequired observations separately, retaining
historical answers without implying resumable live context. It labels initial
and follow-up failures distinctly from successful agent replies. Requested turn
ceiling exhaustion, initial failure and active-turn ownership explain disabled
composition. Requested ceilings are explicitly not advertised as host-approved
remaining capacity; effective narrowed host limits still need their own status
contract. Input text warns that per-message size does not guarantee space in
the harness's retained context. No replay, reset-budget or reconstruction action
was added.

Frontend build passed and all 15 intercepted conversation browser tests passed
in 19 seconds (`target/parent-lifecycle-browser.log`). Coverage includes stale
generation suppression, terminal explanations, failed output labels, active
turns, exhaustion across reload, identity mismatch and no automatic POST replay.
These are browser contract tests, not new hardware/model failure proofs. The
isolated Vite test listener on 4179 was stopped; the hands-on API/parent was not
restarted and still serves its previous embedded UI build.

During the unchanged hands-on session, read-only inspection observed the scoped
token file change from issued-at 2026-09-08T22:06:01Z / expiry 22:16:01Z to
issued-at 22:11:01Z / expiry 22:21:01Z, with the same namespace-specific
ServiceAccount subject. This proves the first real five-minute renewal and
publication occurred without controller restart. It does not yet prove a
controller request after the original token's expiry; that remains a later
live-session check. No token values were logged.

### Durable turn reconciliation observations

AgentRunTurn now has standard generation-scoped conditions. The turn controller
records a generic `CellnTurnComplete=False/ReconciliationRequired` observation
when admission or outcome cannot be confirmed, instead of leaving the only
explanation in logs. This is not a durable refusal certificate: it cannot clear
an attempt, free the active slot, or authorize resubmission. A fresh committed
result takes precedence over a concurrent failed observation, including a
committed unsuccessful result. UID/generation/deletion checks prevent stale
observations changing replacement turns, and unchanged observations do not
rewrite status. Errors, credentials and operator paths are not copied into the
condition message. The UI shows only current-generation reconciliation conditions
for turns without a committed result.

Deepcopy and all three CRD copies were regenerated. Go controller/parent/API race
suites, controller vet, frontend build and all 16 intercepted conversation tests
passed (`target/parent-turn-observation-browser.log`, 20 seconds). Tests cover
identity preservation, unchanged observations, committed failure precedence and
stale UI condition suppression. This schema/controller/UI change is local and
has not been installed into the running hands-on environment; hardware proof
of this new observation path remains outstanding. Its tests do not claim safe
automatic recovery of an unconfirmed request.

### Live work after original controller credential expiry

The unchanged controller PID 1845881 successfully recorded a new model/tool
turn after its original credential expired at 2026-09-08T22:16:01Z. Submission
at 22:17:03.559Z and observed completion at 22:17:05.587Z used the existing
hands-on parent incarnation. The fresh child differed from the initial child;
private native audit showed two broker requests, zero denials and one uppercase
call on retained `violet`, returning `VIOLET`. The parent stayed Running, with
one accepted follow-up and ten remaining requested turns. The current token had
issued-at 22:16:01Z and expiry 22:26:01Z. No controller or parent restart occurred.
See `docs/evidence/celln-parent-renewal-2026-09-08.json` for identities and
the private audit reference. This verifies useful work after credential expiry,
not a completed twelve-hour soak or final teardown of this still-running hold.

### API occupied-turn refusal and storage error classification

The turn API now refuses a new request when the observed parent already owns
an active turn. Exact original-request observation remains before that check,
so an idempotent request can still retrieve its record while another turn owns
the slot. This is early backpressure for an already-known busy parent, not an
atomic admission lock: simultaneous submissions still rely on the controller's
parent-status CAS to serialize execution. No attempt is cleared or refunded.

Parent lookup now distinguishes Kubernetes NotFound (404) from other read
failures (503) without exposing storage error details. Regression tests verify
busy-parent, initial-failure and exhausted-budget refusal creates no new turn
and does not prevent observing the original request. GET and POST storage
failures are tested separately. API/parent/controller race suites, API vet and
diff whitespace checks passed. These changes remain local; the long-lived
hands-on processes were not rebuilt or restarted.

### Broad Go regression checkpoint and epic evidence audit

`go test -race ./...` passed across the current Sympozium worktree, including
existing controller Harness task-mode, HarnessSession and server-related unit
coverage alongside the new parent components. Log:
`target/enduring-full-go-regression.log`. The integration packages' ordinary
tests are included, but their environment-gated real-model/cluster flows are
not implied by this command. The full M3 live one-shot/server/OCI/security and
installation regression gate therefore remains open.

Re-read native `ParentContext` implementation and completed pair artifacts:
history is retained by the guest parent, serialized into the next worker task,
and updated only from matching successful results. The completed all-process
browser proof establishes the initial value, submits a follow-up without
restating it, checks separate child identities and verifies actual native
uppercase tool events with two model requests per turn. These scope-matched
facts justify checking the M2 native borrowed-tool model-work item and M3 first
real-model two-turn browser item, explicitly as local evidence, not release.
Other broad checklist items remain open rather than inferring completion from
nearby tests. No hands-on processes were restarted by this audit.

### UID-pinned destructive conversation cleanup

The conversation now offers an explicitly destructive “Delete run and stop
parent” action. Confirmation explains loss of Kubernetes run/turn history and
non-resumable live context; host audit retention is separately identified.
The API accepts an optional single bounded `uid` query parameter and passes it
as a Kubernetes deletion precondition. Older callers without that parameter
retain compatibility. The new enduring client always supplies its displayed
UID and namespace, preventing a stale name from targeting a replacement run.

Deletion intent is saved in sessionStorage before the request, sending is
disabled while intent/deletion is pending, and uncertain deletion is neither
replayed nor forgotten on reload. HTTP deletion acceptance is explicitly not
presented as confirmed host teardown. The existing controller finalizer still
owns original-parent stop and confirmation. This is destructive cleanup, not
the still-unimplemented non-destructive stop/resume or turn-cancel UI.

API race tests verify exact UID/namespace options, invalid UID rejection before
storage and legacy compatibility. Frontend build, API vet and all 19 intercepted
conversation tests passed (`target/parent-delete-ui-browser.log`, 21 seconds),
including confirmation decline, exact request identity and uncertain deletion
across reload without another DELETE. No live parent was deleted by these
tests. The isolated Vite listener was stopped; `hands-on-h2ms4` remained
Running/Ready. New UI-to-hardware joined deletion still needs an isolated live
proof before its acceptance gate can be claimed.

### Live browser deletion and stale cached run fix

Added opt-in `CELLN_INTEROP_BROWSER_DELETE=true` to the standalone proof and
enabled it in the hands-on launcher. Before template reuse, the browser sends
a deliberately mismatched deletion UID, verifies HTTP 409 and that the original
run remains, then confirms the destructive UI action. The driver independently
waits for Kubernetes finalizer removal; the outer Rust proof checks the original
owner's joined stop, reclaimed capacity and historical-only restart.

The first attempt failed because a React Query refetch error retained cached
run data after deletion; the browser never rendered “Run not found.” The failed
proof is retained as `target/parent-browser-delete-live.log` in Celln validation
(52.33 seconds, not a pass). Its namespace and processes were confirmed removed.
Run detail now removes the cached view on a confirmed 404, but retains history
and disables sending during other refresh failures. Detail lookup distinguishes
Kubernetes NotFound from storage failure (503). An API deletion UID conflict is
reported as a bounded 409, not a raw internal error. Browser tests cover cached
404 and unavailable refresh separately.

The corrected all-process proof passed in 59.19 seconds (Go/browser 58.05),
including mismatched-UID refusal, actual UI deletion, two native parents/four
tool-using model turns, joined cleanup, capacity reclamation and restart checks.
Evidence: Celln validation
`target/declared-parent-pair-1876584-1788906616924726531/` and
`target/parent-browser-delete-fixed-live.log`. Namespace
`celln-parent-manager-zwwct` and owned PIDs 1876827/1876903/1878708/1878730 were
confirmed absent. This proof used a separately built updated API/embedded UI
and driver with the already-qualified standalone controller binary; it is not
live qualification of the newly added turn-condition schema/controller writes.

All 21 intercepted conversation tests passed (48 seconds,
`target/parent-delete-fixed-browser.log`), along with frontend build, API and
controller race tests and vet. The temporary Vite listener was stopped. The
independent twelve-hour hands-on parent was not rebuilt, restarted or deleted.
This qualifies destructive UI deletion; it does not satisfy non-destructive
stop/resume, active-turn cancellation or crash-recovery acceptance.

### Independent child cancellation prerequisite

Inspection confirmed the existing owner `/cancel` action cancels the entire
parent control, including its owned child. It must not be presented as a
turn-only cancellation operation. ParentOwner also treats any handler error as
lost context and terminates admission; a recoverable cancelled turn cannot be
implemented by merely returning a generic handler error.

Celln Control now supports independently cancellable child contexts. Each child
inherits parent cancellation and has a deadline no later than its parent's;
its cancellation leaves parent/siblings usable. Nested thread-local scopes do
not hide ancestor cancellation, and stopped parents cannot produce usable new
children. Existing constructors remain standalone and existing scope behavior
is preserved. Eight control tests and full Celln CI passed
(`target/parent-child-control-ci.log`). This is a cooperative host-control
primitive only: no VM teardown or new turn-cancellation route is claimed.

Required next integration, without widening the scope of `/cancel`:

1. Register the exact parent/turn/child identity with the child control and
   atomically match cancellation against it. Late requests must not select the
   next active turn; no unscoped “cancel whichever is active” action.
2. Install child control only around child execution/broker work, retaining
   parent control for guest-parent exchange. Join child resources and account
   reservations without refunds before committing a bounded failed/cancelled
   result to the existing parent context. Unconfirmed teardown must remain
   unavailable, not become a successful cancellation or implicit resume.
3. Expose a durable UID-bound cancellation intent in Sympozium, reconcile the
   original owner without replay, and drive UI status from confirmed records.
   Prove delayed cancellation, parent-stop races, model interruption and guest
   context retention with real child execution before closing the lifecycle gate.

The active hands-on owner is unchanged and does not use the new primitive yet.

### Exact child cancellation rendezvous

Added Celln warden `parent_child_control::ChildControlSlot`, scoped to one
admitted parent. Registration validates the reserved parent/turn-derived child
binding, refuses an occupied slot and remembers up to 1024 consumed identities.
Exact parent/turn/child comparison and cancellation run under the same lock;
late cancellation and retirement cannot select the next turn. Cancellation is
only a signal: it neither retires the slot nor refunds/reuses identity. Parent
control is inherited; dropping the slot signals its active child but does not
claim teardown or cancel unrelated parent/sibling work.

This rendezvous is not independent admission or a durable journal. The eventual
host caller must authenticate the parent principal, reserve/persist the turn,
and join/record child outcome before retiring the slot. In particular, public
`retire` is a host-call contract, not an unforgeable hardware teardown witness.
It is not yet exposed through HTTP or installed around the native VM worker.

Four tests cover exact/repeated cancellation, no release/refund, a delayed
cross-thread cancellation after the next turn registers, changed parent/child
bindings, parent stop inheritance and slot-drop interruption. Full Celln CI
passed (`target/parent-child-identity-ci.log`). The hands-on supervisor was
confirmed still running and was not restarted. Next integration remains actual
worker scope, confirmed teardown/result commit, then a durable turn-targeted
Sympozium action and guest/model cancellation proof.

### Native worker child scope wired and regressed

ParentSession accepts an optional admitted child-control slot. After durable
turn reservation it registers the exact child, executes only the worker under
the child control scope, and restores parent control before child-outcome and
parent-context commit. Slot retirement follows the matching parent acknowledgement
and durable parent-committed journal entry, not cancellation request or worker
return alone. Existing uncontrolled protocol-test constructors stay compatible.
The production native parent composition now requires serving-owner control and
installs a parent-bound slot, so actual child KVM/broker execution inherits the
bounded child deadline without losing parent cancellation.

A synthetic trusted-executor sequencing test cancels a child, supplies an
explicit DestroyedChild failure, verifies parent mailbox/result commit still
has usable parent control, and completes a subsequent turn. It is not native
cancellation teardown evidence. Native worker failure still closes the parent
until cancellation-specific completed-failure handling is qualified, and no
turn-targeted HTTP action is yet exposed. Do not label this “Cancel turn works.”

Full Celln CI passed (`target/parent-child-scope-fixed-ci.log`). A separately
built dispatcher with the new scope then passed the complete browser/model/tool
and destructive-deletion regression in 55.69 seconds (Go 54.56): two native
parents, four distinct actual tool-using children, retained context, stale-UID
deletion refusal, joined teardown, reclaimed capacity and historical-only
restart. Evidence: Celln validation
`target/declared-parent-pair-1904905-1788907311882260262/` and
`target/parent-child-scope-live.log`. Namespace `celln-parent-manager-x85nr`
and owned PIDs 1905139/1905205/1905904/1905926 were confirmed gone. The separate
hands-on supervisor remained live and was not restarted or deleted.

### Real child cancellation with live parent guest continuation

Added and ran the KVM probe
`vmm::boot::tests::child_cancellation_preserves_live_parent_guest_context`.
The parent warm-forked cell first stored private guest state. A separate
warm-forked child reached its guest busy-code marker, re-entered execution under
child control, and returned a timed-out run report after explicit cancellation.
The test destroyed that child before continuing the original parent, which
returned its second-turn counter and retained private value. A fresh child then
ran with an independent empty state. Parent and child hot-path reports were
checked for absence of a Linux boot banner. This is actual guest code and VM
execution, not a host transcript assertion.

Explicit probe build/run used `scripts/mkparent-probe.sh`,
`CELLN_PARENT_PROBE_INITRD=.../target/parent-child-cancel-probe.cpio`, and
`cargo test -p celln-warden --features kvm
child_cancellation_preserves_live_parent_guest_context -- --nocapture
--test-threads=1`. It passed without skipping in 2.96 seconds;
log `target/parent-child-cancel-kvm.log`. Full Celln CI also passed. The probe
does not include model interruption or signed native harness cancellation.

The native worker now recognizes host cancellation on the executor's successful
VM-destroyed return path and returns a bounded unsuccessful DestroyedChild
result. It does not normalize executor errors, deadline expiry, missing host
cancellation or overridden authority-mismatch denials as successful cancellation.
Private opt-in audit marks cancellation and permits empty partial output.
Parent stop still prevents the later parent-scope context commit. Classification
tests and full CI passed (`target/parent-cancelled-result-ci.log`). End-to-end
interrupted native model work, the exact turn-targeted transport, and durable
Sympozium/UI cancellation remain unqualified; no cancel-turn action is exposed.

### Caller-scoped child cancellation routing

ParentOwner now has an explicit child-aware constructor sharing the same
parent-derived child-control slot with its runtime. Existing constructors and
callbacks retain their contracts and refuse child cancellation. ParentRegistry
adds child-aware admitted construction and caller-scoped exact-identity cancel
routing. Owner initialization remains outside execution on the registry lock;
cancellation only signals the matched control and does not wait for a running
handler, release capacity, stop the parent or acknowledge teardown.

A real owner-thread/synthetic-worker test verifies wrong-tenant refusal,
cancellation while a handler runs, parent readiness after the child completes,
capacity retention, stale cancellation during the next turn, refusal after
parent stop and explicit legacy-owner refusal. No VM execution is claimed by
that routing test. The native dispatcher still needs to adopt this shared slot
instead of its private slot before an authenticated HTTP action can reach it.

Focused routing tests passed. Initial full CI hit
`a_state_root_has_one_dispatcher_owner_and_releases_on_drop` at its immediate
post-drop relock assertion (`target/parent-child-routing-ci.log`). That existing
test passed in isolation, and the full rerun passed including it and the new
routing test (`target/parent-child-routing-repeat-ci.log`). This intermittent
root-lock test failure remains a follow-up investigation; no ownership check
was weakened or skipped and its cause has not been established.
