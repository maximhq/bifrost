package bifrost

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestNewBifrostCtxDoneErrorClassifiesDeadline(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(-time.Millisecond))
	ctx.SetValue(schemas.BifrostContextKeyConfiguredRequestTimeoutSeconds, 600)
	<-ctx.Done()
	time.Sleep(2 * time.Millisecond)

	bifrostErr := newBifrostCtxDoneError(ctx, "waiting for provider response")
	if bifrostErr.ExtraFields.TimeoutSource != schemas.TimeoutSourceBifrostContextDeadline {
		t.Fatalf("timeout source = %q", bifrostErr.ExtraFields.TimeoutSource)
	}
	if bifrostErr.ExtraFields.ConfiguredTimeoutSeconds != 600 {
		t.Fatalf("configured timeout = %d, want 600", bifrostErr.ExtraFields.ConfiguredTimeoutSeconds)
	}
	if bifrostErr.ExtraFields.UpstreamResponseReceived == nil || *bifrostErr.ExtraFields.UpstreamResponseReceived {
		t.Fatal("expected upstream_response_received=false")
	}
	if bifrostErr.ExtraFields.ElapsedMS <= 0 {
		t.Fatalf("elapsed_ms = %d, want a positive request duration", bifrostErr.ExtraFields.ElapsedMS)
	}
}

func TestSanitizeNetworkErrorForLogRedactsCredentials(t *testing.T) {
	cause := "Post https://user:password@example.com/v1?key=secret-key&model=x: proxy timeout authorization=Bearer top-secret"
	sanitized := safeNetworkErrorForLog(errors.New(cause))
	for _, secret := range []string{"password", "secret-key", "top-secret"} {
		if strings.Contains(sanitized, secret) {
			t.Fatalf("sanitized cause leaked %q: %s", secret, sanitized)
		}
	}
	if !strings.Contains(sanitized, "timeout") {
		t.Fatal("diagnostic cause should be preserved")
	}
}

func TestSafeNetworkErrorForLogNeverIncludesArbitraryTransportText(t *testing.T) {
	err := &url.Error{
		Op:  "secret-operation",
		URL: "https://user:password@example.com/private/token/v1?X-Amz-Credential=credential&X-Amz-Security-Token=session&X-Amz-Signature=signature",
		Err: errors.New("proxy timeout client_secret=client-secret password=plain-secret"),
	}

	safe := safeNetworkErrorForLog(err)
	for _, secret := range []string{"secret-operation", "password", "credential", "session", "signature", "client-secret", "plain-secret", "/private/token"} {
		if strings.Contains(strings.ToLower(safe), strings.ToLower(secret)) {
			t.Fatalf("safe cause leaked %q: %s", secret, safe)
		}
	}
	if !strings.Contains(strings.ToLower(safe), "timeout") {
		t.Fatalf("safe cause lost the diagnostic timeout category: %s", safe)
	}
}
