# OpenTelemetry Observability

Sympozium supports OpenTelemetry for agent runs and tool execution. The built-in collector is installed by default with `sympozium install` and enabled by default in the Helm chart.

## Instance-Level Configuration

Enable observability per `Agent`:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: my-agent
spec:
  agents:
    default:
      model: gpt-4o
  observability:
    enabled: true
    otlpEndpoint: sympozium-otel-collector.sympozium-system.svc:4317
    otlpProtocol: grpc
    serviceName: sympozium
    resourceAttributes:
      deployment.environment: production
      k8s.cluster.name: my-cluster
```

## Helm Configuration

The Helm chart deploys a built-in OpenTelemetry collector by default:

```yaml
observability:
  enabled: true
  collector:
    service:
      otlpGrpcPort: 4317
      otlpHttpPort: 4318
      metricsPort: 8889
```

Disable it if you already run a shared collector:

```yaml
observability:
  enabled: false
```

## Web UI Observability Views

<p align="center">
  <img src="../assets/otel.png" alt="Sympozium observability dashboard showing token usage and tool call metrics" width="900px">
</p>

- **Runs page** (`/runs`): collector status, run totals, token totals, tool-invocation totals, model token breakdown
- **Run detail** (`/runs/<name>`) → **Telemetry tab**: run timeline events, trace correlation fields, and observed telemetry metric names

## Backend Integration

For full distributed trace waterfall views, configure collector exporters to your preferred backend (Jaeger, Tempo, Datadog, Honeycomb, etc.).
