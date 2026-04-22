package bifrost

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestPluginPipelineStreamingRace reproduces the production panic:
//
//	fatal error: concurrent map read and map write
//	(*PluginPipeline).FinalizeStreamingPostHookSpans
//
// It hammers accumulatePluginTiming (per-chunk writer) concurrently with
// FinalizeStreamingPostHookSpans (end-of-stream reader) and resetPluginPipeline
// (pool-release writer). Before the streamingMu fix these three paths had no
// synchronisation and the -race detector / runtime map check would trip
// immediately. Run with: go test -race -run PluginPipelineStreamingRace
func TestPluginPipelineStreamingRace(t *testing.T) {
	p := &PluginPipeline{
		logger: NewDefaultLogger(schemas.LogLevelError),
		tracer: &schemas.NoOpTracer{},
	}

	const writers = 8
	const iterations = 2000

	var wg sync.WaitGroup

	// Per-chunk accumulator writers — simulate multiple plugins accumulating
	// timing for every streamed chunk.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			pluginName := fmt.Sprintf("plugin-%d", id%3) // a few distinct plugin keys
			for i := 0; i < iterations; i++ {
				p.accumulatePluginTiming(pluginName, time.Microsecond, i%17 == 0)
			}
		}(w)
	}

	// End-of-stream finalizer racing with writers.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx := context.Background()
		for i := 0; i < iterations/10; i++ {
			p.FinalizeStreamingPostHookSpans(ctx)
		}
	}()

	// resetPluginPipeline racing with writers — simulates the pool returning
	// the pipeline to another request mid-flight.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations/10; i++ {
			p.resetPluginPipeline()
		}
	}()

	// Concurrent GetChunkCount readers.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = p.GetChunkCount()
		}
	}()

	wg.Wait()
}
