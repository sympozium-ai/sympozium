# Native Celln installation — Linux amd64/KVM

This is the installation path for a persistent native Harness parent and
disposable per-turn cells. It is separate from the one-shot router installation.
For an existing host dispatcher, see the
[one-shot router migration](celln-external-router-migration.md).
The chart wiring and standalone installation commands are qualified on framework.
Track epic #464 and starter-tools #467 for the supported MLP and deferred work.
Release archives contain binaries and example units, not pre-approved authority,
model credentials, a kernel, or a tenant-ready signed package.

## Choosing Celln in the UI

Kubernetes remains the default. Selecting a Harness on an Agent does not switch
its runs to Celln.

### Create an Agent with saved execution defaults

Use **Create Agent → Harness / Run → Execution plane → SkillPacks**. The
execution-plane step is always shown, including when you start with a preselected
Harness. Choose Kubernetes for the built-in Run path or an OCI-compatible
Harness; choose Celln with a compatible native Harness.

For Celln, explicitly continue without SkillPacks (native SkillPacks are not
supported yet), then choose **Borrow tools**. Workspace read/write and HTTPS
fetch are marked as starter suggestions when installed. Select the exact
catalogue revisions you need, or leave the list empty to request no tools.
The wizard does not silently remove SkillPacks or automatically grant suggested
tools. Native model credentials remain on the host, so this flow skips API-key,
channel and heartbeat setup. Kubernetes retains those steps and its SkillPacks.

The confirmation and YAML preview include the plane, runtime, lifecycle and
tool revisions. Creation stores these in `Agent.spec.execution`; a subsequent
run can inherit them without repeating the choices. Enduring defaults are a
600-second lease, 8 turns, 24 model requests and 8192 output tokens. Review or
edit limits on the Agent Harness tab before starting work.

**Creating an Agent is not operator approval.** The Agent's new identity and
specification must be covered by current grants and host registration. The
effective-permission preview on its Harness tab is available after creation.
Existing approvals for another Agent are not transferable. An unapproved run
must remain unapproved rather than acquiring authority from wizard defaults.

See [the paired Agent/AgentRun YAML example](../../config/samples/agent-native-execution-defaults.yaml).

### Start a run or override its defaults

1. Open **Runs → New Run**. You can also follow the run-creation link from an
   Agent’s **Harness** tab or the feed’s quick-task input; these carry the Agent
   selection into the form.
2. Choose **Celln** in **Execution environment** at the top of the form. The
   availability message reports host eligibility, not permission to execute.
3. Select the Agent and a compatible native **Harness for this run**. Existing
   OCI harnesses and SkillPack sidecars do not become native Celln tools.
4. For a continuing conversation, enable **Enduring conversation**, review its
   lifetime and usage limits, and select approved borrowed tools. For a one-off
   task, leave enduring mode off.
5. Review the permission preview and submit. The operator preparation below is
   still required; a catalogue entry or suggested toolbox is not a grant.

The feed’s quick-send path inherits saved Agent execution defaults. Agents with
no execution defaults still use Kubernetes. Use New Run for explicit overrides;
incompatible overrides are rejected rather than silently dropping tools.
The **Concepts** guide explains these choices first, with implementation and
YAML terminology in an expandable glossary.

## Architecture and boundaries

The existing host Celln owner retains the live parent, admits warm motes, owns
the workspace and enforces child/model authority. The Kubernetes parent-only
controller uses the local Celln CLI only to provision approved run identities;
it communicates with that same owner over authenticated HTTPS.

The controller runs on one explicit node, with one replica and Recreate updates.
It mounts one existing operator-owned directory at its original absolute path.
It receives neither `/dev/kvm`, host networking, a host-root mount, nor model
credentials. Its writable authority store is a trusted operator boundary, not a
tenant workspace. Never allow tenant pods to mount it or use this controller's
ServiceAccount. The chart does not make a compromised trusted provisioner safe.

