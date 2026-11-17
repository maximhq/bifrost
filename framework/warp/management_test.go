package warp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// fakeManagement is the gateway's Bifrost MCP as a test sees it: a fixed
// catalog, and a record of every call that reached /api and who made it.
type fakeManagement struct {
	catalog   *ManagementCatalog
	failWrite string // when set, createVirtualKey is refused by /api with this result
	mu        sync.Mutex
	calls     []fakeManagementCall
}

type fakeManagementCall struct {
	name    string
	args    map[string]any
	user    string
	catalog *ManagementCatalog
}

func newFakeManagement() *fakeManagement {
	return &fakeManagement{catalog: NewManagementCatalog([]ManagementTool{
		{Name: "listVirtualKeys", Description: "List virtual keys.", InputSchema: `{"type":"object","properties":{}}`},
		{Name: "createVirtualKey", Description: "Create a virtual key.", InputSchema: `{"type":"object","properties":{"name":{"type":"string"}}}`, Write: true},
	})}
}

func (f *fakeManagement) Catalog() *ManagementCatalog { return f.catalog }

func (f *fakeManagement) Call(ctx context.Context, catalog *ManagementCatalog, name string, args map[string]any) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	user, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
	f.calls = append(f.calls, fakeManagementCall{name: name, args: args, user: user, catalog: catalog})
	if name == "createVirtualKey" && f.failWrite != "" {
		return f.failWrite, true, nil
	}
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

// withManagement binds management tools at construction, for tests that run
// turns: Warp refuses a turn without the Bifrost MCP.
func withManagement(management ManagementTools) Option {
	return func(s *Service) { s.management = management }
}

// managementService is a configured Warp with creates allowed: the fake
// catalog's one write is a create, and these tests are about how writes behave.
func managementService(model *scriptedModel, store *memoryConversations, management ManagementTools) *Service {
	return managementServiceAllowing(model, store, management, schemas.WarpManagementCreate)
}

// managementServiceAllowing is managementService with these kinds of change
// allowed in Warp's settings; none is reads only, the default.
func managementServiceAllowing(model *scriptedModel, store *memoryConversations, management ManagementTools, writes ...string) *Service {
	row := validWarpConfigRow()
	if len(writes) > 0 {
		encoded, err := sonic.MarshalString(writes)
		if err != nil {
			panic(err)
		}
		row.ManagementWrites = &encoded
	}
	opts := []Option{
		WithConfigStore(&recordingStore{row: row}),
		WithVectorStore(newFakeWarpVectorStore()),
		WithLogReader(&fakeLogReader{}),
		WithChatFunc(model.respond),
		WithBifrostMCP(true),
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
	// The turn's catalog is the load's, narrowed to what Warp's settings allow; Allowing caches it and keeps the binding.
	require.Same(t, management.catalog.Allowing([]string{schemas.WarpManagementCreate}), calls[1].catalog, "the write runs against the catalog the turn classified it from")
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

// A confirmed write that /api refuses has still returned a result: the reply
// reporting the failure is the answer, not held back as one that rests on no data.
func TestWarpManagementFailedConfirmedWriteIsAnswered(t *testing.T) {
	store := newMemoryConversations()
	management := newFakeManagement()
	management.failWrite = `{"error":"a virtual key named team-a already exists"}`
	model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{
		ToolTurn("c1", "createVirtualKey", `{"name":"team-a"}`),
		TextTurn("The virtual key was not created: one named team-a already exists."),
		TextTurn("redirected reply"),
	}}
	service := managementService(model, store, management)
	first := runManagementTurn(t, service, "u1", "", "create a virtual key called team-a")
	require.NotNil(t, first.Question)

	second := runManagementTurn(t, service, "u1", first.ConversationID, first.Question.Options[0].Hint)
	require.Len(t, management.called(), 1)
	require.Equal(t, "The virtual key was not created: one named team-a already exists.", second.Answer)
	require.Equal(t, 2, model.calls, "the reply reporting the failure is not sent back for another step")
}

// Arguments too long to show whole on the card are refused rather than cut: the
// person must see everything the Confirm would run.
func TestWarpManagementWriteWithOversizedArgumentsIsRefused(t *testing.T) {
	store := newMemoryConversations()
	management := newFakeManagement()
	long := `{"name":"` + strings.Repeat("a", maxConfirmArgumentChars) + `"}`
	model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{
		ToolTurn("c1", "createVirtualKey", long),
		TextTurn("That name is too long to confirm."),
	}}
	service := managementService(model, store, management)
	response := runManagementTurn(t, service, "u1", "", "create a virtual key with a very long name")
	require.Nil(t, response.Question, "no card is offered for arguments it cannot show whole")
	require.Empty(t, management.called())
	require.Contains(t, inputText(t, model.lastInput), "too long to show")

	// Arguments that fit are shown whole.
	question, err := confirmationQuestion(ManagementTool{Name: "createVirtualKey", Write: true}, " "+`{"name":"`+strings.Repeat("b", 1400)+`"}`+" ")
	require.NoError(t, err)
	require.Contains(t, question.Question, `{"name":"`+strings.Repeat("b", 1400)+`"}?`)
}

