package utils

import "testing"

func TestPinAcceptEncodingForPassthroughStreamingKeepsOnlyDecodableCodings(t *testing.T) {
	// A Claude Code client advertises gzip, deflate, br, zstd. Only gzip can be
	// decoded incrementally, so that is all the upstream may be offered.
	headers := map[string]string{"accept-encoding": "gzip, deflate, br, zstd"}
	PinAcceptEncodingForPassthrough(headers, true)
	if got := headers["accept-encoding"]; got != "gzip" {
		t.Fatalf("accept-encoding = %q, want %q", got, "gzip")
	}
}

func TestPinAcceptEncodingForPassthroughBufferedKeepsTheWiderSet(t *testing.T) {
	headers := map[string]string{"accept-encoding": "br, zstd, exotic"}
	PinAcceptEncodingForPassthrough(headers, false)
	if got := headers["accept-encoding"]; got != "br, zstd" {
		t.Fatalf("accept-encoding = %q, want %q", got, "br, zstd")
	}
}

func TestPinAcceptEncodingForPassthroughPinsIdentityWhenNothingMatches(t *testing.T) {
	// Deleting the header would mean "any coding is acceptable" (RFC 9110 12.5.3),
	// which is wider than what the caller asked for and wider than what we can decode.
	headers := map[string]string{"accept-encoding": "br"}
	PinAcceptEncodingForPassthrough(headers, true)
	if got := headers["accept-encoding"]; got != "identity" {
		t.Fatalf("accept-encoding = %q, want identity", got)
	}
}

func TestPinAcceptEncodingForPassthroughIsCaseInsensitiveOnTheHeaderName(t *testing.T) {
	headers := map[string]string{"Accept-Encoding": "gzip, br"}
	PinAcceptEncodingForPassthrough(headers, true)
	if got := headers["Accept-Encoding"]; got != "gzip" {
		t.Fatalf("Accept-Encoding = %q, want gzip", got)
	}
}

func TestPinAcceptEncodingForPassthroughPreservesQValues(t *testing.T) {
	headers := map[string]string{"accept-encoding": "gzip;q=0.8, br;q=1.0"}
	PinAcceptEncodingForPassthrough(headers, true)
	if got := headers["accept-encoding"]; got != "gzip;q=0.8" {
		t.Fatalf("accept-encoding = %q, want %q", got, "gzip;q=0.8")
	}
}

func TestPinAcceptEncodingForPassthroughLeavesOtherHeadersAlone(t *testing.T) {
	headers := map[string]string{"user-agent": "claude-cli/2.0.1", "accept": "application/json"}
	PinAcceptEncodingForPassthrough(headers, true)
	if headers["user-agent"] != "claude-cli/2.0.1" || headers["accept"] != "application/json" {
		t.Fatalf("unrelated headers were modified: %v", headers)
	}
	PinAcceptEncodingForPassthrough(nil, true) // must not panic
}