Kubernetes projects and rotates the controller's ten-minute ServiceAccount
credential. Its namespace Role permits run/turn reconciliation, catalogue reads
and reads of exactly three grant ConfigMaps. It cannot read Secrets through the
API, issue its own Kubernetes tokens, change grants or launch tenant pods.
Kubelet mounts the separately provisioned owner token/CA configuration Secret.

No NATS credential is mounted in this packaging increment. Kubernetes run/turn
status remains the source of conversation history. The installed UI/API path is
qualified; NATS event-bus fanout is not provided by this parent-only controller.

## Operator prerequisites

Prepare these explicitly before enabling the chart:

1. A dedicated namespace, for example `celln-agents`, outside the general
   manager's watch scope. Use `controller.excludeWatchNamespaces: [celln-agents]`
   to retain its existing multi-namespace watches. This is mutually exclusive
   with `controller.watchNamespace`. Exclusion filters namespaced caches, not
   cluster-scoped discovery or Kubernetes RBAC. Start with an empty dedicated
   namespace and verify the general controller rollout before creating runs.
   Do not reassign already-bound runs by changing namespace configuration.
2. A qualified Linux/KVM host owner with signed parent/worker motes, admitted
   native runtime and the exact `workspace-read`, `workspace-write`, `https-fetch`
   catalogue revisions. Installation alone grants no tool authority. Guest
   startup remains warm-mote CoW; the controller image does not boot guests.
3. A directory such as `/var/lib/sympozium-celln/starter` containing `authority/`,
   `journal/` and `approvals/`. Its UID/GID must match the chart settings. The
   owner serves this same `authority/` root. The chart deliberately uses
   `hostPath.type: Directory`, not DirectoryOrCreate, and never changes ownership.
   Store model keys outside the entire mounted tree, in host-only credential
   storage. Do not repurpose a test fixture's private directory as installation.
4. A stable TLS owner endpoint reachable from the controller pod, with a verified
   server certificate and an independently scoped owner bearer token. Keep the
   plaintext owner backend on loopback. Restrict owner ingress to intended
   operators; the chart does not configure the host firewall or claim that a
   generic NetworkPolicy protects a host-network endpoint.
5. Three independent grant ConfigMaps and an operator-owned `parent-config`
   Secret in `celln-agents`. The Secret contains `registrations.json`, plus
   `owner-token` and `owner-ca.pem` at minimum. It must not contain a model key.
   `RegistrationConfig` uses:

   - `journal`: `/var/lib/sympozium-celln/starter/journal`
   - `approvals`: `/var/lib/sympozium-celln/starter/approvals`
   - `localProvisioner.binary`: `/usr/local/bin/celln`
   - `localProvisioner.root`: `/var/lib/sympozium-celln/starter/authority`
   - matching localProvisioner journal/approvals paths
   - token/CA paths under `/etc/sympozium/celln-parent/`
   - the exact owner HTTPS target, live selection digest and host templates

   The selected subjects include Kubernetes identities. Bind templates to the
   installed catalogue and grants, not copied UIDs from a previous cluster.
6. A permission-preview ConfigMap in the API-server namespace, referencing
   those grant sources. It contains no execution credential. Existing preview
   RBAC must authorize the API server's intended reads. Preview is not proof
   of owner readiness or permission to execute.

## Standalone preparation and installation

Celln provides `starter-package`, `starter-inspect`, `starter-admit` and
`starter-configure`. See Celln's `docs/NATIVE_STARTER_PACKAGING.md` for the
explicit publisher approval, package hash and bounded-effects approval steps.
Admission executes guest member checks; configuration does not launch a model
or read its credential. Preserve the resulting configuration and receipt.

After the general controller has completed its namespace-separated rollout and
the dedicated namespace exists, run:

