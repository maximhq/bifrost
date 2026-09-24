package warp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/mcptools"
)

// Test doubles for the MCP side of the loop.
//
// Warp's tools live on Bifrost's MCP server (framework/mcptools). The agent
// loop tests need that server to answer - so the MCPExecutor and
// MCPToolLister doubles here run a real mcptools server in-process, over
// mcp-go's in-process client, against a fake log reader. What is skipped is
// core/mcp's own manager (client registry, include-filter, retries), which has
// its own tests; what is kept is everything from the JSON-RPC envelope down,
// so the loop is exercised against the real tool schemas and the real
// bounding, scoping and error shapes, not a copy of them.

// LogReaderStub embeds the LogReader interface without implementing it, so a
// fake that embeds it satisfies the interface but panics on any method it
// does not override - the signal that a tool started reaching somewhere new.
type LogReaderStub struct {
	LogReader
}

// fakeLogReader answers the handful of store methods the loop tests drive
// tools into, and records enough about each call to assert on concurrency,
// ordering and context propagation. mu guards every field: the loop runs a
// step's calls concurrently.
type fakeLogReader struct {
	LogReaderStub

	mu sync.Mutex

	sawContext context.Context
	// statsCalled and statsCalls record GetStats reaching the store: once, and
	// how many times, so a test can tell "the tool ran" from "every call ran".
	statsCalled bool
	statsCalls  int
	// *Delay make the matching method take real wall-clock time, the lever a
	// test has to prove calls overlap or complete out of order.
	statsDelay          time.Duration
	histogramDelay      time.Duration
	costHistogramDelay  time.Duration
	tokenHistogramDelay time.Duration
	// activeStatsCalls and peakStatsCalls track how many GetStats calls are in
	// flight at once, for the global-concurrency-cap tests.
	activeStatsCalls int32
	peakStatsCalls   int32
	// entered and release, when both set, turn GetStats into a rendezvous
	// point: each call sends on entered once counted, then blocks until
	// release is closed, so a test can assert on an exact in-flight count.
	entered chan struct{}
	release chan struct{}
}

func (f *fakeLogReader) enter() (exit func()) {
	active := atomic.AddInt32(&f.activeStatsCalls, 1)
	for {
		peak := atomic.LoadInt32(&f.peakStatsCalls)
		if active <= peak || atomic.CompareAndSwapInt32(&f.peakStatsCalls, peak, active) {
			break
		}
	}
	return func() { atomic.AddInt32(&f.activeStatsCalls, -1) }
}

// wait sleeps without holding the fake's lock, so a delay observes rather
// than serializes concurrent callers.
func wait(ctx context.Context, delay time.Duration) {
	if delay <= 0 {
		return
	}
	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
}

func (f *fakeLogReader) Search(ctx context.Context, _ *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawContext = ctx
	return &logstore.SearchResult{Pagination: *pagination}, nil
}

