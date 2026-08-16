package utils

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
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
