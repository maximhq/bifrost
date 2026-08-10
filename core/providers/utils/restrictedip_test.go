package utils

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Regression tests for #5855: SSRF-guard dial rejections were classified as
// the generic "failed to execute HTTP request to provider API", hiding the
// blocked IP and the allow_private_network remedy from logs and API errors.

func TestConfigureDialer_RestrictedIPTagged(t *testing.T) {
	client := &fasthttp.Client{ReadTimeout: time.Second}
	ConfigureDialer(client, false)

	for _, addr := range []string{"10.0.0.1:80", "169.254.169.254:80", "0.0.0.0:80"} {
		_, err := client.Dial(addr)
		if err == nil {
			t.Fatalf("expected %s to be rejected", addr)
		}
		if !errors.Is(err, ErrRestrictedIPBlocked) {
			t.Errorf("rejection for %s not tagged with ErrRestrictedIPBlocked: %v", addr, err)
		}
	}
}

func TestMakeRequest_RestrictedIPGetsActionableMessage(t *testing.T) {
	client := &fasthttp.Client{ReadTimeout: 2 * time.Second}
	ConfigureDialer(client, false)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI("http://10.0.0.1:8000/v1/models")
	req.Header.SetMethod("GET")

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr, wait := MakeRequestWithContext(ctx, client, req, resp)
	wait()
	if bifrostErr == nil {
		t.Fatal("expected error dialing private IP")
	}
	if bifrostErr.Error.Message != schemas.ErrProviderRestrictedIPBlocked {
		t.Errorf("message = %q, want the restricted-IP guidance", bifrostErr.Error.Message)
	}
	if bifrostErr.Error.Error == nil || !strings.Contains(bifrostErr.Error.Error.Error(), "private IP 10.0.0.1") {
		t.Errorf("underlying error should name the blocked IP, got %v", bifrostErr.Error.Error)
	}
}

func TestFormatListModelsFailureIncludesUnderlyingError(t *testing.T) {
	tests := []struct {
		name  string
		field *schemas.ErrorField
		want  string
	}{
		{
			name: "message and underlying combined",
			field: &schemas.ErrorField{
				Message: "failed to execute HTTP request to provider API",
				Error:   fmt.Errorf("connection to private IP 172.18.0.5 is not allowed"),
			},
			want: "failed to execute HTTP request to provider API: connection to private IP 172.18.0.5 is not allowed",
		},
		{
			name:  "message only",
			field: &schemas.ErrorField{Message: "boom"},
			want:  "boom",
		},
		{
			name:  "underlying only",
			field: &schemas.ErrorField{Error: fmt.Errorf("dial refused")},
			want:  "dial refused",
		},
		{
			name: "identical message and underlying not doubled",
			field: &schemas.ErrorField{
				Message: "same text",
				Error:   fmt.Errorf("same text"),
			},
			want: "same text",
		},
		{name: "nil field", field: nil, want: "unknown error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatListModelsFailure(tt.field); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
