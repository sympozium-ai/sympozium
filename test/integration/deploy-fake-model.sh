#!/usr/bin/env bash
# Build the deterministic OpenAI-compatible model fixture
# (test/integration/fake-model), load it into a Kind cluster and run it as
# Service fake-model:8080 in TEST_NAMESPACE. The Kind integration lane points
# test-persistent-harness-session.sh at it instead of a small local LLM so the
# lane tests Sympozium's persistence and lifecycle, not a model's memory
# (issue #471). Real-model qualification is a separate, explicit run.
#
# FAKE_MODEL_TLS_SECRET names a kubernetes.io/tls Secret in TEST_NAMESPACE:
# the fixture then serves HTTPS with it (as a provider receiving a key must)
# on Service port 443, so its origin carries no port, as a mediated route's
# must; e.g. the mediated-fleet journey's key canary. FAKE_MODEL_NAME names
# the Deployment and Service (default fake-model).
set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
KIND_CLUSTER="${KIND_CLUSTER_NAME:-sympozium-ci}"
IMAGE="${FAKE_MODEL_IMAGE:-sympozium-fixture/fake-model:ci}"
NAME="${FAKE_MODEL_NAME:-fake-model}"
TLS_SECRET="${FAKE_MODEL_TLS_SECRET:-}"
PORT=8080 SERVICE_PORT=8080 SCHEME=HTTP ARGS="[]" TLS_MOUNT="" TLS_VOLUME=""
if [ -n "$TLS_SECRET" ]; then
  PORT=8443 SERVICE_PORT=443 SCHEME=HTTPS
  ARGS='["-listen", ":8443", "-tls-cert", "/tls/tls.crt", "-tls-key", "/tls/tls.key"]'
  TLS_MOUNT='          volumeMounts: [{name: tls, mountPath: /tls, readOnly: true}]'
  TLS_VOLUME="      volumes: [{name: tls, secret: {secretName: ${TLS_SECRET}}}]"
fi
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

for command in go docker kind kubectl; do
  command -v "$command" >/dev/null 2>&1 || { echo "required command not found: $command" >&2; exit 1; }
done

CONTEXT_DIR="$(mktemp -d)"
trap 'rm -rf "$CONTEXT_DIR"' EXIT
(cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -o "$CONTEXT_DIR/fake-model" ./test/integration/fake-model)
cp "$ROOT/test/integration/fake-model/Dockerfile" "$CONTEXT_DIR/Dockerfile"
docker build -q -t "$IMAGE" "$CONTEXT_DIR" >/dev/null
kind load docker-image "$IMAGE" --name "$KIND_CLUSTER"

kubectl apply -n "$NAMESPACE" -f - <<YAML
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${NAME}
  labels:
    app: ${NAME}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ${NAME}
  template:
    metadata:
      labels:
        app: ${NAME}
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: fake-model
          image: ${IMAGE}
          imagePullPolicy: IfNotPresent
          args: ${ARGS}
          ports:
            - containerPort: ${PORT}
          readinessProbe:
            httpGet:
              path: /healthz
              port: ${PORT}
              scheme: ${SCHEME}
            periodSeconds: 2
          resources:
            requests:
              cpu: 10m
              memory: 16Mi
            limits:
              memory: 64Mi
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
${TLS_MOUNT}
${TLS_VOLUME}
---
apiVersion: v1
kind: Service
metadata:
  name: ${NAME}
spec:
  selector:
    app: ${NAME}
  ports:
    - name: http
      port: ${SERVICE_PORT}
      targetPort: ${PORT}
YAML
kubectl rollout status "deployment/${NAME}" -n "$NAMESPACE" --timeout=120s
echo "${NAME} ready at $(echo "$SCHEME" | tr A-Z a-z)://${NAME}.${NAMESPACE}.svc.cluster.local:${SERVICE_PORT}/v1"
