package server

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// A deployment with no governance resolves no access, and the listing must keep the fan-out it
// already had. Publishing an empty list here would mean "no provider may serve this", turning an
// unrestricted listing into one that lists nothing, so this pins the guard rather than the rule.
func TestNarrowListModelsProvidersLeavesFanOutAloneWithoutGovernance(t *testing.T) {
	// ResolveAccess warns through the package logger when no governance plugin is registered,
	// which is nil until a server sets it.
	SetLogger(bifrost.NewNoOpLogger())

	// Ctx is what the governance plugin is looked up through, and a real server always has one.
	s := &BifrostHTTPServer{
		Config: &lib.Config{},
		Ctx:    schemas.NewBifrostContext(context.Background(), time.Time{}),
	}
	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	s.NarrowListModelsProviders(bifrostCtx)

	if got := bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders); got != nil {
		t.Fatalf("expected nothing to be published, got %#v", got)
	}
}

// Narrowing runs on the request path, so a context it was never given must not take the listing
// down with it.
func TestNarrowListModelsProvidersWithoutContext(t *testing.T) {
	SetLogger(bifrost.NewNoOpLogger())

	s := &BifrostHTTPServer{
		Config: &lib.Config{},
		Ctx:    schemas.NewBifrostContext(context.Background(), time.Time{}),
	}

	s.NarrowListModelsProviders(nil)
}