// The claim on a card is durable: a Confirm that reaches another instance, or
// this one after a restart, while the card is still the thread's newest message
// does not run the write a second time.
func TestWarpManagementConfirmationIsClaimedDurably(t *testing.T) {
	store := newMemoryConversations()
	management := newFakeManagement()
	model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{
		ToolTurn("c1", "createVirtualKey", `{"name":"team-a"}`),
		TextTurn("Created virtual key team-a."),
		TextTurn("Nothing was run."),
	}}
	first := runManagementTurn(t, managementService(model, store, management), "u1", "", "create a virtual key called team-a")
	require.NotNil(t, first.Question)
	confirm := first.Question.Options[0].Hint

	// The confirmed turn is not filed, so the card stays the newest message.
	store.failAppend = errors.New("store unavailable")
	runManagementTurn(t, managementService(model, store, management), "u1", first.ConversationID, confirm)
	require.Len(t, management.called(), 1)
	store.failAppend = nil

	// A fresh service has no memory of the claim; only the store does.
	runManagementTurn(t, managementService(model, store, management), "u1", first.ConversationID, confirm)
	require.Len(t, management.called(), 1, "the write ran once")
	require.Contains(t, inputText(t, model.lastInput), "nothing was run")
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
	require.Contains(t, model.lastInstructions, managementNotAllowed, "with no way to confirm, the prompt keeps writes out")
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
	require.Contains(t, model.lastInstructions, managementWriteCapabilityFor([]string{schemas.WarpManagementCreate}))
	require.Contains(t, model.lastInstructions, managementCoverage)
	require.NotContains(t, model.lastInstructions, managementCapability)

	// With the management tools, the prompt reads configuration through them and
	// never tells the model only traffic is reachable.
	require.Contains(t, SystemPrompt, managementCapability)
	require.Contains(t, SystemPrompt, managementCoverage)
	require.NotContains(t, SystemPrompt, "no tool says what is set up")
	require.NotContains(t, SystemPrompt, "They do not cover any other configuration")
	withManagement := systemInstructionsFor(nil, toolAvailability{management: true}, timeContext{now: time.Unix(0, 0)})
	require.Equal(t, SystemPrompt, withManagement[:len(SystemPrompt)])
	require.NotEqual(t, SystemPrompt, managementWriteSystemPromptFor([]string{schemas.WarpManagementCreate}))

	// Without them (Warp decoupled from the Bifrost MCP), the prompt is the
	// read-only traffic one, with no mention of tools the model was not given.
	trafficOnly := systemInstructionsFor(nil, toolAvailability{}, timeContext{now: time.Unix(0, 0)})
	require.Equal(t, trafficSystemPrompt, trafficOnly[:len(trafficSystemPrompt)])
	for _, management := range []string{managementCapability, managementCoverage, managementConfiguredRule, "management tools"} {
		require.NotContains(t, trafficOnly, management)
	}
	require.Contains(t, trafficOnly, readOnlyCapability)
	require.Contains(t, trafficOnly, trafficCoverage)
	require.Contains(t, trafficOnly, trafficConfiguredRule)
}

// Without the Bifrost MCP Warp does not answer at all: the turn is refused
// before the model runs, so no traffic answer stands in for a configuration one.
func TestWarpTurnRefusedWithoutBifrostMCP(t *testing.T) {
	for name, management := range map[string]ManagementTools{
		"never bound":     nil,
		"spec not loaded": &fakeManagement{},
	} {
		t.Run(name, func(t *testing.T) {
			model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{TextTurn("There are two routing rules.")}}
			service := managementService(model, nil, management)
			_, err := service.NewTurn(context.Background(), &ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "what routing rules exist?"}}}, 64)
			require.ErrorIs(t, err, ErrNoBifrostMCP)
			require.Zero(t, model.calls, "the model must not run")

			view, err := service.ConfigView(context.Background())
			require.NoError(t, err)
			require.False(t, view.BifrostMCPAvailable)
		})
	}
	view, err := managementService(&scriptedModel{}, nil, newFakeManagement()).ConfigView(context.Background())
	require.NoError(t, err)
	require.True(t, view.BifrostMCPAvailable)
}