func (f *fakeLogReader) GetStats(ctx context.Context, _ *logstore.SearchFilters) (*logstore.SearchStats, error) {
	f.mu.Lock()
	f.sawContext = ctx
	f.statsCalled = true
	f.statsCalls++
	delay := f.statsDelay
	entered, release := f.entered, f.release
	f.mu.Unlock()

	defer f.enter()()
	if entered != nil && release != nil {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	wait(ctx, delay)
	return &logstore.SearchStats{}, nil
}

// GetModelRankings and the describe_filter_space lookups answer with nothing:
// the loop tests that reach them care that the tool ran and returned, not what
// it found. The tools' own behaviour is tested in mcptools.
func (f *fakeLogReader) GetModelRankings(context.Context, *logstore.SearchFilters) (*logstore.ModelRankingResult, error) {
	return &logstore.ModelRankingResult{}, nil
}
func (f *fakeLogReader) GetAvailableModels(context.Context, int, string) ([]string, error) {
	return nil, nil
}
func (f *fakeLogReader) GetAvailableApps(context.Context, int, string) ([]string, error) {
	return nil, nil
}
func (f *fakeLogReader) GetAvailableStopReasons(context.Context, int, string) ([]string, error) {
	return nil, nil
}
func (f *fakeLogReader) GetAvailableAliases(context.Context, int, string) ([]string, error) {
	return nil, nil
}
func (f *fakeLogReader) GetAvailableRoutingEngines(context.Context, int, string) ([]string, error) {
	return nil, nil
}
func (f *fakeLogReader) GetAvailableToolCallNames(context.Context, int, string) ([]string, error) {
	return nil, nil
}
func (f *fakeLogReader) GetAvailableMetadataKeys(context.Context, int, string) (map[string][]string, error) {
	return nil, nil
}
func (f *fakeLogReader) GetAvailableVirtualKeys(context.Context, int, string) ([]KeyPair, error) {
	return nil, nil
}
func (f *fakeLogReader) GetAvailableTeams(context.Context, int, string) ([]KeyPair, error) {
	return nil, nil
}
func (f *fakeLogReader) GetAvailableCustomers(context.Context, int, string) ([]KeyPair, error) {
	return nil, nil
}
func (f *fakeLogReader) GetAvailableBusinessUnits(context.Context, int, string) ([]KeyPair, error) {
	return nil, nil
}
func (f *fakeLogReader) GetAvailableRoutingRules(context.Context, int, string) ([]KeyPair, error) {
	return nil, nil
}
func (f *fakeLogReader) GetAvailableSelectedKeys(context.Context, int, string) ([]KeyPair, error) {
	return nil, nil
}

func (f *fakeLogReader) GetHistogram(ctx context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.HistogramResult, error) {
	f.mu.Lock()
	f.sawContext = ctx
	delay := f.histogramDelay
	f.mu.Unlock()
	wait(ctx, delay)
	return &logstore.HistogramResult{}, nil
}

func (f *fakeLogReader) GetTokenHistogram(ctx context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.TokenHistogramResult, error) {
	f.mu.Lock()
	f.sawContext = ctx
	delay := f.tokenHistogramDelay
	f.mu.Unlock()
	wait(ctx, delay)
	return &logstore.TokenHistogramResult{}, nil
}

func (f *fakeLogReader) GetCostHistogram(ctx context.Context, _ *logstore.SearchFilters, _ int64) (*logstore.CostHistogramResult, error) {
	f.mu.Lock()
	f.sawContext = ctx
	delay := f.costHistogramDelay
	f.mu.Unlock()
	wait(ctx, delay)
	return &logstore.CostHistogramResult{}, nil
}

// testMCP is a real mcptools server reached over an in-process MCP client,
// wrapped in the two function types the agent depends on.
type testMCP struct {
	client *mcpclient.Client
}

// newTestMCP hosts mcptools over fake, with the same handshake a remote
// client would perform. t.Cleanup closes it.
func newTestMCP(t testing.TB, fake *fakeLogReader) *testMCP {
	t.Helper()
	server := mcptools.NewServer(mcptools.StaticDeps(&mcptools.Deps{LogManager: fake}))
	client, err := mcpclient.NewInProcessClient(server)
	if err != nil {
		t.Fatalf("in-process mcp client: %v", err)
	}
	ctx := context.Background()
	if err := client.Start(ctx); err != nil {
		t.Fatalf("start mcp client: %v", err)
	}
	if _, err := client.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "warp-test", Version: "0"},
	}}); err != nil {
		t.Fatalf("initialize mcp client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return &testMCP{client: client}
}

// execute is the MCPExecutor: strip the client prefix core/mcp would strip,
// call the tool over the in-process transport, and fold the MCP result into
// the ChatMessage shape ExecuteChatMCPTool returns. Argument parsing and the
// unknown-tool refusal mirror core/mcp/toolmanager.go, so the loop's own
// tests for those shapes still hold.
func (m *testMCP) execute(ctx *schemas.BifrostContext, toolCall *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	name := ""
	if toolCall.Function.Name != nil {
		name = strings.TrimPrefix(*toolCall.Function.Name, mcpToolPrefix)
	}
	args := map[string]any{}
	if strings.TrimSpace(toolCall.Function.Arguments) != "" {
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
			return errorChatMessage(fmt.Sprintf("arguments were not valid JSON: %s", err.Error())), nil
		}
	}
	result, err := m.client.CallTool(ctx, mcp.CallToolRequest{
		Request: mcp.Request{Method: string(mcp.MethodToolsCall)},
		Params:  mcp.CallToolParams{Name: name, Arguments: args},
	})
	if err != nil {
		return errorChatMessage(err.Error()), nil
	}
	var text strings.Builder
	for _, content := range result.Content {
		text.WriteString(mcp.GetTextFromContent(content))
	}
	message := &schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleTool,
		Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text.String())},
	}
	if result.IsError {
		message.ChatToolMessage = &schemas.ChatToolMessage{IsError: schemas.Ptr(true)}
	}
	return message, nil
}

// list is the MCPToolLister: what the server declares, prefixed the way
// core/mcp's tool map keys are.
func (m *testMCP) list(ctx *schemas.BifrostContext) []schemas.ChatTool {
	listed, err := m.client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil
	}
	tools := make([]schemas.ChatTool, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			continue
		}
		var parameters schemas.ToolFunctionParameters
		if err := json.Unmarshal(raw, &parameters); err != nil {
			continue
		}
		tools = append(tools, schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        mcpToolPrefix + tool.Name,
				Description: schemas.Ptr(tool.Description),
				Parameters:  &parameters,
			},
		})
	}
	return tools
}

// errorChatMessage builds the tool-result shape a failed MCP call returns:
// content carrying the error text, marked failed via IsError.
func errorChatMessage(message string) *schemas.ChatMessage {
	return &schemas.ChatMessage{
		Role:            schemas.ChatMessageRoleTool,
		Content:         &schemas.ChatMessageContent{ContentStr: &message},
		ChatToolMessage: &schemas.ChatToolMessage{IsError: schemas.Ptr(true)},
	}
}
