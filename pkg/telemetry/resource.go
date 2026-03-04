package telemetry

import (
	"os"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// buildResource creates an OTel Resource from Config fields and K8s
// downward-API environment variables.
//
// Detected attributes:
//
//	service.name          — Config.ServiceName
//	service.version       — Config.ServiceVersion
//	k8s.namespace.name    — Config.Namespace or NAMESPACE env
//	k8s.pod.name          — POD_NAME env
//	k8s.node.name         — NODE_NAME env
//	sympozium.instance    — INSTANCE_NAME env
//	sympozium.component   — derived from ServiceName
func buildResource(cfg Config) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		semconv.ServiceName(cfg.ServiceName),
	}

	if cfg.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.ServiceVersion))
	}

	if cfg.Namespace != "" {
		attrs = append(attrs, semconv.K8SNamespaceName(cfg.Namespace))
	}

	if v := os.Getenv("POD_NAME"); v != "" {
		attrs = append(attrs, semconv.K8SPodName(v))
	}

	if v := os.Getenv("NODE_NAME"); v != "" {
		attrs = append(attrs, semconv.K8SNodeName(v))
	}

	if v := os.Getenv("INSTANCE_NAME"); v != "" {
		attrs = append(attrs, attribute.String("sympozium.instance", v))
	}

	// Derive component name: "sympozium-agent-runner" → "agent-runner"
	component := cfg.ServiceName
	if after, ok := strings.CutPrefix(component, "sympozium-"); ok {
		component = after
	}
	attrs = append(attrs, attribute.String("sympozium.component", component))

	return resource.NewWithAttributes(semconv.SchemaURL, attrs...), nil
}
