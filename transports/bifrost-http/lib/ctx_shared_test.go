package lib

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestConvertToBifrostContext_ReusesSharedContext(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	base := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	base.SetValue(schemas.BifrostContextKeyRequestID, "req-shared")
	ctx.SetUserValue(FastHTTPUserValueBifrostContext, base)

	converted, cancel := ConvertToBifrostContext(ctx, false, nil)
	defer cancel()

	if converted == nil {
		t.Fatal("expected non-nil converted context")
	}
	if got, _ := converted.Value(schemas.BifrostContextKeyRequestID).(string); got != "req-shared" {
		t.Fatalf("expected converted context to preserve parent values, got request-id=%q", got)
	}
	if stored, ok := ctx.UserValue(FastHTTPUserValueBifrostContext).(*schemas.BifrostContext); !ok || stored == nil {
		t.Fatal("expected shared context pointer to be stored on fasthttp user values")
	}
	if ctx.UserValue(FastHTTPUserValueBifrostCancel) == nil {
		t.Fatal("expected shared cancel function to be stored on fasthttp user values")
	}
}

func TestConvertToBifrostContext_SecondCallReturnsSameSharedContext(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}

	first, cancelFirst := ConvertToBifrostContext(ctx, false, nil)
	defer cancelFirst()
	if first == nil {
		t.Fatal("expected first context to be non-nil")
	}

	second, cancelSecond := ConvertToBifrostContext(ctx, false, nil)
	defer cancelSecond()
	if second == nil {
		t.Fatal("expected second context to be non-nil")
	}
	if first != second {
		t.Fatal("expected ConvertToBifrostContext to reuse the shared context on repeated calls")
	}
}
