// Package livestoretest builds live stores for tests.
package livestoretest

import (
	"testing"

	"github.com/maximhq/bifrost/framework/livestore"
)

// New returns a new, empty live store, closed when the test ends.
func New(t testing.TB) *livestore.Store {
	t.Helper()
	live, err := livestore.New()
	if err != nil {
		t.Fatalf("live store: %v", err)
	}
	t.Cleanup(live.Close)
	return live
}
