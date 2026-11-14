package grant

import (
	"reflect"
	"sync/atomic"

	"github.com/maximhq/bifrost/core/schemas"
)

// Grant is the envelope a request carries its sections in. See schemas.Grant for what each
// section is and when it settles.
//
// Each section is held behind an atomic pointer and replaced whole. A consumer that read a section
// keeps the answer it read, so two readers of one attempt never see a torn or partially updated
// value, and a section being replaced cannot change a decision that was already made against it.
type Grant struct {
	identity atomic.Pointer[schemas.Identity]
	access   atomic.Pointer[schemas.Access]
	limits   atomic.Pointer[schemas.Limits]
}

// New returns an empty grant.
func New() *Grant {
	return &Grant{}
}

// Identity implements schemas.Grant.
func (g *Grant) Identity() schemas.Identity {
	if g == nil {
		return nil
	}
	if p := g.identity.Load(); p != nil {
		return *p
	}
	return nil
}

// Access implements schemas.Grant.
func (g *Grant) Access() schemas.Access {
	if g == nil {
		return nil
	}
	if a := g.access.Load(); a != nil {
		return *a
	}
	return nil
}

// Limits implements schemas.Grant.
func (g *Grant) Limits() schemas.Limits {
	if g == nil {
		return nil
	}
	if l := g.limits.Load(); l != nil {
		return *l
	}
	return nil
}

// SetIdentity implements schemas.Grant. Recording nothing is a no-op, as the interface says.
func (g *Grant) SetIdentity(identity schemas.Identity) bool {
	if g == nil || isNil(identity) {
		return false
	}
	g.identity.Store(&identity)
	return true
}

// SetAccess implements schemas.Grant. Recording nothing is a no-op, as the interface says.
func (g *Grant) SetAccess(access schemas.Access) bool {
	if g == nil || isNil(access) {
		return false
	}
	g.access.Store(&access)
	return true
}

// SetLimits implements schemas.Grant. Recording nothing is a no-op, as the interface says.
func (g *Grant) SetLimits(limits schemas.Limits) bool {
	if g == nil || isNil(limits) {
		return false
	}
	g.limits.Store(&limits)
	return true
}

// isNil reports whether an interface value is nil, including one that holds a nil pointer. A
// section holding a nil pointer would read as settled while answering nothing, which is exactly
// the confusion a nil section exists to prevent, so setters refuse it as they refuse a bare nil.
func isNil(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface:
		return rv.IsNil()
	}
	return false
}
