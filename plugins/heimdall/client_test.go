package heimdall

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// testAccount is a minimal schemas.Account. Each provider gets one key and the config it was added with.
type testAccount struct {
	configs map[schemas.ModelProvider]*schemas.ProviderConfig
}

func newTestAccount() *testAccount {
	return &testAccount{configs: map[schemas.ModelProvider]*schemas.ProviderConfig{}}
}

func (a *testAccount) add(p schemas.ModelProvider, baseURL string) *schemas.ProviderConfig {
	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 30,
			MaxRetries:                     1,
			RetryBackoffInitial:            10 * time.Millisecond,
			RetryBackoffMax:                50 * time.Millisecond,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 100, BufferSize: 100},
	}
	a.configs[p] = cfg
	return cfg
}

func (a *testAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	out := make([]schemas.ModelProvider, 0, len(a.configs))
	for p := range a.configs {
		out = append(out, p)
	}
	return out, nil
}

func (a *testAccount) GetKeysForProvider(_ context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	if _, ok := a.configs[p]; !ok {
		return nil, fmt.Errorf("no provider %q", p)
	}
	return []schemas.Key{{
		ID:     "key-" + string(p),
		Name:   "key-" + string(p),
		Value:  *schemas.NewSecretVar("sk-test"),
		Models: schemas.WhiteList{"*"},
		Weight: 1,
	}}, nil
}

func (a *testAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	cfg, ok := a.configs[p]
	if !ok {
		return nil, fmt.Errorf("no provider %q", p)
	}
	return cfg, nil
}

