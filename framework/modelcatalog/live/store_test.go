package live

import (
	"slices"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	openai    = schemas.OpenAI
	anthropic = schemas.Anthropic
)

// asModels lifts bare identifiers into the stored model shape, using each
// identifier as both the model ID and its routing projection.
func asModels(ids []string) []schemas.Model {
	out := make([]schemas.Model, len(ids))
	for i, id := range ids {
		out[i] = schemas.Model{ID: id}
	}
	return out
}

func upsertFiltered(s *Store, p schemas.ModelProvider, keyID string, models []string) {
	s.Upsert(p, keyID, false, asModels(models), models)
}

func upsertUnfiltered(s *Store, p schemas.ModelProvider, keyID string, models []string) {
	s.Upsert(p, keyID, true, asModels(models), models)
}

// upsertIfCurrentFiltered is the guarded counterpart of upsertFiltered: it
// supplies both projections from one identifier list, so the generation tests
// below can stay focused on which writes land rather than on entry shape.
func upsertIfCurrentFiltered(s *Store, p schemas.ModelProvider, keyID string, models []string, gen uint64) bool {
	return s.UpsertIfCurrent(p, keyID, false, asModels(models), models, gen)
}

func TestUpsertAndReadFiltered(t *testing.T) {
	s := New(nil)
	upsertFiltered(s, openai, "k1", []string{"gpt-4o", "gpt-4o-mini"})

	got := s.ModelsForProvider(openai)
	want := []string{"gpt-4o", "gpt-4o-mini"}
	if !slices.Equal(got, want) {
		t.Fatalf("ModelsForProvider = %v, want %v", got, want)
	}
}

func TestFilteredAndUnfilteredDoNotMix(t *testing.T) {
	s := New(nil)
	upsertFiltered(s, openai, "k1", []string{"gpt-4o"})
	upsertUnfiltered(s, openai, "k1", []string{"gpt-4o", "gpt-4o-mini", "o1"})

	if got := s.ModelsForProvider(openai); !slices.Equal(got, []string{"gpt-4o"}) {
		t.Errorf("filtered read = %v, want [gpt-4o]", got)
	}
	if got := s.UnfilteredModelsForProvider(openai); !slices.Equal(got, []string{"gpt-4o", "gpt-4o-mini", "o1"}) {
		t.Errorf("unfiltered read = %v, want [gpt-4o gpt-4o-mini o1]", got)
	}
}

func TestUnionAcrossKeys(t *testing.T) {
	s := New(nil)
	upsertFiltered(s, openai, "k1", []string{"gpt-4o", "gpt-4o-mini"})
	upsertFiltered(s, openai, "k2", []string{"gpt-4o-mini", "o1"})

	got := s.ModelsForProvider(openai)
	want := []string{"gpt-4o", "gpt-4o-mini", "o1"}
	if !slices.Equal(got, want) {
		t.Fatalf("ModelsForProvider = %v, want %v", got, want)
	}
}

func TestInvalidateOneKeyPreservesOthers(t *testing.T) {
	s := New(nil)
	upsertFiltered(s, openai, "k1", []string{"gpt-4o"})
	upsertFiltered(s, openai, "k2", []string{"o1"})
	upsertUnfiltered(s, openai, "k1", []string{"gpt-4o", "extra"})

	s.Invalidate(openai, "k1")

	if got := s.ModelsForProvider(openai); !slices.Equal(got, []string{"o1"}) {
		t.Errorf("after Invalidate, filtered = %v, want [o1]", got)
	}
	if got := s.UnfilteredModelsForProvider(openai); len(got) != 0 {
		t.Errorf("after Invalidate, unfiltered = %v, want empty (k1's unfiltered should also drop)", got)
	}
}

