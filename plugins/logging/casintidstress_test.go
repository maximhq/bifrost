package logging

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestCasIntIDHighConcurrencySoak drives the production write-queue shape
// (capacity 8, single-entry batches, bounded-producer synchronous fallback
// when full) with 24 conversation writers whose histories grow every round —
// the quadratic ref-edge pattern of agent traffic, all edges resolved through
// integer blob ids — while a maintenance goroutine continuously sweeps,
// batch-deletes and churns temporary logs. Asserts: zero dropped logs, zero
// warnings, every live log hydrates byte-identically, and the ref graph stays
// closed.
func TestCasIntIDHighConcurrencySoak(t *testing.T) {
	audit := &casStressLogger{}
	s, err := logstore.NewLogStore(context.Background(), &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config:  &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "soak.db")},
		ContentAddressed: &logstore.ContentAddressedConfig{
			Enabled: true, MinFieldBytes: 32, MinChunkBytes: 32,
		},
	}, audit)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })
	db := s.(*logstore.CasLogStore).LogStore.(interface {
		ScopedDB(context.Context) *gorm.DB
	}).ScopedDB(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	p := &LoggerPlugin{ctx: ctx, store: s, logger: testLogger{},
		writeQueue: make(chan *writeQueueEntry, 8)}
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			entry, ok := <-p.writeQueue
			if !ok {
				return
			}
			p.processBatch([]*writeQueueEntry{entry})
		}
	}()

	const workers = 24
	const rounds = 40
	const keepAlive = 5

	msg := func(text string) schemas.ChatMessage {
		return schemas.ChatMessage{Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text)}}
	}
	// Identical across conversations: high fan-in targets on the reverse index.
	shared := strings.Repeat("shared project context ", 25)
	makeRow := func(w, r int) *logstore.Log {
		history := make([]schemas.ChatMessage, 0, 2*r+2)
		history = append(history, msg(shared))
		for j := 0; j < 2*r; j++ {
			history = append(history, msg(fmt.Sprintf("conv%d turn%d ", w, j)+strings.Repeat("history-", 30)))
		}
		history = append(history, msg(fmt.Sprintf("conv%d round%d unique ", w, r)+strings.Repeat("prompt-", 60)))
		return &logstore.Log{
			ID: fmt.Sprintf("soak-%d-%d", w, r), Timestamp: time.Now(),
			Status: "success", Model: "test",
			InputHistoryParsed: history, TotalTokens: 1234, Cost: schemas.Ptr(0.123),
		}
	}

	var latMu sync.Mutex
	var verified, deleted, maintenance atomic.Int64
	latencies := make([]time.Duration, 0, workers*rounds)
	errs := make(chan error, workers*rounds*2)
	var wg sync.WaitGroup
	stopMaint := make(chan struct{})
	var maintWG sync.WaitGroup

	// Maintenance goroutine: continuous GC sweeps plus create/update/delete
	// churn, overlapping the writers the whole time.
	maintWG.Add(1)
	go func() {
		defer maintWG.Done()
		for i := 0; ; i++ {
			select {
			case <-stopMaint:
				return
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			if err := s.Flush(ctx, time.Now().Add(-24*time.Hour)); err != nil {
				errs <- fmt.Errorf("flush: %w", err)
				return
			}
			if _, err := s.DeleteLogsBatch(ctx, time.Now().Add(-24*time.Hour), 50); err != nil {
				errs <- fmt.Errorf("batch delete: %w", err)
				return
			}
			id := fmt.Sprintf("soak-temp-%d", i)
			row := makeRow(0, 2)
			row.ID = id
			if err := s.Create(ctx, row); err != nil {
				errs <- fmt.Errorf("temp create: %w", err)
				return
			}
			if err := s.Update(ctx, id, map[string]interface{}{"cost": 0.25}); err != nil {
				errs <- fmt.Errorf("temp update: %w", err)
				return
			}
			if err := s.DeleteLog(ctx, id); err != nil {
				errs <- fmt.Errorf("temp delete: %w", err)
				return
			}
			maintenance.Add(1)
		}
	}()

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				row := makeRow(w, r)
				t0 := time.Now()
				done := make(chan struct{})
				p.enqueueLogEntry(row, func(*logstore.Log) {
					latMu.Lock()
					latencies = append(latencies, time.Since(t0))
					latMu.Unlock()
					close(done)
				})
				select {
				case <-done:
				case <-ctx.Done():
					errs <- ctx.Err()
					return
				}
				got, err := s.FindByID(ctx, row.ID)
				if err != nil {
					errs <- fmt.Errorf("persisted accounting %s: %w", row.ID, err)
					return
				}
				if !assert.ObjectsAreEqual(row.InputHistoryParsed, got.InputHistoryParsed) || got.TotalTokens != row.TotalTokens || !assert.ObjectsAreEqual(row.Cost, got.Cost) {
					errs <- fmt.Errorf("persisted hydration mismatch %s", row.ID)
					return
				}
				verified.Add(1)
				// Delete an older round so gcForManifests churns shared and
				// private chunks concurrently with the other writers.
				if r >= keepAlive {
					if err := s.DeleteLog(ctx, fmt.Sprintf("soak-%d-%d", w, r-keepAlive)); err != nil {
						errs <- fmt.Errorf("delete old round: %w", err)
						return
					}
					deleted.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()
	close(stopMaint)
	maintWG.Wait()
	close(p.writeQueue)
	<-drainDone
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	require.Len(t, latencies, workers*rounds)
	require.EqualValues(t, workers*rounds, verified.Load())
	require.EqualValues(t, workers*(rounds-keepAlive), deleted.Load())
	require.Positive(t, maintenance.Load(), "maintenance must overlap workload")
	require.Positive(t, p.queueFullSyncPersists.Load(), "must exercise synchronous fallback")
	var live int64
	require.NoError(t, db.Model(&logstore.Log{}).Count(&live).Error)
	require.EqualValues(t, workers*keepAlive, live, "exact surviving rows, no temporary leftovers")
	for w := 0; w < workers; w++ {
		for r := 0; r < rounds-keepAlive; r++ {
			_, err := s.FindByID(ctx, fmt.Sprintf("soak-%d-%d", w, r))
			require.Error(t, err, "deleted row must be absent")
		}
	}
	t.Logf("accounting: expected=960 verified=%d deleted=%d live=%d maintenance_cycles=%d", verified.Load(), deleted.Load(), live, maintenance.Load())
	require.Zero(t, p.droppedRequests.Load(), "no request log may be dropped")
	require.Zero(t, audit.warnings.Load(), "maintenance must not emit warnings (locks, GC, payload)")

	// Every surviving log hydrates byte-identically.
	for w := 0; w < workers; w++ {
		for r := rounds - keepAlive; r < rounds; r++ {
			got, err := s.FindByID(ctx, fmt.Sprintf("soak-%d-%d", w, r))
			require.NoError(t, err)
			want := makeRow(w, r)
			require.Equal(t, want.InputHistoryParsed, got.InputHistoryParsed)
			require.Equal(t, want.TotalTokens, got.TotalTokens)
			require.Equal(t, want.Cost, got.Cost)
		}
	}

	// Ref graph closure on the integer layout.
	var dangling int64
	require.NoError(t, db.Raw("SELECT count(*) FROM cas_refs r WHERE NOT EXISTS (SELECT 1 FROM cas_blobs b WHERE b.id = r.target_id) OR NOT EXISTS (SELECT 1 FROM cas_blobs b WHERE b.id = r.owner_id)").Scan(&dangling).Error)
	require.Zero(t, dangling)
	var orphans int64
	require.NoError(t, db.Raw("SELECT count(*) FROM cas_refs r WHERE NOT EXISTS (SELECT 1 FROM cas_payloads p JOIN cas_blobs b ON b.hash = p.blob_hash WHERE b.id = r.owner_id)").Scan(&orphans).Error)
	require.Zero(t, orphans, "maintenance sweeps must have reclaimed every dead manifest's edges")

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	pct := func(q float64) time.Duration {
		if len(latencies) == 0 {
			return 0
		}
		return latencies[min(len(latencies)-1, int(float64(len(latencies))*q))]
	}
	var refs int64
	require.NoError(t, db.Raw("SELECT count(*) FROM cas_refs").Scan(&refs).Error)
	var pages, pageSize int64
	require.NoError(t, db.Raw("PRAGMA page_count").Scan(&pages).Error)
	require.NoError(t, db.Raw("PRAGMA page_size").Scan(&pageSize).Error)
	t.Logf("soak: writes=%d refs=%d db=%dMB sync_fallback=%d dropped=%d warnings=%d p50=%s p99=%s max=%s",
		len(latencies), refs, pages*pageSize/1024/1024,
		p.queueFullSyncPersists.Load(), p.droppedRequests.Load(), audit.warnings.Load(),
		pct(0.50), pct(0.99), latencies[len(latencies)-1])
}
