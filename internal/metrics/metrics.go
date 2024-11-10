package metrics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type BundlerMetrics struct {
	meter  metric.Meter
	ticker *time.Ticker

	useropsCounter            metric.Int64Counter
	inflightUserOpCounter     metric.Int64UpDownCounter
	useropsProcessingDuration metric.Float64Histogram

	ethMethodCalls    metric.Int64Counter
	ethMethodDuration metric.Float64Histogram
}

func New(meter metric.Meter) (*BundlerMetrics, error) {

	m := &BundlerMetrics{
		meter:  meter,
		ticker: time.NewTicker(time.Second * 10),
	}

	var err error

	m.useropsCounter, err = meter.Int64Counter("userops_total",
		metric.WithDescription("Number of userops processed"))
	if err != nil {
		return nil, err
	}

	m.inflightUserOpCounter, err = meter.Int64UpDownCounter("userops_in_flight",
		metric.WithDescription("Userops currently being processed"))
	if err != nil {
		return nil, err
	}

	m.useropsProcessingDuration, err = meter.Float64Histogram(
		"userops_duration_seconds",
		metric.WithDescription("Duration it takes to process userops"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	m.ethMethodCalls, err = meter.Int64Counter(
		"eth_method_calls_total",
		metric.WithDescription("Total number of Ethereum method calls"),
		metric.WithUnit("{calls}"),
	)
	if err != nil {
		return nil, err
	}

	m.ethMethodDuration, err = meter.Float64Histogram(
		"eth_method_duration_seconds",
		metric.WithDescription("Duration of Ethereum method calls"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	return m, nil
}

func (m *BundlerMetrics) Stop() { m.ticker.Stop() }

func (m *BundlerMetrics) Start() {
	go func() {
		for {
			select {
			case <-m.ticker.C:
				m.collect(context.Background())
			}
		}
	}()
}

func (m *BundlerMetrics) TrackETHMethodCall(ctx context.Context,
	method string, duration time.Duration) {

	attrs := []attribute.KeyValue{
		attribute.String("method", method),
		// Do we want to track this?
		// Might make sense for normal eth_calls and other supported std methods
		// but we can already see this detailed metrics from node providers
		// attribute.String("status", status),
	}

	m.ethMethodCalls.Add(ctx, 1,
		metric.WithAttributes(attrs...))

	m.ethMethodDuration.Record(ctx,
		duration.Seconds(),
		metric.WithAttributes(attrs...))
}

func (m *BundlerMetrics) AddUserOpInFlight() { m.inflightUserOpCounter.Add(context.Background(), 1) }

func (m *BundlerMetrics) RemoveUserOpInFlight() {
	m.inflightUserOpCounter.Add(context.Background(), -1)
}

// ENUM(successful,failed)
type UserOpCounterStatus string

func (m *BundlerMetrics) AddUserOp(status UserOpCounterStatus,
	duration time.Duration) {

	m.useropsCounter.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("status", status.String())))

	m.useropsProcessingDuration.Record(context.Background(), duration.Seconds())
}

// should only be used for automatic/runtime type of metrics that do not require any actions
// Like goroutines count and others
func (m *BundlerMetrics) collect(ctx context.Context) {}