func TestInvalidateProviderDropsEverything(t *testing.T) {
	s := New(nil)
	upsertFiltered(s, openai, "k1", []string{"gpt-4o"})
	upsertFiltered(s, openai, "k2", []string{"o1"})
	upsertFiltered(s, anthropic, "k3", []string{"claude-3-5-sonnet"})

	s.InvalidateProvider(openai)

	if got := s.ModelsForProvider(openai); len(got) != 0 {
		t.Errorf("openai after InvalidateProvider = %v, want empty", got)
	}
	if got := s.ModelsForProvider(anthropic); !slices.Equal(got, []string{"claude-3-5-sonnet"}) {
		t.Errorf("anthropic untouched = %v, want [claude-3-5-sonnet]", got)
	}
}

func TestRetainKeysDropsOnlyMissingKeys(t *testing.T) {
	s := New(nil)
	upsertFiltered(s, openai, "k1", []string{"gpt-4o"})
	upsertUnfiltered(s, openai, "k1", []string{"gpt-4o", "gpt-4o-mini"})
	upsertFiltered(s, openai, "k2", []string{"o1"})
	upsertUnfiltered(s, openai, "k2", []string{"o1", "o1-mini"})
	upsertFiltered(s, openai, "k3", []string{"o3"})
	upsertFiltered(s, anthropic, "k9", []string{"claude-3-5-sonnet"})

	s.RetainKeys(openai, map[string]struct{}{"k1": {}, "k3": {}})

	// k2 is gone in both modes; k1 and k3 survive in both.
	if got := s.ModelsForProvider(openai); !slices.Equal(got, []string{"gpt-4o", "o3"}) {
		t.Errorf("filtered after RetainKeys = %v, want [gpt-4o o3]", got)
	}
	if got := s.UnfilteredModelsForProvider(openai); !slices.Equal(got, []string{"gpt-4o", "gpt-4o-mini"}) {
		t.Errorf("unfiltered after RetainKeys = %v, want [gpt-4o gpt-4o-mini]", got)
	}
	if got := s.ModelsForProvider(anthropic); !slices.Equal(got, []string{"claude-3-5-sonnet"}) {
		t.Errorf("other provider must be untouched, got %v", got)
	}
}

func TestRetainKeysEmptyKeepSetDropsAll(t *testing.T) {
	for _, tt := range []struct {
		name string
		keep map[string]struct{}
	}{
		{"nil keep set", nil},
		{"empty keep set", map[string]struct{}{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := New(nil)
			upsertFiltered(s, openai, "k1", []string{"gpt-4o"})
			upsertFiltered(s, anthropic, "k9", []string{"claude-3-5-sonnet"})

			s.RetainKeys(openai, tt.keep)

			if got := s.ModelsForProvider(openai); len(got) != 0 {
				t.Errorf("openai after empty RetainKeys = %v, want empty", got)
			}
			if got := s.ModelsForProvider(anthropic); !slices.Equal(got, []string{"claude-3-5-sonnet"}) {
				t.Errorf("other provider must be untouched, got %v", got)
			}
		})
	}
}

// TestRetainKeysKeylessSentinel pins the "" key ID (used by keyless providers
// such as Vertex and Bedrock) as an ordinary retainable member of the keep
// set, not a wildcard or an absent entry.
func TestRetainKeysKeylessSentinel(t *testing.T) {
	s := New(nil)
	upsertFiltered(s, schemas.Vertex, "", []string{"gemini-2.0-flash"})
	upsertFiltered(s, schemas.Vertex, "stale-key", []string{"gemini-1.0-pro"})

	s.RetainKeys(schemas.Vertex, map[string]struct{}{"": {}})

	if got := s.ModelsForProvider(schemas.Vertex); !slices.Equal(got, []string{"gemini-2.0-flash"}) {
		t.Errorf("keyless retain = %v, want [gemini-2.0-flash]", got)
	}
}

