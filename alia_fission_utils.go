package alia_fission_utils

import (
	"context"
	"os"
	"strconv"
	"net/http"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.12.0"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
)

type Tracing struct {
	Tracer *trace.Tracer
	Logger *zap.Logger
	Ctx context.Context
}

const (
	OtelEnvPrefix      = "OTEL_"
	OtelEndpointEnvVar = "OTEL_EXPORTER_OTLP_ENDPOINT"
	OtelInsecureEnvVar = "OTEL_EXPORTER_OTLP_INSECURE"
	OtelPropagaters    = "OTEL_PROPAGATORS"
)

type OtelConfig struct {
	endpoint string
	insecure bool
}

func parseOtelConfig() OtelConfig {
	config := OtelConfig{}
	config.endpoint = os.Getenv(OtelEndpointEnvVar)
	insecure, err := strconv.ParseBool(os.Getenv(OtelInsecureEnvVar))
	if err != nil {
		insecure = true
	}
	config.insecure = insecure
	return config
}

func LoggerWithTraceID(context context.Context, logger *zap.Logger) *zap.Logger {
	if span := trace.SpanContextFromContext(context); span.TraceID().IsValid() {
		logger.Info("setting trace_id to logger")
		return logger.With(zap.String("trace_id", span.TraceID().String()))
	}
	logger.Info("invalid trace_id from span")
	return logger
}

func getTraceExporter(ctx context.Context, logger *zap.Logger) (*otlptrace.Exporter, error) {
	otelConfig := parseOtelConfig()
	if otelConfig.endpoint == "" {
		if logger != nil {
			logger.Info("OTEL_EXPORTER_OTLP_ENDPOINT not set, skipping Opentelemtry tracing")
		}
		return nil, nil
	}

	grpcOpts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(otelConfig.endpoint),
		otlptracegrpc.WithDialOption(grpc.WithBlock()),
	}
	if otelConfig.insecure {
		grpcOpts = append(grpcOpts, otlptracegrpc.WithInsecure())
	} else {
		grpcOpts = append(grpcOpts, otlptracegrpc.WithTLSCredentials(credentials.NewClientTLSFromCert(nil, "")))
	}

	exporter, err := otlptracegrpc.New(ctx, grpcOpts...)
	if err != nil {
		return nil, err
	}
	return exporter, nil
}

func NewTracing(r *http.Request) *Tracing {
	
	//traceparent := r.Header.Get("Traceparent")
	tracer := otel.Tracer("router")
	logger, _ := zap.NewProduction()

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	ctx := otel.GetTextMapPropagator().Extract(
		r.Context(), propagation.HeaderCarrier(r.Header),
	)

	logger = LoggerWithTraceID(ctx, logger)

	logger.Info("Try to intialize tracing")
	/*
		    _, err := otelUtils.InitProvider(ctx, Logger, "Fission-Function")
			if err != nil {
				Logger.Fatal("error initializing provider for OTLP", zap.Error(err))
			}
	*/

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String("inside_function"),
			attribute.String("environment", "dev"),
			attribute.Int64("ID", 5869),
		)),
	)
	otel.SetTracerProvider(tracerProvider)
	//otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	traceExporter, err := getTraceExporter(ctx, logger)
	if err != nil {
		logger.Error("error initializing provider for OTLP", zap.Error(err))
	}
	logger.Info("Tracing initialized")
	if traceExporter != nil {
		bsp := sdktrace.NewBatchSpanProcessor(traceExporter)
		tracerProvider.RegisterSpanProcessor(bsp)
	}
	t := &Tracing{
		Tracer: &tracer,
		Logger: logger,
		Ctx:	ctx,
	}
	return t
}
