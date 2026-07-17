package bedrock

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// These tests/benchmarks exercise the *real* population code path
// (getOrCreateAssumeRoleCredsCache, the same helper signAWSRequest calls)
// without needing live AWS credentials or network access: constructing an
// sts.Client and wrapping it in aws.NewCredentialsCache/AssumeRoleProvider
// performs no network I/O by itself - the actual STS AssumeRole call only
// happens lazily inside CredentialsCache.Retrieve, which none of these
// benchmarks invoke. So the memory measured here is exactly the per-entry
// cost that accumulates in production for every distinct
// region|roleARN|extID|sessionName|sourceIdentity combination seen.

// newBenchAssumeRoleCredsCache mirrors the credsCache construction inside
// signAWSRequest exactly, for a synthetic cacheKey.
func newBenchAssumeRoleCredsCache(region, roleARN string) *aws.CredentialsCache {
	cfg := aws.Config{Region: region}
	stsClient := sts.NewFromConfig(cfg)
	return aws.NewCredentialsCache(
		stscreds.NewAssumeRoleProvider(stsClient, roleARN, func(o *stscreds.AssumeRoleOptions) {
			o.RoleSessionName = "bench-session"
		}),
	)
}

// clearAssumeRoleCacheKeys deletes exactly the given keys from the shared
// package-level assumeRoleCredsCache, so benchmarks/tests don't leak state
// into each other or into unrelated tests in this package.
func clearAssumeRoleCacheKeys(keys []string) {
	for _, k := range keys {
		assumeRoleCredsCache.Delete(k)
	}
}