// TestRetainKeysUnknownKeepEntriesAreHarmless covers the reload race where a
// key is in the config but was never successfully fetched: naming it in the
// keep set must not fabricate an entry or disturb the others.
func TestRetainKeysUnknownKeepEntriesAreHarmless(t *testing.T) {
	s := New(nil)
	upsertFiltered(s, openai, "k1", []string{"gpt-4o"})

	s.RetainKeys(openai, map[string]struct{}{"k1": {}, "never-fetched": {}})

	if got := s.ModelsForProvider(openai); !slices.Equal(got, []string{"gpt-4o"}) {
		t.Errorf("after RetainKeys with unknown member = %v, want [gpt-4o]", got)
	}
	if n := len(s.Snapshot()); n != 1 {
		t.Errorf("Snapshot has %d entries, want 1 (no entry fabricated)", n)
	}
}

// TestRetainKeysIsRaceFreeUnderConcurrentAccess exercises RetainKeys against
// every other mutator and reader on the store at once. RetainKeys is the only
// method that both iterates the map and deletes from it, so it is the one most
// likely to be written without the write lock, and a map iteration racing a
// concurrent write is a runtime throw rather than a silent corruption. Run
// under -race to catch the unsynchronized case even when the scheduler happens
// not to interleave them.
func TestRetainKeysIsRaceFreeUnderConcurrentAccess(t *testing.T) {
	s := New(nil)
	const goroutines = 8
	const iterations = 200

	providers := []schemas.ModelProvider{openai, anthropic}
	keyIDs := []string{"", "k1", "k2", "k3"}

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			p := providers[g%len(providers)]
			for i := range iterations {
				keyID := keyIDs[i%len(keyIDs)]
				switch g % 4 {
				case 0:
					models := []string{"model-a", "model-b"}
					s.Upsert(p, keyID, i%2 == 0, asModels(models), models)
				case 1:
					// Alternate the retained set so entries are constantly
					// being pruned and repopulated underneath the readers.
					keep := map[string]struct{}{keyID: {}}
					if i%3 == 0 {
						keep["k1"] = struct{}{}
					}
					s.RetainKeys(p, keep)
				case 2:
					_ = s.ModelsForProvider(p)
					_ = s.UnfilteredModelsForProvider(p)
				case 3:
					if i%5 == 0 {
						s.Invalidate(p, keyID)
					}
					_ = s.Snapshot()
				}
			}
		}(g)
	}
	wg.Wait()

	// Sanity check that the store is still coherent rather than merely
	// race-free: a final retain must leave a readable, well-formed view.
	s.Upsert(openai, "k1", false, asModels([]string{"model-a"}), []string{"model-a"})
	s.RetainKeys(openai, map[string]struct{}{"k1": {}})
	if got := s.ModelsForProvider(openai); !slices.Equal(got, []string{"model-a"}) {
		t.Fatalf("store incoherent after concurrent access: %v", got)
	}
}

// TestRetainKeysDoesNotRetainCallerMap pins the ownership half of the
// concurrency contract: the store must not hold a reference to the caller's
// keep map, or a caller reusing or mutating that map after the call would be
// racing the store's internals.
func TestRetainKeysDoesNotRetainCallerMap(t *testing.T) {
	s := New(nil)
	upsertFiltered(s, openai, "k1", []string{"gpt-4o"})
	upsertFiltered(s, openai, "k2", []string{"o1"})

	keep := map[string]struct{}{"k1": {}}
	s.RetainKeys(openai, keep)

	// Mutating the caller's map afterwards must not retroactively change what
	// the store kept.
	keep["k2"] = struct{}{}
	delete(keep, "k1")

	if got := s.ModelsForProvider(openai); !slices.Equal(got, []string{"gpt-4o"}) {
		t.Fatalf("store observed post-call mutation of the keep map: %v, want [gpt-4o]", got)
	}
}

func TestKeylessProviderUsesEmptyKeyID(t *testing.T) {
	s := New(nil)
	upsertFiltered(s, schemas.Vertex, "", []string{"gemini-2.0-flash"})

	if got := s.ModelsForProvider(schemas.Vertex); !slices.Equal(got, []string{"gemini-2.0-flash"}) {
		t.Errorf("keyless read = %v, want [gemini-2.0-flash]", got)
	}
}

