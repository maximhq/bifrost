package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"testing"

	"github.com/maximhq/bifrost/core/providers/typesafe"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
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
	if !converted.DecisionRequest.Input.IsEmpty() {
		t.Errorf("null state must stay null, got %#v", converted.DecisionRequest.Input)
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
		Answers:        []schemas.DecisionAnswer{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("q"), Probability: schemas.Ptr(0.25)}},
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

	// A custom provider backed by Typesafe reports its own name; the base
	// provider type on the context is what marks the body as native.
	customCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	customCtx.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.Typesafe)
	resp.ExtraFields.Provider = "my-typesafe"
	out, err = route.DecisionResponseConverter(customCtx, resp)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if got, ok := out.(json.RawMessage); !ok || string(got) != native {
		t.Errorf("custom typesafe provider's native body not relayed verbatim: %#v", out)
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

// systemOneMultipart builds a multipart systemone body from parts in order: each
// part is a name, an optional filename (a file part), an optional content type,
// and its data.
func systemOneMultipart(t *testing.T, parts ...[4]string) (*fasthttp.RequestCtx, *typesafe.TypesafeDecisionRequest) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for _, p := range parts {
		header := textproto.MIMEHeader{}
		disposition := fmt.Sprintf(`form-data; name=%q`, p[0])
		if p[1] != "" {
			disposition += fmt.Sprintf(`; filename=%q`, p[1])
		}
		header.Set("Content-Disposition", disposition)
		if p[2] != "" {
			header.Set("Content-Type", p[2])
		}
		w, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("create part: %v", err)
		}
		_, _ = w.Write([]byte(p[3]))
	}
	_ = writer.Close()
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetContentType(writer.FormDataContentType())
	ctx.Request.SetBody(buf.Bytes())
	return ctx, &typesafe.TypesafeDecisionRequest{}
}

// TestTypesafeSystemOneMultipartParser pins the systemone route's multipart body: the
// JSON is read from the "request" part, a plain field or a file part, in any position,
// and each file part's bytes become the asset that names it, keeping its name.
func TestTypesafeSystemOneMultipartParser(t *testing.T) {
	request := `{"model":"jev-latest","state":{"door":"r-door","note":"r-note"},"assets":{"r-door":{"type":"image","part":"door"},"r-note":{"format":"png","data_base64":"Tk9URQ=="}},"questions":{"q":{"type":"noul","instructions":"Is someone at the door?"}}}`
	for name, parts := range map[string][][4]string{
		"request field first":   {{"request", "", "application/json", request}, {"door", "door.jpg", "image/jpeg", "JPEG"}},
		"request as a file":     {{"request", "request.json", "application/json", request}, {"door", "door.jpg", "image/jpeg", "JPEG"}},
		"request not first":     {{"door", "door.jpg", "image/jpeg", "JPEG"}, {"request", "", "application/json", request}},
		"part without its type": {{"request", "", "", request}, {"door", "door.jpg", "", "JPEG"}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, native := systemOneMultipart(t, parts...)
			if err := parseTypesafeSystemOneRequest(ctx, native); err != nil {
				t.Fatalf("parse: %v", err)
			}
			shared, err := native.ToBifrostDecisionRequest(nil)
			if err != nil {
				t.Fatalf("to shared: %v", err)
			}
			door := shared.Input.Messages[0].Content.Parts[1]
			if *door.ID != "r-door" || door.Name == nil || *door.Name != "door" || string(door.Data) != "JPEG" {
				t.Errorf("door part = %+v", door)
			}
			if note := shared.Input.Messages[0].Content.Parts[2]; note.Name != nil || string(note.Data) != "NOTE" {
				t.Errorf("base64 asset in a multipart request changed: %+v", note)
			}
		})
	}
}

// TestTypesafeSystemOneMultipartParserRejections pins the 400s for a multipart body
// the request cannot be built from; Typesafe rejects each of them too.
func TestTypesafeSystemOneMultipartParserRejections(t *testing.T) {
	request := func(assets string) string {
		return `{"model":"jev-latest","state":"r-a","assets":` + assets + `,"questions":{"q":{"type":"noul","instructions":"x"}}}`
	}
	one := request(`{"r-a":{"part":"a"}}`)
	for name, parts := range map[string][][4]string{
		"no request part":      {{"a", "a.png", "image/png", "PNG"}},
		"duplicate part":       {{"request", "", "", one}, {"a", "a.png", "image/png", "PNG"}, {"a", "b.png", "image/png", "PNG"}},
		"missing part":         {{"request", "", "", request(`{"r-a":{"part":"gone"}}`)}},
		"unused part":          {{"request", "", "", one}, {"a", "a.png", "image/png", "PNG"}, {"b", "b.png", "image/png", "PNG"}},
		"stray form field":     {{"request", "", "", one}, {"a", "a.png", "image/png", "PNG"}, {"note", "", "", "hello"}},
		"two assets, one part": {{"request", "", "", request(`{"r-a":{"part":"a"},"r-b":{"part":"a"}}`)}, {"a", "a.png", "image/png", "PNG"}},
		"part named request":   {{"request", "", "", request(`{"r-a":{"part":"request"}}`)}},
		"part without a name":  {{"request", "", "", one}, {"a", "a.png", "image/png", "PNG"}, {"", "b.png", "image/png", "PNG"}},
	} {
		ctx, native := systemOneMultipart(t, parts...)
		if err := parseTypesafeSystemOneRequest(ctx, native); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// TestTypesafeSystemOneParserJSON pins that a JSON body still decodes as before.
func TestTypesafeSystemOneParserJSON(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetContentType("application/json")
	ctx.Request.SetBody([]byte(`{"model":"jev-latest","state":"text","questions":{"q":{"type":"noul","instructions":"x"}}}`))
	native := &typesafe.TypesafeDecisionRequest{}
	if err := parseTypesafeSystemOneRequest(ctx, native); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if native.State != "text" || native.Model != "jev-latest" || len(native.Questions) != 1 {
		t.Errorf("decoded = %+v", native)
	}
}
