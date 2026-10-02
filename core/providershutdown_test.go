package bifrost

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func requireProviderShutdownError(t *testing.T, err *schemas.BifrostError, requestType schemas.RequestType) {
	t.Helper()
	require.NotNil(t, err)
	require.NotNil(t, err.StatusCode, "shutdown must carry a retryable HTTP status")
	require.Equal(t, http.StatusServiceUnavailable, *err.StatusCode)
	require.NotNil(t, err.Error)
	require.Equal(t, "provider is shutting down", err.Error.Message)
	require.NotNil(t, err.Error.Type)
	require.Equal(t, "provider_shutting_down", *err.Error.Type)
	require.False(t, err.IsBifrostError, "shutdown must preserve the existing error classification")
	require.Nil(t, err.AllowFallbacks, "provider shutdown must not disable fallbacks")
	require.Equal(t, schemas.OpenAI, err.ExtraFields.Provider)
	require.Equal(t, "test-model", err.ExtraFields.OriginalModelRequested)
	require.Equal(t, requestType, err.ExtraFields.RequestType)
}

func TestProviderShutdownRequests(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		requestType := schemas.ChatCompletionRequest
		if streaming {
			requestType = schemas.ChatCompletionStreamRequest
		}
		for _, closingBeforeRequest := range []bool{false, true} {
			name := string(requestType) + "/blocked-enqueue"
			if closingBeforeRequest {
				name = string(requestType) + "/already-closing"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					client := newStreamTestClient(t, NewMockAccount())
					pq := &ProviderQueue{queue: make(chan *ChannelMessage), done: make(chan struct{})}
					client.requestQueues.Store(schemas.OpenAI, pq)
					t.Cleanup(func() { client.requestQueues.Delete(schemas.OpenAI) })
					if closingBeforeRequest {
						pq.signalClosing()
					}
					errors := make(chan *schemas.BifrostError, 1)
					go func() {
						ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
						req := &schemas.BifrostRequest{
							RequestType: requestType,
							ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "test-model"},
						}
						if streaming {
							_, err := client.tryStreamRequest(ctx, req)
							errors <- err
						} else {
							_, err := client.tryRequest(ctx, req)
							errors <- err
						}
					}()
					// With no worker on this unbuffered queue, the producer must
					// block in enqueue before shutdown is signalled.
					synctest.Wait()
					pq.signalClosing()
					requireProviderShutdownError(t, <-errors, requestType)
				})
			})
		}
	}
}

func TestProviderShutdownDrain(t *testing.T) {
	client := &Bifrost{}
	pq := &ProviderQueue{queue: make(chan *ChannelMessage, 1), done: make(chan struct{})}
	msg := &ChannelMessage{
		BifrostRequest: schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "test-model"},
		},
		Context: schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		Err:     make(chan schemas.BifrostError, 1),
	}
	pq.queue <- msg
	pq.signalClosing()
	client.drainQueueWithErrors(pq)
	select {
	case err := <-msg.Err:
		requireProviderShutdownError(t, &err, schemas.ChatCompletionRequest)
	default:
		t.Fatal("queued request did not receive a shutdown error")
	}
}

func TestProviderShutdownWorkerDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &Bifrost{}
		pq := &ProviderQueue{done: make(chan struct{})}
		var workers sync.WaitGroup
		workers.Add(1)
		go client.requestWorker(nil, nil, pq, &workers)
		// Park the worker with dequeuing disabled. Its select captures the nil
		// queue, so closing done below deterministically enters the drain branch
		// even though requests are now queued. No provider call can occur.
		synctest.Wait()
		pq.queue = make(chan *ChannelMessage, 2)
		messages := make([]*ChannelMessage, 0, 2)
		for _, requestType := range []schemas.RequestType{schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest} {
			msg := &ChannelMessage{
				BifrostRequest: schemas.BifrostRequest{
					RequestType: requestType,
					ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "test-model"},
				},
				Context: schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
				Err:     make(chan schemas.BifrostError, 1),
			}
			messages = append(messages, msg)
			pq.queue <- msg
		}
		pq.signalClosing()
		workers.Wait()
		for _, msg := range messages {
			select {
			case err := <-msg.Err:
				requireProviderShutdownError(t, &err, msg.RequestType)
			default:
				t.Fatal("worker did not deliver a shutdown error to the queued request")
			}
		}
	})
}
