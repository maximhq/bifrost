package utils

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestFirstStreamChunkPassthrough429RetryClassifier(t *testing.T) {
	src := make(chan *schemas.BifrostStreamChunk, 2)
	src <- &schemas.BifrostStreamChunk{BifrostPassthroughResponse: &schemas.BifrostPassthroughResponse{StatusCode: 429, Body: []byte(`{"error":"limited"}`)}}
	close(src)
	code := 429
	expected := &schemas.BifrostError{StatusCode: &code, Error: &schemas.ErrorField{Message: "retryable"}}
	got, done, err := CheckFirstStreamChunkForError(context.Background(), src, func(first *schemas.BifrostStreamChunk) *schemas.BifrostError {
		if first.BifrostPassthroughResponse != nil && first.BifrostPassthroughResponse.StatusCode == 429 {
			return expected
		}
		return nil
	})
	if got != nil || err != expected {
		t.Fatalf("first failure must remain internal: stream=%v err=%v", got, err)
	}
	<-done
}

func TestFirstStreamChunkPassthrough429BufferOverflowFailsClosed(t *testing.T) {
	first := &schemas.BifrostStreamChunk{BifrostPassthroughResponse: &schemas.BifrostPassthroughResponse{StatusCode: 429, Headers: map[string]string{"X-Origin": "kept"}, Body: []byte("first")}}
	rest := make(chan *schemas.BifrostStreamChunk, 1)
	lastBody := strings.Repeat("x", maxPassthrough429RetryBodyBytes)
	last := &schemas.BifrostStreamChunk{BifrostPassthroughResponse: &schemas.BifrostPassthroughResponse{StatusCode: 429, Body: []byte(lastBody)}}
	rest <- last
	close(rest)
	classified := false
	got, done, retryErr, handled := bufferPassthrough429ForRetry(context.Background(), first, rest, func(*schemas.BifrostStreamChunk) *schemas.BifrostError {
		classified = true
		return nil
	})
	if !handled || retryErr != nil || classified {
		t.Fatalf("overflow must bypass retry classification: handled=%v retryErr=%v classified=%v", handled, retryErr, classified)
	}
	var chunks []*schemas.BifrostStreamChunk
	for chunk := range got {
		chunks = append(chunks, chunk)
	}
	<-done
	if len(chunks) != 2 || chunks[0] != first || chunks[1] != last || chunks[0].BifrostPassthroughResponse.StatusCode != 429 || chunks[0].BifrostPassthroughResponse.Headers["X-Origin"] != "kept" || string(chunks[1].BifrostPassthroughResponse.Body) != lastBody {
		t.Fatalf("overflow did not preserve the original stream: chunks=%d", len(chunks))
	}
}

func TestFirstStreamChunkPassthrough429BufferOverflowCancellationDrainsTail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := &schemas.BifrostStreamChunk{BifrostPassthroughResponse: &schemas.BifrostPassthroughResponse{StatusCode: 429, Body: []byte("first")}}
	large := &schemas.BifrostStreamChunk{BifrostPassthroughResponse: &schemas.BifrostPassthroughResponse{StatusCode: 429, Body: []byte(strings.Repeat("x", maxPassthrough429RetryBodyBytes))}}
	// Keep the tail send active until the forwarder starts draining it.
	rest := make(chan *schemas.BifrostStreamChunk)
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		rest <- large
		rest <- &schemas.BifrostStreamChunk{}
		close(rest)
	}()

	got, done, err := CheckFirstStreamChunkForError(ctx, streamWithFirst(first, rest), func(*schemas.BifrostStreamChunk) *schemas.BifrostError { return nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The first send is intentionally left unread. Cancellation must abandon
	// output forwarding and drain the active source producer.
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("overflow forwarder did not finish after cancellation")
	}
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("overflow forwarder did not drain the source tail")
	}
	for range got {
	}
}

func streamWithFirst(first *schemas.BifrostStreamChunk, rest chan *schemas.BifrostStreamChunk) chan *schemas.BifrostStreamChunk {
	stream := make(chan *schemas.BifrostStreamChunk)
	go func() {
		defer close(stream)
		stream <- first
		for chunk := range rest {
			stream <- chunk
		}
	}()
	return stream
}

func TestFirstStreamChunkPassthroughNoRetryPreservesRawStream(t *testing.T) {
	src := make(chan *schemas.BifrostStreamChunk, 2)
	original := &schemas.BifrostStreamChunk{BifrostPassthroughResponse: &schemas.BifrostPassthroughResponse{StatusCode: 429, Headers: map[string]string{"x-should-retry": "false"}, Body: []byte("opaque")}}
	src <- original
	close(src)
	got, done, err := CheckFirstStreamChunkForError(context.Background(), src, func(first *schemas.BifrostStreamChunk) *schemas.BifrostError {
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	first, ok := <-got
	if !ok || first != original {
		t.Fatalf("raw error must remain visible to caller: chunk=%v ok=%v", first, ok)
	}
	for range got {
	}
	<-done
}
