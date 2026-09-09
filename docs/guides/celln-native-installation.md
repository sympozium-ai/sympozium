# Native Celln installation — packaging work in progress

This is the installation path for a persistent native Harness parent and
disposable per-turn cells. It is separate from the one-shot router installation.
The chart wiring has local render/refusal tests; it has **not yet passed the
framework deployment or real-model acceptance test**. It is not an installable
release announcement. Track this work against epic #464 and starter-tools #467.

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

No NATS credential is mounted in this first packaging increment. Kubernetes
run/turn status remains the source of conversation history; event-bus fanout
and deployed UI behaviour still need qualification.

## Operator prerequisites

Prepare these explicitly before enabling the chart:

1. A dedicated namespace, for example `celln-agents`, outside the general
   manager's watch scope. Do not change a cluster-wide manager to single-namespace
   operation without inventorying and draining or reassigning its existing
   workloads. Framework currently has workloads in multiple namespaces; do not
   apply the example to that installation as an unattended upgrade.
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

The standalone artifact/catalogue preparation command is still outstanding;
today those preparation steps are implemented in the test fixture. Completing
that extraction is required before this can be called a standard installation.

## Build and chart wiring

Build `images/celln-parent-controller/Dockerfile` from this repository, providing
`CELLN_IMAGE` as a qualified **digest-pinned** Celln image. That base must contain
the matching `/usr/local/bin/celln` and required shared libraries. Record both
source revisions and the resulting combined image digest. The image-specific
context allowlist excludes local `target/` evidence and credentials.

Do not substitute an older published Celln image simply because it contains a
binary with the right name. Native parent protocol/provisioning support must be
qualified. No qualified combined image is published by this change.

Merge the following settings into a reviewed installation's values. Placeholders
are intentionally invalid; render/inspect before applying. Do not use
`--reuse-values` to bypass an audit of controller watch scope.

```yaml
controller:
  watchNamespace: sympozium-system
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

For a controller-only update, retain the same host, authority root and journals;
do not restart the owner just to update the UI/controller. Restarted controllers
must reconcile original identities rather than recreate parents.

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