func TestSnapshotIsDefensiveCopy(t *testing.T) {
	s := New(nil)
	upsertFiltered(s, openai, "k1", []string{"gpt-4o"})

	snap := s.Snapshot()
	k := Key{Provider: openai, KeyID: "k1", Unfiltered: false}
	snap[k].Models[0].ID = "MUTATED"
	snap[k].IDs[0] = "MUTATED"

	got := s.ModelsForProvider(openai)
	if !slices.Equal(got, []string{"gpt-4o"}) {
		t.Errorf("store mutated through Snapshot: %v", got)
	}
	if full, ok := s.FullModelsFor(openai, []string{"k1"}, false); !ok || full[0].ID != "gpt-4o" {
		t.Errorf("models mutated through Snapshot: %+v (hit=%v)", full, ok)
	}
}

func TestUpsertCopiesInputSlice(t *testing.T) {
	s := New(nil)
	input := []string{"gpt-4o"}
	inputModels := asModels(input)
	s.Upsert(openai, "k1", false, inputModels, input)

	input[0] = "MUTATED"
	inputModels[0].ID = "MUTATED"

	if got := s.ModelsForProvider(openai); !slices.Equal(got, []string{"gpt-4o"}) {
		t.Errorf("store mutated through input slice: %v", got)
	}
	full, ok := s.FullModelsFor(openai, []string{"k1"}, false)
	if !ok || len(full) != 1 || full[0].ID != "gpt-4o" {
		t.Errorf("store mutated through input model slice: %+v (hit=%v)", full, ok)
	}
}

func TestModelsForProviderUnknownProviderReturnsEmpty(t *testing.T) {
	s := New(nil)
	if got := s.ModelsForProvider(openai); len(got) != 0 {
		t.Errorf("unknown provider = %v, want empty", got)
	}
}

func TestUpsertOverwritesSameKey(t *testing.T) {
	s := New(nil)
	upsertFiltered(s, openai, "k1", []string{"gpt-4o"})
	upsertFiltered(s, openai, "k1", []string{"o1"})

	if got := s.ModelsForProvider(openai); !slices.Equal(got, []string{"o1"}) {
		t.Errorf("after re-Upsert = %v, want [o1] (overwrite, not append)", got)
	}
}

// TestUpsertIfCurrentAcceptsUnchangedGeneration is the baseline: with nothing
// invalidating the provider in between, a guarded commit behaves exactly like
// a plain Upsert.
func TestUpsertIfCurrentAcceptsUnchangedGeneration(t *testing.T) {
	s := New(nil)
	gen := s.Generation(openai)

	if !upsertIfCurrentFiltered(s, openai, "k1", []string{"gpt-4o"}, gen) {
		t.Fatal("UpsertIfCurrent reported a dropped write with no intervening invalidation")
	}
	if got := s.ModelsForProvider(openai); !slices.Equal(got, []string{"gpt-4o"}) {
		t.Errorf("after guarded upsert = %v, want [gpt-4o]", got)
	}
}

// TestUpsertIfCurrentDropsWriteAfterInvalidate is the core of the deleted-key
// fix. It reproduces the ordering a background refresh hits: read generation,
// start the (here, elided) upstream call, have the key deleted mid-flight, and
// only then try to commit. The commit must not resurrect the entry Invalidate
// just dropped, because nothing ever re-prunes a key that is no longer
// configured.
func TestUpsertIfCurrentDropsWriteAfterInvalidate(t *testing.T) {
	s := New(nil)
	upsertFiltered(s, openai, "k1", []string{"gpt-4o"})

	gen := s.Generation(openai) // refresh pass starts here

	s.Invalidate(openai, "k1") // key deleted while the fetch is in flight

	if upsertIfCurrentFiltered(s, openai, "k1", []string{"gpt-4o"}, gen) {
		t.Fatal("UpsertIfCurrent committed a result fetched before the key was invalidated")
	}
	if got := s.ModelsForProvider(openai); len(got) != 0 {
		t.Errorf("after dropped commit = %v, want [] (deleted key must stay gone)", got)
	}
}

