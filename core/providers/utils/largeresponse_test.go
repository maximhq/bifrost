package utils

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestPrepareResponseStreaming_EnterpriseThresholdOverridesClientLimit(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyLargeResponseThreshold, int64(4096))
	base := &fasthttp.Client{MaxResponseBodySize: 1024}
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	active := PrepareResponseStreaming(ctx, base, resp)

	if active.MaxResponseBodySize != 4096 {
		t.Fatalf("Enterprise threshold: got %d, want 4096", active.MaxResponseBodySize)
	}
	if !active.StreamResponseBody {
		t.Fatal("Enterprise response client must enable streaming")
	}
}
