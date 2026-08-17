package schemas

import (
	"context"
	"testing"
)

// fakeForeignContextKey is a context key that belongs to no package here, standing in for
// whatever a caller outside this one might put on a context. A dedicated type rather than a bare
// string, so it cannot collide with a real key by accident, including inside this test itself.
type fakeForeignContextKey struct{}

// fakeGrant is the smallest Grant an installing layer could supply, so the context's wrapping and
// guarding can be tested without the package that implements a real one.
type fakeGrant struct {
	identity Identity
	access   Access
	limits   Limits
}

func (g *fakeGrant) Identity() Identity { return g.identity }
func (g *fakeGrant) Access() Access     { return g.access }
func (g *fakeGrant) Limits() Limits     { return g.limits }
func (g *fakeGrant) SetIdentity(p Identity) bool {
	if p == nil {
		return false
	}
	g.identity = p
	return true
}
func (g *fakeGrant) SetAccess(a Access) bool {
	if a == nil {
		return false
	}
	g.access = a
	return true
}
func (g *fakeGrant) SetLimits(l Limits) bool {
	if l == nil {
		return false
	}
	g.limits = l
	return true
}

type fakeLimits struct{ budgets []Limit }

func (l *fakeLimits) Budgets() []Limit    { return l.budgets }
func (l *fakeLimits) RateLimits() []Limit { return nil }

func TestGrantIsNilUntilInstalled(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	if ctx.Grant() != nil {
		t.Fatal("a fresh context has no grant")
	}
	var none *BifrostContext
	if none.Grant() != nil {
		t.Fatal("a nil context has no grant")
	}
	if none.SetGrant(&fakeGrant{}) {
		t.Fatal("nothing installs on a nil context")
	}
	if ctx.SetGrant(nil) {
		t.Fatal("installing nothing is refused")
	}
	if ctx.Grant() != nil {
		t.Fatal("a refused install leaves no grant behind")
	}
}

func TestGrantIsSharedByScopedContexts(t *testing.T) {
	root := NewBifrostContext(context.Background(), NoDeadline)
	inner := &fakeGrant{}
	if !root.SetGrant(inner) {
		t.Fatal("install should land on an idle context")
	}
	scoped := root.WithPluginScope(Ptr("plugin"))
	defer scoped.ReleasePluginScope()

	limits := &fakeLimits{budgets: []Limit{{ID: "b1"}}}
	if !root.Grant().SetLimits(limits) {
		t.Fatal("write should land outside a hook batch")
	}
	if got := scoped.Grant().Limits(); got != Limits(limits) {
		t.Fatalf("scoped context reads the root's grant, got %v", got)
	}
}

func TestGrantWritesLandDuringHookBatches(t *testing.T) {
	root := NewBifrostContext(context.Background(), NoDeadline)
	inner := &fakeGrant{}
	root.SetGrant(inner)
	limits := &fakeLimits{}

	root.BlockRestrictedWrites()
	defer root.UnblockRestrictedWrites()
	scoped := root.WithPluginScope(Ptr("plugin"))
	defer scoped.ReleasePluginScope()

	if !scoped.Grant().SetLimits(limits) {
		t.Fatal("a plugin may replace a section")
	}
	if inner.limits != Limits(limits) {
		t.Fatal("the write did not reach the installed grant")
	}
	replacement := &fakeGrant{}
	if !root.SetGrant(replacement) || root.Grant() != Grant(replacement) {
		t.Fatal("a grant may be installed at any time")
	}
}

func TestDerivedContextSharesTheAncestorsGrant(t *testing.T) {
	parent := NewBifrostContext(context.Background(), NoDeadline)
	inner := &fakeGrant{limits: &fakeLimits{budgets: []Limit{{ID: "parent"}}}}
	parent.SetGrant(inner)

	child := NewBifrostContext(parent, NoDeadline)
	grandchild := NewBifrostContext(child, NoDeadline)

	if grandchild.Grant() != Grant(inner) {
		t.Fatal("a derived context shares its ancestor's grant")
	}
	if child.Grant() != Grant(inner) {
		t.Fatal("every derived context shares the same grant")
	}

	replaced := &fakeLimits{budgets: []Limit{{ID: "child"}}}
	if !grandchild.Grant().SetLimits(replaced) {
		t.Fatal("write should land on the shared grant")
	}
	if parent.Grant().Limits() != Limits(replaced) {
		t.Fatal("a request and its derived contexts are one request, with one answer")
	}
}

func TestDerivedContextWithoutAncestorGrantHasNone(t *testing.T) {
	parent := NewBifrostContext(context.Background(), NoDeadline)
	child := NewBifrostContext(parent, NoDeadline)
	if child.Grant() != nil {
		t.Fatal("nothing to derive from means no grant")
	}
	foreign := NewBifrostContext(context.WithValue(context.Background(), fakeForeignContextKey{}, "v"), NoDeadline)
	if foreign.Grant() != nil {
		t.Fatal("a foreign parent offers no grant")
	}
}
