// Package telemetry provides shared OpenTelemetry initialization for all
// Sympozium components. It supports traces, metrics, and structured logs
// via OTLP gRPC export, with automatic noop fallback when no collector
// endpoint is configured.
//
// Usage:
//
//	tel, err := telemetry.Init(ctx, telemetry.Config{
//	    ServiceName:  "sympozium-agent-runner",
//	    BatchTimeout: 1 * time.Second,
//	})
//	if err != nil {
//	    log.Printf("otel init: %v", err)
//	}
//	defer tel.Shutdown(context.Background())
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const (
	defaultBatchTimeout   = 5 * time.Second
	defaultShutdownTimeout = 30 * time.Second
	defaultSamplingRatio  = 1.0
)

// Config controls how the OTel SDK is initialized for a component.
type Config struct {
	// ServiceName identifies this component (e.g., "sympozium-agent-runner").
	ServiceName string

	// ServiceVersion is the build version (e.g., "v0.0.49").
	ServiceVersion string

	// Namespace is the Kubernetes namespace. Read from NAMESPACE env if empty.
	Namespace string

	// BatchTimeout controls how often the batch exporter flushes.
	// Default: 5s. Agent-runner should set to 1s for ephemeral pods.
	BatchTimeout time.Duration

	// ShutdownTimeout is the maximum wait for flush on shutdown.
	// Default: 30s. Agent-runner should set to 10s.
	ShutdownTimeout time.Duration

	// SamplingRatio is the trace sampling probability (0.0 to 1.0).
	// Default: 1.0 (sample everything).
	SamplingRatio float64
}

// applyDefaults fills in zero-valued fields with defaults.
func (c *Config) applyDefaults() {
	if c.BatchTimeout == 0 {
		c.BatchTimeout = defaultBatchTimeout
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = defaultShutdownTimeout
	}
	if c.SamplingRatio == 0 {
		c.SamplingRatio = defaultSamplingRatio
	}
	if c.Namespace == "" {
		c.Namespace = os.Getenv("NAMESPACE")
	}
}

// Telemetry holds initialized OTel providers and offers typed accessors.
type Telemetry struct {
	tracerProvider  *sdktrace.TracerProvider
	meterProvider   *sdkmetric.MeterProvider
	loggerProvider  *sdklog.LoggerProvider
	tracer          trace.Tracer
	meter           metric.Meter
	logger          *slog.Logger
	enabled         bool
	shutdownTimeout time.Duration
}

// Init initializes the OTel SDK for a Sympozium component.
//
// If OTEL_EXPORTER_OTLP_ENDPOINT is not set, returns a Telemetry backed by
// noop providers with zero runtime overhead.
//
// Init registers the global TracerProvider, MeterProvider, and
// TextMapPropagator (W3C TraceContext).
func Init(ctx context.Context, cfg Config) (*Telemetry, error) {
	cfg.applyDefaults()

	// Always register W3C propagator so Extract/Inject works even in noop mode.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return initNoop(cfg), nil
	}

	res, err := buildResource(cfg)
	if err != nil {
		return nil, err
	}

	tp, err := initTracerProvider(ctx, cfg, res)
	if err != nil {
		return nil, err
	}

	mp, err := initMeterProvider(ctx, cfg, res)
	if err != nil {
		// Clean up already-created trace provider.
		_ = tp.Shutdown(ctx)
		return nil, err
	}

	lp, err := initLoggerProvider(ctx, cfg, res)
	if err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return nil, err
	}

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	// Create an slog handler that fans out to both stderr (for pod logs)
	// and the OTel log bridge (for OTLP export with trace correlation).
	stderrHandler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	otelHandler := otelslog.NewHandler(cfg.ServiceName,
		otelslog.WithLoggerProvider(lp),
	)
	logger := slog.New(&fanoutHandler{
		handlers: []slog.Handler{stderrHandler, otelHandler},
	})
	slog.SetDefault(logger)

	tel := &Telemetry{
		tracerProvider:  tp,
		meterProvider:   mp,
		loggerProvider:  lp,
		tracer:          tp.Tracer(cfg.ServiceName),
		meter:           mp.Meter(cfg.ServiceName),
		logger:          logger,
		enabled:         true,
		shutdownTimeout: cfg.ShutdownTimeout,
	}

	return tel, nil
}

// initNoop returns a Telemetry backed by noop providers.
func initNoop(cfg Config) *Telemetry {
	return &Telemetry{
		tracer:          tracenoop.NewTracerProvider().Tracer(cfg.ServiceName),
		meter:           metricnoop.NewMeterProvider().Meter(cfg.ServiceName),
		logger:          slog.Default(),
		enabled:         false,
		shutdownTimeout: cfg.ShutdownTimeout,
	}
}

// initTracerProvider creates a TracerProvider with OTLP gRPC exporter.
func initTracerProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	sampler := sdktrace.ParentBased(
		sdktrace.TraceIDRatioBased(cfg.SamplingRatio),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(cfg.BatchTimeout),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)

	return tp, nil
}

// initMeterProvider creates a MeterProvider with OTLP gRPC exporter.
func initMeterProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	exporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(exporter,
				sdkmetric.WithInterval(cfg.BatchTimeout),
			),
		),
		sdkmetric.WithResource(res),
	)

	return mp, nil
}

// initLoggerProvider creates a LoggerProvider with OTLP gRPC exporter.
// Log records sent through the OTel slog bridge automatically include
// trace_id and span_id from the context, enabling log→trace correlation.
func initLoggerProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdklog.LoggerProvider, error) {
	exporter, err := otlploggrpc.New(ctx, otlploggrpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(
			sdklog.NewBatchProcessor(exporter,
				sdklog.WithExportTimeout(cfg.BatchTimeout),
			),
		),
		sdklog.WithResource(res),
	)

	return lp, nil
}

// Shutdown flushes pending telemetry and shuts down all providers.
// It blocks up to ShutdownTimeout. Must be called before process exit.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	if !t.enabled {
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, t.shutdownTimeout)
	defer cancel()

	var errs []error

	if t.tracerProvider != nil {
		if err := t.tracerProvider.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, err)
		}
	}

	if t.meterProvider != nil {
		if err := t.meterProvider.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, err)
		}
	}

	if t.loggerProvider != nil {
		if err := t.loggerProvider.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// Tracer returns a named tracer for creating spans.
func (t *Telemetry) Tracer() trace.Tracer {
	return t.tracer
}

// Meter returns a named meter for creating metric instruments.
func (t *Telemetry) Meter() metric.Meter {
	return t.meter
}

// Logger returns a structured logger. When OTel is enabled, log records
// can be correlated with traces by passing a context containing a span.
func (t *Telemetry) Logger() *slog.Logger {
	return t.logger
}

// IsEnabled returns true if OTel export is configured (not noop mode).
func (t *Telemetry) IsEnabled() bool {
	return t.enabled
}

// TracerProvider returns the underlying SDK TracerProvider.
// Useful for tests that need to call ForceFlush.
func (t *Telemetry) TracerProvider() *sdktrace.TracerProvider {
	return t.tracerProvider
}

// MeterProvider returns the underlying SDK MeterProvider.
func (t *Telemetry) MeterProvider() *sdkmetric.MeterProvider {
	return t.meterProvider
}
