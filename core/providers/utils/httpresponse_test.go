package utils

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestReadHTTPResponseBody_EnforcesKnownContentLength(t *testing.T) {
	resp := &http.Response{
		ContentLength: 5,
		Body:          io.NopCloser(strings.NewReader("12345")),
	}

	body, err := ReadHTTPResponseBody(resp, 4)
	if !errors.Is(err, ErrResponseBodyTooLarge) {
		t.Fatalf("error = %v, want ErrResponseBodyTooLarge", err)
	}
	if body != nil {
		t.Fatalf("body = %q, want nil", body)
	}
}

func TestReadHTTPResponseBody_EnforcesUnknownContentLength(t *testing.T) {
	resp := &http.Response{
		ContentLength: -1,
		Body:          io.NopCloser(strings.NewReader("12345")),
	}

	body, err := ReadHTTPResponseBody(resp, 4)
	if !errors.Is(err, ErrResponseBodyTooLarge) {
		t.Fatalf("error = %v, want ErrResponseBodyTooLarge", err)
	}
	if body != nil {
		t.Fatalf("body = %q, want nil", body)
	}
}

func TestReadHTTPResponseBody_AllowsExactLimitAndUnlimited(t *testing.T) {
	for _, tc := range []struct {
		name string
		max  int64
	}{
		{name: "exact", max: 5},
		{name: "unlimited", max: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{ContentLength: 5, Body: io.NopCloser(strings.NewReader("12345"))}
			body, err := ReadHTTPResponseBody(resp, tc.max)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if string(body) != "12345" {
				t.Fatalf("body = %q, want 12345", body)
			}
		})
	}
}

func TestNewBifrostOperationError_ResponseBodyTooLargeSetsBadGateway(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "net/http bounded reader", err: ErrResponseBodyTooLarge},
		{name: "fasthttp unary client", err: fasthttp.ErrBodyTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := NewBifrostOperationError("error reading response", tc.err)
			if err.StatusCode == nil || *err.StatusCode != http.StatusBadGateway {
				t.Fatalf("StatusCode = %v, want %d", err.StatusCode, http.StatusBadGateway)
			}
			if err.Error == nil || err.Error.Message != "provider response body exceeds configured maximum size" {
				t.Fatalf("error message = %#v, want response-size error", err.Error)
			}
		})
	}
}