// TestInvalidateBumpsGenerationWithNoCachedEntry pins why the bump is
// unconditional. The dangerous case is precisely the one where Invalidate
// deletes nothing: the fetch it has to cancel has not written yet. Bumping only
// when an entry existed would leave that write unguarded.
func TestInvalidateBumpsGenerationWithNoCachedEntry(t *testing.T) {
	s := New(nil)
	gen := s.Generation(openai)

	s.Invalidate(openai, "k1") // nothing cached for k1 yet

	if upsertIfCurrentFiltered(s, openai, "k1", []string{"gpt-4o"}, gen) {
		t.Fatal("UpsertIfCurrent committed after an Invalidate that happened to delete nothing")
	}
}

// TestInvalidateProviderAndRetainKeysBumpGeneration covers the other two
// invalidation entrypoints: provider delete and the pre-refetch prune both
// cancel in-flight fetches the same way Invalidate does.
func TestInvalidateProviderAndRetainKeysBumpGeneration(t *testing.T) {
	tests := []struct {
		name       string
		invalidate func(*Store)
	}{
		{"InvalidateProvider", func(s *Store) { s.InvalidateProvider(openai) }},
		{"RetainKeys", func(s *Store) { s.RetainKeys(openai, map[string]struct{}{"k2": {}}) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(nil)
			gen := s.Generation(openai)

			tt.invalidate(s)

			if upsertIfCurrentFiltered(s, openai, "k1", []string{"gpt-4o"}, gen) {
				t.Fatalf("UpsertIfCurrent committed a fetch that predates %s", tt.name)
			}
		})
	}
}

// TestGenerationIsPerProvider makes sure the counter does not couple unrelated
// providers: deleting a key on one must not throw away a healthy in-flight
// refresh for another. Providers refresh concurrently, so a shared counter
// would make every key edit anywhere cost a whole refresh cycle.
func TestGenerationIsPerProvider(t *testing.T) {
	s := New(nil)
	gen := s.Generation(anthropic)

	s.Invalidate(openai, "k1")

	if !upsertIfCurrentFiltered(s, anthropic, "k1", []string{"claude-sonnet"}, gen) {
		t.Fatal("an OpenAI invalidation dropped an Anthropic commit")
	}
	if got := s.ModelsForProvider(anthropic); !slices.Equal(got, []string{"claude-sonnet"}) {
		t.Errorf("Anthropic after unrelated invalidation = %v, want [claude-sonnet]", got)
	}
}

// TestGenerationSurvivesProviderRemoval guards the decision not to prune the
// gen map. Resetting a removed provider's counter to 0 would make a fetch that
// captured 0 before the removal look current again once the provider was
// re-added, which is the exact write the guard exists to stop.
func TestGenerationSurvivesProviderRemoval(t *testing.T) {
	s := New(nil)
	gen := s.Generation(openai) // fetch starts against the original provider

	s.InvalidateProvider(openai) // provider deleted...
	upsertFiltered(s, openai, "k1", []string{"o1"})
	s.InvalidateProvider(openai) // ...and re-added, then reloaded

	if upsertIfCurrentFiltered(s, openai, "k1", []string{"gpt-4o"}, gen) {
		t.Fatal("a pre-removal fetch committed after the provider was recreated")
	}
}

