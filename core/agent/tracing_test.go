package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// a2aContentTracer exercises the gate with real shared spans and content demand.
type a2aContentTracer struct {
	schemas.NoOpTracer
	span, root   *schemas.Span
	content, raw bool
}

// A2AContentDemand makes retention policy explicit in gate tests.
func (t *a2aContentTracer) A2AContentDemand() (bool, bool) { return t.content, t.raw }

// StartSpan returns an operation span sharing the root's trace.
func (t *a2aContentTracer) StartSpan(ctx context.Context, name string, kind schemas.SpanKind) (context.Context, schemas.SpanHandle) {
	t.span = &schemas.Span{SpanID: "op", Name: name, Kind: kind}
	return context.WithValue(ctx, schemas.BifrostContextKeySpanID, t.span.SpanID), t.span
}

// GetSpanHandleByID exposes the root used by trace-level content views.
func (t *a2aContentTracer) GetSpanHandleByID(string, *string) schemas.SpanHandle { return t.root }

// SpanFromHandle resolves the test's concrete span handles.
func (t *a2aContentTracer) SpanFromHandle(h schemas.SpanHandle) *schemas.Span {
	s, _ := h.(*schemas.Span)
	return s
}

// SetAttribute delegates synchronization to the shared span implementation.
func (t *a2aContentTracer) SetAttribute(h schemas.SpanHandle, key string, value any) {
	t.SpanFromHandle(h).SetAttribute(key, value)
}

// EndSpan records the gate's final outcome.
func (t *a2aContentTracer) EndSpan(h schemas.SpanHandle, status schemas.SpanStatus, message string) {
	t.SpanFromHandle(h).End(status, message)
}

// AddEvent retains the same event shape consumed by production exporters.
func (t *a2aContentTracer) AddEvent(h schemas.SpanHandle, name string, attrs map[string]any) {
	t.SpanFromHandle(h).AddEvent(schemas.SpanEvent{Name: name, Attributes: attrs, Timestamp: time.Now()})
}

// TestA2ATraceUnaryPolicy covers returned IDs, failed tasks, redaction, raw
// capture/export independence, and virtual-key suppression through the gate.
func TestA2ATraceUnaryPolicy(t *testing.T) {
	for _, content := range []bool{false, true} {
		for _, raw := range []bool{false, true} {
			for _, capture := range []bool{false, true} {
				for _, disabled := range []bool{false, true} {
					t.Run(fmt.Sprintf("content=%t/raw=%t/capture=%t/vkDisabled=%t", content, raw, capture, disabled), func(t *testing.T) {
						tracer := &a2aContentTracer{content: content, raw: raw, root: &schemas.Span{SpanID: "root"}}
						ctx := tracedGateCtx(tracer)
						ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace")
						ctx.SetValue(schemas.BifrostContextKeyGovernanceDisableContentLogging, disabled)
						m := &Manager{rawCaptureFn: func() bool { return capture }}
						request := `{"message":{"messageId":"in","role":"user","parts":[{"text":"hello"}]},"configuration":{"pushNotificationConfig":{"token":"callback-secret"}}}`
						response := `{"id":"new-task","contextId":"new-context","status":{"state":"failed","message":{"parts":[{"text":"unable to finish"}]}},"authentication":{"credentials":"response-secret"}}`
						_, bErr := m.RunWithPluginPipeline(ctx, &schemas.BifrostA2ARequest{RequestType: schemas.A2ARequestTypeSendMessage, RequestBody: &request}, func(_ *schemas.BifrostA2ARequest, _ func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
							if c := schemas.GetA2ARawCapture(ctx); c != nil {
								c.Append([]byte(request), true)
								c.Append([]byte(response), false)
							}
							result := describeSendMessageResult(&a2a.Task{ID: "new-task", ContextID: "new-context", Status: a2a.TaskStatus{State: a2a.TaskStateFailed}})
							result.ResponseBody = &response
							return result, nil
						})
						require.Nil(t, bErr)
						require.Equal(t, "new-task", tracer.span.Attributes[schemas.AttrA2ATaskID])
						require.Equal(t, "new-context", tracer.span.Attributes[schemas.AttrConversationID])
						require.Equal(t, string(a2a.TaskStateFailed), tracer.span.Attributes[schemas.AttrA2ATaskState])
						require.Equal(t, schemas.SpanStatusError, tracer.span.Status)
						_, hasInput := tracer.span.Attributes[schemas.AttrInputMessages]
						require.Equal(t, content && !disabled, hasInput)
						require.Equal(t, tracer.span.Attributes[schemas.AttrOutputMessages], tracer.root.Attributes[schemas.AttrOutputMessages])
						rawRequest, rawResponse := tracer.span.RawPayloads()
						require.Equal(t, raw && capture && !disabled, rawRequest != "")
						require.Equal(t, raw && capture && !disabled, rawResponse != "")
						require.NotContains(t, rawRequest, "callback-secret")
						require.NotContains(t, rawResponse, "response-secret")
						serialized, err := json.Marshal(tracer.span)
						require.NoError(t, err)
						require.NotContains(t, string(serialized), "response-secret")
						require.NotContains(t, string(serialized), "RawRequest")
						require.Nil(t, schemas.GetA2ARawCapture(ctx), "capture manager must release the operation")
					})
				}
			}
		}
	}
}

