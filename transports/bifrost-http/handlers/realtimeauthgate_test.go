package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

func newRealtimeGateContext(t *testing.T) (*schemas.BifrostContext, context.CancelFunc) {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	return ctx, cancel
}

// TestRefuseUnauthenticatedRealtime_AnonymousRefusedWhenEnforced is the reported bypass: an
// operator has turned on enforce_auth_on_inference, and a client presenting nothing at all
// opens a realtime session on the operator's provider key.
func TestRefuseUnauthenticatedRealtime_AnonymousRefusedWhenEnforced(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()

	err := refuseUnauthenticatedRealtime(true, nil, ctx, "")

	if err == nil {
		t.Fatal("expected an anonymous realtime connection to be refused when auth is enforced")
	}
	if err.StatusCode == nil || *err.StatusCode != 401 {
		t.Errorf("expected status 401, got %v", err.StatusCode)
	}
}

// TestRefuseUnauthenticatedRealtime_AnonymousAllowedWhenNotEnforced proves the gate follows the
// operator's own switch. With enforce_auth_on_inference off, realtime must stay exactly as open
// as /v1/chat/completions on the same deployment - this fix must not make realtime stricter than
// the rest of inference.
func TestRefuseUnauthenticatedRealtime_AnonymousAllowedWhenNotEnforced(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()

	if err := refuseUnauthenticatedRealtime(false, nil, ctx, ""); err != nil {
		t.Fatalf("expected an anonymous connection to be allowed when auth is not enforced, got %v", err)
	}
}

// TestRefuseUnauthenticatedRealtime_VirtualKeyAllowed proves a virtual-key caller is admitted.
// The credential is recorded the same way createBifrostContextFromAuth records it at upgrade.
func TestRefuseUnauthenticatedRealtime_VirtualKeyAllowed(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-test")
	lib.RecordCredential(ctx, grant.NewCredential(grant.CredentialVirtualKey, "sk-bf-test"))

	if err := refuseUnauthenticatedRealtime(true, nil, ctx, ""); err != nil {
		t.Fatalf("expected a virtual-key caller to be admitted, got %v", err)
	}
}

// TestRefuseUnauthenticatedRealtime_ValidEphemeralTokenAllowed proves the documented browser
// flow still works: POST /v1/realtime/client_secrets mints an ek_ token, and a browser that
// cannot hold a virtual key connects with that instead. Refusing it would break the very flow
// the realtime design delegates auth to.
func TestRefuseUnauthenticatedRealtime_ValidEphemeralTokenAllowed(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()

	kv, err := kvstore.New(kvstore.Config{})
	if err != nil {
		t.Fatalf("failed to create kv store: %v", err)
	}
	token := "ek_test_token"
	payload, err := json.Marshal(realtimeEphemeralKeyMapping{KeyID: "key-1"})
	if err != nil {
		t.Fatalf("failed to marshal mapping: %v", err)
	}
	if err := kv.SetWithTTL(buildRealtimeEphemeralKeyMappingKey(token), payload, time.Minute); err != nil {
		t.Fatalf("failed to seed kv store: %v", err)
	}

	if gateErr := refuseUnauthenticatedRealtime(true, kv, ctx, "Bearer "+token); gateErr != nil {
		t.Fatalf("expected a valid ephemeral client secret to be admitted, got %v", gateErr)
	}
}

// TestRefuseUnauthenticatedRealtime_UnknownEphemeralTokenRefused proves an ek_ token that maps
// to nothing - expired, revoked, or forged - does not get in on the strength of its prefix.
func TestRefuseUnauthenticatedRealtime_UnknownEphemeralTokenRefused(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()

	kv, err := kvstore.New(kvstore.Config{})
	if err != nil {
		t.Fatalf("failed to create kv store: %v", err)
	}

	gateErr := refuseUnauthenticatedRealtime(true, kv, ctx, "Bearer ek_not_a_real_token")

	if gateErr == nil {
		t.Fatal("expected an unmapped ephemeral token to be refused")
	}
	if gateErr.StatusCode == nil || *gateErr.StatusCode != 401 {
		t.Errorf("expected status 401, got %v", gateErr.StatusCode)
	}
}

