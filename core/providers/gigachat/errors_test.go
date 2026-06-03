package gigachat

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func testGigaChatErrors(t *testing.T) {
	t.Parallel()

	t.Run("ParsesCommonPayloads", testGigaChatErrorParsesCommonPayloads)
	t.Run("ParsesOAuthPayloads", testGigaChatErrorParsesOAuthPayloads)
	t.Run("UsesFallbackForNonJSON", testGigaChatErrorUsesFallbackForNonJSON)
	t.Run("RedactsRawPayloads", testGigaChatErrorRedactsRawPayloads)
	t.Run("RedactsExpandedRawAuthMaterial", testGigaChatErrorRedactsExpandedRawAuthMaterial)
	t.Run("RedactsExistingRawResponse", testGigaChatErrorRedactsExistingRawResponse)
	t.Run("RedactsStreamingCallbackRawResponse", testGigaChatErrorRedactsStreamingCallbackRawResponse)
	t.Run("PreservesSafeRawPayloadOrder", testGigaChatErrorPreservesSafeRawPayloadOrder)
	t.Run("RedactsTextPayloads", testGigaChatErrorRedactsTextPayloads)
}

func TestGigaChatErrors(t *testing.T) {
	testGigaChatErrors(t)
}

func testGigaChatErrorParsesCommonPayloads(t *testing.T) {
	t.Parallel()

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	resp.SetStatusCode(http.StatusTooManyRequests)
	resp.SetBodyString(`{"status":429,"code":7,"message":"quota exceeded"}`)

	bifrostErr := ParseGigaChatError(resp, schemas.GigaChat)
	if bifrostErr == nil {
		t.Fatal("expected error, got nil")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status mismatch: %#v", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "quota exceeded" {
		t.Fatalf("message mismatch: %#v", bifrostErr.Error)
	}
	if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "7" {
		t.Fatalf("code mismatch: %#v", bifrostErr.Error)
	}
	if bifrostErr.ExtraFields.Provider != schemas.GigaChat {
		t.Fatalf("provider mismatch: got %q", bifrostErr.ExtraFields.Provider)
	}
}

func testGigaChatErrorParsesOAuthPayloads(t *testing.T) {
	t.Parallel()

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	resp.SetStatusCode(http.StatusUnauthorized)
	resp.SetBodyString(`{"error":"invalid_client","error_description":"bad credentials","code":"AUTH_FAILED"}`)

	bifrostErr := ParseGigaChatError(resp, schemas.GigaChat)
	if bifrostErr == nil {
		t.Fatal("expected error, got nil")
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "bad credentials" {
		t.Fatalf("message mismatch: %#v", bifrostErr.Error)
	}
	if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "AUTH_FAILED" {
		t.Fatalf("code mismatch: %#v", bifrostErr.Error)
	}
}

func testGigaChatErrorUsesFallbackForNonJSON(t *testing.T) {
	t.Parallel()

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	resp.SetStatusCode(http.StatusBadGateway)
	resp.SetBodyString("upstream unavailable")

	bifrostErr := ParseGigaChatError(resp, schemas.GigaChat)
	if bifrostErr == nil {
		t.Fatal("expected error, got nil")
	}
	if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "provider API error") {
		t.Fatalf("message mismatch: %#v", bifrostErr.Error)
	}
}

func testGigaChatErrorRedactsRawPayloads(t *testing.T) {
	t.Parallel()

	ctx := testBifrostContext()
	requestBody := []byte(`{"model":"GigaChat","credentials":"super-secret-credentials","nested":{"password":"super-secret-password"}}`)
	responseBody := []byte(`{"message":"bad","access_token":"super-secret-token","authorization":"Bearer super-secret-token"}`)
	bifrostErr := newGigaChatProviderResponseError("failed", nil)

	enriched := enrichGigaChatError(ctx, bifrostErr, requestBody, responseBody, true, true)
	output := stringifyGigaChatRaw(enriched.ExtraFields.RawRequest) + stringifyGigaChatRaw(enriched.ExtraFields.RawResponse)
	for _, secret := range []string{"super-secret-credentials", "super-secret-password", "super-secret-token"} {
		if strings.Contains(output, secret) {
			t.Fatalf("raw payload leaked %q in %s", secret, output)
		}
	}
	if !strings.Contains(output, "redacted") {
		t.Fatalf("expected redacted marker in raw payloads, got %s", output)
	}
}

func testGigaChatErrorRedactsTextPayloads(t *testing.T) {
	t.Parallel()

	payload := []byte("error: authorization bearer super-secret-token failed; access_token=super-secret-access; user=super-secret-user; password=super-secret-password; key_file=/secure/client.key; Basic super-secret-basic rejected; -----BEGIN PRIVATE KEY-----\nsuper-secret-private-key\n-----END PRIVATE KEY-----")
	redacted := string(redactGigaChatRawPayload(payload))
	for _, secret := range []string{"super-secret-token", "super-secret-access", "super-secret-user", "super-secret-password", "/secure/client.key", "super-secret-basic", "super-secret-private-key"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("text payload leaked %q in %s", secret, redacted)
		}
	}
}

