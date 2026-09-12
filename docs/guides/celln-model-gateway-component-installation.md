# Model gateway component packaging (#506)

The chart can deploy the separate gateway component for controlled qualification.
It is disabled by default and **does not enable mediated Celln admission** or
complete the install-once tenant journey. The reviewed contract/runtime/broker
and installed release gates remain prerequisites for production enablement.

## Operator prerequisites

1. Build `images/model-gateway/Dockerfile`, publish and record its image digest.
2. Provision PostgreSQL separately. Apply reviewed migrations
   `migrations/002_celln_model_budget.sql` and
   `migrations/003_celln_model_gateway.sql` in order; preserve existing rows.
   The chart does not initialize, restore, reset or migrate databases. The binary
   refuses startup/readiness when the schema/dependencies are unavailable.
3. In the control-plane namespace, provision an operator-controlled PVC containing
   `gateway.json`, public-only JWKS, TLS certificate/key, static issuer
   registration transport token and database connection file. Files must be
   readable by UID/GID 65532; registration/database files must be owner-only
   (0600 or 0400). The chart does not chmod/chown or regenerate files.
4. `gateway.json` uses the actual `cmd/model-gateway` configuration fields:

   ```json
   {
     "clusterId": "operator-cluster-id",
     "issuer": "operator-issuer-id",
     "listen": ":8443",
     "tlsCertificateFile": "/configuration/tls.crt",
     "tlsKeyFile": "/configuration/tls.key",
     "verificationKeysFile": "/configuration/public-jwks.json",
     "registrationTokenFile": "/configuration/registration-token",
     "databaseUrlFile": "/configuration/database-url",
     "privateOrigins": []
   }
   ```

   Review private provider origins explicitly when needed. Provider keys remain
   in the referenced tenant Secrets, not on this PVC. Never put issuer private
   signing keys or ephemeral run/model tokens in this configuration or Helm
   values. Public JWKS reload is explicit SIGHUP; retain outstanding verification
   keys through rotation. TLS/static-token configuration changes require a
   controlled restart; do not assume hot reload.
5. Declare reviewed NetworkPolicy egress peers/ports for DNS, Kubernetes API,
   PostgreSQL and approved providers. There is no generated allow-all fallback.
   Kubernetes API/DNS IPs and CNI behaviour are installation-specific. Verify with
   an enforcing CNI before claiming network isolation.

Use `modelGateway.enabled=true`, `modelGateway.image=repository@sha256:...`,
`modelGateway.configurationClaim=<reviewed-pvc>`, and a reviewed
`modelGateway.egress` NetworkPolicy rule list in the ordinary chart values.
Missing claim/image/egress or an unpinned image refuses rendering. This creates
one Deployment/Service/ServiceAccount/ClusterRole/Binding/NetworkPolicy; no second
Celln plane. The claim is mounted read-only. Recreate rollout avoids concurrent
claim attachment and causes component downtime; outstanding reservations remain
in PostgreSQL and must not be refunded merely because transport was interrupted.

## Trust and limits

The role grants cluster-wide **get on Secrets**, not list/watch. This remains
broad privilege even though the application checks exact namespaced route/UID
bindings. Existing controller permissions are unchanged. Protect the control-plane
namespace, configuration claim, database and operator identities accordingly.
The chart does not grant a tenant user access to the operator API.

Ingress requires namespace **and** pod selectors in each peer, allowing the
existing controller in the control-plane namespace and router/dispatcher in
`celln-system`. Tenant copied labels in another namespace do not satisfy these
peers. These are network selectors, not authentication: TLS verification and the
separate registration/model credentials remain mandatory. Kubernetes HTTPS probe
success is readiness evidence, not client verification of the service certificate.

The gateway is rootless with a read-only root filesystem and no capabilities.
Its configuration PVC is external operator state; Helm uninstall does not delete
it or PostgreSQL. Normal chart reconciliation cannot generate signing keys, reset
journals or create per-tenant registration Secrets. Renderer tests are not evidence
that installed TLS, database restore, CNI or KVM paths have been qualified.
