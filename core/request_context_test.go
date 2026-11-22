package bifrost

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestRequestContextCreatesIsolatedScopeForNilSDKContext(t *testing.T) {
	root := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	root.SetValue("inherited", "root-value")
	bifrost := &Bifrost{ctx: root}

	first := bifrost.requestContext(nil)
	second := bifrost.requestContext(nil)
	if first == nil || second == nil || first == second {
		t.Fatalf("requestContext(nil) returned (%p, %p), want two distinct contexts", first, second)
	}
	first.SetValue("request-local", "first")
	if got := first.Value("inherited"); got != "root-value" {
		t.Fatalf("first inherited value = %v, want root-value", got)
	}
	if got := second.Value("request-local"); got != nil {
		t.Fatalf("second request observed first request's state: %v", got)
	}
	if got := root.Value("request-local"); got != nil {
		t.Fatalf("shared bifrost context received request-local state: %v", got)
	}
}