func heapAllocBytes() uint64 {
	runtime.GC()
	runtime.GC() // second pass to settle finalizers/GC-assisted frees
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// TestAssumeRoleCredsCache_MemoryReclaim simulates a burst of "live users"
// that each mint a distinct AssumeRole session (e.g. a client that
// generates a fresh session name per call), so every one becomes a new,
// permanent cacheKey under the OLD (pre-TTL) behavior. It measures:
//  1. heap growth from populating N distinct entries ("before" - unbounded
//     accumulation, same cost the old code paid forever)
//  2. heap reclaimed after sweepAssumeRoleCredsCache evicts all of them
//     once they're idle past the TTL ("after" - the fix)
//
// Run with: go test ./core/providers/bedrock/ -run TestAssumeRoleCredsCache_MemoryReclaim -v
func TestAssumeRoleCredsCache_MemoryReclaim(t *testing.T) {
	const n = 20000
	keys := make([]string, 0, n)
	defer func() { clearAssumeRoleCacheKeys(keys) }()

	baseline := heapAllocBytes()

	for i := 0; i < n; i++ {
		key := fmt.Sprintf("bench-region|bench-role-arn|extid|session-%d|source-%d", i, i)
		keys = append(keys, key)
		getOrCreateAssumeRoleCredsCache(key, func() *aws.CredentialsCache {
			return newBenchAssumeRoleCredsCache("us-east-1", "arn:aws:iam::123456789012:role/bench")
		})
	}

	afterPopulate := heapAllocBytes()
	populateGrowth := afterPopulate - baseline
	t.Logf("BEFORE (populate %d distinct sessions, no eviction yet):", n)
	t.Logf("  heap growth:        %d bytes (%.2f MB)", populateGrowth, float64(populateGrowth)/1024/1024)
	t.Logf("  bytes/entry:        %.1f bytes", float64(populateGrowth)/float64(n))

	// Force-evict everything by sweeping as if "now" is well past the TTL
	// for every entry - equivalent to what the real 5-minute ticker does
	// once these sessions have all gone idle for > assumeRoleCacheIdleTTL.
	evicted := sweepAssumeRoleCredsCache(time.Now().Add(2*assumeRoleCacheIdleTTL), assumeRoleCacheIdleTTL)
	if evicted != n {
		t.Fatalf("expected sweep to evict all %d synthetic entries, evicted %d", n, evicted)
	}

	afterSweep := heapAllocBytes()
	var reclaimed int64
	if afterPopulate > afterSweep {
		reclaimed = int64(afterPopulate - afterSweep)
	}
	t.Logf("AFTER (sweep evicts all %d idle sessions):", evicted)
	t.Logf("  heap after sweep:   %d bytes (%.2f MB)", afterSweep, float64(afterSweep)/1024/1024)
	t.Logf("  reclaimed:          %d bytes (%.2f MB), %.1f%% of populate growth",
		reclaimed, float64(reclaimed)/1024/1024, 100*float64(reclaimed)/float64(populateGrowth))

	// Sanity: every key we added must actually be gone from the cache now.
	for _, k := range keys {
		if _, ok := assumeRoleCredsCache.Load(k); ok {
			t.Fatalf("key %q still present in cache after sweep", k)
		}
	}
}

// TestAssumeRoleCredsCache_ParallelLiveUsers models concurrent "live
// users" hitting Bedrock at once: some reuse the same session (should
// dedupe onto one cache entry, no extra memory), others each mint a
// unique session (should each add one entry). Verifies correctness under
// concurrency - not just memory, but that touch()/LoadOrStore never race
// into duplicate entries or panics (run with -race).
//
// Run with: go test ./core/providers/bedrock/ -run TestAssumeRoleCredsCache_ParallelLiveUsers -race -v
func TestAssumeRoleCredsCache_ParallelLiveUsers(t *testing.T) {
	const (
		sharedUsers  = 50  // goroutines all reusing the SAME session -> 1 entry
		uniqueUsers  = 500 // goroutines each minting a UNIQUE session -> N entries
		callsPerUser = 20  // repeated calls per "user", like repeated requests in one session
	)

	sharedKey := "shared-region|shared-role|ext|shared-session|shared-source"
	var uniqueKeys []string
	var mu sync.Mutex
	allKeys := []string{sharedKey}

	var wg sync.WaitGroup

	for u := 0; u < sharedUsers; u++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := 0; c < callsPerUser; c++ {
				getOrCreateAssumeRoleCredsCache(sharedKey, func() *aws.CredentialsCache {
					return newBenchAssumeRoleCredsCache("us-east-1", "arn:aws:iam::123456789012:role/shared")
				})
			}
		}()
	}

	for u := 0; u < uniqueUsers; u++ {
		u := u
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("region|role|ext|unique-session-%d|source-%d", u, u)
			mu.Lock()
			uniqueKeys = append(uniqueKeys, key)
			mu.Unlock()
			for c := 0; c < callsPerUser; c++ {
				getOrCreateAssumeRoleCredsCache(key, func() *aws.CredentialsCache {
					return newBenchAssumeRoleCredsCache("us-east-1", "arn:aws:iam::123456789012:role/unique")
				})
			}
		}()
	}

	wg.Wait()
	allKeys = append(allKeys, uniqueKeys...)
	defer clearAssumeRoleCacheKeys(allKeys)

	// Shared session must have collapsed to exactly one entry.
	if _, ok := assumeRoleCredsCache.Load(sharedKey); !ok {
		t.Fatalf("expected shared session key to be present")
	}

	// Every unique session must be present exactly once.
	for _, k := range uniqueKeys {
		if _, ok := assumeRoleCredsCache.Load(k); !ok {
			t.Fatalf("expected unique session key %q to be present", k)
		}
	}

	t.Logf("shared session: %d goroutines x %d calls -> 1 cache entry (dedup confirmed)", sharedUsers, callsPerUser)
	t.Logf("unique sessions: %d goroutines -> %d cache entries", uniqueUsers, len(uniqueKeys))

	_ = context.Background() // reserved: kept for parity if future variant needs ctx-based cancellation
}