// TestRefuseUnauthenticatedRealtime_NonEphemeralBearerRefused proves an arbitrary bearer token
// that is neither a virtual key nor an ephemeral secret does not satisfy the gate. Without this
// the check would degrade to "sent an Authorization header".
func TestRefuseUnauthenticatedRealtime_NonEphemeralBearerRefused(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()

	if gateErr := refuseUnauthenticatedRealtime(true, nil, ctx, "Bearer sk-some-openai-key"); gateErr == nil {
		t.Fatal("expected a non-virtual-key, non-ephemeral bearer token to be refused")
	}
}

// TestRefuseUnauthenticatedRealtime_DirectKeyAllowed proves a direct provider key admits the
// connection. Governance's own first step counts it as authentication presented, and the gate
// must give the same answer rather than a stricter one of its own.
func TestRefuseUnauthenticatedRealtime_DirectKeyAllowed(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyDirectKey, schemas.Key{Value: *schemas.NewSecretVar("sk-direct")})

	if gateErr := refuseUnauthenticatedRealtime(true, nil, ctx, ""); gateErr != nil {
		t.Fatalf("expected a direct-key caller to be admitted, got %v", gateErr)
	}
}

// newRealtimeUpgradeRequest builds a well-formed WebSocket handshake for path, so that if the
// handler does reach the upgrader the upgrade succeeds and the response records 101. That is
// what lets the tests below tell "refused on the HTTP request" (401) apart from "upgraded and
// told in-band" (101), which is the whole distinction the admission gate exists to draw.
func newRealtimeUpgradeRequest(path string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI(path)
	ctx.Request.Header.SetMethod(fasthttp.MethodGet)
	ctx.Request.Header.Set("Connection", "Upgrade")
	ctx.Request.Header.Set("Upgrade", "websocket")
	ctx.Request.Header.Set("Sec-WebSocket-Version", "13")
	ctx.Request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	return ctx
}

func newRealtimeUpgradeHandler(enforceAuthOnInference bool) *WSRealtimeHandler {
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: enforceAuthOnInference}}
	return &WSRealtimeHandler{config: config, handlerStore: config}
}

// TestWSRealtimeHandleUpgrade_AnonymousRefusedBeforeTargetResolution pins the gate's position in
// the WebSocket handler, not just its verdict. With auth enforced, an anonymous caller must be
// refused with a plain 401 on the HTTP request regardless of what else is wrong with it. If the
// target is resolved first, a request with a missing or malformed model is upgraded and answered
// in-band with a 400 before the gate ever runs, which both hands an anonymous caller a completed
// upgrade and lets them probe which models and paths the deployment accepts.
func TestWSRealtimeHandleUpgrade_AnonymousRefusedBeforeTargetResolution(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := newRealtimeUpgradeRequest("/v1/realtime") // no model: target resolution fails

	newRealtimeUpgradeHandler(true).handleUpgrade(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusUnauthorized {
		t.Fatalf("expected an anonymous upgrade with an unresolvable target to be refused with 401 before any upgrade, got %d", got)
	}
}

// TestWSRealtimeHandleUpgrade_TargetErrorStaysInBandWhenNotEnforced is the control for the test
// above: with auth not enforced, an unresolvable target must still take the existing path of a
// completed upgrade and an in-band error frame, so the reorder narrows nothing for open
// deployments.
func TestWSRealtimeHandleUpgrade_TargetErrorStaysInBandWhenNotEnforced(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := newRealtimeUpgradeRequest("/v1/realtime")

	newRealtimeUpgradeHandler(false).handleUpgrade(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusSwitchingProtocols {
		t.Fatalf("expected an open deployment to upgrade and report the target error in-band (101), got %d", got)
	}
}