// TestA2ATraceStream retains accepted task, artifact and status bodies in order,
// including output before a transport error, with and without a plugin pipeline.
func TestA2ATraceStream(t *testing.T) {
	for _, pipeline := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("pipeline=%t/error=%t", pipeline, failed), func(t *testing.T) {
				m := &Manager{}
				if pipeline {
					m = newGatedManager(t)
				}
				tracer := &a2aContentTracer{content: true, root: &schemas.Span{SpanID: "root"}}
				ctx := tracedGateCtx(tracer)
				ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace")
				_, bErr := m.RunWithPluginPipeline(ctx, &schemas.BifrostA2ARequest{RequestType: schemas.A2ARequestTypeSendStreamingMessage}, func(_ *schemas.BifrostA2ARequest, observe func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
					events := []a2a.Event{
						&a2a.Task{ID: "created-task", ContextID: "created-context", Status: a2a.TaskStatus{State: a2a.TaskStateWorking}},
						&a2a.TaskArtifactUpdateEvent{TaskID: "created-task", ContextID: "created-context", Append: true, Artifact: &a2a.Artifact{ID: "artifact", Parts: a2a.ContentParts{a2a.NewTextPart("chunk")}}},
						&a2a.TaskStatusUpdateEvent{TaskID: "created-task", ContextID: "created-context", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}},
					}
					for i, event := range events {
						observe(describeA2AEvent(nil, int64(i+1), event))
					}
					if failed {
						return nil, context.DeadlineExceeded
					}
					return &schemas.BifrostA2AResponse{}, nil
				})
				require.Equal(t, failed, bErr != nil)
				require.Len(t, tracer.span.Events, 3)
				require.Equal(t, string(a2a.TaskStateCompleted), tracer.span.Attributes[schemas.AttrA2ATaskState])
				var output []json.RawMessage
				require.NoError(t, json.Unmarshal([]byte(tracer.span.Attributes[schemas.AttrOutputMessages].(string)), &output))
				require.Len(t, output, 3)
				require.Contains(t, string(output[1]), `"artifact"`)
				require.Contains(t, string(output[1]), `"append":true`)
				if failed {
					require.Equal(t, schemas.SpanStatusError, tracer.span.Status)
					require.Equal(t, "timeout", tracer.span.Attributes[schemas.AttrErrorTypeSpec])
				}
			})
		}
	}
}

// TestA2ATraceBounds ensures a long stream cannot grow trace retention forever.
func TestA2ATraceBounds(t *testing.T) {
	tracer := &a2aContentTracer{content: true}
	ctx := tracedGateCtx(tracer)
	_, err := (&Manager{}).RunWithPluginPipeline(ctx, operationEnvelope("fixture"), func(_ *schemas.BifrostA2ARequest, observe func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		body := `{"text":"` + strings.Repeat("x", a2aTraceContentLimit/2) + `"}`
		for i := 0; i < a2aTraceEventLimit+5; i++ {
			observe(&schemas.BifrostA2AEvent{EventType: schemas.A2AEventTypeMessage, Body: &body})
		}
		observe(&schemas.BifrostA2AEvent{TaskID: "last-task", TaskState: string(a2a.TaskStateFailed)})
		return nil, errors.New("interrupted")
	})
	require.NotNil(t, err)
	require.Len(t, tracer.span.Events, a2aTraceEventLimit)
	require.Equal(t, true, tracer.span.Attributes["bifrost.a2a.events.dropped"])
	require.Equal(t, true, tracer.span.Attributes["bifrost.a2a.content.truncated"])
	require.Equal(t, "last-task", tracer.span.Attributes[schemas.AttrA2ATaskID])
	require.LessOrEqual(t, len(tracer.span.Attributes[schemas.AttrOutputMessages].(string)), a2aTraceContentLimit+2)
}

