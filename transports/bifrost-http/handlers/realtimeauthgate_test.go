package handlers

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/grant"
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

	err := refuseUnauthenticatedRealtime(true, ctx, "")

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

	if err := refuseUnauthenticatedRealtime(false, ctx, ""); err != nil {
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

	if err := refuseUnauthenticatedRealtime(true, ctx, ""); err != nil {
		t.Fatalf("expected a virtual-key caller to be admitted, got %v", err)
	}
}

// TestRefuseUnauthenticatedRealtime_MappedEphemeralTokenAllowed proves the documented browser
// flow still works. Admission depends on the credential shape, while mapping resolution happens
// later in the transport.
func TestRefuseUnauthenticatedRealtime_MappedEphemeralTokenAllowed(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()

	if gateErr := refuseUnauthenticatedRealtime(true, ctx, "Bearer ek_test_token"); gateErr != nil {
		t.Fatalf("expected an ephemeral client secret to be admitted, got %v", gateErr)
	}
}

// TestRefuseUnauthenticatedRealtime_UnmappedBifrostPrefixedTokenAllowed keeps provider-issued
// credentials opaque. A provider may issue an ek_bf_ token too, so absence from Bifrost's mapping
// cannot establish that the credential is invalid. The provider makes that decision upstream.
func TestRefuseUnauthenticatedRealtime_UnmappedBifrostPrefixedTokenAllowed(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()

	if gateErr := refuseUnauthenticatedRealtime(true, ctx, "Bearer ek_bf_external_token"); gateErr != nil {
		t.Fatalf("expected an unmapped ephemeral token to be passed upstream, got %v", gateErr)
	}
}

