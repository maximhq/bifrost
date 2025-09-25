package otel

import (
	"context"
)

// OtelClient is the interface for the OpenTelemetry client
type OtelClient interface {
	Emit(ctx context.Context, rs []*ResourceSpan) error
	Close() error
}