// TestA2ATraceSanitizedContentBounds prevents redaction expansion from exceeding the stream budget.
func TestA2ATraceSanitizedContentBounds(t *testing.T) {
	for _, remaining := range []int{len(`{"token":""}`), len(`{"token":"REDACTED"}`)} {
		t.Run(fmt.Sprintf("remaining=%d", remaining), func(t *testing.T) {
			tracer := &a2aContentTracer{content: true}
			_, err := (&Manager{}).RunWithPluginPipeline(tracedGateCtx(tracer), operationEnvelope("fixture"), func(_ *schemas.BifrostA2ARequest, observe func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
				first := `{"text":"` + strings.Repeat("x", a2aTraceContentLimit-remaining-len(`{"text":""}`)) + `"}`
				second := `{"token":""}`
				observe(&schemas.BifrostA2AEvent{EventType: schemas.A2AEventTypeMessage, Body: &first})
				observe(&schemas.BifrostA2AEvent{EventType: schemas.A2AEventTypeMessage, Body: &second})
				return nil, nil
			})
			require.Nil(t, err)
			require.Len(t, tracer.span.Events, 2)
			if remaining < len(`{"token":"REDACTED"}`) {
				require.NotContains(t, tracer.span.Events[1].Attributes, schemas.AttrOutputMessages)
				require.Equal(t, true, tracer.span.Attributes["bifrost.a2a.content.truncated"])
			} else {
				require.Equal(t, `{"token":"REDACTED"}`, tracer.span.Events[1].Attributes[schemas.AttrOutputMessages])
				require.NotContains(t, tracer.span.Attributes, "bifrost.a2a.content.truncated")
			}
			retained := 0
			for _, event := range tracer.span.Events {
				retained += len(schemas.GetStringAttr(event.Attributes, schemas.AttrOutputMessages))
			}
			require.LessOrEqual(t, retained, a2aTraceContentLimit)
		})
	}
}

// TestA2ATraceTaskOutcomes uses the SDK's strict-v1 enum values so successful
// RPCs returning failed/rejected tasks cannot accidentally export as success.
func TestA2ATraceTaskOutcomes(t *testing.T) {
	for _, state := range []a2a.TaskState{a2a.TaskStateWorking, a2a.TaskStateCompleted, a2a.TaskStateCanceled, a2a.TaskStateFailed, a2a.TaskStateRejected} {
		t.Run(string(state), func(t *testing.T) {
			tracer := &a2aContentTracer{}
			_, err := (&Manager{}).RunWithPluginPipeline(tracedGateCtx(tracer), operationEnvelope("fixture"), func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
				return describeSendMessageResult(&a2a.Task{ID: "task", ContextID: "context", Status: a2a.TaskStatus{State: state}}), nil
			})
			require.Nil(t, err, "task failure does not change the protocol response")
			require.Equal(t, string(state), tracer.span.Attributes[schemas.AttrA2ATaskState])
			want := schemas.SpanStatusOk
			if state == a2a.TaskStateFailed || state == a2a.TaskStateRejected {
				want = schemas.SpanStatusError
			}
			require.Equal(t, want, tracer.span.Status)
		})
	}
}

// TestA2ATraceRawError preserves safe complete captures when an operation fails,
// while omitting an incomplete response instead of exporting a partial body.
func TestA2ATraceRawError(t *testing.T) {
	for _, complete := range []bool{false, true} {
		tracer := &a2aContentTracer{raw: true}
		ctx := tracedGateCtx(tracer)
		manager := &Manager{rawCaptureFn: func() bool { return true }}
		_, err := manager.RunWithPluginPipeline(ctx, operationEnvelope("fixture"), func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
			capture := schemas.GetA2ARawCapture(ctx)
			capture.Append([]byte(`{"text":"request"}`), true)
			capture.Append([]byte(`{"error":{"message":"failure"},"token":"secret"}`), false)
			capture.SetComplete(false, complete)
			return nil, context.DeadlineExceeded
		})
		require.NotNil(t, err)
		request, response := tracer.span.RawPayloads()
		require.NotEmpty(t, request)
		require.Equal(t, complete, response != "")
		require.NotContains(t, response, "secret")
		require.Equal(t, schemas.SpanStatusError, tracer.span.Status)
	}
}

