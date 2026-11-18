package obs

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Metrics are the engine's Prometheus metrics. You cannot benchmark or debug
// what you cannot see: slot lag says whether Postgres's disk is filling,
// end-to-end latency says how fresh views are.
type Metrics struct {
	Registry        *prometheus.Registry
	SlotLagBytes    prometheus.Gauge
	AppliedLSN      prometheus.Gauge
	EndToEnd        prometheus.Histogram
	ApplySeconds    prometheus.Histogram
	CommitSeconds   prometheus.Histogram
	Txns            prometheus.Counter
	Rows            prometheus.Counter
	Commits         prometheus.Counter
	OutputRows      *prometheus.CounterVec
	DeltaRows       prometheus.Histogram
	Subscribers     prometheus.Gauge
	SubscriberDrops prometheus.Counter
	QueueDepth      prometheus.Gauge
}

// NewMetrics registers every metric on a fresh registry. stateBytes is
// sampled on each scrape.
func NewMetrics(stateBytes func() float64) *Metrics {
	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	f := func(c prometheus.Collector) { r.MustRegister(c) }
	m := &Metrics{
		Registry: r,
		SlotLagBytes: prometheus.NewGauge(prometheus.GaugeOpts{Name: "deltaview_slot_lag_bytes",
			Help: "WAL bytes between pg_current_wal_lsn() and the slot's confirmed position."}),
		AppliedLSN: prometheus.NewGauge(prometheus.GaugeOpts{Name: "deltaview_applied_lsn",
			Help: "End LSN of the last durably committed transaction."}),
		EndToEnd: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "deltaview_end_to_end_seconds",
			Help:    "Postgres commit time to view commit time.",
			Buckets: prometheus.ExponentialBuckets(0.0005, 2, 16)}),
		ApplySeconds: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "deltaview_apply_seconds",
			Help: "Time to run one step through every circuit, before commit.", Buckets: prometheus.ExponentialBuckets(0.00005, 2, 18)}),
		CommitSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "deltaview_commit_seconds",
			Help: "Time to commit (and fsync) one state batch.", Buckets: prometheus.ExponentialBuckets(0.00005, 2, 18)}),
		Txns: prometheus.NewCounter(prometheus.CounterOpts{Name: "deltaview_transactions_applied_total",
			Help: "Postgres transactions applied."}),
		Rows: prometheus.NewCounter(prometheus.CounterOpts{Name: "deltaview_rows_applied_total",
			Help: "Row-level changes applied."}),
		Commits: prometheus.NewCounter(prometheus.CounterOpts{Name: "deltaview_state_commits_total",
			Help: "State batches committed (fewer than transactions when group commit is on)."}),
		OutputRows: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "deltaview_view_delta_rows_total",
			Help: "Rows in view output deltas."}, []string{"view"}),
		DeltaRows: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "deltaview_view_delta_size_rows",
			Help: "Size of each non-empty view output delta.", Buckets: prometheus.ExponentialBuckets(1, 4, 10)}),
		Subscribers: prometheus.NewGauge(prometheus.GaugeOpts{Name: "deltaview_subscribers",
			Help: "Open SSE subscriptions."}),
		SubscriberDrops: prometheus.NewCounter(prometheus.CounterOpts{Name: "deltaview_subscriber_drops_total",
			Help: "Subscribers dropped because their buffer overflowed."}),
		QueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{Name: "deltaview_queue_depth",
			Help: "Transactions waiting between the CDC reader and the apply loop."}),
	}
	for _, c := range []prometheus.Collector{m.SlotLagBytes, m.AppliedLSN, m.EndToEnd, m.ApplySeconds,
		m.CommitSeconds, m.Txns, m.Rows, m.Commits, m.OutputRows, m.DeltaRows, m.Subscribers,
		m.SubscriberDrops, m.QueueDepth} {
		f(c)
	}
	if stateBytes != nil {
		f(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "deltaview_state_bytes",
			Help: "Size of the state store."}, stateBytes))
	}
	return m
}

// Tracer creates OpenTelemetry spans around apply steps and operators.
// With no exporter configured the global provider is a no-op, so spans
// cost almost nothing.
type Tracer struct {
	t trace.Tracer
}

// NewTracer returns a tracer from the global provider.
func NewTracer() *Tracer { return &Tracer{t: otel.Tracer("deltaview")} }

// StartStep opens the span for one apply step.
func (t *Tracer) StartStep(ctx context.Context, txns int, endLSN uint64) (context.Context, trace.Span) {
	return t.t.Start(ctx, "apply", trace.WithAttributes(
		attribute.Int("deltaview.txns", txns), attribute.Int64("deltaview.end_lsn", int64(endLSN))))
}

// StartOp implements circuit.OpTracer.
func (t *Tracer) StartOp(ctx context.Context, view, op string) (context.Context, func(int)) {
	ctx, span := t.t.Start(ctx, op, trace.WithAttributes(attribute.String("deltaview.view", view)))
	return ctx, func(rows int) {
		span.SetAttributes(attribute.Int("deltaview.out_rows", rows))
		span.End()
	}
}

// SetupTracing installs a trace exporter: OTLP over HTTP when
// OTEL_EXPORTER_OTLP_ENDPOINT is set, stdout when DV_TRACE_STDOUT=1,
// otherwise none. The returned function flushes and stops it.
func SetupTracing(ctx context.Context) (func(context.Context) error, error) {
	var exp sdktrace.SpanExporter
	var err error
	switch {
	case os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "":
		exp, err = otlptracehttp.New(ctx)
	case os.Getenv("DV_TRACE_STDOUT") == "1":
		exp, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
	default:
		return func(context.Context) error { return nil }, nil
	}
	if err != nil {
		return nil, fmt.Errorf("obs: trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(time.Second)))
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}