// Decoupled (BifrostMCPEnabled off, the default), a service ignores the Bifrost
// MCP the gateway still binds: the turn runs on Warp's own tools with the
// read-only traffic prompt, and the config read says Warp does not use it, so
// the dashboard shows no setup step.
func TestWarpDecoupledFromBifrostMCPByDefault(t *testing.T) {
	require.False(t, BifrostMCPEnabled, "Warp is decoupled from the Bifrost MCP for now")
	for name, management := range map[string]ManagementTools{
		"never bound": nil,
		"bound":       newFakeManagement(),
	} {
		t.Run(name, func(t *testing.T) {
			model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{TextTurn("Hello.")}}
			service := NewService(nil,
				WithConfigStore(&recordingStore{row: validWarpConfigRow()}),
				WithVectorStore(newFakeWarpVectorStore()),
				WithLogReader(&fakeLogReader{}),
				WithChatFunc(model.respond),
			)
			service.SetManagementTools(management)
			runManagementTurn(t, service, "u1", "", "which providers are configured?")
			require.NotZero(t, model.calls, "the turn runs without the Bifrost MCP")

			declared := map[string]bool{}
			for _, tool := range model.lastTools {
				declared[*tool.Name] = true
			}
			require.True(t, declared["query_metrics"])
			require.False(t, declared["listVirtualKeys"], "no management tool is offered")
			require.False(t, declared["createVirtualKey"], "no management tool is offered")
			require.Contains(t, model.lastInstructions, readOnlyCapability)
			require.NotContains(t, model.lastInstructions, "management tools")

			view, err := service.ConfigView(context.Background())
			require.NoError(t, err)
			require.False(t, view.BifrostMCPEnabled)
			require.False(t, view.BifrostMCPAvailable)
			require.Empty(t, view.ManagementToolSizes)
			require.False(t, view.ManagementWritesAvailable)
		})
	}
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

// A tool whose schema does not parse is dropped from the catalog, so one bad
// operation in the spec does not take every management tool, and the turn, with it.
func TestWarpManagementCatalogDropsInvalidSchemas(t *testing.T) {
	catalog := NewManagementCatalog([]ManagementTool{
		{Name: "brokenTool", InputSchema: `{"type":`},
		{Name: "getConfig", InputSchema: `{"type":"object"}`},
	})
	require.Equal(t, 1, catalog.Len())
	_, ok := catalog.Tool("brokenTool")
	require.False(t, ok)
	declared, err := catalog.declarations()
	require.NoError(t, err)
	require.Len(t, declared, 1)
	require.Equal(t, "getConfig", *declared[0].Name)
}

// An oversized management result keeps whole entries of its largest list, up
// to the budget, and says how many it kept, so the model can still answer from
// it; the rest of the document is left as it was.
func TestWarpManagementResultTrimsItsLargestList(t *testing.T) {
	var entries []string
	for i := 0; i < 60; i++ {
		entries = append(entries, `{"name":"provider-`+strings.Repeat("x", 3)+string(rune('a'+i%26))+`","config":"`+strings.Repeat("c", 600)+`"}`)
	}
	result := `{"providers":[` + strings.Join(entries, ",") + `],"total":60}`
	require.Greater(t, len(result), MaxToolResultBytes)

	bounded := boundManagementResult(result, `{"type":"object","properties":{}}`)
	require.LessOrEqual(t, len(bounded), MaxToolResultBytes)
	var doc struct {
		Providers []map[string]any `json:"providers"`
		Total     int              `json:"total"`
		Truncated map[string]any   `json:"truncated"`
	}
	require.NoError(t, sonic.UnmarshalString(bounded, &doc), bounded)
	require.NotEmpty(t, doc.Providers, "whole entries are kept, not none")
	require.Less(t, len(doc.Providers), 60)
	require.Equal(t, 60, doc.Total, "the rest of the document is unchanged")
	require.Equal(t, "providers", doc.Truncated["field"])
	require.EqualValues(t, len(doc.Providers), doc.Truncated["shown"])
	require.EqualValues(t, 60, doc.Truncated["total"])
	require.NotContains(t, bounded, "limit", "the operation takes no limit or filter, so none is suggested")
	require.True(t, strings.HasPrefix(bounded, `{"providers":[{"name":"provider-xxxa"`), "the original bytes and order are kept")

	// A top-level list is trimmed the same way and wrapped so the note fits.
	list := `[` + strings.Join(entries, ",") + `]`
	bounded = boundManagementResult(list, "")
	require.LessOrEqual(t, len(bounded), MaxToolResultBytes)
	var wrapped struct {
		Items     []map[string]any `json:"items"`
		Truncated map[string]any   `json:"truncated"`
	}
	require.NoError(t, sonic.UnmarshalString(bounded, &wrapped), bounded)
	require.NotEmpty(t, wrapped.Items)
	require.EqualValues(t, 60, wrapped.Truncated["total"])

	// A small result is untouched.
	require.Equal(t, `{"providers":[]}`, boundManagementResult(`{"providers":[]}`, ""))
}

