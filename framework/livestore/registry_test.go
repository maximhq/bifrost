package livestore

import (
	"context"
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/livestore/registry"
)

// keyOrderStore returns a store from New loaded, without a database, with one provider per entry
// of providers and, under each, a key record per key ID with the Order given.
func keyOrderStore(t *testing.T, providers map[schemas.ModelProvider]map[string]int64) *Store {
	t.Helper()
	s, err := New()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(s.Close)
	var recs []Record
	for name, keys := range providers {
		recs = append(recs, Rec(s.Collections.Providers, &registry.Provider{Name: name}))
		for id, order := range keys {
			recs = append(recs, Rec(s.Collections.ProviderKeys, &registry.ProviderKey{Provider: name, Order: order, Key: schemas.Key{ID: id, Name: id}}))
		}
	}
	if err := s.LoadRecords(context.Background(), recs); err != nil {
		t.Fatalf("load: %v", err)
	}
	return s
}

// keyListIDs lists the IDs in a provider's key list, in order; nil when it has no list.
func keyListIDs(s *Store, provider schemas.ModelProvider) []string {
	list, ok := s.Views.ProviderKeyLists.Get(string(provider))
	if !ok {
		return nil
	}
	out := make([]string, 0, len(*list))
	for _, k := range *list {
		out = append(out, k.ID)
	}
	return out
}

// TestProviderKeyListsHoldEachProvidersKeysInCreationOrder checks that a provider's key list holds
// its own keys sorted by Order, not by ID, and that reading it allocates nothing, since every
// request reads it.
func TestProviderKeyListsHoldEachProvidersKeysInCreationOrder(t *testing.T) {
	s := keyOrderStore(t, map[schemas.ModelProvider]map[string]int64{
		schemas.OpenAI:    {"k-a": 2, "k-b": 0, "k-c": 1},
		schemas.Anthropic: {"a-1": 0},
	})
	if got := keyListIDs(s, schemas.OpenAI); !slices.Equal(got, []string{"k-b", "k-c", "k-a"}) {
		t.Fatalf("openai keys %v, want creation order", got)
	}
	if got := keyListIDs(s, schemas.Anthropic); !slices.Equal(got, []string{"a-1"}) {
		t.Fatalf("anthropic keys %v", got)
	}
	if got := keyListIDs(s, schemas.Gemini); got != nil {
		t.Fatalf("a provider that is not configured has no list, got %v", got)
	}
	if allocs := testing.AllocsPerRun(100, func() { _, _ = s.Views.ProviderKeyLists.Get(string(schemas.OpenAI)) }); !raceEnabled && allocs != 0 {
		t.Fatalf("reading a key list allocates %v times", allocs)
	}
}

// TestProviderKeyListsFollowRemovals checks that removing keys in one refresh swaps the provider's
// list once, with every removal in it, without changing the slice a reader already holds, and that
// removing a provider removes its keys with it and drops its list.
func TestProviderKeyListsFollowRemovals(t *testing.T) {
	ctx := context.Background()
	s := keyOrderStore(t, map[schemas.ModelProvider]map[string]int64{
		schemas.OpenAI: {"k-1": 1, "k-2": 2, "k-3": 3},
	})
	before, ok := s.Views.ProviderKeyLists.Get(string(schemas.OpenAI))
	if !ok {
		t.Fatal("openai has no key list")
	}
	held := *before

	// A subscriber registered after New runs after the list was rebuilt: it sees the whole
	// publish, once.
	var seen [][]string
	cancel := s.Collections.ProviderKeys.Subscribe(func(context.Context, []Change[registry.ProviderKey]) {
		seen = append(seen, keyListIDs(s, schemas.OpenAI))
	})
	defer cancel()
	if err := s.Refresh(ctx, []Ref{
		{Kind: KindProviderKey, ID: "k-1", Remove: true},
		{Kind: KindProviderKey, ID: "k-3", Remove: true},
	}); err != nil {
		t.Fatalf("remove keys: %v", err)
	}
	if len(seen) != 1 || !slices.Equal(seen[0], []string{"k-2"}) {
		t.Fatalf("subscriber saw %v, want one swap with both removals", seen)
	}
	if got := keyListIDs(s, schemas.OpenAI); !slices.Equal(got, []string{"k-2"}) {
		t.Fatalf("openai keys %v", got)
	}
	if got := []string{held[0].ID, held[1].ID, held[2].ID}; !slices.Equal(got, []string{"k-1", "k-2", "k-3"}) {
		t.Fatalf("a reader's slice changed under it: %v", got)
	}

	if err := s.Refresh(ctx, []Ref{{Kind: KindProvider, ID: string(schemas.OpenAI), Remove: true}}); err != nil {
		t.Fatalf("remove provider: %v", err)
	}
	if got := keyListIDs(s, schemas.OpenAI); got != nil {
		t.Fatalf("a removed provider keeps its list: %v", got)
	}
	if _, ok := s.Collections.ProviderKeys.Get("k-2"); ok {
		t.Fatal("a provider's keys are removed with it")
	}
}
