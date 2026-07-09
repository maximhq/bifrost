package live

import (
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	openai    = schemas.OpenAI
	anthropic = schemas.Anthropic
)

func upsertFiltered(s *Store, p schemas.ModelProvider, keyID string, models []string) {
	s.Upsert(p, keyID, false, models)
}

func upsertUnfiltered(s *Store, p schemas.ModelProvider, keyID string, models []string) {
	s.Upsert(p, keyID, true, models)
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
	snap[k].Models[0] = "MUTATED"

	got := s.ModelsForProvider(openai)
	if !slices.Equal(got, []string{"gpt-4o"}) {
		t.Errorf("store mutated through Snapshot: %v", got)
	}
}

func TestUpsertCopiesInputSlice(t *testing.T) {
	s := New(nil)
	input := []string{"gpt-4o"}
	s.Upsert(openai, "k1", false, input)

	input[0] = "MUTATED"

	if got := s.ModelsForProvider(openai); !slices.Equal(got, []string{"gpt-4o"}) {
		t.Errorf("store mutated through input slice: %v", got)
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
