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

const (
	maxCollectorDiagnosticBytes = 256
	redactedPlaceholder         = "[redacted]"
)

func (r traceExportResult) diagnostic() string {
	raw := r.partialSuccess.GetErrorMessage()
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

	// Bound the emit prefix so multi-megabyte collector messages cannot cause
	// O(headers * len(raw)) scans or allocations, while looking up to
	// len(secret)-1 bytes past `end` so any secret starting before `end` is
	// matched in full even when earlier replacements shrink the redacted prefix.
	scanLimit := maxCollectorDiagnosticBytes + (maxCollectorDiagnosticBytes/len(redactedPlaceholder)+1)*min(maxSecretLen, 4096)
	if cappedTotal := maxCollectorDiagnosticBytes + min(totalSecretBytes, 4096); cappedTotal > scanLimit {
		scanLimit = cappedTotal
	}
	end := len(raw)
	if end > scanLimit {
		end = scanLimit
		for end > 0 && !utf8.RuneStart(raw[end]) {
			end--
		}
	}

	type match struct{ start, end int }
	var matches []match
	addMatches := func(value string) {
		if end == 0 || len(value) == 0 {
			return
		}
		window := raw[:min(len(raw), end+len(value)-1)]
		for offset := 0; offset < end; {
			index := strings.Index(window[offset:], value)
			if index < 0 {
				break
			}
			start := offset + index
			if start >= end {
				break
			}
			matches = append(matches, match{start, start + len(value)})
			offset = start + 1
		}
	}
	for _, secret := range secrets {
		addMatches(secret)
	}
	// Match the original diagnostic, then redact the union of overlapping values.
	// Replacing one header first can otherwise expose the suffix of another.
	emittedRawEnd := end
	message := raw[:end]
	if len(matches) > 0 {
		sort.Slice(matches, func(i, j int) bool { return matches[i].start < matches[j].start })
		var redacted strings.Builder
		previous := 0
		for i := 0; i < len(matches); {
			start, matchEnd := matches[i].start, matches[i].end
			i++
			for i < len(matches) && matches[i].start <= matchEnd {
				matchEnd = max(matchEnd, matches[i].end)
				i++
			}
			if previous < start {
				redacted.WriteString(raw[previous:start])
			}
			redacted.WriteString(redactedPlaceholder)
			previous = matchEnd
		}
		if previous < end {
			redacted.WriteString(raw[previous:end])
		} else {
			emittedRawEnd = previous
		}
		message = redacted.String()
	}
	preTruncated := len(raw) > emittedRawEnd
	if len(message) <= maxCollectorDiagnosticBytes {
		if preTruncated {
			return message + "..."
		}
		return message
	}
	cut := maxCollectorDiagnosticBytes
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + "..."
}
