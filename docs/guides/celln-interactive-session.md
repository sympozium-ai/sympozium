# Local Harness + Celln hands-on session

This is a bounded, eight-hour developer session on the explicitly selected
`kind-celln-deployed` cluster. It is not a production installation, a persistent
conversation, or a reboot-survival qualification. Framework is not used.

The session keeps the real signed catalogue, approved runtime and borrowed tools,
host issuer, router, KVM dispatcher, namespace-scoped controller, authenticated
API with embedded UI, and authenticated NATS event bus running together. The
DeepSeek credential stays in a private host file, never in the browser or a Pod.
The UI gets a distinct random API credential. Capability discovery gets its own
read-only router credential, not an execution credential.

## Start and verify

Use the prerequisites and explicit paths in [the live catalogue guide](celln-live-catalogue-proof.md).
Build the API image from the current integration worktree and load it into all
Kind nodes using the Podman image-archive workflow in
[the installation guide](celln-mlp-installation.md). Set the ordinary live proof
variables, including the explicit kubeconfig, pinned kernel/initrd checksums,
signed public fixture, native Harness package, binaries and image tags. Add:

```sh
export CELLN_LIVE_INTERACTIVE=1
export CELLN_LIVE_NATS_BINARY=/absolute/path/to/nats-server
export CELLN_LIVE_ISSUER_PROCESS=1
export CELLN_LIVE_AUTOMATIC_ISSUANCE=1
export CELLN_LIVE_OPERATOR_ADMISSION=1
export CELLN_LIVE_BROWSER_SUBMISSION=1
export CELLN_PAUSE_TEST_CONTROLLER=1
```

Run `test/integration/test-celln-catalogue-harness.sh` within the isolated Podman
private network namespace, as in the live proof guide. Do not start another
session while this one owns the paused proof controller. Session startup incurs
two real, bounded DeepSeek runs and refuses readiness unless both complete via
the actual browser, including explicit Harness selection and ordered lending.

The printed evidence directory contains `interactive-ready.json`: the namespace,
session PID, expiry, two successful run names and the private API-token file
location. No token contents are printed. The issuer's read-only Kubernetes token
and unique TLS certificates last nine hours; the session stops after eight,
leaving time for finalizer cleanup. This is not automatic credential renewal.

Forward this session's embedded UI/API from your normal host shell:

```sh
kubectl --context kind-celln-deployed --namespace SESSION_NAMESPACE \
  port-forward --address 127.0.0.1 service/browser 9090:8080
```

Open `http://127.0.0.1:9090/`. Log in with the session API token, then select the
exact namespace from `interactive-ready.json`. Do **not** run `make web-dev-serve`:
it targets the separate historical `sympozium-system` API and can start a second,
mismatched frontend. The session API already includes the matching built UI.

## Try it

1. Open **Runs → New Run** and select Agent **agent**.
2. Choose **runtime** in the one-run Harness selector (or inherit that Agent default).
3. Select **Celln** as the backend and **deepseek-chat** as the model.
4. Lend **uppercase@v1**, then **length@v1**, in that order. This exact combination
   is prepared and registered; arbitrary combinations are not automatically admitted.
5. Submit: `Call uppercase with text hello, then call length with the returned text.
   Use both tools exactly once in that order. Report the uppercase text and its length.`
6. Open the resulting run and select **Result**. The answer is displayed first;
   **Raw Harness output** preserves the original event trace. Reported tool events
   are presentation data, not independent proof of authority or isolation.

You can submit more independent tasks against this selection. Each run permits
at most three model requests, 512 output tokens per request, 1,536 total output
tokens, and only the approved tools and model endpoint. This is a small tool-lending
demo, not a general shell or a conversational Pi/Hermes session. The event stream
connection is real, but native Celln answers are delivered on completion, not
token-streamed. Topology shows the Agent's relationship to the native runtime.

## Stop

Send `SIGUSR1` to the **session PID from its readiness file**, after checking that
it is still the owned `celln-catalogue-setup.test` process. The session archives all
its AgentRuns to `session-runs-at-stop.json`, deletes them with UID preconditions
while the controller can still finalize, tears down its own namespace and host
processes, and restores the original proof controller. Stop the corresponding
UI port-forward too. Other namespaces and Framework are untouched.

The eight-hour session is not resumable after reboot. A restart creates a fresh
namespace, new trust identities and new evidence; it performs admission again.
`interactive-ready.json` is historical after stop: also check that no
`interactive-stopped.json` exists and that the PID, Pods and UI are live.
