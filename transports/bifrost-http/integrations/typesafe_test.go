package integrations

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestSDKFidelityTypesafeSystemOneRouteNullStateAndExtensions pins #7599 on the
// native /typesafe/v1/systemone route: an SDK-valid {"state": null} converts
// instead of failing with "failed to convert request to Bifrost format", and
// additional native request properties (classifier.dev "images") are marked to
// reach the provider wire so an image request never degrades to text-only.
func TestSDKFidelityTypesafeSystemOneRouteNullStateAndExtensions(t *testing.T) {
	var route *RouteConfig
	for i := range CreateTypesafeRouteConfigs("/typesafe") {
		if r := CreateTypesafeRouteConfigs("/typesafe")[i]; r.Path == "/typesafe/v1/systemone" {
			route = &r
			break
		}
	}
	if route == nil {
		t.Fatal("systemone route not registered")
	}

	incoming := route.GetRequestTypeInstance(context.Background())
	body := []byte(`{"model":"jev-1.13.0","state":null,"images":["data:image/png;base64,iVBORw0KGgo="],"questions":{"q":{"type":"noul","instructions":"Does the image contain text?"}}}`)
	if err := parseJSONRequestBody(body, incoming); err != nil {
		t.Fatalf("parse: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	converted, err := route.RequestConverter(ctx, incoming)
	if err != nil {
		t.Fatalf("null state is SDK-valid and must convert, got %v", err)
	}
	if converted == nil || converted.DecisionRequest == nil {
		t.Fatal("decision request not produced")
	}
	if converted.DecisionRequest.State != nil {
		t.Errorf("null state must stay null, got %#v", converted.DecisionRequest.State)
	}
	if passthrough, _ := ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams).(bool); !passthrough {
		t.Error("native extensions present but passthrough not requested; images would be dropped on the wire")
	}
}

// TestSDKFidelityTypesafeSystemOneRouteRelaysNativeBodies pins that the native
// route hands the endpoint's own success and error bodies back verbatim when
// the provider kept them, and rebuilds the native shape otherwise.
func TestSDKFidelityTypesafeSystemOneRouteRelaysNativeBodies(t *testing.T) {
	var route *RouteConfig
	for _, r := range CreateTypesafeRouteConfigs("/typesafe") {
		if r.Path == "/typesafe/v1/systemone" {
			rc := r
			route = &rc
		}
	}
	if route == nil {
		t.Fatal("systemone route not registered")
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	native := `{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.25,"rationale":"extra"}},"usage":{"input_tokens":3,"output_tokens":0,"billed":true}}`
	resp := &schemas.BifrostDecisionResponse{
		Model:          "jev-1.13.0",
		Answers:        map[string]schemas.DecisionAnswer{"q": {Kind: schemas.DecisionKindNoul, Value: 0.25}},
		NativeResponse: json.RawMessage(native),
	}
	resp.ExtraFields.Provider = schemas.Typesafe
	out, err := route.DecisionResponseConverter(ctx, resp)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if got, ok := out.(json.RawMessage); !ok || string(got) != native {
		t.Errorf("native success body not relayed verbatim: %#v", out)
	}

	resp.ExtraFields.Provider = schemas.OpenAI // emulated elsewhere: rebuilt shape
	out, err = route.DecisionResponseConverter(ctx, resp)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if _, ok := out.(json.RawMessage); ok {
		t.Error("non-typesafe responses must be rebuilt, not relayed")
	}

	nativeErr := `{"detail":[{"type":"missing","loc":["body","state"],"msg":"Field required"}]}`
	bifrostErr := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "state: Field required"}}
	bifrostErr.ExtraFields.NativeErrorResponse = json.RawMessage(nativeErr)
	if got, ok := route.ErrorConverter(ctx, bifrostErr).(json.RawMessage); !ok || string(got) != nativeErr {
		t.Errorf("native error body not relayed verbatim: %#v", route.ErrorConverter(ctx, bifrostErr))
	}
	local := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "state is required"}}
	if _, ok := route.ErrorConverter(ctx, local).(json.RawMessage); ok {
		t.Error("errors Bifrost raised itself must use the rebuilt envelope")
	}
}
