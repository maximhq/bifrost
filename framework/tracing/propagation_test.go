package tracing

import (
	"testing"

	"github.com/valyala/fasthttp"
)

func TestParseTraceparent_ValidHeader(t *testing.T) {
	// Example from W3C spec and the user's actual Datadog headers
	tests := []struct {
		name        string
		traceparent string
		wantTraceID string
		wantParent  string
		wantFlags   string
	}{
		{
			name:        "valid traceparent from Datadog",
			traceparent: "00-69538b980000000079943934f90c1d40-aad09d1659b4c7e3-01",
			wantTraceID: "69538b980000000079943934f90c1d40",
			wantParent:  "aad09d1659b4c7e3",
			wantFlags:   "01",
		},
		{
			name:        "valid traceparent with sampled flag",
			traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
			wantTraceID: "0af7651916cd43dd8448eb211c80319c",
			wantParent:  "b7ad6b7169203331",
			wantFlags:   "01",
		},
		{
			name:        "valid traceparent not sampled",
			traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00",
			wantTraceID: "0af7651916cd43dd8448eb211c80319c",
			wantParent:  "b7ad6b7169203331",
			wantFlags:   "00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ParseTraceparent(tt.traceparent)
			if ctx == nil {
				t.Fatalf("ParseTraceparent() returned nil for valid header")
			}
			if ctx.TraceID != tt.wantTraceID {
				t.Errorf("TraceID = %q, want %q", ctx.TraceID, tt.wantTraceID)
			}
			if ctx.ParentID != tt.wantParent {
				t.Errorf("ParentID = %q, want %q", ctx.ParentID, tt.wantParent)
			}
			if ctx.TraceFlags != tt.wantFlags {
				t.Errorf("TraceFlags = %q, want %q", ctx.TraceFlags, tt.wantFlags)
			}
		})
	}
}

func TestParseTraceparent_InvalidVersion(t *testing.T) {
	// Only version 00 is supported
	tests := []struct {
		name        string
		traceparent string
	}{
		{
			name:        "version 01",
			traceparent: "01-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		},
		{
			name:        "version ff",
			traceparent: "ff-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ParseTraceparent(tt.traceparent)
			if ctx != nil {
				t.Errorf("ParseTraceparent() should return nil for unsupported version")
			}
		})
	}
}

func TestParseTraceparent_InvalidTraceID(t *testing.T) {
	tests := []struct {
		name        string
		traceparent string
	}{
		{
			name:        "trace ID too short",
			traceparent: "00-0af7651916cd43dd8448eb211c8031-b7ad6b7169203331-01",
		},
		{
			name:        "trace ID too long",
			traceparent: "00-0af7651916cd43dd8448eb211c80319c00-b7ad6b7169203331-01",
		},
		{
			name:        "trace ID with invalid chars",
			traceparent: "00-0af7651916cd43dd8448eb211c80319z-b7ad6b7169203331-01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ParseTraceparent(tt.traceparent)
			if ctx != nil {
				t.Errorf("ParseTraceparent() should return nil for invalid trace ID")
			}
		})
	}
}

func TestParseTraceparent_InvalidParentID(t *testing.T) {
	tests := []struct {
		name        string
		traceparent string
	}{
		{
			name:        "parent ID too short",
			traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b71692033-01",
		},
		{
			name:        "parent ID too long",
			traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b716920333100-01",
		},
		{
			name:        "parent ID with invalid chars",
			traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b716920333z-01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ParseTraceparent(tt.traceparent)
			if ctx != nil {
				t.Errorf("ParseTraceparent() should return nil for invalid parent ID")
			}
		})
	}
}

func TestParseTraceparent_MalformedHeader(t *testing.T) {
	tests := []struct {
		name        string
		traceparent string
	}{
		{
			name:        "empty string",
			traceparent: "",
		},
		{
			name:        "missing parts",
			traceparent: "00-0af7651916cd43dd8448eb211c80319c",
		},
		{
			name:        "too many parts",
			traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-extra",
		},
		{
			name:        "wrong delimiter",
			traceparent: "00_0af7651916cd43dd8448eb211c80319c_b7ad6b7169203331_01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ParseTraceparent(tt.traceparent)
			if ctx != nil {
				t.Errorf("ParseTraceparent() should return nil for malformed header")
			}
		})
	}
}

func TestExtractParentID_ReturnsTraceID(t *testing.T) {
	header := &fasthttp.RequestHeader{}
	header.Set(TraceParentHeader, "00-69538b980000000079943934f90c1d40-aad09d1659b4c7e3-01")

	traceID := ExtractParentID(header)
	if traceID != "69538b980000000079943934f90c1d40" {
		t.Errorf("ExtractParentID() = %q, want %q", traceID, "69538b980000000079943934f90c1d40")
	}
}

func TestExtractParentID_EmptyHeader(t *testing.T) {
	header := &fasthttp.RequestHeader{}

	traceID := ExtractParentID(header)
	if traceID != "" {
		t.Errorf("ExtractParentID() = %q, want empty string", traceID)
	}
}

func TestExtractTraceParentSpanID_ReturnsParentSpanID(t *testing.T) {
	header := &fasthttp.RequestHeader{}
	header.Set(TraceParentHeader, "00-69538b980000000079943934f90c1d40-aad09d1659b4c7e3-01")

	parentSpanID := ExtractTraceParentSpanID(header)
	if parentSpanID != "aad09d1659b4c7e3" {
		t.Errorf("ExtractTraceParentSpanID() = %q, want %q", parentSpanID, "aad09d1659b4c7e3")
	}
}

