#!/usr/bin/env bash
# Integration test: MCP bridge end-to-end via a Python MCP server.
#
# What it does:
#   1. Deploys a minimal Python MCP server (JSON-RPC 2.0 / Streamable HTTP)
#      as a Pod + Service in the cluster
#   2. Creates a SympoziumInstance with the MCP server configured
#   3. Creates an AgentRun (via LM Studio) that calls the MCP tool
#   4. Validates the agent discovered and invoked the tool, and received the
#      expected result
#   5. Cleans up all test resources
#
# Prerequisites:
#   - Kind cluster running with Sympozium installed
#   - LM Studio running on the host with a model loaded
#
# Usage:
#   ./test/integration/test-mcp-bridge.sh
#   LMSTUDIO_URL=http://192.168.1.10:1234/v1 ./test/integration/test-mcp-bridge.sh
#   TEST_MODEL=qwen3.5-9b TEST_TIMEOUT=180 ./test/integration/test-mcp-bridge.sh

set -euo pipefail

# --- Configuration ---
NAMESPACE="${TEST_NAMESPACE:-default}"
INSTANCE_NAME="inttest-mcp-bridge"
RUN_NAME="inttest-mcp-bridge-run"
SECRET_NAME="inttest-lmstudio-key"
MCP_POD_NAME="inttest-mcp-echo"
MCP_SERVICE_NAME="inttest-mcp-echo"
MCP_CONFIGMAP_NAME="inttest-mcp-echo-server"
NETPOL_NAME="inttest-mcp-allow-egress"
MODEL="${TEST_MODEL:-qwen3.5-9b}"
LMSTUDIO_URL="${LMSTUDIO_URL:-http://host.docker.internal:1234/v1}"
TIMEOUT="${TEST_TIMEOUT:-180}"         # seconds to wait for AgentRun
MCP_READY_TIMEOUT=60                   # seconds to wait for MCP server pod
MARKER_WORD="Sympozium"               # word the agent sends to the echo tool

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; }
info() { echo -e "${YELLOW}● $*${NC}"; }

cleanup() {
    info "Cleaning up test resources..."
    kubectl delete agentrun "$RUN_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete sympoziuminstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete jobs -n "$NAMESPACE" -l "sympozium.ai/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete pods -n "$NAMESPACE" -l "sympozium.ai/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete service "$MCP_SERVICE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete pod "$MCP_POD_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete configmap "$MCP_CONFIGMAP_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete networkpolicy "$NETPOL_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete secret "$SECRET_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
}

trap cleanup EXIT

# --- Pre-flight checks ---
info "Running integration test: MCP bridge end-to-end"

if ! kubectl get crd agentruns.sympozium.ai >/dev/null 2>&1; then
    fail "Sympozium CRDs not installed. Is the cluster set up?"
    exit 1
fi

if ! kubectl get deployment sympozium-controller-manager -n sympozium-system >/dev/null 2>&1; then
    fail "Sympozium controller not running."
    exit 1
fi

# --- Clean up any previous test run ---
cleanup 2>/dev/null || true
sleep 2

# ============================================================
# Step 1: Deploy Python MCP server
# ============================================================
info "Creating Python MCP server ConfigMap..."

cat <<'PYEOF' | kubectl create configmap "$MCP_CONFIGMAP_NAME" -n "$NAMESPACE" --from-file=server.py=/dev/stdin
"""Minimal MCP server implementing JSON-RPC 2.0 over Streamable HTTP.

Exposes one tool:
  echo(message: str) -> "echo: <message>"

No external dependencies — stdlib only.
"""
import json
from http.server import HTTPServer, BaseHTTPRequestHandler

