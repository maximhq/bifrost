package datasheet

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/livestore"
)

// testStore returns a test store over a live store of its own, closed when the test ends.
func testStore(t testing.TB, baseModelIndex map[string]string) *Store {
	t.Helper()
	s, err := NewTestStore(baseModelIndex)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// newForTest returns a store from New over a new live store closed when the test ends.
func newForTest(t testing.TB, configStore configstore.ConfigStore, logger schemas.Logger, cfg Config) *Store {
	t.Helper()
	live, err := livestore.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(live.Close)
	s, err := New(configStore, live, logger, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// tableValues returns t's values, in no particular order.
func tableValues[K comparable, V any](t *livestore.Table[K, V]) []V {
	var out []V
	for _, v := range tableEntries(t) {
		out = append(out, v)
	}
	return out
}
