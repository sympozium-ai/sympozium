#!/usr/bin/env bash
# Build the deterministic OpenAI-compatible model fixture
# (test/integration/fake-model), load it into a Kind cluster and run it as
# Service fake-model:8080 in TEST_NAMESPACE. The Kind integration lane points
# test-persistent-harness-session.sh at it instead of a small local LLM so the
# lane tests Sympozium's persistence and lifecycle, not a model's memory
# (issue #471). Real-model qualification is a separate, explicit run.
set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
KIND_CLUSTER="${KIND_CLUSTER_NAME:-sympozium-ci}"
IMAGE="${FAKE_MODEL_IMAGE:-sympozium-fixture/fake-model:ci}"
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
  name: fake-model
  labels:
    app: fake-model
spec:
  replicas: 1
  selector:
    matchLabels:
      app: fake-model
  template:
    metadata:
      labels:
        app: fake-model
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
          ports:
            - containerPort: 8080
          readinessProbe:
            httpGet:
              path: /healthz
              port: 8080
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
---
apiVersion: v1
kind: Service
metadata:
  name: fake-model
spec:
  selector:
    app: fake-model
  ports:
    - name: http
      port: 8080
      targetPort: 8080
YAML
kubectl rollout status deployment/fake-model -n "$NAMESPACE" --timeout=120s
echo "fake-model ready at http://fake-model.${NAMESPACE}.svc.cluster.local:8080/v1"