// A result with no list to trim is refused as before, and the advice to page or
// filter is given only when the operation takes such a parameter.
func TestWarpManagementResultAdviceFollowsTheOperation(t *testing.T) {
	big := `{"blob":"` + strings.Repeat("b", MaxToolResultBytes) + `"}`
	withLimit := boundManagementResult(big, `{"type":"object","properties":{"limit":{"type":"integer"},"search":{"type":"string"}}}`)
	require.Contains(t, withLimit, `"truncated":true`)
	require.Contains(t, withLimit, "limit")
	require.Contains(t, withLimit, "search")

	without := boundManagementResult(big, `{"type":"object","properties":{"id":{"type":"string"}}}`)
	require.Contains(t, without, `"truncated":true`)
	require.NotContains(t, without, "limit")
	require.NotContains(t, without, "filter")
}

// newFakeManagementAllKinds offers a read and one write of every kind.
func newFakeManagementAllKinds() *fakeManagement {
	object := `{"type":"object","properties":{}}`
	return &fakeManagement{catalog: NewManagementCatalog([]ManagementTool{
		{Name: "listVirtualKeys", Description: "List virtual keys.", InputSchema: object},
		{Name: "createVirtualKey", Description: "Create a virtual key.", InputSchema: object, Kind: schemas.WarpManagementCreate},
		{Name: "updateVirtualKey", Description: "Update a virtual key.", InputSchema: object, Kind: schemas.WarpManagementUpdate},
		{Name: "deleteVirtualKey", Description: "Delete a virtual key.", InputSchema: object, Kind: schemas.WarpManagementDelete, Destructive: true},
		{Name: "testProviderKey", Description: "Test a provider key.", InputSchema: object, Kind: schemas.WarpManagementAction},
	})}
}

// Warp's settings decide which kinds of change the model sees. By default it is
// reads only: no write is declared, the prompt says an administrator can allow
// changes, and a write the model names anyway is not offered, so no card is made.
// Allowing kinds declares exactly those, and the prompt names them and how to
// decline the rest.
func TestWarpManagementWritesFollowWarpSettings(t *testing.T) {
	declared := func(model *scriptedModel) []string {
		var names []string
		for _, tool := range model.lastTools {
			names = append(names, *tool.Name)
		}
		return names
	}

	t.Run("reads only by default", func(t *testing.T) {
		management := newFakeManagementAllKinds()
		model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{ToolTurn("w1", "createVirtualKey", `{"name":"a"}`), TextTurn("I can't.")}}
		service := managementServiceAllowing(model, newMemoryConversations(), management)
		response := runManagementTurn(t, service, "u1", "", "create a virtual key called a")

		require.Nil(t, response.Question, "a write the settings do not allow makes no card")
		require.Empty(t, management.called())
		names := declared(model)
		require.Contains(t, names, "listVirtualKeys")
		for _, write := range []string{"createVirtualKey", "updateVirtualKey", "deleteVirtualKey", "testProviderKey"} {
			require.NotContains(t, names, write)
		}
		require.Contains(t, model.lastInstructions, managementNotAllowed)
		require.Contains(t, model.lastInstructions, "an administrator can allow it in Warp's settings")
	})

	t.Run("updates and deletes allowed", func(t *testing.T) {
		model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{TextTurn("ok")}}
		service := managementServiceAllowing(model, newMemoryConversations(), newFakeManagementAllKinds(), schemas.WarpManagementUpdate, schemas.WarpManagementDelete)
		runManagementTurn(t, service, "u1", "", "hi")

		names := declared(model)
		require.Contains(t, names, "updateVirtualKey")
		require.Contains(t, names, "deleteVirtualKey")
		require.NotContains(t, names, "createVirtualKey")
		require.NotContains(t, names, "testProviderKey")
		require.Contains(t, model.lastInstructions, "You can change and delete things")
		require.Contains(t, model.lastInstructions, "replaces the whole list", "an update reads first and sends lists whole")
		require.Contains(t, model.lastInstructions, "needs its exact target", "a delete names its target")
		require.Contains(t, model.lastInstructions, "You cannot create and run operations such as tests or cache flushes here")
		require.NotContains(t, model.lastInstructions, managementNotAllowed)
	})
}

