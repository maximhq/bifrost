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
	ackWarning     string
}

const maxCollectorDiagnosticBytes = 256

func (r traceExportResult) diagnostic() string {
	message := r.partialSuccess.GetErrorMessage()
	var secrets []string
	totalSecretBytes := 0
	maxSecretLen := 0
	recordSecret := func(v string) {
		if v == "" {
			return
		}
		secrets = append(secrets, v)
		totalSecretBytes += len(v)
		if len(v) > maxSecretLen {
			maxSecretLen = len(v)
		}
	}
	for name, value := range r.headers {
		recordSecret(value)
		if strings.EqualFold(name, "Authorization") {
			if _, credential, ok := strings.Cut(value, " "); ok {
				recordSecret(strings.TrimSpace(credential))
			}
		}
	}

	// Bound the scan window before matching so multi-megabyte collector messages
	// cannot cause O(headers * len(message)) work or allocations. Keeping
	// maxCollectorDiagnosticBytes plus bounded header-length headroom ensures
	// any secret straddling the 256-byte boundary is still matched and redacted.
	headroom := maxSecretLen
	if cappedTotal := min(totalSecretBytes, 4096); cappedTotal > headroom {
		headroom = cappedTotal
	}
	scanLimit := maxCollectorDiagnosticBytes + headroom
	preTruncated := false
	if len(message) > scanLimit {
		end := scanLimit
		for end > 0 && !utf8.RuneStart(message[end]) {
			end--
		}
		message = message[:end]
		preTruncated = true
	}

	type match struct{ start, end int }
	var matches []match
	addMatches := func(value string) {
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
	for _, secret := range secrets {
		addMatches(secret)
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
		if preTruncated {
			return message + "..."
		}
		return message
	}
	end := maxCollectorDiagnosticBytes
	for end > 0 && !utf8.RuneStart(message[end]) {
		end--
	}
	return message[:end] + "..."
}