// TestUpsertIfCurrentCopiesInputSlice mirrors TestUpsertCopiesInputSlice: the
// guarded path must not retain either of the caller's backing arrays.
func TestUpsertIfCurrentCopiesInputSlice(t *testing.T) {
	s := New(nil)
	models := asModels([]string{"gpt-4o"})
	ids := []string{"gpt-4o"}
	s.UpsertIfCurrent(openai, "k1", false, models, ids, s.Generation(openai))

	models[0].ID = "MUTATED"
	ids[0] = "MUTATED"

	if got := s.ModelsForProvider(openai); !slices.Equal(got, []string{"gpt-4o"}) {
		t.Errorf("store mutated through ids slice: %v", got)
	}
	full, _ := s.FullModelsFor(openai, []string{"k1"}, false)
	if len(full) != 1 || full[0].ID != "gpt-4o" {
		t.Errorf("store mutated through models slice: %v", full)
	}
}

// TestUpsertIfCurrentStoresBothProjections is the regression for the guarded
// path storing only identifiers: a commit has to leave the entry able to answer
// both readers, since /v1/models reads Models and routing reads IDs. Storing one
// without the other leaves the other silently empty.
func TestUpsertIfCurrentStoresBothProjections(t *testing.T) {
	s := New(nil)
	// The two projections differ, as they do in production: routing strips the
	// owning provider's own prefix while /v1/models keeps what the provider said.
	ownedBy := "openai"
	models := []schemas.Model{{ID: "openai/gpt-4o", OwnedBy: &ownedBy}}
	if !s.UpsertIfCurrent(openai, "k1", false, models, []string{"gpt-4o"}, s.Generation(openai)) {
		t.Fatal("guarded commit dropped with no intervening invalidation")
	}

	if got := s.ModelsForProvider(openai); !slices.Equal(got, []string{"gpt-4o"}) {
		t.Errorf("routing view = %v, want [gpt-4o]", got)
	}
	full, ok := s.FullModelsFor(openai, []string{"k1"}, false)
	if !ok {
		t.Fatal("FullModelsFor missed an entry a guarded commit just wrote")
	}
	if len(full) != 1 || full[0].ID != "openai/gpt-4o" || full[0].OwnedBy == nil || *full[0].OwnedBy != "openai" {
		t.Errorf("full view = %+v, want the provider's model verbatim", full)
	}
}

// TestConcurrentInvalidateAndGuardedUpsertAreRaceFree runs the guard under -race
// with invalidations and commits interleaving freely. The invariant is not which
// writes land but that the store stays consistent and no commit outlives the
// invalidation that followed its Generation read.
func TestConcurrentInvalidateAndGuardedUpsertAreRaceFree(t *testing.T) {
	s := New(nil)
	const workers = 8
	const iterations = 100

	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for range iterations {
				gen := s.Generation(openai)
				if i%2 == 0 {
					s.Invalidate(openai, "k1")
					continue
				}
				upsertIfCurrentFiltered(s, openai, "k1", []string{"gpt-4o"}, gen)
				_ = s.ModelsForProvider(openai)
			}
		}(i)
	}
	wg.Wait()
}

// TestFullModelsForPreservesEveryField is the point of storing whole models
// rather than identifiers: a cache hit has to reproduce what the provider
// reported, field for field, or /v1/models silently degrades when served from
// cache instead of live.
func TestFullModelsForPreservesEveryField(t *testing.T) {
	s := New(nil)
	alias := "gpt-4o-deployment"
	owner := "openai"
	ctxLen := 128000
	created := int64(1715367049)
	stored := []schemas.Model{{
		ID:                  "gpt-4o",
		Alias:               &alias,
		OwnedBy:             &owner,
		ContextLength:       &ctxLen,
		Created:             &created,
		SupportedParameters: []string{"tools", "temperature"},
		Architecture:        &schemas.Architecture{InputModalities: []string{"text", "image"}},
	}}
	s.Upsert(openai, "k1", false, stored, []string{"gpt-4o"})

	got, ok := s.FullModelsFor(openai, []string{"k1"}, false)
	if !ok {
		t.Fatal("expected a hit")
	}
	if len(got) != 1 {
		t.Fatalf("got %d models, want 1", len(got))
	}
	m := got[0]
	switch {
	case m.ID != "gpt-4o":
		t.Errorf("ID = %q", m.ID)
	case m.Alias == nil || *m.Alias != alias:
		t.Errorf("Alias = %v, want %q", m.Alias, alias)
	case m.OwnedBy == nil || *m.OwnedBy != owner:
		t.Errorf("OwnedBy = %v, want %q", m.OwnedBy, owner)
	case m.ContextLength == nil || *m.ContextLength != ctxLen:
		t.Errorf("ContextLength = %v, want %d", m.ContextLength, ctxLen)
	case m.Created == nil || *m.Created != created:
		t.Errorf("Created = %v, want %d", m.Created, created)
	case !slices.Equal(m.SupportedParameters, []string{"tools", "temperature"}):
		t.Errorf("SupportedParameters = %v", m.SupportedParameters)
	case m.Architecture == nil || !slices.Equal(m.Architecture.InputModalities, []string{"text", "image"}):
		t.Errorf("Architecture = %+v", m.Architecture)
	}
}

