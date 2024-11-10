package rpc

import (
	"context"
	"errors"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/blndgs/bundler/internal/metrics"
	"github.com/ethereum/go-ethereum/common"
	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	metrictypes "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var (
	opCounter metrictypes.Int64Counter
)

type options struct {
	ServiceName     string
	CollectorHeader map[string]string
	CollectorUrl    string
	InsecureMode    bool

	// Bundler specific attributes
	ChainID *big.Int
	Address common.Address
}

func initResources(opts *options, logger logr.Logger) *resource.Resource {
	if opts.ChainID == nil {
		logger.Error(errors.New("please provide a valid chain ID"), "")
		os.Exit(1)
	}

	if opts.Address.Hex() == "" {
		logger.Error(errors.New("please provide a valid bundler address"), "")
		os.Exit(1)
	}

	resources, err := resource.New(
		context.Background(),
		resource.WithAttributes(
			attribute.String("service.name", opts.ServiceName),
			attribute.String("library.language", "go"),
			attribute.String("bundler.address", opts.Address.Hex()),
			attribute.Int64("bundler.chain_id", opts.ChainID.Int64()),
		),
	)
	if err != nil {
		logger.Error(err, "could not set up resources")
		os.Exit(1)
	}

	return resources
}

func initOTELCapabilities(cfg *options, logger logr.Logger) (*metrics.BundlerMetrics, func()) {

	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{}),
	)

	resources := initResources(cfg, logger)

	var (
		tracesSuffixEndpoint  = "/v1/traces"
		metricsSuffixEndpoint = "/v1/metrics"
	)

	// By default, Otel sends traces and metrics, logs to v1/* paths
	// but some providers like Grafana have their OTEL collector on a subpath
	// so /otlp/v1/*
	// The sdk is pretty stringent as that format does not match the standard
	// so it doesn't accept, this makes sure to split out the url and make it match
	splittedEndpoint := strings.Split(cfg.CollectorUrl, "/")

	if len(splittedEndpoint) == 2 {
		// pick out the host
		cfg.CollectorUrl = splittedEndpoint[0]

		// make sure to use the remaining path and prepend to the actual
		// standard /v1 paths
		tracesSuffixEndpoint = splittedEndpoint[1] + tracesSuffixEndpoint
		metricsSuffixEndpoint = splittedEndpoint[1] + metricsSuffixEndpoint
	}

	var traceOptions = []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(cfg.CollectorUrl),
		otlptracehttp.WithURLPath(tracesSuffixEndpoint),
		otlptracehttp.WithHeaders(cfg.CollectorHeader),
	}

	if cfg.InsecureMode {
		traceOptions = append(traceOptions, otlptracehttp.WithInsecure())
	}

	traceExporter, err := otlptrace.New(
		context.Background(),
		otlptracehttp.NewClient(traceOptions...))

	if err != nil {
		logger.Error(err, "could not setup OTEL tracing")
		os.Exit(1)
	}

	otel.SetTracerProvider(
		sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.AlwaysSample()),
			sdktrace.WithBatcher(traceExporter,
				sdktrace.WithMaxExportBatchSize(sdktrace.DefaultMaxExportBatchSize),
				sdktrace.WithBatchTimeout(5*time.Second),
			),
			sdktrace.WithResource(resources),
		),
	)

	var metricsOptions = []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(cfg.CollectorUrl),
		otlpmetrichttp.WithURLPath(metricsSuffixEndpoint),
		otlpmetrichttp.WithHeaders(cfg.CollectorHeader),
	}

	if cfg.InsecureMode {
		metricsOptions = append(metricsOptions, otlpmetrichttp.WithInsecure())
	}

	metricExporter, err := otlpmetrichttp.New(
		context.Background(), metricsOptions...)
	if err != nil {
		logger.Error(err, "could not setup OTEL metris")
		os.Exit(1)
	}

	meterProvider := metric.NewMeterProvider(
		metric.WithResource(resources),
		metric.WithReader(
			metric.NewPeriodicReader(metricExporter)))

	otel.SetMeterProvider(meterProvider)

	meter := meterProvider.Meter("balloondogs.meter")

	m, err := metrics.New(meter)
	if err != nil {
		logger.Error(err, "could not setup bundler metris")
		os.Exit(1)
	}

	return m, func() {
		m.Stop()

		_ = traceExporter.Shutdown(context.Background())
		_ = metricExporter.Shutdown(context.Background())
	}
}
