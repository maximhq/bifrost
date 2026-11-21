package otel

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// OtelClient is the interface for the OpenTelemetry client
type OtelClient interface {
	Emit(ctx context.Context, rs []*ResourceSpan) error
	Close() error
}

type traceExporter interface {
	emit(ctx context.Context, rs []*ResourceSpan) (traceExportResult, error)
	Close() error
}

type traceExportResult struct {
	partialSuccess *collectorpb.ExportTracePartialSuccess
	headers        map[string]string
}

const maxCollectorDiagnosticBytes = 256

func (r traceExportResult) diagnostic() string {
	message := r.partialSuccess.GetErrorMessage()
	type match struct{ start, end int }
	var matches []match
	addMatches := func(value string) {
		if value == "" {
			return
		}
		for offset := 0; offset < len(message); {
			index := strings.Index(message[offset:], value)
			if index < 0 {
				break
			}
			start := offset + index
			matches = append(matches, match{start, start + len(value)})
			offset = start + 1
		}
	}
	for name, value := range r.headers {
		addMatches(value)
		if strings.EqualFold(name, "Authorization") {
			if _, credential, ok := strings.Cut(value, " "); ok {
				addMatches(strings.TrimSpace(credential))
			}
		}
	}
	// Match the original diagnostic, then redact the union of overlapping values.
	// Replacing one header first can otherwise expose the suffix of another.
	if len(matches) > 0 {
		sort.Slice(matches, func(i, j int) bool { return matches[i].start < matches[j].start })
		var redacted strings.Builder
		previous := 0
		for i := 0; i < len(matches); {
			start, end := matches[i].start, matches[i].end
			i++
			for i < len(matches) && matches[i].start <= end {
				end = max(end, matches[i].end)
				i++
			}
			redacted.WriteString(message[previous:start])
			redacted.WriteString("[redacted]")
			previous = end
		}
		redacted.WriteString(message[previous:])
		message = redacted.String()
	}
	if len(message) <= maxCollectorDiagnosticBytes {
		return message
	}
	end := maxCollectorDiagnosticBytes
	for !utf8.RuneStart(message[end]) {
		end--
	}
	return message[:end] + "..."
}
