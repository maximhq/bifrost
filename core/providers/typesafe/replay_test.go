package typesafe

import (
	"context"
	"net/http"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestDecisionIdempotencyKeyIsScopedPerVirtualKey pins that the forwarded key
// is a deterministic hash of virtual key ID and client key, so tenants sharing
// an upstream credential never collide while retries still match.
func TestDecisionIdempotencyKeyIsScopedPerVirtualKey(t *testing.T) {
	send := func(vk, key string) string {
		var got string
		_, provider := newTypesafeFixture(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			got = r.Header.Get("Idempotency-Key")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fixtureSuccessBody))
		})
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		if vk != "" {
			ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, vk)
		}
		if key != "" {
			ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{"idempotency-key": {key}})
		}
		if _, err := provider.Decision(ctx, fixtureKey(), decisionRequest(t, "fixture", map[string]TypesafeQuestion{
			"q": {Type: TypesafeQuestionTypeNoul, Instructions: "Evaluate this state."},
		})); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return got
	}
	a1, a2, b := send("vk-a", "same"), send("vk-a", "same"), send("vk-b", "same")
	if a1 == "" || a1 == "same" || a1 != schemas.DecisionIdempotencyScope("vk-a", "jev-1.13.0", "same") {
		t.Fatalf("key not scoped: %q", a1)
	}
	if a1 != a2 {
		t.Fatalf("retry must keep the scoped key: %q vs %q", a1, a2)
	}
	if a1 == b {
		t.Fatalf("tenants collide on %q", a1)
	}
	if got := send("vk-a", ""); got != "" {
		t.Fatalf("no client key must send none, got %q", got)
	}
}

// TestDecisionForwardsRawBodyVerbatim pins that, with the raw-body flag set,
// the client's exact bytes (key order, whitespace, precise numbers) reach the
// wire, and that a request without raw bytes is rebuilt from the parsed form.
func TestDecisionForwardsRawBodyVerbatim(t *testing.T) {
	raw := `{ "model":"jev-1.13.0","state":{"n":12345678901234567890.123456789,"big":9007199254740993},"questions":{"q":{"type":"noul","instructions":"x"}} }`
	for _, tc := range []struct {
		name    string
		raw     string
		wantRaw bool
	}{
		{name: "raw body", raw: raw, wantRaw: true},
		{name: "flag without bytes", raw: "", wantRaw: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			_, provider := newTypesafeFixture(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
				got = string(body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(fixtureSuccessBody))
			})
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
			request := decisionRequest(t, "fixture", map[string]TypesafeQuestion{
				"q": {Type: TypesafeQuestionTypeNoul, Instructions: "Evaluate this state."},
			})
			request.RawRequestBody = []byte(tc.raw)
			if _, err := provider.Decision(ctx, fixtureKey(), request); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantRaw && got != raw {
				t.Fatalf("body altered:\n got %s\nwant %s", got, raw)
			}
			if !tc.wantRaw && got == "" {
				t.Fatal("fallback body must be rebuilt, got empty")
			}
		})
	}
}

// TestDecisionRawBodyCarriesRoutedModel pins that only the top-level model
// value changes when governance routed to another model, that a prefixed model
// is replaced, and that ambiguous model keys are rejected.
func TestDecisionRawBodyCarriesRoutedModel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{name: "routed differs", body: `{ "state":{"n":1.10,"model":"inner"}, "model" : "alias" ,"q":1e400}`, want: `{ "state":{"n":1.10,"model":"inner"}, "model" : "fixture" ,"q":1e400}`},
		{name: "prefix stripped", body: `{"model":"typesafe/fixture","state":"s"}`, want: `{"model":"fixture","state":"s"}`},
		{name: "no model key", body: `{"state":"s"}`, want: `{"state":"s"}`},
		{name: "duplicate", body: `{"model":"a","model":"b"}`, wantErr: true},
		{name: "case variant", body: `{"model":"a","Model":"b"}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := withRoutedModel([]byte(tc.body), "fixture")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %s", got)
				}
				return
			}
			if err != nil || string(got) != tc.want {
				t.Fatalf("got %s, %v; want %s", got, err, tc.want)
			}
		})
	}

	// End to end: the wire carries the routed model, a duplicate is a 400.
	var wire string
	_, provider := newTypesafeFixture(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		wire = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixtureSuccessBody))
	})
	send := func(body string) *schemas.BifrostError {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		request := decisionRequest(t, "fixture", map[string]TypesafeQuestion{"q": {Type: TypesafeQuestionTypeNoul, Instructions: "x"}})
		request.RawRequestBody = []byte(body)
		_, err := provider.Decision(ctx, fixtureKey(), request)
		return err
	}
	if err := send(`{"model":"typesafe/other","state":{"n":9007199254740993}}`); err != nil || wire != `{"model":"jev-1.13.0","state":{"n":9007199254740993}}` {
		t.Fatalf("wire = %s, err = %v", wire, err)
	}
	if err := send(`{"model":"a","model":"b"}`); err == nil || err.StatusCode == nil || *err.StatusCode != 400 {
		t.Fatalf("duplicate model must be a 400, got %+v", err)
	}
}

// TestDecisionFallbackModelGetsOwnIdempotencyKey pins that the upstream key
// differs per wire model, so a fallback never reuses the primary's key with a
// different body.
func TestDecisionFallbackModelGetsOwnIdempotencyKey(t *testing.T) {
	a := schemas.DecisionIdempotencyScope("vk", "model-a", "k")
	b := schemas.DecisionIdempotencyScope("vk", "model-b", "k")
	if a == b {
		t.Fatal("models must not share a key")
	}
	if schemas.DecisionIdempotencyScope("a:b", "c") == schemas.DecisionIdempotencyScope("a", "b:c") {
		t.Fatal("encoding must be injective")
	}
}
