// Package tracing provides distributed tracing infrastructure for Bifrost
package tracing

import (
	"strings"

	"github.com/valyala/fasthttp"
)

// normalizeTraceID normalizes a trace ID to W3C-compliant format.
// Strips hyphens and ensures 32 lowercase hex characters.
// Returns empty string if input cannot be normalized to a valid trace ID.
func normalizeTraceID(traceID string) string {
	// Remove hyphens (handles UUID format)
	normalized := strings.ReplaceAll(traceID, "-", "")
	normalized = strings.ToLower(normalized)

	// Validate length - must be exactly 32 hex chars
	if len(normalized) != 32 {
		return ""
	}

	// Validate hex characters
	if !isHex(normalized) {
		return ""
	}

	return normalized
}

// normalizeSpanID normalizes a span ID to W3C-compliant format.
// Strips hyphens and ensures 16 lowercase hex characters.
// If input is longer (e.g., UUID format), takes first 16 hex chars.
// Returns empty string if input cannot be normalized to a valid span ID.
func normalizeSpanID(spanID string) string {
	// Remove hyphens (handles UUID format)
	normalized := strings.ReplaceAll(spanID, "-", "")
	normalized = strings.ToLower(normalized)

	// If longer than 16 chars, truncate (e.g., full UUID -> first 16 hex chars)
	if len(normalized) > 16 {
		normalized = normalized[:16]
	}

	// Validate length - must be exactly 16 hex chars
	if len(normalized) != 16 {
		return ""
	}

	// Validate hex characters
	if !isHex(normalized) {
		return ""
	}

	return normalized
}

// W3C Trace Context header names
const (
	TraceParentHeader = "traceparent"
	TraceStateHeader  = "tracestate"
)

// W3CTraceContext holds parsed W3C trace context values
type W3CTraceContext struct {
	TraceID    string // 32 hex characters
	ParentID   string // 16 hex characters (span ID of parent)
	TraceFlags string // 2 hex characters
	TraceState string // Optional vendor-specific trace state
}

// ExtractParentID extracts the trace ID from W3C traceparent header.
// This returns the trace ID (32 hex chars) which should be used to continue
// the distributed trace from the upstream service.
// Returns empty string if header is not present or invalid.
func ExtractParentID(header *fasthttp.RequestHeader) string {
	traceParent := string(header.Peek(TraceParentHeader))
	if traceParent == "" {
		return ""
	}
	ctx := ParseTraceparent(traceParent)
	if ctx == nil {
		return ""
	}
	return ctx.TraceID
}

// ExtractTraceParentSpanID extracts the parent span ID from W3C traceparent header.
// This returns the span ID (16 hex chars) of the upstream service's span that
// initiated this request. This should be set as the ParentID of the root span
// in the receiving service to establish the parent-child relationship.
// Returns empty string if header is not present or invalid.
func ExtractTraceParentSpanID(header *fasthttp.RequestHeader) string {
	traceParent := string(header.Peek(TraceParentHeader))
	if traceParent == "" {
		return ""
	}
	ctx := ParseTraceparent(traceParent)
	if ctx == nil {
		return ""
	}
	return ctx.ParentID
}

// ExtractTraceContext extracts full W3C trace context from headers
func ExtractTraceContext(header *fasthttp.RequestHeader) *W3CTraceContext {
	traceparent := string(header.Peek(TraceParentHeader))
	if traceparent == "" {
		return nil
	}

	ctx := ParseTraceparent(traceparent)
	if ctx == nil {
		return nil
	}

	// Also extract tracestate if present
	ctx.TraceState = string(header.Peek(TraceStateHeader))

	return ctx
}

// ParseTraceparent parses a W3C traceparent header value
// Format: version-traceid-parentid-traceflags
// Example: 00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01
func ParseTraceparent(traceparent string) *W3CTraceContext {
	parts := strings.Split(traceparent, "-")
	if len(parts) != 4 {
		return nil
	}

	version := parts[0]
	traceID := parts[1]
	parentID := parts[2]
	traceFlags := parts[3]

	// Validate version (only 00 is currently supported)
	if version != "00" {
		return nil
	}

	// Validate trace ID (32 hex characters)
	if len(traceID) != 32 || !isHex(traceID) {
		return nil
	}

	// Validate parent ID (16 hex characters)
	if len(parentID) != 16 || !isHex(parentID) {
		return nil
	}

	// Validate trace flags (2 hex characters)
	if len(traceFlags) != 2 || !isHex(traceFlags) {
		return nil
	}

	return &W3CTraceContext{
		TraceID:    traceID,
		ParentID:   parentID,
		TraceFlags: traceFlags,
	}
}

// FormatTraceparent formats a W3C traceparent header value.
// It normalizes trace ID and span ID to W3C-compliant format:
// - trace ID: 32 lowercase hex characters
// - span ID: 16 lowercase hex characters
// Returns empty string if IDs cannot be normalized to valid format.
func FormatTraceparent(traceID, spanID, traceFlags string) string {
	normalizedTraceID := normalizeTraceID(traceID)
	normalizedSpanID := normalizeSpanID(spanID)

	if normalizedTraceID == "" || normalizedSpanID == "" {
		return ""
	}

	// Normalize and validate traceFlags
	traceFlags = strings.ToLower(traceFlags)
	if len(traceFlags) != 2 || !isHex(traceFlags) {
		traceFlags = "00" // Default: not sampled
	}

	return "00-" + normalizedTraceID + "-" + normalizedSpanID + "-" + traceFlags
}

// InjectTraceContext injects W3C trace context headers into outgoing request
func InjectTraceContext(header *fasthttp.RequestHeader, traceID, spanID, traceFlags, traceState string) {
	if traceID == "" || spanID == "" {
		return
	}

	traceparent := FormatTraceparent(traceID, spanID, traceFlags)
	if traceparent == "" {
		return // IDs could not be normalized to valid W3C format
	}
	header.Set(TraceParentHeader, traceparent)

	if traceState != "" {
		header.Set(TraceStateHeader, traceState)
	}
}

// isHex checks if a string contains only hexadecimal characters
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