class MCPHandler(BaseHTTPRequestHandler):
    """Handle JSON-RPC 2.0 requests for the MCP protocol."""

    session_id = "inttest-session-1"

    # --- health probe ---------------------------------------------------
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"status":"ok"}')

    # --- JSON-RPC -------------------------------------------------------
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length)) if length else {}
        method = body.get("method", "")
        req_id = body.get("id")

        if method == "initialize":
            result = {
                "protocolVersion": "2025-03-26",
                "capabilities": {"tools": {"listChanged": False}},
                "serverInfo": {"name": "inttest-echo", "version": "1.0.0"},
            }
        elif method == "notifications/initialized":
            # notification — no response required, but we send 200
            self._send_json({"jsonrpc": "2.0", "id": req_id, "result": {}})
            return
        elif method == "tools/list":
            result = {
                "tools": [
                    {
                        "name": "echo",
                        "description": "Echoes the message back with a greeting prefix.",
                        "inputSchema": {
                            "type": "object",
                            "properties": {
                                "message": {
                                    "type": "string",
                                    "description": "The message to echo back.",
                                }
                            },
                            "required": ["message"],
                        },
                    }
                ]
            }
        elif method == "tools/call":
            params = body.get("params", {})
            tool_name = params.get("name", "")
            args = params.get("arguments", {})

            if tool_name == "echo":
                msg = args.get("message", "")
                result = {
                    "content": [
                        {"type": "text", "text": f"hello from mcp: {msg}"}
                    ]
                }
            else:
                self._send_error(req_id, -32601, f"Unknown tool: {tool_name}")
                return
        else:
            self._send_error(req_id, -32601, f"Method not found: {method}")
            return

        self._send_json({"jsonrpc": "2.0", "id": req_id, "result": result})

    # --- helpers --------------------------------------------------------
    def _send_json(self, obj):
        data = json.dumps(obj).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Mcp-Session-Id", self.session_id)
        self.end_headers()
        self.wfile.write(data)

    def _send_error(self, req_id, code, message):
        self._send_json({
            "jsonrpc": "2.0",
            "id": req_id,
            "error": {"code": code, "message": message},
        })

    def log_message(self, fmt, *args):
        # Prefix logs for easy grep in kubectl logs
        print(f"[mcp-echo] {fmt % args}", flush=True)


if __name__ == "__main__":
    addr = ("0.0.0.0", 8080)
    print(f"[mcp-echo] Listening on {addr[0]}:{addr[1]}", flush=True)
    HTTPServer(addr, MCPHandler).serve_forever()
PYEOF

info "Creating MCP server Pod..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${MCP_POD_NAME}
  namespace: ${NAMESPACE}
  labels:
    app: inttest-mcp-echo
spec:
  containers:
    - name: mcp
      image: python:3.12-slim
      command: ["python", "/srv/server.py"]
      ports:
        - containerPort: 8080
      readinessProbe:
        httpGet:
          path: /
          port: 8080
        initialDelaySeconds: 2
        periodSeconds: 3
      volumeMounts:
        - name: server-script
          mountPath: /srv
  volumes:
    - name: server-script
      configMap:
        name: ${MCP_CONFIGMAP_NAME}
EOF

info "Creating MCP server Service..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Service
metadata:
  name: ${MCP_SERVICE_NAME}
  namespace: ${NAMESPACE}
spec:
  selector:
    app: inttest-mcp-echo
  ports:
    - port: 8080
      targetPort: 8080
EOF