func testGigaChatErrorRedactsExpandedRawAuthMaterial(t *testing.T) {
	t.Parallel()

	ctx := testBifrostContext()
	requestBody := []byte(`{
		"model":"GigaChat",
		"authorization":"Basic super-secret-request-basic",
		"credentials":"super-secret-credentials",
		"user":"super-secret-user",
		"password":"super-secret-password",
		"key_file":"/secure/client.key",
		"cert_file":"/secure/client.crt",
		"ca_bundle_file":"/secure/ca.crt",
		"private_key":"-----BEGIN PRIVATE KEY-----\nsuper-secret-private-key\n-----END PRIVATE KEY-----",
		"messages":[{"role":"user","content":"safe prompt"}]
	}`)
	responseBody := []byte(`{
		"message":"bad",
		"authorization":"Bearer super-secret-response-bearer",
		"access_token":"super-secret-access-token",
		"client_secret":"super-secret-client-secret",
		"refresh_token":"super-secret-refresh-token",
		"errors":["Basic super-secret-array-basic"]
	}`)
	bifrostErr := newGigaChatProviderResponseError("authorization Bearer super-secret-error-bearer failed with password=super-secret-error-password and -----BEGIN PRIVATE KEY-----\nsuper-secret-error-private-key\n-----END PRIVATE KEY-----", nil)

	enriched := enrichGigaChatError(ctx, bifrostErr, requestBody, responseBody, true, true)
	requestOutput := stringifyGigaChatRaw(enriched.ExtraFields.RawRequest)
	responseOutput := stringifyGigaChatRaw(enriched.ExtraFields.RawResponse)
	errorOutput := enriched.String()

	assertGigaChatOutputOmits(t, "raw request", requestOutput, []string{
		"super-secret-request-basic",
		"super-secret-credentials",
		"super-secret-user",
		"super-secret-password",
		"/secure/client.key",
		"/secure/client.crt",
		"/secure/ca.crt",
		"super-secret-private-key",
	})
	assertGigaChatOutputOmits(t, "raw response", responseOutput, []string{
		"super-secret-response-bearer",
		"super-secret-access-token",
		"super-secret-client-secret",
		"super-secret-refresh-token",
		"super-secret-array-basic",
	})
	assertGigaChatOutputOmits(t, "error message", errorOutput, []string{
		"super-secret-error-bearer",
		"super-secret-error-password",
		"super-secret-error-private-key",
	})
	if !strings.Contains(requestOutput+responseOutput+errorOutput, "redacted") {
		t.Fatalf("expected redacted markers, got request=%s response=%s error=%s", requestOutput, responseOutput, errorOutput)
	}
}

func testGigaChatErrorRedactsExistingRawResponse(t *testing.T) {
	t.Parallel()

	ctx := testBifrostContext()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	resp.SetStatusCode(http.StatusBadRequest)
	resp.SetBodyString(`{"status":400,"message":"authorization bearer super-secret-token","access_token":"super-secret-token"}`)

	bifrostErr := ParseGigaChatError(resp, schemas.GigaChat)
	enriched := enrichGigaChatError(ctx, bifrostErr, nil, nil, false, true)
	output := enriched.String() + stringifyGigaChatRaw(enriched.ExtraFields.RawResponse)
	if strings.Contains(output, "super-secret-token") {
		t.Fatalf("existing raw response leaked secret in %s", output)
	}
	if !strings.Contains(output, "redacted") {
		t.Fatalf("expected redacted marker in existing raw response, got %s", output)
	}
}

func testGigaChatErrorRedactsStreamingCallbackRawResponse(t *testing.T) {
	t.Parallel()

	handler := handleGigaChatChatStreamResponse(schemas.GigaChat)
	var response schemas.BifrostChatResponse
	_, rawResponse, bifrostErr := handler(
		[]byte(`{"status":401,"message":"bearer super-secret-token","access_token":"super-secret-token"}`),
		&response,
		[]byte(`{"model":"GigaChat"}`),
		true,
		true,
	)
	if bifrostErr == nil {
		t.Fatal("expected streaming error, got nil")
	}

	output := bifrostErr.String() + stringifyGigaChatRaw(rawResponse)
	if strings.Contains(output, "super-secret-token") {
		t.Fatalf("streaming raw response leaked secret in %s", output)
	}
}

func testGigaChatErrorPreservesSafeRawPayloadOrder(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"z":1,"a":2,"message":"safe"}`)
	redacted := redactGigaChatRawPayload(payload)
	if string(redacted) != string(payload) {
		t.Fatalf("safe payload order changed: got %s, want %s", redacted, payload)
	}
}

func stringifyGigaChatRaw(raw interface{}) string {
	if raw == nil {
		return ""
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	return string(data)
}

func assertGigaChatOutputOmits(t *testing.T, label string, output string, secrets []string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(output, secret) {
			t.Fatalf("%s leaked %q in %s", label, secret, output)
		}
	}
}
