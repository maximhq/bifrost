package otel

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	promb "go.opentelemetry.io/contrib/bridges/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// initOTELHTTPExporter creates an OTLP metrics exporter using HTTP transport.
func initOTELHTTPExporter(ctx context.Context, config *Config) (metric.Exporter, error) {
	opts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(config.MetricsEndpoint)}
	tlsConfig, err := createTLSConfig(config.MetricsTLSCACert, config.MetricsInsecure)
	if err != nil {
		return nil, err
	}
	opts = append(opts, otlpmetrichttp.WithTLSClientConfig(tlsConfig))
	if len(config.MetricsHeaders) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(config.MetricsHeaders))
	}
	opts = append(opts, otlpmetrichttp.WithTimeout(time.Duration(config.MetricsExporterTimeout)*time.Second))
	exporter, err := otlpmetrichttp.New(ctx, opts...)
	return exporter, err
}

// initOTELGRPCExporter creates an OTLP metrics exporter using gRPC transport.
func initOTELGRPCExporter(ctx context.Context, config *Config) (metric.Exporter, error) {
	opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(config.MetricsEndpoint)}
	tlsConfig, err := createTLSConfig(config.MetricsTLSCACert, config.MetricsInsecure)
	if err != nil {
		return nil, err
	}
	var creds credentials.TransportCredentials
	if tlsConfig.InsecureSkipVerify {
		creds = insecure.NewCredentials()
	} else {
		creds = credentials.NewTLS(tlsConfig)
	}
	opts = append(opts, otlpmetricgrpc.WithTLSCredentials(creds))
	if len(config.MetricsHeaders) > 0 {
		opts = append(opts, otlpmetricgrpc.WithHeaders(config.MetricsHeaders))
	}
	opts = append(opts, otlpmetricgrpc.WithTimeout(time.Duration(config.MetricsExporterTimeout)*time.Second))
	exporter, err := otlpmetricgrpc.New(ctx, opts...)
	return exporter, err
}

// initOTELMeterProvider create and set the background meter provider that periodically read
// from the input prometheus registry and submit metrics to a collector.
func initOTELMeterProvider(ctx context.Context, serviceName string, config *Config) (*metric.MeterProvider, error) {
	// Resolve env. prefixed header values from environment variables
	if config.MetricsHeaders != nil {
		if err := resolveEnvHeaders(config.MetricsHeaders); err != nil {
			return nil, err
		}
	}
	// The producer mirrors prometheus registry
	promtRegistryOpt := promb.WithGatherer(config.PrometheusRegistry)
	producer := promb.NewMetricProducer(promtRegistryOpt)
	var (
		exporter    metric.Exporter
		exporterErr error
	)
	// Supports both HTTP and GRPC
	switch config.MetricsProtocol {
	case ProtocolHTTP:
		exporter, exporterErr = initOTELHTTPExporter(ctx, config)
	case ProtocolGRPC:
		exporter, exporterErr = initOTELGRPCExporter(ctx, config)
	default:
		exporterErr = errors.New(fmt.Sprintf("invalid protocol '%s'", string(config.MetricsProtocol)))
	}
	if exporterErr != nil {
		return nil, errors.Wrap(exporterErr, "fail to init OTEL metrics exporter")
	}
	// Get metric values the prometheus registry every interval
	periodicReader := metric.NewPeriodicReader(
		exporter,
		metric.WithProducer(producer),
		metric.WithInterval(time.Duration(config.MetricsPushInterval)*time.Second),
	)
	defaultResources, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, errors.Wrap(err, "fail to create default resources")
	}
	staticResources, err := resource.New(
		ctx,
		resource.WithAttributes(mapToAttributes(config.MetricsExtraLabels)...),
		resource.WithHost(),
	)
	if err != nil {
		return nil, errors.Wrap(err, "fail to create static resources")
	}
	resources, err := resource.Merge(defaultResources, staticResources)
	if err != nil {
		return nil, errors.Wrap(err, "fail to create resources")
	}
	provider := metric.NewMeterProvider(
		metric.WithResource(resources),
		metric.WithReader(periodicReader),
	)
	// Background global provider
	otel.SetMeterProvider(provider)
	return provider, nil
}