// TestSanitizeA2APayload covers sensitive protocol fields, SSE, malformed bodies,
// precision preservation, and transport captures that are not safe JSON.
func TestSanitizeA2APayload(t *testing.T) {
	for _, input := range []string{
		`{"token":"secret","parts":[{"text":"hello"}],"n":9007199254740993}`,
		`{"authentication":{"credentials":"secret"},"securitySchemes":{"key":"secret"},"url":"https://user:secret@example.com/callback?token=secret"}`,
		`{"configuration":{"taskPushNotificationConfig":{"url":"https://example.com/callback/secret"}},"taskPushNotificationConfigs":[{"url":"https://example.com/callback/secret"}]}`,
		`{"task_push_notification_config":{"url":"https://example.com/callback/secret"},"task_push_notification_configs":[{"url":"https://example.com/callback/secret"}]}`,
		"data: {\"token\":\"secret\",\"text\":\"hello\"}\n\ndata: [DONE]\n\n",
		"event: message\ndata: {\ndata: \"token\":\"secret\",\ndata: \"text\":\"hello\"}\n\n",
	} {
		result := sanitizeA2ARaw(input)
		require.NotEmpty(t, result)
		require.NotContains(t, result, "secret")
		if strings.Contains(input, "9007199254740993") {
			require.Contains(t, result, "9007199254740993")
		}
	}
	for _, input := range []string{"broken JSON", "data: broken\n\n", string([]byte{0, 1, 2}), `{"text":"hello"} trailing`} {
		require.Empty(t, sanitizeA2ARaw(input))
	}
	clean := "id: 1\r\ndata: { \"text\": \"hello\" }\r\n\r\n"
	require.Equal(t, clean, sanitizeA2ARaw(clean))
}

// BenchmarkA2ATraceUnary measures content enrichment separately from the empty
// metadata path, without network or provider costs.
func BenchmarkA2ATraceUnary(b *testing.B) {
	for _, mode := range []string{"no tracer", "metadata", "content"} {
		b.Run(mode, func(b *testing.B) {
			body := `{"message":{"role":"user","parts":[{"text":"hello"}]}}`
			response := `{"role":"agent","parts":[{"text":"world"}]}`
			m := &Manager{}
			var tracer schemas.Tracer
			if mode != "no tracer" {
				tracer = &a2aContentTracer{content: mode == "content"}
			}
			ctx := tracedGateCtx(tracer)
			req := &schemas.BifrostA2ARequest{RequestType: schemas.A2ARequestTypeSendMessage, RequestBody: &body}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = m.RunWithPluginPipeline(ctx, req, func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
					return &schemas.BifrostA2AResponse{ResponseBody: &response}, nil
				})
			}
		})
	}
}

// TestA2ATraceAllOperationTypes verifies shared enrichment for every protocol
// operation, including lifecycle arguments and push-attempt correlation.
func TestA2ATraceAllOperationTypes(t *testing.T) {
	for _, operation := range []schemas.A2ARequestType{schemas.A2ARequestTypeGetAgentCard, schemas.A2ARequestTypeGetExtendedAgentCard, schemas.A2ARequestTypeSendMessage, schemas.A2ARequestTypeSendStreamingMessage, schemas.A2ARequestTypeGetTask, schemas.A2ARequestTypeListTasks, schemas.A2ARequestTypeCancelTask, schemas.A2ARequestTypeSubscribeToTask, schemas.A2ARequestTypeCreateTaskPushConfig, schemas.A2ARequestTypeGetTaskPushConfig, schemas.A2ARequestTypeListTaskPushConfigs, schemas.A2ARequestTypeDeleteTaskPushConfig, schemas.A2ARequestTypePushNotification, schemas.A2ARequestTypePushDelivery} {
		t.Run(string(operation), func(t *testing.T) {
			tracer := &a2aContentTracer{content: true, root: &schemas.Span{SpanID: "root"}}
			request := `{"taskId":"task","token":"secret"}`
			response := `{"result":"accepted"}`
			_, err := (&Manager{}).RunWithPluginPipeline(tracedGateCtx(tracer), &schemas.BifrostA2ARequest{RequestType: operation, AgentName: "fixture", RequestBody: &request, BifrostA2APushRequest: &schemas.BifrostA2APushRequest{PushConfigID: "config", DeliveryID: "delivery", AttemptID: "attempt"}}, func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
				return &schemas.BifrostA2AResponse{ResponseBody: &response}, nil
			})
			require.Nil(t, err)
			require.Equal(t, operation.OperationName(), tracer.span.Attributes[schemas.AttrBifrostA2AOperationName])
			require.Contains(t, tracer.span.Attributes[schemas.AttrInputMessages], "task")
			require.NotContains(t, tracer.span.Attributes[schemas.AttrInputMessages], "secret")
			require.Contains(t, tracer.span.Attributes[schemas.AttrOutputMessages], "accepted")
			require.Equal(t, "delivery", tracer.span.Attributes["bifrost.a2a.push.delivery.id"])
		})
	}
}