// The settings page shows what each kind costs, a destructive write's card says
// it cannot be undone, and narrowing keeps the load's binding and is cached.
func TestWarpManagementCatalogSizesNarrowingAndCards(t *testing.T) {
	catalog := newFakeManagementAllKinds().catalog.Bind("load-1")
	sizes := catalog.sizes()
	kinds := make([]string, 0, len(sizes))
	for _, size := range sizes {
		kinds = append(kinds, size.Kind)
		require.Equal(t, 1, size.Tools, size.Kind)
		require.Positive(t, size.EstimatedTokens, size.Kind)
	}
	require.Equal(t, []string{ManagementReadKind, schemas.WarpManagementCreate, schemas.WarpManagementUpdate, schemas.WarpManagementDelete, schemas.WarpManagementAction}, kinds)

	narrowed := catalog.Allowing([]string{schemas.WarpManagementDelete})
	require.Equal(t, 2, narrowed.Len())
	require.Equal(t, "load-1", narrowed.Binding(), "a narrowed catalog calls against the same load")
	require.Same(t, narrowed, catalog.Allowing([]string{schemas.WarpManagementDelete}), "cached per set of kinds")
	require.Equal(t, []string{schemas.WarpManagementDelete}, narrowed.writeKinds())

	remove, _ := narrowed.Tool("deleteVirtualKey")
	question, err := confirmationQuestion(remove, `{"vk_id":"vk-1"}`)
	require.NoError(t, err)
	require.Equal(t, `Run deleteVirtualKey with {"vk_id":"vk-1"}? This cannot be undone.`, question.Question)
	require.Equal(t, &schemas.WarpConfirmAction{Tool: "deleteVirtualKey", Arguments: `{"vk_id":"vk-1"}`, Destructive: true}, question.Action)
	stored, err := sonic.MarshalString(storedQuestionFrom(question))
	require.NoError(t, err)
	require.Contains(t, stored, `"action":{"tool":"deleteVirtualKey"`)
	declarations, err := narrowed.declarations()
	require.NoError(t, err)
	for _, declaration := range declarations {
		if *declaration.Name == "deleteVirtualKey" {
			require.Contains(t, *declaration.Description, "cannot be undone")
		}
	}
	create, _ := catalog.Tool("createVirtualKey")
	question, err = confirmationQuestion(create, `{"name":"a"}`)
	require.NoError(t, err)
	require.Equal(t, `Run createVirtualKey with {"name":"a"}?`, question.Question)
	require.False(t, question.Action.Destructive)
}

// leasingManagement counts the leases its turns hold and whether it was closed,
// like the gateway's adapter, whose leases keep a replaced client open.
type leasingManagement struct {
	*fakeManagement
	leases, released, closed int
}

func (l *leasingManagement) LeaseCatalog() (*ManagementCatalog, func()) {
	l.leases++
	return l.catalog, func() { l.released++ }
}

func (l *leasingManagement) Close() error { l.closed++; return nil }

// A turn holds its catalog's load until it ends: RunTurn releases it, a turn
// that is never run releases it with Release, and releasing twice counts once.
// Replaced and shut-down management tools are closed.
func TestWarpManagementTurnLeasesItsCatalog(t *testing.T) {
	management := &leasingManagement{fakeManagement: newFakeManagement()}
	model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{TextTurn("No keys.")}}
	service := managementService(model, newMemoryConversations(), management)

	runManagementTurn(t, service, "u1", "", "list my virtual keys")
	require.Equal(t, 1, management.leases)
	require.Equal(t, 1, management.released, "RunTurn releases the turn's lease")

	turn, err := service.NewTurn(context.Background(), &ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}, 2)
	require.NoError(t, err)
	require.Equal(t, 2, management.leases)
	turn.Release()
	turn.Release()
	require.Equal(t, 2, management.released, "a turn that is not run releases once")

	replacement := &leasingManagement{fakeManagement: newFakeManagement()}
	service.SetManagementTools(replacement)
	require.Equal(t, 1, management.closed, "replaced tools are closed")
	service.SetManagementTools(replacement)
	require.Equal(t, 0, replacement.closed, "setting the same tools again does not close them")

	service.Shutdown()
	require.Equal(t, 1, replacement.closed, "shutdown closes the tools")
	late := &leasingManagement{fakeManagement: newFakeManagement()}
	service.SetManagementTools(late)
	require.Equal(t, 1, late.closed, "tools set after shutdown are closed at once")
}
