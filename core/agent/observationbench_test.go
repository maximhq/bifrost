package agent

import (
	"context"
	"encoding/json"
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
	message.Parts[0].MediaType = "text/plain"
	artifactPart := a2a.NewTextPart("artifact")
	artifactPart.MediaType = "text/markdown"
	for _, test := range []struct {
		name  string
		event a2a.Event
		want  schemas.BifrostA2AEvent
	}{
		{name: "message", event: message, want: schemas.BifrostA2AEvent{EventType: schemas.A2AEventTypeMessage, TaskID: "task-1", ContextID: "context-1", MessageID: "message-1", ContentType: "text/plain"}},
		{name: "task with message", event: &a2a.Task{ID: "task-1", ContextID: "context-1", Status: a2a.TaskStatus{State: a2a.TaskStateWorking, Message: message}}, want: schemas.BifrostA2AEvent{EventType: schemas.A2AEventTypeTask, TaskID: "task-1", ContextID: "context-1", TaskState: string(a2a.TaskStateWorking), MessageID: "message-1", ContentType: "text/plain"}},
		{name: "failed status with message", event: &a2a.TaskStatusUpdateEvent{TaskID: "task-1", ContextID: "context-1", Status: a2a.TaskStatus{State: a2a.TaskStateFailed, Message: message}}, want: schemas.BifrostA2AEvent{EventType: schemas.A2AEventTypeStatusUpdate, TaskID: "task-1", ContextID: "context-1", TaskState: string(a2a.TaskStateFailed), MessageID: "message-1", ContentType: "text/plain"}},
		{name: "artifact skips nil and untyped parts", event: &a2a.TaskArtifactUpdateEvent{TaskID: "task-1", ContextID: "context-1", Append: true, Artifact: &a2a.Artifact{ID: "artifact-1", Parts: a2a.ContentParts{nil, &a2a.Part{}, artifactPart}}}, want: schemas.BifrostA2AEvent{EventType: schemas.A2AEventTypeArtifactUpdate, TaskID: "task-1", ContextID: "context-1", ArtifactID: "artifact-1", ContentType: "text/markdown"}},
		{name: "task without message", event: &a2a.Task{ID: "task-2", Status: a2a.TaskStatus{State: a2a.TaskStateSubmitted}}, want: schemas.BifrostA2AEvent{EventType: schemas.A2AEventTypeTask, TaskID: "task-2", TaskState: string(a2a.TaskStateSubmitted)}},
		{name: "status without message", event: &a2a.TaskStatusUpdateEvent{TaskID: "task-2", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}, want: schemas.BifrostA2AEvent{EventType: schemas.A2AEventTypeStatusUpdate, TaskID: "task-2", TaskState: string(a2a.TaskStateCompleted)}},
		{name: "missing artifact", event: &a2a.TaskArtifactUpdateEvent{TaskID: "task-2"}, want: schemas.BifrostA2AEvent{EventType: schemas.A2AEventTypeArtifactUpdate, TaskID: "task-2"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, withEnvelope := range []bool{false, true} {
				var envelope *schemas.BifrostA2ARequest
				want := test.want
				want.Sequence = 7
				if withEnvelope {
					envelope = &schemas.BifrostA2ARequest{AgentName: "fixture", RequestType: schemas.A2ARequestTypeSendStreamingMessage}
					want.AgentName, want.RequestType = envelope.AgentName, envelope.RequestType
				}
				metadata := describeA2AEventMetadata(envelope, 7, test.event)
				if *metadata != want {
					t.Fatalf("metadata = %+v, want %+v", metadata, want)
				}
				full := describeA2AEvent(envelope, 7, test.event)
				body, err := json.Marshal(test.event)
				if err != nil {
					// Invalid SDK events may not encode, but metadata extraction
					// must still be safe and full observation must keep Body nil.
					if full.Body != nil {
						t.Fatal("invalid event unexpectedly produced a body")
					}
				} else if full.Body == nil || *full.Body != string(body) {
					t.Fatalf("complete event body changed: %v", full.Body)
				}
				full.Body = nil
				if *full != want {
					t.Fatalf("full observation metadata = %+v, want %+v", full, want)
				}
			}
		})
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