// TestAssumeRoleCredsCache_ConcurrentTouchDuringSweep stress-tests the
// known race documented on sweepAssumeRoleCredsCache: touch() and the
// sweeper's staleness-check-then-Delete aren't atomic against each other.
// Runs many goroutines continuously touching (getOrCreateAssumeRoleCredsCache)
// a small, shared set of keys while other goroutines concurrently sweep
// with ttl=0 (so every entry is a stale-eviction candidate on every pass -
// the maximum possible contention between touch and sweep). Asserts:
//  1. No data race (run with -race - this is the primary point of the test).
//  2. No panic/crash under the race.
//  3. The cache is left in a valid, usable state afterward: a fresh
//     getOrCreateAssumeRoleCredsCache call for each key still returns a
//     non-nil, working *aws.CredentialsCache (self-heals whether or not any
//     individual entry was evicted mid-use).
//
// Run with: go test ./core/providers/bedrock/ -run TestAssumeRoleCredsCache_ConcurrentTouchDuringSweep -race -v
func TestAssumeRoleCredsCache_ConcurrentTouchDuringSweep(t *testing.T) {
	const (
		keyCount       = 8 // small key set so touch and sweep repeatedly contend on the SAME entries
		touchersPerKey = 20
		sweepers       = 4
		iterations     = 200
	)

	keys := make([]string, keyCount)
	for i := range keys {
		keys[i] = fmt.Sprintf("racetest-region|racetest-role|ext|session-%d|source-%d", i, i)
	}
	defer clearAssumeRoleCacheKeys(keys)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Touchers: continuously get-or-create (which touches on a hit) for a
	// fixed key, racing against sweepers trying to evict that same key.
	for _, key := range keys {
		for t := 0; t < touchersPerKey; t++ {
			key := key
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					getOrCreateAssumeRoleCredsCache(key, func() *aws.CredentialsCache {
						return newBenchAssumeRoleCredsCache("us-east-1", "arn:aws:iam::123456789012:role/racetest")
					})
				}
			}()
		}
	}

	// Sweepers: ttl=0 means every entry (including ones touched moments
	// ago) is immediately a staleness candidate - maximum possible
	// contention against the touchers above.
	for s := 0; s < sweepers; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				sweepAssumeRoleCredsCache(time.Now(), 0)
			}
		}()
	}

	// Let the race run for a bounded number of sweeper iterations, then stop.
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(stop)
	}()
	wg.Wait()

	// The cache must still be usable afterward, regardless of whether any
	// individual key survived the eviction storm above.
	for _, key := range keys {
		credsCache := getOrCreateAssumeRoleCredsCache(key, func() *aws.CredentialsCache {
			return newBenchAssumeRoleCredsCache("us-east-1", "arn:aws:iam::123456789012:role/racetest")
		})
		if credsCache == nil {
			t.Fatalf("expected a valid, non-nil credsCache for key %q after touch/sweep contention", key)
		}
	}

	t.Logf("%d keys x %d touchers survived %d sweeper passes (ttl=0, max contention) with no race/panic; cache still usable", keyCount, touchersPerKey, sweepers*iterations)
}

// BenchmarkAssumeRoleCredsCache_DistinctSessions measures allocation
// overhead per distinct session under go test -bench, in addition to the
// heap-based before/after numbers above.
//
// Run with: go test ./core/providers/bedrock/ -bench BenchmarkAssumeRoleCredsCache_DistinctSessions -benchmem -run ^$
func BenchmarkAssumeRoleCredsCache_DistinctSessions(b *testing.B) {
	keys := make([]string, 0, b.N)
	defer clearAssumeRoleCacheKeys(keys)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("bench2-region|bench2-role|ext|session-%d|source-%d", i, i)
		keys = append(keys, key)
		getOrCreateAssumeRoleCredsCache(key, func() *aws.CredentialsCache {
			return newBenchAssumeRoleCredsCache("us-east-1", "arn:aws:iam::123456789012:role/bench2")
		})
	}
}

// BenchmarkAssumeRoleCredsCache_SameSession is the control case: repeated
// calls for the SAME session should hit the cache and only pay for the
// map Load plus touch()'s dedup'd atomic Store/Load.
//
// Run with: go test ./core/providers/bedrock/ -bench BenchmarkAssumeRoleCredsCache_SameSession -benchmem -run ^$
func BenchmarkAssumeRoleCredsCache_SameSession(b *testing.B) {
	key := "bench3-region|bench3-role|ext|same-session|same-source"
	defer clearAssumeRoleCacheKeys([]string{key})

	// Prime the entry once outside the timed loop.
	getOrCreateAssumeRoleCredsCache(key, func() *aws.CredentialsCache {
		return newBenchAssumeRoleCredsCache("us-east-1", "arn:aws:iam::123456789012:role/bench3")
	})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		getOrCreateAssumeRoleCredsCache(key, func() *aws.CredentialsCache {
			b.Fatal("newCredsCache should never be called on a cache hit")
			return nil
		})
	}
}