```sh
sympozium --kubeconfig /ABS/FRAMEWORK_KUBECONFIG -n celln-agents \
  celln-tool install-native \
  --configuration-dir /ABS/REVIEWED_CONFIGURATION \
  --output-dir /ABS/NEW_PRIVATE_INSTALLATION_OUTPUT \
  --state-path /var/lib/sympozium-celln/starter \
  --owner-target https://OWNER_ADDRESS:19443 \
  --scope STABLE_INSTALLATION_ID \
  --reviewed-package-hash blake3:REVIEWED_PACKAGE_JSON_HASH \
  --approve-starter-tools
```

This creates `celln-native`, `celln-agent`, three tool revisions and three
explicit grant ConfigMaps, then binds their actual UIDs/generations into
`registrations.json`. It writes `preview.json`, `run.json` and `installed.json`
but submits no run and asserts no execution readiness. Existing resources or
output directories are refused. Inspect partial state after failure; do not
delete journals or change the scope to retry consumed authority.

The native runtime intentionally has no OCI image or OCI Ready condition.
Upgrade the admission webhook too: catalogue-selected Celln tasks must not
inherit an OCI adapter from `Agent.runtimeRef`. Explicit OCI harness tasks are
still rejected on the Celln backend; all ordinary policy checks remain active.

Create the controller Secret from registrations plus the owner bearer and CA
files, and create the API preview ConfigMap from `preview.json` under the key
`config.json`. The model key remains host-only. The owner needs a separate
`trusted-parent-clients.json` principal/token-hash policy: its generic dispatcher
`--token-file` does not grant parent access.

Example hardened service units are in `config/host/sympozium-celln-native-*.service`.
They use a dedicated non-root owner, loopback backend and separate TLS edge;
review paths, node, UID, address and ceilings for the installation. Prepare
certificates and host credentials separately, run `systemd-analyze verify`,
and qualify startup before enabling them across reboots. Existing owners are
not replaced. Certificate expiry and key rotation are operator responsibilities.

For an incremental upgrade from v0.10.56, the additive bridge in
`config/samples/celln-native-upgrade-rbac.yaml` provides the new API turn/catalogue
and controller workspace permissions. It targets the standard service-account
names. Remove that bridge only after the regular chart roles supply the same
rights. Apply the new CRDs before upgrading consumers.

## Build and chart wiring

Sympozium v0.10.57 publishes `sympozium-celln-native-linux-amd64.tar.gz`, its
SHA-256 sidecar and `celln-parent-controller.digest` as release assets. The host
archive combines the checksum-pinned Celln v0.5.8 bundle, both TLS proxies,
certificate renewal helper and example service units. `SHA256SUMS` checks the
unpacked files and `share/sympozium/SOURCES.json` records the source pair.
Verify checksums and extract into a **new staging directory**, then review the
installation paths; do not unpack over a running owner or its authority state.

Use `ghcr.io/sympozium-ai/sympozium/celln-parent-controller` with the digest from
the matching release asset. Native host/controller artifacts are amd64-only;
the ordinary Sympozium image fleet still supports its existing platforms.
The workflow also builds candidate artifacts before release so the downloaded
bytes and container can be qualified on the actual host.

Build `images/celln-parent-controller/Dockerfile` from this repository, providing
`CELLN_IMAGE` as a qualified **digest-pinned** Celln image. That base must contain
the matching `/usr/local/bin/celln` and required shared libraries. Record both
source revisions and the resulting combined image digest. The image-specific
context allowlist excludes local `target/` evidence and credentials.

Do not substitute an older published Celln image simply because it contains a
binary with the right name. Native parent protocol/provisioning support must be
qualified. The exact release dependency is pinned in
`images/celln-parent-controller/celln-release.json`.

Merge the following settings into a reviewed installation's values. Placeholders
are intentionally invalid; render/inspect before applying. Do not use
`--reuse-values` to bypass an audit of controller watch scope.

```yaml
controller:
  # Keep existing namespaces managed; reserve only the new native namespace.
  excludeWatchNamespaces: [celln-agents]
celln:
  # Native parents do not require enabling the legacy one-shot router.
  enabled: false
  permissionPreviewConfigMap: celln-parent-preview
  nativeParent:
    enabled: true
    namespace: celln-agents
    nodeName: framework
    statePath: /var/lib/sympozium-celln/starter
    configSecret: parent-config
    uid: 10001
    gid: 10001
    image:
      repository: YOUR_REGISTRY/celln-parent-controller
      digest: sha256:YOUR_QUALIFIED_DIGEST
```