// A miss and an empty hit mean different things: a provider that serves no
// models is answerable from cache, whereas a key never fetched must fall
// through to the provider.
func TestFullModelsForDistinguishesMissFromEmpty(t *testing.T) {
	s := New(nil)

	if _, ok := s.FullModelsFor(openai, []string{"never-fetched"}, false); ok {
		t.Error("expected a miss for a key that was never fetched")
	}

	s.Upsert(openai, "k1", false, []schemas.Model{}, []string{})
	got, ok := s.FullModelsFor(openai, []string{"k1"}, false)
	if !ok {
		t.Error("expected a hit for a key fetched with zero models")
	}
	if len(got) != 0 {
		t.Errorf("expected zero models, got %v", got)
	}
}

func TestFullModelsForUnionsAndDedupesAcrossKeys(t *testing.T) {
	s := New(nil)
	s.Upsert(openai, "k1", false, asModels([]string{"gpt-4o", "o1"}), []string{"gpt-4o", "o1"})
	s.Upsert(openai, "k2", false, asModels([]string{"o1", "gpt-4o-mini"}), []string{"o1", "gpt-4o-mini"})

	got, ok := s.FullModelsFor(openai, []string{"k1", "k2"}, false)
	if !ok {
		t.Fatal("expected a hit")
	}
	ids := make([]string, len(got))
	for i, m := range got {
		ids[i] = m.ID
	}
	// Sorted so the response is stable regardless of map iteration order.
	if !slices.Equal(ids, []string{"gpt-4o", "gpt-4o-mini", "o1"}) {
		t.Errorf("union = %v, want sorted [gpt-4o gpt-4o-mini o1]", ids)
	}
}

// Keyless providers cache under the empty-string sentinel, and callers reach
// them by passing no key IDs at all.
func TestFullModelsForEmptyKeyIDsUsesKeylessSentinel(t *testing.T) {
	s := New(nil)
	s.Upsert(openai, "", false, asModels([]string{"gpt-4o"}), []string{"gpt-4o"})

	got, ok := s.FullModelsFor(openai, nil, false)
	if !ok || len(got) != 1 || got[0].ID != "gpt-4o" {
		t.Errorf("FullModelsFor(nil keys) = %+v (hit=%v), want the keyless entry", got, ok)
	}
}

func TestFullModelsForSeparatesFilteredFromUnfiltered(t *testing.T) {
	s := New(nil)
	s.Upsert(openai, "k1", false, asModels([]string{"gpt-4o"}), []string{"gpt-4o"})

	// The gated view exists; the raw one has never been fetched. Answering the
	// unfiltered request from the filtered entry would under-report, and the
	// reverse would leak models the key is not allowed to use.
	if _, ok := s.FullModelsFor(openai, []string{"k1"}, true); ok {
		t.Error("expected unfiltered lookup to miss when only the filtered entry exists")
	}
}