# Wait for the MCP server to become ready.
info "Waiting for MCP server pod to be ready..."
elapsed=0
while [[ $elapsed -lt $MCP_READY_TIMEOUT ]]; do
    phase=$(kubectl get pod "$MCP_POD_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    ready=$(kubectl get pod "$MCP_POD_NAME" -n "$NAMESPACE" -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null || echo "")
    if [[ "$phase" == "Running" && "$ready" == "true" ]]; then
        break
    fi
    sleep 2
    elapsed=$((elapsed + 2))
    if (( elapsed % 10 == 0 )); then
        info "  ...${elapsed}s elapsed (phase: ${phase:-Pending}, ready: ${ready:-false})"
    fi
done

if [[ "$ready" != "true" ]]; then
    fail "MCP server pod did not become ready within ${MCP_READY_TIMEOUT}s"
    kubectl describe pod "$MCP_POD_NAME" -n "$NAMESPACE" 2>/dev/null | tail -20
    exit 1
fi
pass "MCP server pod is ready"

# ============================================================
# Step 2: Network policy — allow agent egress to MCP server + LM Studio
# ============================================================
info "Creating supplementary NetworkPolicy for test traffic..."
cat <<EOF | kubectl apply -f -
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ${NETPOL_NAME}
  namespace: ${NAMESPACE}
spec:
  podSelector:
    matchLabels:
      sympozium.ai/role: agent
  policyTypes:
    - Egress
  egress:
    # Allow agent → MCP echo server (in-cluster, port 8080)
    - to:
        - podSelector:
            matchLabels:
              app: inttest-mcp-echo
      ports:
        - protocol: TCP
          port: 8080
    # Allow agent → LM Studio on host (port 1234)
    - to: []
      ports:
        - protocol: TCP
          port: 1234
EOF
pass "NetworkPolicy created"

# ============================================================
# Step 3: Create dummy auth secret (LM Studio doesn't need a real key)
# ============================================================
if ! kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
    info "Creating dummy auth secret for LM Studio"
    kubectl create secret generic "$SECRET_NAME" \
        --from-literal=OPENAI_API_KEY="lm-studio" \
        -n "$NAMESPACE"
fi

# ============================================================
# Step 4: Create SympoziumInstance with MCP server
# ============================================================
MCP_URL="http://${MCP_SERVICE_NAME}.${NAMESPACE}.svc:8080"
info "Creating SympoziumInstance: $INSTANCE_NAME (MCP URL: $MCP_URL)"

cat <<EOF | kubectl apply -f -
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: ${INSTANCE_NAME}
  namespace: ${NAMESPACE}
spec:
  agents:
    default:
      model: ${MODEL}
      baseURL: "${LMSTUDIO_URL}"
  authRefs:
    - secret: ${SECRET_NAME}
  mcpServers:
    - name: echo
      url: "${MCP_URL}"
      toolsPrefix: echo
      timeout: 15
EOF

# ============================================================
# Step 5: Create AgentRun
# ============================================================
info "Creating AgentRun: $RUN_NAME"
cat <<EOF | kubectl apply -f -
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: ${RUN_NAME}
  namespace: ${NAMESPACE}
  labels:
    sympozium.ai/instance: ${INSTANCE_NAME}
spec:
  instanceRef: ${INSTANCE_NAME}
  agentId: default
  sessionKey: "inttest-mcp-$(date +%s)"
  task: |
    You have access to an MCP tool called echo_echo.
    Call the echo_echo tool with message set to exactly "${MARKER_WORD}".
    After you receive the result, reply with the exact tool output and nothing else.
  model:
    provider: lm-studio
    model: ${MODEL}
    baseURL: "${LMSTUDIO_URL}"
    authSecretRef: ${SECRET_NAME}
  timeout: "5m"
EOF

# ============================================================
# Step 6: Wait for completion
# ============================================================
info "Waiting up to ${TIMEOUT}s for AgentRun to complete..."
elapsed=0
phase=""
pod=""
while [[ $elapsed -lt $TIMEOUT ]]; do
    phase=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    if [[ -z "$pod" ]]; then
        pod=$(kubectl get pods -n "$NAMESPACE" -l "sympozium.ai/agentrun=$RUN_NAME" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
        if [[ -n "$pod" ]]; then
            info "Agent pod found: $pod"
        fi
    fi
    if [[ "$phase" == "Succeeded" || "$phase" == "Failed" ]]; then
        break
    fi
    sleep 5
    elapsed=$((elapsed + 5))
    if (( elapsed % 15 == 0 )); then
        info "  ...${elapsed}s elapsed (phase: ${phase:-Pending})"
    fi
done

if [[ "$phase" != "Succeeded" && "$phase" != "Failed" ]]; then
    fail "AgentRun did not complete within ${TIMEOUT}s (last phase: ${phase:-unknown})"
    info "Debug: kubectl describe agentrun $RUN_NAME -n $NAMESPACE"
    if [[ -n "$pod" ]]; then
        info "Agent logs (last 30 lines):"
        kubectl logs "$pod" -c agent -n "$NAMESPACE" --tail=30 2>/dev/null || true
        info "MCP bridge logs:"
        kubectl logs "$pod" -c mcp-bridge -n "$NAMESPACE" --tail=30 2>/dev/null || true
    fi
    exit 1
fi

# ============================================================
# Step 7: Validate results
# ============================================================
echo ""
failures=0

if [[ "$phase" == "Failed" ]]; then
    fail "AgentRun phase: Failed"
    kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status}' | python3 -m json.tool 2>/dev/null || true
    if [[ -n "$pod" ]]; then
        info "Agent logs (last 30 lines):"
        kubectl logs "$pod" -c agent -n "$NAMESPACE" --tail=30 2>/dev/null || true
        info "MCP bridge logs:"
        kubectl logs "$pod" -c mcp-bridge -n "$NAMESPACE" --tail=30 2>/dev/null || true
    fi
    exit 1
fi

pass "AgentRun phase: Succeeded"

result=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.result}' 2>/dev/null || echo "")

