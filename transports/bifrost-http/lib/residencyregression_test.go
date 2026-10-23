package lib

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestResidencyAdapterSelection(t *testing.T) {
	for _, adapter := range []schemas.ResidencyAdapter{schemas.ResidencyAdapterAuto, schemas.ResidencyAdapterOllama} {
		t.Run(string(adapter), func(t *testing.T) {
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				switch r.URL.Path {
				case "/v1/models/status":
					http.NotFound(w, r)
				case "/v1/models":
					fmt.Fprint(w, `{"data":[{"id":"cold"},{"id":"warm"}]}`)
				case "/api/ps":
					fmt.Fprint(w, `{"models":[{"name":"warm"}]}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			target := &residencyTarget{provider: "test", adapter: adapter, baseURL: server.URL, allowPrivate: true}
			snapshot, detected, err := probeResidency(t.Context(), target, "", server.Client())
			if err != nil || detected != schemas.ResidencyAdapterOllama {
				t.Fatalf("adapter=%s err=%v paths=%v", detected, err, paths)
			}
			if snapshot.StateOf("warm") != schemas.ResidencyReady || snapshot.StateOf("cold") != schemas.ResidencyUnloaded {
				t.Fatalf("wrong states: %+v", snapshot)
			}
			if adapter == schemas.ResidencyAdapterOllama && len(paths) != 1 {
				t.Fatalf("explicit adapter probed %v", paths)
			}
		})
	}
}

func TestResidencyExplicitNeverFallsBack(t *testing.T) {
	for _, adapter := range []schemas.ResidencyAdapter{schemas.ResidencyAdapterOMLX, schemas.ResidencyAdapterLlamaCPP, schemas.ResidencyAdapterOllama} {
		t.Run(string(adapter), func(t *testing.T) {
			hits := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; http.NotFound(w, r) }))
			defer server.Close()
			target := &residencyTarget{adapter: adapter, baseURL: server.URL, allowPrivate: true}
			if _, _, err := probeResidency(t.Context(), target, "", server.Client()); err == nil {
				t.Fatal("expected error")
			}
			if hits != 1 {
				t.Fatalf("explicit adapter made %d probes", hits)
			}
		})
	}
}

func TestResidencyRejectsMissingStatus(t *testing.T) {
	for _, body := range []string{`{"data":[{"id":"a"}]}`, `{"data":[{"id":"a","status":{"value":"unexpected"}}]}`, `{"models":[{"id":"a","loaded":"true"}]}`} {
		if err := parseStatusList(decode(t, body), nil, newSnapshot()); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestResidencyExplicitPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom-status" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"models":[{"id":"a","loaded":true}]}`)
	}))
	defer server.Close()
	target := &residencyTarget{adapter: schemas.ResidencyAdapterOMLX, baseURL: server.URL, path: "/custom-status", allowPrivate: true}
	snapshot, _, err := probeResidency(t.Context(), target, schemas.ResidencyAdapterOllama, server.Client())
	if err != nil || snapshot.StateOf("a") != schemas.ResidencyReady {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestResidencyCoalescesZeroTTL(t *testing.T) {
	var hits atomic.Int32
	entered := make(chan struct{}, 10)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		entered <- struct{}{}
		<-release
		fmt.Fprint(w, `{"models":[{"name":"warm"}]}`)
	}))
	defer server.Close()
	service := NewVirtualModelService(nil, nil)
	target := &residencyTarget{provider: "test", adapter: schemas.ResidencyAdapterOllama, baseURL: server.URL, allowPrivate: true, timeout: 5 * time.Second}
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	first := make(chan *schemas.ResidencySnapshot, 1)
	go func() { first <- service.snapshot(ctx, target) }()
	<-entered
	// A canceled waiter must not cancel the in-flight probe.
	canceled := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	canceled.Cancel()
	if got := service.snapshot(canceled, target); got.Authoritative {
		t.Fatal("canceled waiter was authoritative")
	}
	const count = 20
	var wg sync.WaitGroup
	results := make(chan *schemas.ResidencySnapshot, count)
	wg.Add(count)
	for i := 0; i < count; i++ {
		go func() { defer wg.Done(); results <- service.snapshot(ctx, target) }()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	original := <-first
	wg.Wait()
	close(results)
	for result := range results {
		if result != original {
			t.Fatal("waiter did not share probe result")
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("got %d probes, want 1", hits.Load())
	}
	service.snapshot(ctx, target)
	if hits.Load() != 2 {
		t.Fatalf("sequential zero-TTL call reused cache: %d", hits.Load())
	}
}

func TestResidencyFailedRefreshKeepsObservation(t *testing.T) {
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"models":[{"name":"warm"}]}`)
	}))
	defer server.Close()
	service := NewVirtualModelService(nil, nil)
	target := &residencyTarget{provider: "test", adapter: schemas.ResidencyAdapterOllama, baseURL: server.URL, allowPrivate: true, timeout: time.Second}
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	first := service.snapshot(ctx, target)
	fail.Store(true)
	second := service.snapshot(ctx, target)
	if first != second || !second.Authoritative || first.ObservedAt != second.ObservedAt {
		t.Fatal("failed refresh changed successful snapshot")
	}
	empty := NewVirtualModelService(nil, nil).snapshot(ctx, target)
	if empty.Authoritative {
		t.Fatal("failed initial probe was authoritative")
	}
	vm := &schemas.VirtualModel{Name: "test", Provider: "test", Models: []string{"warm"}}
	if _, err := schemas.SelectVirtualModelTarget(vm, empty); err == nil || *err.StatusCode != 502 {
		t.Fatalf("want 502, got %v", err)
	}
}

func BenchmarkResidencyCached(b *testing.B) {
	service := NewVirtualModelService(nil, nil)
	target := &residencyTarget{provider: "test", baseURL: "http://localhost", slotKey: "test|http://localhost"}
	slot := service.slotFor(target)
	slot.snapshot = &schemas.ResidencySnapshot{Authoritative: true}
	slot.expiresAt = time.Now().Add(time.Hour)
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		service.snapshot(ctx, target)
	}
}