func TestExtractTraceParentSpanID_EmptyHeader(t *testing.T) {
	header := &fasthttp.RequestHeader{}

	parentSpanID := ExtractTraceParentSpanID(header)
	if parentSpanID != "" {
		t.Errorf("ExtractTraceParentSpanID() = %q, want empty string", parentSpanID)
	}
}

func TestExtractTraceParentSpanID_InvalidHeader(t *testing.T) {
	header := &fasthttp.RequestHeader{}
	header.Set(TraceParentHeader, "invalid-header")

	parentSpanID := ExtractTraceParentSpanID(header)
	if parentSpanID != "" {
		t.Errorf("ExtractTraceParentSpanID() = %q, want empty string for invalid header", parentSpanID)
	}
}

func TestFormatTraceparent_NormalizesIDs(t *testing.T) {
	tests := []struct {
		name       string
		traceID    string
		spanID     string
		traceFlags string
		want       string
	}{
		{
			name:       "already normalized",
			traceID:    "69538b980000000079943934f90c1d40",
			spanID:     "aad09d1659b4c7e3",
			traceFlags: "01",
			want:       "00-69538b980000000079943934f90c1d40-aad09d1659b4c7e3-01",
		},
		{
			name:       "uppercase to lowercase",
			traceID:    "69538B980000000079943934F90C1D40",
			spanID:     "AAD09D1659B4C7E3",
			traceFlags: "01",
			want:       "00-69538b980000000079943934f90c1d40-aad09d1659b4c7e3-01",
		},
		{
			name:       "UUID format trace ID",
			traceID:    "69538b98-0000-0000-7994-3934f90c1d40",
			spanID:     "aad09d1659b4c7e3",
			traceFlags: "01",
			want:       "00-69538b980000000079943934f90c1d40-aad09d1659b4c7e3-01",
		},
		{
			name:       "default trace flags when invalid",
			traceID:    "69538b980000000079943934f90c1d40",
			spanID:     "aad09d1659b4c7e3",
			traceFlags: "xyz",
			want:       "00-69538b980000000079943934f90c1d40-aad09d1659b4c7e3-00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatTraceparent(tt.traceID, tt.spanID, tt.traceFlags)
			if got != tt.want {
				t.Errorf("FormatTraceparent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatTraceparent_InvalidIDs(t *testing.T) {
	tests := []struct {
		name    string
		traceID string
		spanID  string
	}{
		{
			name:    "empty trace ID",
			traceID: "",
			spanID:  "aad09d1659b4c7e3",
		},
		{
			name:    "empty span ID",
			traceID: "69538b980000000079943934f90c1d40",
			spanID:  "",
		},
		{
			name:    "invalid trace ID length",
			traceID: "69538b98",
			spanID:  "aad09d1659b4c7e3",
		},
		{
			name:    "invalid span ID length",
			traceID: "69538b980000000079943934f90c1d40",
			spanID:  "aad09d16",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatTraceparent(tt.traceID, tt.spanID, "01")
			if got != "" {
				t.Errorf("FormatTraceparent() = %q, want empty string for invalid IDs", got)
			}
		})
	}
}

func TestExtractTraceContext_WithTraceState(t *testing.T) {
	header := &fasthttp.RequestHeader{}
	header.Set(TraceParentHeader, "00-69538b980000000079943934f90c1d40-aad09d1659b4c7e3-01")
	header.Set(TraceStateHeader, "dd=p:aad09d1659b4c7e3;s:1;t.dm:-1;t.tid:69538b9800000000")

	ctx := ExtractTraceContext(header)
	if ctx == nil {
		t.Fatal("ExtractTraceContext() returned nil")
	}
	if ctx.TraceID != "69538b980000000079943934f90c1d40" {
		t.Errorf("TraceID = %q, want %q", ctx.TraceID, "69538b980000000079943934f90c1d40")
	}
	if ctx.ParentID != "aad09d1659b4c7e3" {
		t.Errorf("ParentID = %q, want %q", ctx.ParentID, "aad09d1659b4c7e3")
	}
	if ctx.TraceState != "dd=p:aad09d1659b4c7e3;s:1;t.dm:-1;t.tid:69538b9800000000" {
		t.Errorf("TraceState = %q, want Datadog tracestate", ctx.TraceState)
	}
}

func TestInjectTraceContext(t *testing.T) {
	header := &fasthttp.RequestHeader{}

	InjectTraceContext(header, "69538b980000000079943934f90c1d40", "aad09d1659b4c7e3", "01", "dd=s:1")

	traceparent := string(header.Peek(TraceParentHeader))
	if traceparent != "00-69538b980000000079943934f90c1d40-aad09d1659b4c7e3-01" {
		t.Errorf("traceparent = %q, want formatted header", traceparent)
	}

	tracestate := string(header.Peek(TraceStateHeader))
	if tracestate != "dd=s:1" {
		t.Errorf("tracestate = %q, want %q", tracestate, "dd=s:1")
	}
}

func TestInjectTraceContext_EmptyIDs(t *testing.T) {
	header := &fasthttp.RequestHeader{}

	InjectTraceContext(header, "", "aad09d1659b4c7e3", "01", "")

	traceparent := string(header.Peek(TraceParentHeader))
	if traceparent != "" {
		t.Errorf("traceparent should not be set for empty trace ID")
	}
}