# Validation 1: MCP bridge discovered the echo tool
if [[ -n "$pod" ]]; then
    bridge_logs=$(kubectl logs "$pod" -c mcp-bridge -n "$NAMESPACE" 2>/dev/null || echo "")
    if echo "$bridge_logs" | grep -qi "echo"; then
        pass "MCP bridge logs reference the echo tool"
    else
        fail "MCP bridge logs do not reference the echo tool"
        failures=$((failures + 1))
        info "Bridge logs (last 20 lines):"
        echo "$bridge_logs" | tail -20
    fi
else
    info "Pod not found — cannot check mcp-bridge logs"
fi

# Validation 2: The Python MCP server received a tool call
mcp_logs=$(kubectl logs "$MCP_POD_NAME" -n "$NAMESPACE" 2>/dev/null || echo "")
if echo "$mcp_logs" | grep -qi "tools/call"; then
    pass "Python MCP server received tools/call request"
else
    fail "Python MCP server did not receive tools/call"
    failures=$((failures + 1))
    info "MCP server logs:"
    echo "$mcp_logs" | tail -20
fi

# Validation 3: Agent result contains the expected echo output
if echo "$result" | grep -qi "hello from mcp.*${MARKER_WORD}\|${MARKER_WORD}.*hello from mcp"; then
    pass "Agent result contains MCP echo output with '${MARKER_WORD}'"
elif echo "$result" | grep -qi "hello from mcp"; then
    pass "Agent result contains MCP echo output (partial match)"
elif echo "$result" | grep -qi "${MARKER_WORD}"; then
    info "Agent result mentions '${MARKER_WORD}' but not the exact echo format"
    info "Result (first 500 chars):"
    echo "$result" | head -c 500
    echo ""
else
    fail "Agent result does not contain expected MCP output"
    failures=$((failures + 1))
    info "Result (first 500 chars):"
    echo "$result" | head -c 500
    echo ""
fi

# Validation 4: Agent logs show MCP tool invocation
if [[ -n "$pod" ]]; then
    agent_logs=$(kubectl logs "$pod" -c agent -n "$NAMESPACE" 2>/dev/null || echo "")
    if echo "$agent_logs" | grep -qi "echo_echo\|mcp.*tool\|MCP tool"; then
        pass "Agent logs confirm MCP tool invocation"
    else
        info "Agent logs do not explicitly mention MCP tool (may still have worked)"
        if [[ -n "$agent_logs" ]]; then
            info "Agent logs (last 20 lines):"
            echo "$agent_logs" | tail -20
        fi
    fi
fi

# ============================================================
# Summary
# ============================================================
echo ""
echo "=============================="
echo " MCP Bridge Integration Test"
echo "=============================="
echo " AgentRun:   $RUN_NAME"
echo " Phase:      $phase"
echo " Model:      $MODEL"
echo " LM Studio:  $LMSTUDIO_URL"
echo " MCP Server: $MCP_URL"
if [[ -n "$pod" ]]; then
    echo " Agent Pod:  $pod"
fi
echo " Failures:   $failures"
echo "=============================="
echo ""

if [[ $failures -gt 0 ]]; then
    fail "Integration test finished with $failures failure(s)"
    exit 1
fi

pass "MCP bridge integration test complete"
