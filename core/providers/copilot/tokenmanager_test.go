package copilot

import (
	"sync"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func copilotTestKey(id, token string) schemas.Key {
	return schemas.Key{ID: id, Value: *schemas.NewSecretVar(token)}
}

func TestGetOrCreateTokenManager_ReusesManagerForSameToken(t *testing.T) {
	provider := &CopilotProvider{}
	key := copilotTestKey("key-1", "ghu_same")

	first := provider.getOrCreateTokenManager(key)
	second := provider.getOrCreateTokenManager(key)

	if first != second {
		t.Error("expected the same token manager to be reused for an unchanged token")
	}
	if first.accessToken != "ghu_same" {
		t.Errorf("expected manager to carry the caller's token, got %q", first.accessToken)
	}
}

func TestGetOrCreateTokenManager_ReplacesRotatedToken(t *testing.T) {
	provider := &CopilotProvider{}

	old := provider.getOrCreateTokenManager(copilotTestKey("key-1", "ghu_old"))
	rotated := provider.getOrCreateTokenManager(copilotTestKey("key-1", "ghu_new"))

	if old == rotated {
		t.Error("expected a rotated token to get a fresh manager")
	}
	if rotated.accessToken != "ghu_new" {
		t.Errorf("expected rotated manager to carry the new token, got %q", rotated.accessToken)
	}
	if again := provider.getOrCreateTokenManager(copilotTestKey("key-1", "ghu_new")); again != rotated {
		t.Error("expected the rotated manager to be cached for subsequent calls")
	}
}

// TestGetOrCreateTokenManager_ConcurrentRotationKeepsCallerToken pins that a caller
// losing the swap race never inherits another caller's credential. Previously the
// loser re-read the map and returned the winner's manager, so a request could be
// signed with an OAuth token it never presented.
func TestGetOrCreateTokenManager_ConcurrentRotationKeepsCallerToken(t *testing.T) {
	provider := &CopilotProvider{}
	// Seed the pre-rotation entry both callers will contend against.
	provider.getOrCreateTokenManager(copilotTestKey("key-1", "ghu_old"))

	const goroutines = 32
	tokens := []string{"ghu_a", "ghu_b"}
	results := make([]*copilotTokenManager, goroutines)

	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := 0; i < goroutines; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			results[i] = provider.getOrCreateTokenManager(copilotTestKey("key-1", tokens[i%len(tokens)]))
		}(i)
	}
	start.Done()
	done.Wait()

	for i, tm := range results {
		want := tokens[i%len(tokens)]
		if tm == nil {
			t.Fatalf("goroutine %d got a nil token manager", i)
		}
		if tm.accessToken != want {
			t.Errorf("goroutine %d got manager for token %q, want %q", i, tm.accessToken, want)
		}
	}
}

// TestGetOrCreateTokenManager_ConcurrentFirstUseKeepsCallerToken covers the same
// hazard on the create path, which keys sharing an empty Key.ID hit routinely
// because nothing in core backfills that field.
func TestGetOrCreateTokenManager_ConcurrentFirstUseKeepsCallerToken(t *testing.T) {
	provider := &CopilotProvider{}

	const goroutines = 32
	tokens := []string{"ghu_a", "ghu_b"}
	results := make([]*copilotTokenManager, goroutines)

	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := 0; i < goroutines; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			results[i] = provider.getOrCreateTokenManager(copilotTestKey("", tokens[i%len(tokens)]))
		}(i)
	}
	start.Done()
	done.Wait()

	for i, tm := range results {
		want := tokens[i%len(tokens)]
		if tm == nil {
			t.Fatalf("goroutine %d got a nil token manager", i)
		}
		if tm.accessToken != want {
			t.Errorf("goroutine %d got manager for token %q, want %q", i, tm.accessToken, want)
		}
	}
}
