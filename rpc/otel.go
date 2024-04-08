package rpc

import (
	"context"
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/credentials"
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

func initResources(opts *options) *resource.Resource {
	if opts.ChainID == nil {
		log.Fatal("please provide a valid chain ID")
	}

	if opts.Address.Hex() == "" {
		log.Fatal("please provide a valid bundler address")
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
		log.Fatal(err)
	}

	return resources
}

func initTracer(opts *options) func() {
	secureOption := otlptracegrpc.WithTLSCredentials(credentials.NewClientTLSFromCert(nil, ""))
	if opts.InsecureMode {
		secureOption = otlptracegrpc.WithInsecure()
	}

	exporter, err := otlptrace.New(
		context.Background(),
		otlptracegrpc.NewClient(
			secureOption,
			otlptracegrpc.WithHeaders(opts.CollectorHeader),
			otlptracegrpc.WithEndpoint(opts.CollectorUrl),
		),
	)
	if err != nil {
		log.Fatal(err)
	}

	otel.SetTracerProvider(
		sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.AlwaysSample()),
			sdktrace.WithBatcher(exporter),
			sdktrace.WithResource(initResources(opts)),
		),
	)
	propagator := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	otel.SetTextMapPropagator(propagator)
	return func() {
		_ = exporter.Shutdown(context.Background())
	}
}

func initMetrics(opts *options) func() {
	secureOption := otlpmetricgrpc.WithTLSCredentials(credentials.NewClientTLSFromCert(nil, ""))
	if opts.InsecureMode {
		secureOption = otlpmetricgrpc.WithInsecure()
	}

	exporter, err := otlpmetricgrpc.New(
		context.Background(),
		secureOption,
		otlpmetricgrpc.WithHeaders(opts.CollectorHeader),
		otlpmetricgrpc.WithEndpoint(opts.CollectorUrl),
	)
	if err != nil {
		log.Fatal(err)
	}

	otel.SetMeterProvider(
		sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(initResources(opts)),
			sdkmetric.WithReader(
				sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(30*time.Second)),
			),
		),
	)
	return func() {
		_ = exporter.Shutdown(context.Background())
	}
}
