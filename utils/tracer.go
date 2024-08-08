package utils

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("bundler")

func GetTracer() trace.Tracer { return tracer }
