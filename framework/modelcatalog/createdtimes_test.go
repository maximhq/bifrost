package modelcatalog

import (
	"maps"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestGetModelCreatedTimes keeps the provider creation time from cached
// list-models responses, takes the latest time across keys, skips models with
// no time, and drops times when the key is invalidated.
func TestGetModelCreatedTimes(t *testing.T) {
	mc := NewTestCatalog(nil)
	mc.UpsertLiveFromResponse(schemas.OpenAI, "k1", false, &schemas.BifrostListModelsResponse{Data: []schemas.Model{
		{ID: "openai/model-a", Created: schemas.Ptr(int64(100))},
		{ID: "openai/model-b"},
		{ID: "anthropic/other-provider", Created: schemas.Ptr(int64(900))},
	}})
	gen := mc.LiveGeneration(schemas.OpenAI)
	if !mc.UpsertLiveFromResponseIfCurrent(schemas.OpenAI, "k2", false, &schemas.BifrostListModelsResponse{Data: []schemas.Model{
		{ID: "model-a", Created: schemas.Ptr(int64(200))},
		{ID: "model-c", Created: schemas.Ptr(int64(0))},
	}}, gen) {
		t.Fatal("current generation write was dropped")
	}
	want := map[string]int64{"model-a": 200}
	if got := mc.GetModelCreatedTimes(schemas.OpenAI); !maps.Equal(got, want) {
		t.Fatalf("created = %v, want %v", got, want)
	}
	mc.InvalidateLive(schemas.OpenAI, "k2")
	if got := mc.GetModelCreatedTimes(schemas.OpenAI); !maps.Equal(got, map[string]int64{"model-a": 100}) {
		t.Fatalf("after invalidation created = %v", got)
	}
	if got := mc.GetModelCreatedTimes(schemas.Anthropic); len(got) != 0 {
		t.Fatalf("other providers' rows must not leak: %v", got)
	}
}
