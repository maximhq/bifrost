package utils

import (
	"context"
	"net/http"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TestSetExtraHeaders_SendsResolvedSecretValues checks that both setters put a header's
// resolved value on the wire (never its env.* reference), skip listed headers, and
// leave headers already on the request alone.
func TestSetExtraHeaders_SendsResolvedSecretValues(t *testing.T) {
	t.Setenv("BF_TEST_CLIENT_SECRET", "s3cr3t-from-env")
	headers := map[string]schemas.SecretVar{
		"x-client-secret": *schemas.NewSecretVar("env.BF_TEST_CLIENT_SECRET"),
		"X-User-Email":    *schemas.NewSecretVar("ops@example.com"),
		"X-Skipped":       *schemas.NewSecretVar("never"),
		"Authorization":   *schemas.NewSecretVar("Bearer from-config"),
	}
	skip := []string{"X-Skipped"}

	t.Run("fasthttp", func(t *testing.T) {
		req := fasthttp.AcquireRequest()
		defer fasthttp.ReleaseRequest(req)
		req.Header.Set("Authorization", "Bearer from-key")

		SetExtraHeaders(context.Background(), req, headers, skip)

		if got := string(req.Header.Peek("X-Client-Secret")); got != "s3cr3t-from-env" {
			t.Errorf("X-Client-Secret = %q", got)
		}
		if got := string(req.Header.Peek("X-User-Email")); got != "ops@example.com" {
			t.Errorf("X-User-Email = %q", got)
		}
		if got := req.Header.Peek("X-Skipped"); len(got) != 0 {
			t.Errorf("skipped header was sent: %q", got)
		}
		if got := string(req.Header.Peek("Authorization")); got != "Bearer from-key" {
			t.Errorf("existing header overwritten: %q", got)
		}
	})

	t.Run("net/http", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, "https://example.com", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer from-key")

		SetExtraHeadersHTTP(context.Background(), req, headers, skip)

		if got := req.Header.Get("X-Client-Secret"); got != "s3cr3t-from-env" {
			t.Errorf("X-Client-Secret = %q", got)
		}
		if got := req.Header.Get("X-User-Email"); got != "ops@example.com" {
			t.Errorf("X-User-Email = %q", got)
		}
		if got := req.Header.Get("X-Skipped"); got != "" {
			t.Errorf("skipped header was sent: %q", got)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer from-key" {
			t.Errorf("existing header overwritten: %q", got)
		}
	})
}
