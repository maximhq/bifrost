package logging

import (
	"context"
	"fmt"
	"gorm.io/gorm"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

type casStressLogger struct {
	testLogger
	warnings atomic.Int64
}

func (l *casStressLogger) Warn(format string, args ...interface{}) { l.warnings.Add(1) }

func TestCASConcurrentMaintenanceSaturatedQueue(t *testing.T) {
	audit := &casStressLogger{}
	s, err := logstore.NewLogStore(context.Background(), &logstore.Config{Enabled: true, Type: logstore.LogStoreTypeSQLite, Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "stress.db")}, ContentAddressed: &logstore.ContentAddressedConfig{Enabled: true, MinFieldBytes: 32, MinChunkBytes: 32}}, audit)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })
	db := s.(*logstore.CasLogStore).LogStore.(interface {
		ScopedDB(context.Context) *gorm.DB
	}).ScopedDB(context.Background())
	require.NoError(t, db.Exec("INSERT INTO cas_blobs(hash,codec,orig_len,data,created_at) VALUES('stress-orphan','zstd',0,X'',0)").Error)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	const rows = 160
	const turns = 96
	makeRow := func(i int) *logstore.Log {
		history := make([]schemas.ChatMessage, turns)
		for j := range history {
			history[j] = schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(fmt.Sprintf("session%d-turn%d-", i/8, j) + strings.Repeat("large-history-", 100))}}
		}
		return &logstore.Log{ID: fmt.Sprintf("stress-%d", i), Timestamp: time.Now(), Status: "success", Model: "test", InputHistoryParsed: history, TotalTokens: 1234, Cost: schemas.Ptr(0.123)}
	}
	started := time.Now()
	for i := 0; i < rows; i++ {
		require.NoError(t, s.Create(ctx, makeRow(i)))
	}
	// A full queue deterministically routes all following submissions through
	// the production synchronous fallback in their bounded caller goroutines.
	p := &LoggerPlugin{ctx: ctx, store: s, logger: testLogger{}, writeQueue: make(chan *writeQueueEntry, 8)}
	var callbacks sync.WaitGroup
	callbacks.Add(rows)
	for i := 0; i < 8; i++ {
		p.enqueueLogEntry(makeRow(rows+i), func(*logstore.Log) { callbacks.Done() })
	}
	require.Len(t, p.writeQueue, 8)
	var workers sync.WaitGroup
	start := make(chan struct{})
	entered := make(chan struct{})
	release := make(chan struct{})
	var arrivals sync.WaitGroup
	arrivals.Add(8)
	var arrived atomic.Int64
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("stress_writer_barrier", func(tx *gorm.DB) {
		if tx.Statement.Table == "logs" && arrived.Add(1) <= 8 {
			arrivals.Done()
			<-release
		}
	}))
	defer db.Callback().Create().Remove("stress_writer_barrier")
	var once sync.Once
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("stress_gc_barrier", func(tx *gorm.DB) {
		if tx.Statement.Table == "cas_inventories" {
			once.Do(func() { close(entered); <-release })
		}
	}))
	defer db.Callback().Delete().Remove("stress_gc_barrier")
	failures := make(chan error, 32)
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(w int) {
			defer workers.Done()
			<-start
			for i := 8 + w; i < rows; i += 8 {
				p.enqueueLogEntry(makeRow(rows+i), func(*logstore.Log) { callbacks.Done() })
			}
		}(worker)
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		for i := 0; i < 8; i++ {
			if err := s.Flush(ctx, time.Now().Add(-24*time.Hour)); err != nil {
				failures <- err
				return
			}
			if _, err := s.DeleteLogsBatch(ctx, time.Now().Add(-24*time.Hour), 50); err != nil {
				failures <- err
				return
			}
			id := fmt.Sprintf("temporary-%d", i)
			row := makeRow(0)
			row.ID = id
			if err := s.Create(ctx, row); err != nil {
				failures <- err
				return
			}
			if err := s.Update(ctx, id, map[string]interface{}{"cost": 0.25}); err != nil {
				failures <- err
				return
			}
			if err := s.DeleteLog(ctx, id); err != nil {
				failures <- err
				return
			}
		}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(start)
	arrivals.Wait()
	close(release)
	workers.Wait()
	for len(p.writeQueue) > 0 {
		p.processBatch([]*writeQueueEntry{<-p.writeQueue})
	}
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.Zero(t, p.droppedRequests.Load())
	require.Zero(t, audit.warnings.Load(), "maintenance must not swallow GC failures")
	var orphans int64
	require.NoError(t, db.Table("cas_blobs").Where("hash = ?", "stress-orphan").Count(&orphans).Error)
	require.Zero(t, orphans, "full sweep must actually reclaim the unique orphan")
	require.Equal(t, int64(rows-8), p.queueFullSyncPersists.Load())
	done := make(chan struct{})
	go func() { callbacks.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for i := 0; i < 2*rows; i++ {
		got, err := s.FindByID(ctx, fmt.Sprintf("stress-%d", i))
		require.NoError(t, err)
		want := makeRow(i)
		require.Equal(t, want.InputHistoryParsed, got.InputHistoryParsed)
		require.Equal(t, want.TotalTokens, got.TotalTokens)
		require.Equal(t, want.Cost, got.Cost)
		require.NotContains(t, got.MetadataParsed, "logging_payload_status")
	}
	require.Less(t, time.Since(started), 90*time.Second)
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	t.Logf("submitted=%d full_hydrated=%d dropped=%d synchronous=%d queue_capacity=%d producer_limit=8 elapsed=%s heap_alloc=%d heap_sys=%d", 2*rows, 2*rows, p.droppedRequests.Load(), p.queueFullSyncPersists.Load(), cap(p.writeQueue), time.Since(started), memory.HeapAlloc, memory.HeapSys)
}
