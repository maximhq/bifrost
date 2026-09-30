package utils

import (
	"sync"
	"testing"
	"time"
)

func TestContainerSessionRegistry_ClaimsOncePerContainer(t *testing.T) {
	r := NewContainerSessionRegistry(time.Hour, 100)

	if !r.Claim("openai:cntr_a") {
		t.Fatal("first sighting of a container should be claimed")
	}
	// A multi-turn conversation reuses the same container. Billing every turn is the
	// bug this registry exists to prevent.
	if r.Claim("openai:cntr_a") {
		t.Fatal("second sighting of the same container should not be claimed")
	}
	if !r.Claim("openai:cntr_b") {
		t.Fatal("a different container should be claimed")
	}
}

func TestContainerSessionRegistry_KeysAreProviderScoped(t *testing.T) {
	r := NewContainerSessionRegistry(time.Hour, 100)

	if !r.Claim("openai:cntr_a") {
		t.Fatal("expected the openai container to be claimed")
	}
	// Two providers can mint the same opaque id without one suppressing the other.
	if !r.Claim("azure:cntr_a") {
		t.Fatal("expected the azure container to be claimed independently")
	}
}

func TestContainerSessionRegistry_NeverClaimsAnEmptyKey(t *testing.T) {
	r := NewContainerSessionRegistry(time.Hour, 100)

	// A provider that reports no container id must not be able to bill one.
	if r.Claim("") {
		t.Fatal("empty key should never be claimed")
	}
	if got := r.Len(); got != 0 {
		t.Fatalf("registry length = %d, want 0", got)
	}
}

func TestContainerSessionRegistry_ReclaimsAfterTTL(t *testing.T) {
	r := NewContainerSessionRegistry(time.Nanosecond, 100)

	if !r.Claim("openai:cntr_a") {
		t.Fatal("first sighting should be claimed")
	}
	time.Sleep(time.Millisecond)
	// Past the window the container is assumed gone, so a new sighting is a new session.
	if !r.Claim("openai:cntr_a") {
		t.Fatal("expected the container to be reclaimable once its TTL elapsed")
	}
}

func TestContainerSessionRegistry_StaysWithinCapacity(t *testing.T) {
	r := NewContainerSessionRegistry(time.Hour, 8)

	for i := range 100 {
		r.Claim("openai:cntr_" + string(rune('a'+i%26)) + string(rune('0'+i/26)))
	}
	if got := r.Len(); got > 8 {
		t.Fatalf("registry length = %d, want at most 8", got)
	}
}

func TestContainerSessionRegistry_ConcurrentClaimsBillOnce(t *testing.T) {
	r := NewContainerSessionRegistry(time.Hour, 100)

	const goroutines = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	claims := 0

	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			if r.Claim("openai:cntr_race") {
				mu.Lock()
				claims++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if claims != 1 {
		t.Fatalf("concurrent claims on one container = %d, want exactly 1", claims)
	}
}