The dedicated namespace is not Helm-owned: removing the chart must not delete
run identities and their history. The same applies to host state. Keep the
registered owner, root and journals stable over controller upgrades. Never
replenish authority by discarding or moving journals. Node migration and HA are
not supported by this single-owner package.

## UI, YAML and release acceptance

Once installation is qualified, select the dedicated namespace in the UI,
create a Harness run, choose Celln and enduring conversation, then explicitly
select approved starter revisions. See
[the YAML example](../../config/samples/celln-native-starter-run.yaml). The
installed operator template must match the chosen model, persona and ceilings.
Read/write are logical run-owned artifacts, not host paths; HTTPS destinations
and budgets are explicit grants. Tools cannot be added to a live parent.

Persistence means live context across turns, not recovery after host/parent
loss. Files and live context disappear with the parent. The current approximately
2 KiB context bound must remain visible; Python, shell, arbitrary Harness
adapters, checkpoints and pause/resume are deferred.

Required release evidence, using the installed processes rather than a test
supervisor:

- Clean host/catalogue preparation and image installation, with recorded hashes.
- Actual UI creation with the three approved tools and matching YAML creation.
- Real-model write/read across distinct children, bounded HTTPS, refresh,
  cancellation followed by successful conversation and confirmed teardown.
- Controller credential rotation and controller/API restart without replay.
- Context-loss reporting after owner loss, never an empty claimed resumption.
- Existing one-shot and ordinary Kubernetes agents remain functional.

## Stop, upgrade and uninstall

After qualification, enable the reviewed owner/proxy/router systemd units for
startup; enabling a service is not proof of live-context recovery after reboot.
Leave failed historical identities fenced. Do not automatically resubmit them.

For an operator-managed private CA, `renew-celln-tls.sh` and the daily
`sympozium-celln-tls-renew.timer` renew the endpoint certificate when less than
30 days remain. The helper preserves the private leaf key, atomically replaces
the certificate and restarts only running TLS edges. It refuses if the CA has
less than 93 days remaining. Review the unit's credential directory and IP,
monitor timer/service failures, and distribute CA trust deliberately before CA
expiry. CA/key rotation remains an operator procedure, not implicit renewal.
The CA signing key is root-only and never mounted in Kubernetes. Deployments
with an external certificate authority should use that authority's renewal
mechanism instead; do not install a local CA key just to use this example.

For a controller-only update, retain the same host, authority root and journals;
do not restart the owner just to update the UI/controller. Restarted controllers
must reconcile original identities rather than recreate parents.

Current Celln owners record their host process identity before launching native
parents. After a process crash/restart on the same Linux boot, PID namespace and
UID, normal run deletion can complete once Celln verifies that the original
process has exited. The finalizer is cleared by the normal controller only after
that host acknowledgement. This does not restore files/context or permit replay.
Older journals without process identity, host reboots and changed namespaces
remain conservative: preserve their finalizers and evidence for operator
reconciliation. Do not fabricate process records or reset journals.

Before uninstall, close new submissions and stop/delete every run in the parent
namespace through the normal API. Wait for confirmed cleanup and finalizer
removal while the owner and controller are still running. The pre-delete hook
refuses before mutating deployments if any parent-namespace AgentRun remains or
the Kubernetes read fails. Admission must stay closed during uninstall; this
guard is not an atomic admission barrier. Never bypass it with `--no-hooks` or
strip AgentRun finalizers. The legacy hook no longer strips AgentRun finalizers
anywhere, because a removed finalizer is not evidence that execution stopped.

Uninstall retains the separately created namespace and host state. Archive or
remove those only after checking cleanup and deciding audit retention. This
package performs no recursive deletion of operator state.