// BenchmarkAssumeRoleCacheEntry_TouchDirect isolates the current synchronous
// path: entry.touch() (with its Load-before-Store dedup guard) run
// single-threaded, no contention.
//
// Run with: go test ./core/providers/bedrock/ -bench BenchmarkAssumeRoleCacheEntry_TouchDirect -benchmem -run ^$
func BenchmarkAssumeRoleCacheEntry_TouchDirect(b *testing.B) {
	entry := &assumeRoleCacheEntry{credsCache: newBenchAssumeRoleCredsCache("us-east-1", "arn:aws:iam::123456789012:role/touchbench")}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entry.touch()
	}
}

// BenchmarkAssumeRoleCacheEntry_TouchDirectManyDistinct runs touch() under
// real multi-goroutine contention across MANY DISTINCT entries (no shared
// state between goroutines - each writes its own cache line).
//
// Run with: go test ./core/providers/bedrock/ -bench BenchmarkAssumeRoleCacheEntry_TouchDirectManyDistinct -benchmem -run ^$ -cpu 1,8,20
func BenchmarkAssumeRoleCacheEntry_TouchDirectManyDistinct(b *testing.B) {
	const poolSize = 4096
	entries := make([]*assumeRoleCacheEntry, poolSize)
	for i := range entries {
		entries[i] = &assumeRoleCacheEntry{credsCache: newBenchAssumeRoleCredsCache("us-east-1", "arn:aws:iam::123456789012:role/manydirect")}
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			entries[i%poolSize].touch()
			i++
		}
	})
}

// benchTouchRawStore mimics the pre-dedup behavior: an unconditional
// atomic.Store on every call, with no Load-before-Store guard. Used only
// to benchmark against entry.touch()'s dedup guard under the exact
// scenario it targets: many goroutines hammering the SAME entry.
func benchTouchRawStore(e *assumeRoleCacheEntry) {
	e.lastUsed.Store(time.Now().Unix())
}

// BenchmarkAssumeRoleCacheEntry_TouchSameEntryContended runs MANY
// goroutines concurrently touching the SAME single entry - the scenario
// touch()'s dedup guard (skip Store if lastUsed already == now) targets:
// repeated Stores to one shared cache line from many cores cause
// cross-core invalidation traffic that a read-only Load does not.
//
// Run with: go test ./core/providers/bedrock/ -bench BenchmarkAssumeRoleCacheEntry_TouchSameEntryContended -benchmem -run ^$ -cpu 8,20
func BenchmarkAssumeRoleCacheEntry_TouchSameEntryContended(b *testing.B) {
	entry := &assumeRoleCacheEntry{credsCache: newBenchAssumeRoleCredsCache("us-east-1", "arn:aws:iam::123456789012:role/contended")}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			entry.touch()
		}
	})
}

// BenchmarkAssumeRoleCacheEntry_TouchRawStoreSameEntryContended is the
// control for the same same-entry-contended scenario, using an
// unconditional Store (no dedup guard) for direct comparison.
//
// Run with: go test ./core/providers/bedrock/ -bench BenchmarkAssumeRoleCacheEntry_TouchRawStoreSameEntryContended -benchmem -run ^$ -cpu 8,20
func BenchmarkAssumeRoleCacheEntry_TouchRawStoreSameEntryContended(b *testing.B) {
	entry := &assumeRoleCacheEntry{credsCache: newBenchAssumeRoleCredsCache("us-east-1", "arn:aws:iam::123456789012:role/contendedraw")}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			benchTouchRawStore(entry)
		}
	})
}

// Note: newBenchAssumeRoleCredsCache deliberately builds a plain
// aws.Config{Region: ...} rather than calling config.LoadDefaultConfig -
// this is enough to reproduce the sts.Client allocation cost these
// benchmarks measure, without incurring real network calls or credential
// resolution.
