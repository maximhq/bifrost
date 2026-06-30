package handlers

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/valyala/fasthttp"
)

type loginDecodeConfigStore struct {
	configstore.ConfigStore
}

func TestSessionLoginInvalidPayloadDoesNotExposeDecoderDetails(t *testing.T) {
	h := &SessionHandler{configStore: &loginDecodeConfigStore{}}
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"username":1234,"password":"Suresh"}`)

	h.login(ctx)

	body := string(ctx.Response.Body())
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", ctx.Response.StatusCode(), fasthttp.StatusBadRequest, body)
	}
	if !strings.Contains(body, "Invalid request payload") {
		t.Fatalf("body = %s, want generic invalid payload message", body)
	}
	if strings.Contains(body, "cannot unmarshal") || strings.Contains(body, "username") || strings.Contains(body, "Go struct field") {
		t.Fatalf("body exposes decoder internals: %s", body)
	}
}

func TestPrepareRequestInvalidPayloadDoesNotExposeDecoderDetails(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"model":1234,"prompt":"hello"}`)

	_, _, err := prepareRequest[TextRequest](ctx, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid payload")
	}
	msg := err.Error()
	if msg != "invalid request payload" {
		t.Fatalf("error = %q, want generic invalid payload message", msg)
	}
	if strings.Contains(msg, "cannot unmarshal") || strings.Contains(msg, "model") || strings.Contains(msg, "Go struct field") {
		t.Fatalf("error exposes decoder internals: %s", msg)
	}
}