// toolCallReply is an OpenAI chat completion whose only content is a tool call.
const toolCallReply = `{"id":"c1","object":"chat.completion","created":1,"model":"m",` +
	`"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,` +
	`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"shop-issue_refund","arguments":"{\"order_id\":\"o1\"}"}}]}}],` +
	`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// fakeOpenAI serves toolCallReply for every chat completion and counts requests.
func fakeOpenAI(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, toolCallReply)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func chatRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-test",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("refund order o1")},
		}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:       "shop-issue_refund",
					Parameters: &schemas.ToolFunctionParameters{Type: "object", Properties: schemas.NewOrderedMap()},
				},
			}},
		},
	}
}

func newCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func toolCallNames(resp *schemas.BifrostChatResponse) []string {
	var names []string
	for _, c := range resp.Choices {
		if c.ChatNonStreamResponseChoice == nil || c.Message == nil || c.Message.ChatAssistantMessage == nil {
			continue
		}
		for _, tc := range c.Message.ToolCalls {
			if tc.Function.Name != nil {
				names = append(names, *tc.Function.Name)
			}
		}
	}
	return names
}

func TestReplayAccount_OnlyRunProviders(t *testing.T) {
	base := newTestAccount()
	base.add(schemas.OpenAI, "http://openai.invalid")
	base.add(schemas.Anthropic, "http://anthropic.invalid")
	base.add(schemas.Mistral, "http://mistral.invalid")

	acct := newReplayAccount(base, []Route{
		{Provider: schemas.OpenAI, Model: "a"},
		{Provider: schemas.Anthropic, Model: "b"},
		{Provider: schemas.OpenAI, Model: "c"},
	}, 4)

	got, err := acct.GetConfiguredProviders()
	if err != nil {
		t.Fatal(err)
	}
	want := []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic}
	if !slices.Equal(got, want) {
		t.Errorf("providers = %v, want %v", got, want)
	}
	if _, err := acct.GetKeysForProvider(context.Background(), schemas.Mistral); err == nil {
		t.Error("keys for a provider outside the run must be refused")
	}
	if _, err := acct.GetConfigForProvider(schemas.Mistral); err == nil {
		t.Error("config for a provider outside the run must be refused")
	}
	keys, err := acct.GetKeysForProvider(context.Background(), schemas.OpenAI)
	if err != nil || len(keys) != 1 {
		t.Errorf("keys = %v, %v; want the base account's single key", keys, err)
	}
}

func TestReplayAccount_ConfigIsSanitisedCopy(t *testing.T) {
	base := newTestAccount()
	baseCfg := base.add(schemas.OpenAI, "http://openai.invalid")
	baseCfg.InjectedTools = []schemas.InjectedTool{{Name: "web_search", MCPClientName: "search", ToolName: "query"}}
	baseCfg.SendBackRawRequest = true
	baseCfg.SendBackRawResponse = true

	cfg, err := newReplayAccount(base, []Route{{Provider: schemas.OpenAI, Model: "a"}}, 4).GetConfigForProvider(schemas.OpenAI)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InjectedTools != nil {
		t.Errorf("injected tools were kept: %v", cfg.InjectedTools)
	}
	if cfg.SendBackRawRequest || cfg.SendBackRawResponse {
		t.Error("raw request/response capture must be off")
	}
	if cfg.ConcurrencyAndBufferSize.Concurrency != 4 {
		t.Errorf("concurrency = %d, want it capped at 4", cfg.ConcurrencyAndBufferSize.Concurrency)
	}
	if cfg.NetworkConfig.BaseURL != "http://openai.invalid" {
		t.Errorf("network config was not carried over: %+v", cfg.NetworkConfig)
	}
	if len(baseCfg.InjectedTools) != 1 || !baseCfg.SendBackRawRequest || baseCfg.ConcurrencyAndBufferSize.Concurrency != 100 {
		t.Errorf("the base account's config was modified: %+v", baseCfg)
	}
}

func TestReplayAccount_ConcurrencyCapOnlyLowers(t *testing.T) {
	base := newTestAccount()
	base.add(schemas.OpenAI, "http://openai.invalid").ConcurrencyAndBufferSize.Concurrency = 2
	cfg, err := newReplayAccount(base, []Route{{Provider: schemas.OpenAI}}, 8).GetConfigForProvider(schemas.OpenAI)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConcurrencyAndBufferSize.Concurrency != 2 {
		t.Errorf("concurrency = %d, want the base value 2 (a cap never raises it)", cfg.ConcurrencyAndBufferSize.Concurrency)
	}
}

func TestNewReplayClient_Validation(t *testing.T) {
	if _, err := newReplayClient(context.Background(), nil, []Route{{Provider: schemas.OpenAI}}, 1, nil, testLogger()); err == nil {
		t.Error("expected an error without a base account")
	}
	if _, err := newReplayClient(context.Background(), newTestAccount(), nil, 1, nil, testLogger()); err == nil {
		t.Error("expected an error without routes")
	}
}

// stubMCP satisfies the MCP manager interface. Every method panics, which is what we want if a test
// ever lets the client call one.
type stubMCP struct{ mcp.MCPManagerInterface }

func TestAssertHermetic(t *testing.T) {
	srv, _ := fakeOpenAI(t)
	base := newTestAccount()
	base.add(schemas.OpenAI, srv.URL)
	client, err := newReplayClient(context.Background(), base, []Route{{Provider: schemas.OpenAI, Model: "gpt-test"}}, 2, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Shutdown)

	if err := assertHermetic(client); err != nil {
		t.Errorf("fresh replay client: %v", err)
	}
	if err := assertHermetic(nil); err == nil || !strings.HasPrefix(err.Error(), "not_hermetic") {
		t.Errorf("nil client: err = %v, want not_hermetic", err)
	}

	client.MCPManager = stubMCP{}
	err = assertHermetic(client)
	if err == nil || !strings.HasPrefix(err.Error(), "not_hermetic") {
		t.Errorf("client with an MCP manager: err = %v, want not_hermetic", err)
	}
	client.MCPManager = nil
}

// Spike A: with no MCP manager the model's tool call comes back as data, and nothing calls the provider again.
func TestReplayClient_ToolCallNotExecuted(t *testing.T) {
	srv, calls := fakeOpenAI(t)
	base := newTestAccount()
	base.add(schemas.OpenAI, srv.URL)
	client, err := newReplayClient(context.Background(), base, []Route{{Provider: schemas.OpenAI, Model: "gpt-test"}}, 2, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Shutdown)

	resp, bErr := client.ChatCompletionRequest(newCtx(), chatRequest())
	if bErr != nil {
		t.Fatalf("ChatCompletionRequest: %+v", bErr)
	}
	if got := toolCallNames(resp); !slices.Equal(got, []string{"shop-issue_refund"}) {
		t.Errorf("tool calls = %v, want [shop-issue_refund]", got)
	}
	time.Sleep(200 * time.Millisecond) // an agent loop would already have made its follow-up call
	if n := calls.Load(); n != 1 {
		t.Errorf("provider saw %d requests, want exactly 1", n)
	}
}

// Spike B: a main client and a replay client run side by side, and shutting the replay client down
// leaves the main client serving.
func TestReplayClient_CoexistsWithMainClient(t *testing.T) {
	srv, calls := fakeOpenAI(t)
	base := newTestAccount()
	base.add(schemas.OpenAI, srv.URL)

	main, err := bifrost.Init(context.Background(), schemas.BifrostConfig{Account: base, Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(main.Shutdown)

	replay, err := newReplayClient(context.Background(), base, []Route{{Provider: schemas.OpenAI, Model: "gpt-test"}}, 2, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]*bifrost.Bifrost{"main": main, "replay": replay} {
		if _, bErr := c.ChatCompletionRequest(newCtx(), chatRequest()); bErr != nil {
			t.Fatalf("%s client before shutdown: %+v", name, bErr)
		}
	}
	replay.Shutdown()

	if _, bErr := main.ChatCompletionRequest(newCtx(), chatRequest()); bErr != nil {
		t.Fatalf("main client after the replay client shut down: %+v", bErr)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("provider saw %d requests, want 3", n)
	}
}

func TestCheckCandidate(t *testing.T) { // S18
	err := CheckCandidate(Route{Provider: schemas.Perplexity, Model: "sonar"})
	var denied *UnsupportedCandidateError
	if !errors.As(err, &denied) || denied.Provider != schemas.Perplexity || !strings.HasPrefix(err.Error(), "unsupported_candidate") {
		t.Errorf("perplexity: err = %v; want an unsupported_candidate error", err)
	}
	for _, p := range []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic, schemas.Gemini, schemas.Bedrock} {
		if err := CheckCandidate(Route{Provider: p, Model: "m"}); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestNewReplayClient_RefusesUnsupportedCandidate(t *testing.T) { // S18
	srv, calls := fakeOpenAI(t)
	base := newTestAccount()
	base.add(schemas.OpenAI, srv.URL)
	base.add(schemas.Perplexity, srv.URL)
	routes := []Route{{Provider: schemas.OpenAI, Model: "gpt-test"}, {Provider: schemas.Perplexity, Model: "sonar"}}
	client, err := newReplayClient(context.Background(), base, routes, 1, nil, testLogger())
	if err == nil || client != nil {
		t.Fatal("a run with a perplexity route must be refused")
	}
	if calls.Load() != 0 {
		t.Errorf("%d provider calls before the refusal", calls.Load())
	}
}
