package alia_fission_utils

import (
	"context"
	"fmt"
	nats "github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.12.0"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"net/http"
	"os"
	"strconv"
)

type Tracing struct {
	Tracer      *trace.Tracer
	Logger      *zap.Logger
	Ctx         context.Context
	Traceparent string
}

type OtelConfig struct {
	endpoint string
	insecure bool
}

func parseOtelConfig() OtelConfig {
	config := OtelConfig{}
	config.endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	insecure, err := strconv.ParseBool(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE"))
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
			logger.Info("OTEL_EXPORTER_OTLP_ENDPOINT not set, skipping Opentelemtry tracing!")
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

func NewTracing(r *http.Request, serviceName string, attributes ...attribute.KeyValue) *Tracing {

	//traceparent := r.Header.Get("Traceparent")
	tracer := otel.Tracer("router")
	logger, _ := zap.NewProduction()

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	ctx := otel.GetTextMapPropagator().Extract(
		r.Context(), propagation.HeaderCarrier(r.Header),
	)

	logger = LoggerWithTraceID(ctx, logger)

	logger.Info("Try to intialize tracing")

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			append(attributes,
				semconv.ServiceNameKey.String(serviceName),
				attribute.String("pod_name", os.Getenv("POD_NAME")),
				attribute.String("node_name", os.Getenv("NODE_NAME")),
			)...,
		)),
	)
	otel.SetTracerProvider(tracerProvider)

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
		Tracer:      &tracer,
		Logger:      logger,
		Ctx:         ctx,
		Traceparent: r.Header.Get("Traceparent"),
	}
	return t
}

const (
	streamName     = "input"
	streamSubjects = "input.*"
	//subjectName           = "input.created"
	responseStreamName    = "output"
	responseStreamSubject = "output.response-topic"
	errorStreamName       = "erroutput"
	errorstreamSubjects   = "erroutput.error-topic"
)

type MessageQueue struct {
	js nats.JetStreamContext
	Tr *Tracing
}

func NewMQ(tr *Tracing) (*MessageQueue, error) {

	host := "nats://nats-jetstream.default.svc.cluster.local:4222"

	nc, err := nats.Connect(host)
	if err != nil {
		tr.Logger.Error(fmt.Sprintf("error connecting to host:  %v", err.Error()))
		return nil, err
	}
	// Creates JetStreamContext
	js, err := nc.JetStream()
	if err != nil {
		tr.Logger.Error(fmt.Sprintf("error getting context:  %v", err.Error()))
		return nil, err
	}

	mq := &MessageQueue{
		js: js,
		Tr: tr,
	}

	// Creates stream
	err = mq.createStream(streamName, streamSubjects)
	if err != nil {
		tr.Logger.Error(fmt.Sprintf("error create stream:  %v", err.Error()))
		return nil, err
	}

	// Creates stream
	err = mq.createStream(responseStreamName, responseStreamSubject)
	if err != nil {
		tr.Logger.Error(fmt.Sprintf("error create stream:  %v", err.Error()))
		return nil, err
	}

	// create output & err stream
	err = mq.createStream(errorStreamName, errorstreamSubjects)
	if err != nil {
		tr.Logger.Error(fmt.Sprintf("error create stream:  %v", err.Error()))
		return nil, err
	}
	return mq, nil
}

// createStream creates a stream by using JetStreamContext
func (mq *MessageQueue) createStream(streamName string, streamSubjects string) error {
	stream, err := mq.js.StreamInfo(streamName)
	if err != nil {
		mq.Tr.Logger.Error(fmt.Sprintf("error StreamInfo:  %v", err.Error()))
		err = nil
	}
	if stream == nil {
		mq.Tr.Logger.Info(fmt.Sprintf("creating stream %q and subjects %q", streamName, streamSubjects))
		_, err = mq.js.AddStream(&nats.StreamConfig{
			Name:     streamName,
			Subjects: []string{streamSubjects},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (mq *MessageQueue) Publish(subjectName string, data []byte) error {
	msg := nats.NewMsg(subjectName)
	msg.Data = data
	//msg.Header.Set("data", "true")
	msg.Header.Set("Traceparent", mq.Tr.Traceparent)
	_, err := mq.js.PublishMsg(msg)
	if err != nil {
		mq.Tr.Logger.Error(fmt.Sprintf("Error Publish: ", err))
		return err
	}
	return nil
}
