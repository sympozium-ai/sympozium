# Upgrading a host-managed one-shot Celln router

This path preserves an existing one-shot dispatcher and its state while replacing
the legacy in-cluster router with an authenticated host router and TLS edge.
It is separate from the native enduring parent endpoint. Do not point execution
clients at the parent-only listener, or parent clients at the execution listener.

## Preconditions and safe cutover

1. Close new submissions. Inspect all namespaces for nonterminal Celln AgentRuns
   and inspect the host's actual cells. Complete or cancel work normally before
   stopping services. A Kubernetes phase alone is not proof of host teardown.
2. Save the old Kubernetes manifests, run history, service configuration and
   installed binary. Stop the dispatcher before taking a consistent backup of
   its root and credentials. Store backups privately; they contain authority.
3. Quiesce both the legacy router and installer DaemonSets. The installer must
   not restore the old host binary or service during migration. Preserve their
   manifests and the original host state; do not reset journals or receipts.
4. Install the qualified Celln binary and matching guest/build assets separately
   from the old binary. Keep the dispatcher's original root. Bind it to loopback,
   retain reviewed forge configuration, and explicitly configure its node name,
   stores, capacity and model/HTTPS allowlist. A privileged forge remains a
   trusted host boundary; this migration does not sandbox it.
5. Generate three independent tokens: dispatcher backend, execution client and
   read-only capability discovery. Rotate the old dispatcher token. Only the
   host router gets all three; the controller gets execution and the API server
   gets discovery. Model credentials remain in host-only credential storage.
6. Prepare a private durable ownership directory owned by the router service
   user. Start the router and execution-only TLS edge using the example units
   in `config/host/sympozium-celln-router*.service`. Review their paths and user
   before installation. The router needs no KVM access. Its ownership directory
   is part of the durable execution state, not a disposable cache.
7. Provision a verified server certificate and its renewal procedure. Set
   `CELLN_ROUTER_TLS_LISTEN` in `/etc/celln-router/proxy.env`. Keep both plaintext
   backends on loopback and restrict host ingress independently; Kubernetes
   NetworkPolicy does not establish host-endpoint isolation.
8. Wire the clients below, wait for both rollouts, then qualify a real execution,
   actual-cell cancellation, router restart and same-ID reconciliation before
   reopening submissions. Decide explicitly when to enable services at boot.

## Helm client wiring

Create the two Secrets beforehand, each with a `token` key. For a private CA,
create a public ConfigMap with key `ca-bundle.crt` containing **both** the standard
system trust roots and the operator CA. `SSL_CERT_FILE` selects that complete
bundle for the process, including its other HTTPS clients.

```yaml
celln:
  enabled: true
  routerUrl: https://QUALIFIED_HOST:19444
  allowInsecureHttp: false
  tokenSecret: celln-router-execution
  capabilityTokenSecret: celln-router-discovery
  caConfigMap: celln-router-trust
  router:
    external: true
  installer:
    enabled: false
```

External mode renders only client wiring, not a managed router, installer or
their NetworkPolicy. It refuses missing/shared credential Secret names and
external-plus-installer configurations. It cannot inspect Secret contents:
operators must still ensure the token values differ and are correctly scoped.

When using native parents alongside one-shots, retain the separately configured
native parent controller, preview grants and namespace exclusion. Upgrade the
general controller too: controller-runtime v0.20.0 fails namespace-scoped lists
when using an all-namespace exclusion cache. The v0.20.4 patch fixes that bug;
the regression test also checks that cluster-scoped resources stay unfiltered.

## Recovery and retention

Preserve the ownership ledger and original execution IDs across router upgrades.
After an uncertain submission, reconcile that ID against its original owner;
do not create another request ID to obtain a fresh execution. Verify the exact
original receipt and cell identity after restart.

The old backup is a recovery asset, **not** permission to revert live state after
new requests have been admitted. Before rollback, quiesce again and reconcile all
new requests; assess journal compatibility explicitly. Never start the old router
with an empty ledger, strip finalizers, or restore an older authority snapshot to
replenish consumed limits. Retain protected backups according to audit policy.

See [native installation](celln-native-installation.md) for the independent
persistent parent installation and its context-loss limitations.