// TestRefuseUnauthenticatedRealtime_NonEphemeralBearerRefused proves an arbitrary bearer token
// that is neither a virtual key nor an ephemeral secret does not satisfy the gate. Without this
// the check would degrade to "sent an Authorization header".
func TestRefuseUnauthenticatedRealtime_NonEphemeralBearerRefused(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()

	if gateErr := refuseUnauthenticatedRealtime(true, ctx, "Bearer sk-some-openai-key"); gateErr == nil {
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

	if gateErr := refuseUnauthenticatedRealtime(true, ctx, ""); gateErr != nil {
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

// usableTestPermit is the smallest active, unexpired permit, so a test can put resolved access on
// a grant the way governance's ResolveAccess does.
type usableTestPermit struct{}

func (usableTestPermit) Type() string                              { return "virtual_key" }
func (usableTestPermit) ID() string                                { return "vk-1" }
func (usableTestPermit) Name() string                              { return "test" }
func (usableTestPermit) IsActive() bool                            { return true }
func (usableTestPermit) IsExpired() bool                           { return false }
func (usableTestPermit) ProviderPermits() []schemas.ProviderPermit { return nil }
func (usableTestPermit) MCPPermits() []schemas.MCPPermit           { return nil }
func (usableTestPermit) AllowsAllProviders() bool                  { return true }

// newResolvedVirtualKeyContext is a context whose presented virtual key resolved to usable
// access, mirroring what the per-request pipeline leaves behind for a real key.
func newResolvedVirtualKeyContext(t *testing.T) (*schemas.BifrostContext, context.CancelFunc) {
	t.Helper()
	ctx, cancel := newRealtimeGateContext(t)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-valid")
	lib.RecordCredential(ctx, grant.NewCredential(grant.CredentialVirtualKey, "sk-bf-valid"))
	ctx.Grant().SetAccess(grant.NewAccess([]schemas.Permit{usableTestPermit{}}, nil, "", nil))
	return ctx, cancel
}

// TestRefuseUnresolvedRealtimeCredential_ForgedVirtualKeyRefused is the reported issue: a
// nonexistent sk-bf-* key passes the presence gate, and without this check the connection opens
// an upstream session on the operator's key before the first turn refuses it.
func TestRefuseUnresolvedRealtimeCredential_ForgedVirtualKeyRefused(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-forged")
	lib.RecordCredential(ctx, grant.NewCredential(grant.CredentialVirtualKey, "sk-bf-forged"))
	// No access recorded: this is what the pipeline leaves behind for a key the store cannot
	// resolve.

	err := refuseUnresolvedRealtimeCredential(true, ctx, "sk-bf-forged", false)

	if err == nil {
		t.Fatal("expected a virtual key that resolved to nothing to be refused")
	}
	if err.StatusCode == nil || *err.StatusCode != 401 {
		t.Errorf("expected status 401, got %v", err.StatusCode)
	}
}

// TestRefuseUnresolvedRealtimeCredential_ResolvedVirtualKeyAdmitted proves a key that resolved to
// usable access passes: the check refuses forged keys, never valid ones.
func TestRefuseUnresolvedRealtimeCredential_ResolvedVirtualKeyAdmitted(t *testing.T) {
	ctx, cancel := newResolvedVirtualKeyContext(t)
	defer cancel()

	if err := refuseUnresolvedRealtimeCredential(true, ctx, "sk-bf-valid", false); err != nil {
		t.Fatalf("expected a resolved virtual key to be admitted, got %v", err)
	}
}

// TestRefuseUnresolvedRealtimeCredential_EphemeralTokenExempt keeps ephemeral client secrets
// provider-validated. Bifrost may not have minted the token, so no local resolution can exist and
// its absence proves nothing.
func TestRefuseUnresolvedRealtimeCredential_EphemeralTokenExempt(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()

	if err := refuseUnresolvedRealtimeCredential(true, ctx, "ek_bf_external_token", false); err != nil {
		t.Fatalf("expected an ephemeral token to skip local resolution, got %v", err)
	}
}

// TestRefuseUnresolvedRealtimeCredential_MappedEphemeralTokenNotExempt closes the revocation
// window: a Bifrost-minted token's mapping restores its virtual key before the pipeline runs, so
// that key is locally resolvable and must still resolve. A mapped token whose key was revoked
// after minting is refused at admission rather than at the first turn.
func TestRefuseUnresolvedRealtimeCredential_MappedEphemeralTokenNotExempt(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-revoked")
	lib.RecordCredential(ctx, grant.NewCredential(grant.CredentialVirtualKey, "sk-bf-revoked"))
	// No access recorded: the pipeline could not resolve the mapping's virtual key.

	err := refuseUnresolvedRealtimeCredential(true, ctx, "ek_bf_mapped_token", true)

	if err == nil {
		t.Fatal("expected a mapped token with an unresolved virtual key to be refused")
	}
	if err.StatusCode == nil || *err.StatusCode != 401 {
		t.Errorf("expected status 401, got %v", err.StatusCode)
	}
}

// TestRefuseUnresolvedRealtimeCredential_ProviderTokenOnlyMappingExempt keeps a mapping that
// carries no virtual key (minted on an open deployment or with a direct provider key) on the
// unmapped contract: nothing locally resolvable was restored, so the provider stays authoritative
// and enforcement must not refuse a token Bifrost itself minted. Callers express this by passing
// mappedVirtualKey=false for such mappings.
func TestRefuseUnresolvedRealtimeCredential_ProviderTokenOnlyMappingExempt(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()
	// The ek_bf_ value itself settled as a credential-shaped header value; no access can exist.
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "ek_bf_provider_only_token")
	lib.RecordCredential(ctx, grant.NewCredential(grant.CredentialVirtualKey, "ek_bf_provider_only_token"))

	if err := refuseUnresolvedRealtimeCredential(true, ctx, "ek_bf_provider_only_token", false); err != nil {
		t.Fatalf("expected a provider-token-only mapped token to be admitted, got %v", err)
	}
}

// TestRefuseUnresolvedRealtimeCredential_DirectKeyAdmitted proves a direct provider key passes.
// It resolves to nothing by design, and refusing it for that would close direct-key realtime.
func TestRefuseUnresolvedRealtimeCredential_DirectKeyAdmitted(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyDirectKey, schemas.Key{Value: *schemas.NewSecretVar("sk-direct")})

	if err := refuseUnresolvedRealtimeCredential(true, ctx, "sk-direct", false); err != nil {
		t.Fatalf("expected a direct-key caller to be admitted, got %v", err)
	}
}

// TestRefuseUnresolvedRealtimeCredential_NotEnforcedAllowsEverything follows the operator's
// switch, like the presence gate: an open deployment stays exactly as open as the rest of
// inference.
func TestRefuseUnresolvedRealtimeCredential_NotEnforcedAllowsEverything(t *testing.T) {
	ctx, cancel := newRealtimeGateContext(t)
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-forged")
	lib.RecordCredential(ctx, grant.NewCredential(grant.CredentialVirtualKey, "sk-bf-forged"))

	if err := refuseUnresolvedRealtimeCredential(false, ctx, "sk-bf-forged", false); err != nil {
		t.Fatalf("expected an open deployment to admit an unresolved credential, got %v", err)
	}
}
