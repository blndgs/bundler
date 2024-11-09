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

	useropsCounter        metric.Int64Counter
	inflightUserOpCounter metric.Int64UpDownCounter
}

func New(meter metric.Meter) (*BundlerMetrics, error) {

	m := &BundlerMetrics{
		meter:  meter,
		ticker: time.NewTicker(time.Second * 10),
	}

	var err error

	m.useropsCounter, err = meter.Int64Counter("userops.counter",
		metric.WithDescription("Number of userops processed"))
	if err != nil {
		return nil, err
	}

	m.inflightUserOpCounter, err = meter.Int64UpDownCounter("userops.counter.in_flight")
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

// should only be used for automatic/runtime type of metrics that do not require any actions
// Like goroutines count and others
func (m *BundlerMetrics) collect(ctx context.Context) {}

func (m *BundlerMetrics) AddUserOpInFlight() { m.inflightUserOpCounter.Add(context.Background(), 1) }

func (m *BundlerMetrics) RemoveUserOpInFlight() {
	m.inflightUserOpCounter.Add(context.Background(), -1)
}

// ENUM(successful,pending,failed)
type UserOpCounterStatus string

func (m *BundlerMetrics) AddUserOp(status UserOpCounterStatus) {
	m.useropsCounter.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("status", status.String())))
}
