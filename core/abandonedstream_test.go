package bifrost

import (
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestDrainAbandonedStream_UnblocksProducer covers the stream the caller never
// receives: the worker's 5s delivery timeout fires, or the client context is
// done, and the stream is dropped.
//
// The provider goroutine on the other end is still sending. GateSendChunk only
// escapes on ctx.Done(), so on the timeout path, where the context is very much
// alive, it blocks forever on a channel nobody reads. It never reaches its
// deferred ReleaseStreamingResponse, so its upstream connection is never
// returned and one slot of MaxConnsPerHost is burned permanently.
func TestDrainAbandonedStream_UnblocksProducer(t *testing.T) {
	t.Parallel()

	// Buffer smaller than the number of chunks, so the producer blocks unless
	// something consumes. This is the provider goroutine's shape.
	stream := make(chan *schemas.BifrostStreamChunk, 2)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer close(stream)
		for range 10 {
			stream <- &schemas.BifrostStreamChunk{}
		}
	}()

	drainAbandonedStream(stream)

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("provider goroutine still blocked on an abandoned stream; " +
			"its connection would never be released")
	}
}

// TestDrainAbandonedStream_NilIsSafe guards the delivery path, which can reach
// the abandonment branches with no stream to drain.
func TestDrainAbandonedStream_NilIsSafe(t *testing.T) {
	t.Parallel()
	drainAbandonedStream(nil)
}
