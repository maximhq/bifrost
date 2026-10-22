package warp

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// fakeManagement is the gateway's management MCP as a test sees it: a fixed
// catalog, and a record of every call that reached /api and who made it.
type fakeManagement struct {
	catalog *ManagementCatalog
	mu      sync.Mutex
	calls   []fakeManagementCall
}

type fakeManagementCall struct {
	name string
	args map[string]any
	user string
}

func newFakeManagement() *fakeManagement {
	return &fakeManagement{catalog: NewManagementCatalog([]ManagementTool{
		{Name: "listVirtualKeys", Description: "List virtual keys.", InputSchema: `{"type":"object","properties":{}}`},
		{Name: "createVirtualKey", Description: "Create a virtual key.", InputSchema: `{"type":"object","properties":{"name":{"type":"string"}}}`, Write: true},
	})}
}

func (f *fakeManagement) Catalog() *ManagementCatalog { return f.catalog }

func (f *fakeManagement) Call(ctx context.Context, name string, args map[string]any) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	user, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
	f.calls = append(f.calls, fakeManagementCall{name: name, args: args, user: user})
	if name == "createVirtualKey" {
		return `{"virtual_key":{"id":"vk-1","name":"team-a"}}`, false, nil
	}
	return `{"virtual_keys":[],"count":0}`, false, nil
}

func (f *fakeManagement) called() []fakeManagementCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeManagementCall(nil), f.calls...)
}

func managementService(model *scriptedModel, store *memoryConversations, management ManagementTools) *Service {
	opts := []Option{
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}),
		WithVectorStore(newFakeWarpVectorStore()),
		WithLogReader(&fakeLogReader{}),
		WithChatFunc(model.respond),
	}
	if store != nil {
		opts = append(opts, WithConversationStore(store))
	}
	service := NewService(nil, opts...)
	service.SetManagementTools(management)
	return service
}

// runManagementTurn sends one user message on a thread and returns the response.
func runManagementTurn(t *testing.T, service *Service, owner, conversationID, content string) ChatResponse {
	t.Helper()
	turn, err := service.NewTurn(context.Background(), &ChatRequest{
		ConversationID: conversationID,
		Messages:       []ChatMessage{{Role: "user", Content: content}},
	}, 64)
	require.NoError(t, err)
	return service.RunTurn(ownerCtx(owner), turn, nil)
}

// inputText flattens what the model was sent, so a test can check what it saw.
func inputText(t *testing.T, input []schemas.ResponsesMessage) string {
	t.Helper()
	encoded, err := sonic.MarshalString(input)
	require.NoError(t, err)
	return encoded
}

// A read runs when the model calls it. A create does not: the turn ends with a
// confirmation card, and only the person's Confirm runs it, once, as them.
func TestWarpManagementWriteRunsOnlyAfterConfirm(t *testing.T) {
	store := newMemoryConversations()
	management := newFakeManagement()
	model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{
		ToolTurn("c1", "listVirtualKeys", `{}`),
		ToolTurn("c2", "createVirtualKey", `{"name":"team-a"}`),
		TextTurn("Created virtual key team-a."),
		TextTurn("Nothing was run."),
	}}
	service := managementService(model, store, management)

	first := runManagementTurn(t, service, "u1", "", "create a virtual key called team-a")
	require.Len(t, management.called(), 1, "only the read runs before the person confirms")
	require.Equal(t, "listVirtualKeys", management.called()[0].name)
	require.NotNil(t, first.Question)
	require.Equal(t, ConfirmQuestionKind, first.Question.Kind)
	require.Contains(t, first.Question.Question, "createVirtualKey")
	require.Contains(t, first.Question.Question, `{"name":"team-a"}`)
	require.Len(t, first.Question.Options, 2)
	confirm := first.Question.Options[0].Hint
	require.True(t, strings.HasPrefix(confirm, confirmHintPrefix))
	require.Equal(t, cancelHint, first.Question.Options[1].Hint)

	// The card is filed with the pending call, which the API does not return.
	thread := store.threads[first.ConversationID]
	require.Contains(t, thread.Messages[len(thread.Messages)-1].QuestionJSON, `"tool":"createVirtualKey"`)
	detail, err := service.GetConversation(context.Background(), schemas.WarpOwnerID("u1"), first.ConversationID)
	require.NoError(t, err)
	require.NotNil(t, detail.Messages[len(detail.Messages)-1].Question)
	require.Nil(t, detail.Messages[len(detail.Messages)-1].Question.Pending)

	second := runManagementTurn(t, service, "u1", first.ConversationID, confirm)
	calls := management.called()
	require.Len(t, calls, 2)
	require.Equal(t, "createVirtualKey", calls[1].name)
	require.Equal(t, map[string]any{"name": "team-a"}, calls[1].args)
	require.Equal(t, "u1", calls[1].user, "the write runs as the person who confirmed it")
	require.Equal(t, "Created virtual key team-a.", second.Answer)
	seen := inputText(t, model.lastInput)
	require.Contains(t, seen, "Confirmed.")
	require.Contains(t, seen, `vk-1`, "the model reads the write's result")
	require.NotContains(t, seen, confirm[len(confirmHintPrefix):], "the model never sees the token")
	filed := store.threads[first.ConversationID].Messages
	require.Equal(t, "Confirm", filed[len(filed)-2].Content, "history files what was pressed, not the token")

	// The same Confirm again is not a second write.
	runManagementTurn(t, service, "u1", first.ConversationID, confirm)
	require.Len(t, management.called(), 2)
	require.Contains(t, inputText(t, model.lastInput), "already been answered or had expired")
}

