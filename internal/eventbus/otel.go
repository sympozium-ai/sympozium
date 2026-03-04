// Package eventbus provides OTel trace context propagation over NATS headers.
package eventbus

import (
	"context"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// natsHeaderCarrier implements propagation.TextMapCarrier over nats.Header,
// enabling W3C TraceContext inject/extract through NATS message headers.
type natsHeaderCarrier struct {
	header nats.Header
}

var _ propagation.TextMapCarrier = (*natsHeaderCarrier)(nil)

func (c *natsHeaderCarrier) Get(key string) string {
	return c.header.Get(key)
}

func (c *natsHeaderCarrier) Set(key, value string) {
	c.header.Set(key, value)
}

func (c *natsHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c.header))
	for k := range c.header {
		keys = append(keys, k)
	}
	return keys
}

// InjectTraceContext injects the span context from ctx into NATS message headers.
func InjectTraceContext(ctx context.Context, header nats.Header) {
	otel.GetTextMapPropagator().Inject(ctx, &natsHeaderCarrier{header: header})
}

// ExtractTraceContext extracts trace context from NATS message headers into a new context.
// Returns the original context if no traceparent header is present.
func ExtractTraceContext(ctx context.Context, header nats.Header) context.Context {
	if header == nil {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, &natsHeaderCarrier{header: header})
}
