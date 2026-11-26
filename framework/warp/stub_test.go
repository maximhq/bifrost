package warp

import (
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/livestore"
)

// LogReaderStub embeds the LogReader interface without implementing it.
//
// This is the point: a fake embedding it satisfies the interface at compile
// time, but any method the fake does not override panics with a nil-pointer
// dereference when called. Warp's tools are supposed to touch a small, known
// subset of the read surface, so a test that suddenly panics is telling us an
// executor started reaching somewhere new - which is exactly the change that
// should require a human to look.
type LogReaderStub struct {
	LogReader
}

// serviceForTest builds a Service over store with a fresh live store, failing the test on an
// error and shutting the service down when the test ends.
func serviceForTest(t testing.TB, store configstore.ConfigStore, opts ...Option) *Service {
	t.Helper()
	live, err := livestore.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(live.Close)
	service, err := NewService(store, live, opts...)
	if err != nil {
		t.Fatal(err)
	}
	// Runs before live.Close; Shutdown is safe to repeat for tests that also call it.
	t.Cleanup(service.Shutdown)
	return service
}
