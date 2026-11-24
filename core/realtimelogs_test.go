package bifrost

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// turnLogTracer records attached logs separately for each unit's trace.
type turnLogTracer struct {
	schemas.NoOpTracer
	mu   sync.Mutex
	logs map[string][]schemas.PluginLogEntry
}

// CreateTrace allocates a distinct trace for each unit.
func (t *turnLogTracer) CreateTrace(_ string, _ ...string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	id := fmt.Sprintf("turn-%d", len(t.logs))
	t.logs[id] = nil
	return id
}

// AttachPluginLogs appends each phase's logs to its trace.
func (t *turnLogTracer) AttachPluginLogs(id string, logs []schemas.PluginLogEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.logs[id] = append(t.logs[id], logs...)
}

// turnLogPlugin emits diagnostics from both phases, including refused units.
type turnLogPlugin struct {
	catalogProbePlugin
	refuse bool
}

// PreLLMHook records admission and can refuse the unit before provider work.
func (p *turnLogPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	ctx.Log(schemas.LogLevelInfo, "pre")
	if p.refuse {
		return req, &schemas.LLMPluginShortCircuit{Error: newBifrostErrorFromMsg("refused")}, nil
	}
	return req, nil, nil
}

// PostLLMHook records settlement while preserving the response and error.
func (p *turnLogPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	ctx.Log(schemas.LogLevelInfo, "post")
	return resp, err, nil
}

// TestRealtimeTurnPluginLogsSurviveContextHandoff checks ordered logs and isolation across units.
func TestRealtimeTurnPluginLogsSurviveContextHandoff(t *testing.T) {
	for _, requestType := range []schemas.RequestType{schemas.RealtimeRequest, schemas.LiveRequest} {
		for _, refuse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/refuse=%t", requestType, refuse), func(t *testing.T) {
				tracer := &turnLogTracer{logs: make(map[string][]schemas.PluginLogEntry)}
				plugin := &turnLogPlugin{refuse: refuse}
				client, err := Init(context.Background(), schemas.BifrostConfig{
					Account: NewMockAccount(), Logger: NewNoOpLogger(), Tracer: tracer,
					LLMPlugins: []schemas.LLMPlugin{plugin},
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(client.Shutdown)
				for unit := 0; unit < 2; unit++ {
					preCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
					hooks, preErr := client.RunRealtimeTurnPreHooks(preCtx, &schemas.BifrostRequest{
						RequestType:      requestType,
						ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "test-model"},
					})
					if refuse {
						if preErr == nil || hooks != nil {
							t.Fatal("expected refusal")
						}
					} else {
						if preErr != nil || hooks == nil {
							t.Fatalf("admission failed: %v", preErr)
						}
						// Both transports copy values into a fresh context for settlement.
						postCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
						for key, value := range preCtx.GetUserValues() {
							postCtx.SetValue(key, value)
						}
						_, postErr := hooks.PostHookRunner(postCtx, &schemas.BifrostResponse{
							ResponsesResponse: &schemas.BifrostResponsesResponse{Model: "test-model"},
						}, nil)
						hooks.Cleanup()
						if postErr != nil {
							t.Fatal(postErr)
						}
					}
					traceID := preCtx.Value(schemas.BifrostContextKeyTraceID).(string)
					logs := tracer.logs[traceID]
					if len(logs) != 2 || logs[0].Message != "pre" || logs[1].Message != "post" {
						t.Fatalf("unit %d logs = %+v, want pre then post exactly once", unit, logs)
					}
					if preCtx.HasPluginLogs() {
						t.Fatal("pre-hook context retained undrained logs")
					}
				}
			})
		}
	}
}