// A Confirm only counts on the confirming person's own thread, and only while
// the card is fresh.
func TestWarpManagementConfirmationIsBoundToOwnerAndAge(t *testing.T) {
	for name, tamper := range map[string]func(store *memoryConversations, id string) string{
		"another owner": func(*memoryConversations, string) string { return "u2" },
		"expired": func(store *memoryConversations, id string) string {
			messages := store.threads[id].Messages
			messages[len(messages)-1].CreatedAt = time.Now().Add(-PendingCallTTL - time.Minute)
			return "u1"
		},
		"overtaken by a later turn": func(store *memoryConversations, id string) string {
			store.threads[id].Messages = append(store.threads[id].Messages, store.threads[id].Messages[0])
			return "u1"
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemoryConversations()
			management := newFakeManagement()
			model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{
				ToolTurn("c1", "createVirtualKey", `{"name":"team-a"}`),
				TextTurn("Nothing was run."),
			}}
			service := managementService(model, store, management)
			first := runManagementTurn(t, service, "u1", "", "create a virtual key called team-a")
			require.NotNil(t, first.Question)
			owner := tamper(store, first.ConversationID)

			turn, err := service.NewTurn(context.Background(), &ChatRequest{
				ConversationID: first.ConversationID,
				Messages:       []ChatMessage{{Role: "user", Content: first.Question.Options[0].Hint}},
			}, 64)
			require.NoError(t, err)
			service.RunTurn(ownerCtx(owner), turn, nil)
			require.Empty(t, management.called())
		})
	}
}

// Without conversation history there is nowhere to file the card, so a write is
// refused outright and the model is told why.
func TestWarpManagementWriteIsRefusedWithoutHistory(t *testing.T) {
	management := newFakeManagement()
	model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{
		ToolTurn("c1", "createVirtualKey", `{"name":"team-a"}`),
		TextTurn("I can't do that from Warp here."),
	}}
	service := managementService(model, nil, management)
	response := runManagementTurn(t, service, "u1", "", "create a virtual key called team-a")
	require.Nil(t, response.Question)
	require.Empty(t, management.called())
	require.Contains(t, inputText(t, model.lastInput), "need conversation history")
	require.Contains(t, model.lastInstructions, "You cannot create, change, delete", "with no way to confirm, the prompt keeps writes out")
}

// The management tools are declared beside Warp's own, writes say they need a
// confirmation, and the prompt stops claiming Warp only reads traffic.
func TestWarpManagementToolsAndPrompt(t *testing.T) {
	store := newMemoryConversations()
	model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{TextTurn("Hello.")}}
	service := managementService(model, store, newFakeManagement())
	runManagementTurn(t, service, "u1", "", "hi")

	declared := map[string]string{}
	for _, tool := range model.lastTools {
		declared[*tool.Name] = *tool.Description
	}
	require.Contains(t, declared, "query_metrics")
	require.Equal(t, "List virtual keys.", declared["listVirtualKeys"])
	require.Contains(t, declared["createVirtualKey"], "Runs only after the person confirms it")
	require.Contains(t, model.lastInstructions, managementWriteCapability)
	require.Contains(t, model.lastInstructions, managementCoverage)
	require.NotContains(t, model.lastInstructions, readOnlyCapability)
	require.NotContains(t, model.lastInstructions, trafficCoverage)

	// Without the management tools the prompt is exactly what it was.
	require.Contains(t, SystemPrompt, readOnlyCapability)
	require.Contains(t, SystemPrompt, trafficCoverage)
	require.Equal(t, SystemPrompt, systemInstructionsFor(nil, toolAvailability{}, timeContext{now: time.Unix(0, 0)})[:len(SystemPrompt)])
	require.NotEqual(t, SystemPrompt, managementSystemPrompt)
	require.NotEqual(t, SystemPrompt, managementWriteSystemPrompt)
}

// A management tool named like one of Warp's own is dropped, so the model never
// sees two tools with one name.
func TestWarpManagementCatalogDropsWarpsOwnNames(t *testing.T) {
	catalog := NewManagementCatalog([]ManagementTool{
		{Name: "query_metrics", InputSchema: `{"type":"object"}`},
		{Name: AskUserTool, InputSchema: `{"type":"object"}`},
		{Name: "getConfig", InputSchema: `{"type":"object"}`},
		{Name: "getConfig", InputSchema: `{"type":"object"}`},
	})
	require.Equal(t, 1, catalog.Len())
	_, ok := catalog.Tool("getConfig")
	require.True(t, ok)
}
