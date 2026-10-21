package agent

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestA2AEventMetadataMatchesFullObservation(t *testing.T) {
	message := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("content"))
	message.ID, message.TaskID, message.ContextID = "message-1", "task-1", "context-1"
	for _, event := range []a2a.Event{
		message,
		&a2a.Task{ID: "task-1", ContextID: "context-1", Status: a2a.TaskStatus{State: a2a.TaskStateWorking, Message: message}},
		&a2a.TaskStatusUpdateEvent{TaskID: "task-1", ContextID: "context-1", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted, Message: message}},
		&a2a.TaskArtifactUpdateEvent{TaskID: "task-1", ContextID: "context-1", Append: true, Artifact: &a2a.Artifact{ID: "artifact-1", Parts: a2a.ContentParts{nil, a2a.NewTextPart("artifact")}}},
	} {
		envelope := &schemas.BifrostA2ARequest{AgentName: "fixture", RequestType: schemas.A2ARequestTypeSendStreamingMessage}
		full := describeA2AEvent(envelope, 7, event)
		if full.Body == nil {
			t.Fatal("missing complete body")
		}
		full.Body = nil
		metadata := describeA2AEventMetadata(envelope, 7, event)
		if *metadata != *full {
			t.Fatalf("metadata changed: got %+v, want %+v", metadata, full)
		}
	}
}

// BenchmarkA2AEventObservation measures metadata extraction and the duplicate
// JSON encoding performed before stream events reach observational consumers.
func BenchmarkA2AEventObservation(b *testing.B) {
	for _, size := range []int{1024, 64 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("payload_%d", size), func(b *testing.B) {
			event := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(strings.Repeat("x", size)))
			envelope := &schemas.BifrostA2ARequest{RequestType: schemas.A2ARequestTypeSendStreamingMessage, AgentName: "fixture"}
			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for b.Loop() {
				observed := describeA2AEvent(envelope, 1, event)
				if observed.Body == nil || len(*observed.Body) < size {
					b.Fatal("missing observed event body")
				}
			}
		})
	}
}

// BenchmarkA2AStreamObservation includes the real forwarding loop, operation
// gate and observation preparation. It intentionally excludes protocol I/O.
func BenchmarkA2AStreamObservation(b *testing.B) {
	for _, size := range []int{1024, 64 << 10, 1 << 20} {
		for _, mode := range []string{"no_observer", "metadata", "full_body"} {
			b.Run(fmt.Sprintf("%s/payload_%d", mode, size), func(b *testing.B) {
				_, _, manager := streamingGateway(b)
				required := mode == "full_body"
				pipeline := &streamBodyDemandPipeline{required: required, onEvent: func(event *schemas.BifrostA2AEvent) {
					if required && event.Body == nil {
						b.Fatal("missing complete event body")
					}
				}}
				if mode != "no_observer" {
					manager.SetPluginPipeline(func() PluginPipeline { return pipeline }, nil)
				}
				runtime, _ := manager.runtimes.load("tasks")
				handler := &proxyRequestHandler{runtime: runtime, manager: manager, config: runtime.config}
				envelope := handler.envelope(schemas.A2ARequestTypeSendStreamingMessage)
				event := &a2a.TaskArtifactUpdateEvent{TaskID: "task-1", ContextID: "context-1", Append: true, Artifact: &a2a.Artifact{ID: "artifact-1", Parts: a2a.ContentParts{a2a.NewTextPart(strings.Repeat("x", size))}}}
				const events = 16
				call := func(context.Context, sdkClient) iter.Seq2[a2a.Event, error] {
					return func(yield func(a2a.Event, error) bool) {
						for range events {
							if !yield(event, nil) {
								return
							}
						}
					}
				}
				drain := func() {
					for _, err := range forwardUpstreamStream(context.Background(), handler, envelope, call) {
						if err != nil {
							b.Fatal(err)
						}
					}
				}
				drain() // Prepare the upstream client generation outside timing.
				b.ReportAllocs()
				b.SetBytes(int64(events * size))
				b.ResetTimer()
				for b.Loop() {
					drain()
				}
			})
		}
	}
}

// The public/unary gate must not pay to prepare stream body demand.
func BenchmarkA2APipelineUnary(b *testing.B) {
	for _, configured := range []bool{false, true} {
		b.Run(fmt.Sprintf("pipeline_%t", configured), func(b *testing.B) {
			manager := &Manager{}
			if configured {
				pipeline := &streamBodyDemandPipeline{required: true}
				manager.SetPluginPipeline(func() PluginPipeline { return pipeline }, nil)
			}
			envelope := operationEnvelope("fixture")
			response := &schemas.BifrostA2AResponse{}
			op := func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
				return response, nil
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_, err := manager.RunWithPluginPipeline(gateCtxForTest(), envelope, op)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
