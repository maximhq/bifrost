package utils

import (
	"context"
	"testing"

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
