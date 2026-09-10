package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// promptReloadStore implements only the prompt repository slice of ConfigStore.
// Every write succeeds unless writeErr is set, which is enough to tell apart
// "the handler reloaded" from "the handler reloaded even though the write failed".
type promptReloadStore struct {
	configstore.ConfigStore
	writeErr error
}

func (s *promptReloadStore) GetFolderByID(context.Context, string) (*tables.TableFolder, error) {
	return &tables.TableFolder{ID: "folder-1", Name: "folder"}, nil
}
func (s *promptReloadStore) DeleteFolder(context.Context, string) error { return s.writeErr }
func (s *promptReloadStore) GetPromptByID(context.Context, string) (*tables.TablePrompt, error) {
	return &tables.TablePrompt{ID: "prompt-1", Name: "prompt"}, nil
}
func (s *promptReloadStore) CreatePrompt(context.Context, *tables.TablePrompt, ...*gorm.DB) error {
	return s.writeErr
}
func (s *promptReloadStore) UpdatePrompt(context.Context, *tables.TablePrompt) error {
	return s.writeErr
}
func (s *promptReloadStore) DeletePrompt(context.Context, string) error { return s.writeErr }
func (s *promptReloadStore) CreatePromptVersion(context.Context, *tables.TablePromptVersion) error {
	return s.writeErr
}
func (s *promptReloadStore) DeletePromptVersion(context.Context, uint) error { return s.writeErr }
func (s *promptReloadStore) GetPromptSessionByID(context.Context, uint) (*tables.TablePromptSession, error) {
	return &tables.TablePromptSession{
		ID:       1,
		PromptID: "prompt-1",
		Name:     "session",
		Messages: []tables.TablePromptSessionMessage{{PromptID: "prompt-1", Message: promptTestMessage}},
	}, nil
}
func (s *promptReloadStore) CreatePromptSession(context.Context, *tables.TablePromptSession) error {
	return s.writeErr
}
func (s *promptReloadStore) UpdatePromptSession(context.Context, *tables.TablePromptSession) error {
	return s.writeErr
}
func (s *promptReloadStore) RenamePromptSession(context.Context, uint, string) error {
	return s.writeErr
}
func (s *promptReloadStore) DeletePromptSession(context.Context, uint) error { return s.writeErr }

// promptTestMessage is the smallest valid message body. PromptMessage is a
// json.RawMessage alias, so a zero value fails to marshal.
var promptTestMessage = tables.PromptMessage(`{"role":"user","content":"hi"}`)

type countingPromptReloader struct {
	calls int
	err   error
}

func (r *countingPromptReloader) ReloadPromptCache(context.Context) error {
	r.calls++
	return r.err
}

// promptMutation is one write endpoint: the handler method, the route param it
// reads, and a body that gets it as far as the store write.
type promptMutation struct {
	name    string
	invoke  func(h *PromptsHandler, ctx *fasthttp.RequestCtx)
	routeID string
	body    any
}

func promptMutations() []promptMutation {
	return []promptMutation{
		{
			name:    "deleteFolder",
			invoke:  (*PromptsHandler).deleteFolder,
			routeID: "folder-1",
		},
		{
			name:   "createPrompt",
			invoke: (*PromptsHandler).createPrompt,
			body:   CreatePromptRequest{Name: "greeting"},
		},
		{
			name:    "updatePrompt",
			invoke:  (*PromptsHandler).updatePrompt,
			routeID: "prompt-1",
			body:    UpdatePromptRequest{Name: "greeting v2"},
		},
		{
			name:    "deletePrompt",
			invoke:  (*PromptsHandler).deletePrompt,
			routeID: "prompt-1",
		},
		{
			name:    "createVersion",
			invoke:  (*PromptsHandler).createVersion,
			routeID: "prompt-1",
			body:    CreateVersionRequest{CommitMessage: "first", Messages: []tables.PromptMessage{promptTestMessage}},
		},
		{
			name:    "deleteVersion",
			invoke:  (*PromptsHandler).deleteVersion,
			routeID: "1",
		},
		{
			name:    "createSession",
			invoke:  (*PromptsHandler).createSession,
			routeID: "prompt-1",
			body:    CreateSessionRequest{Name: "scratch", Messages: []tables.PromptMessage{promptTestMessage}},
		},
		{
			name:    "updateSession",
			invoke:  (*PromptsHandler).updateSession,
			routeID: "1",
			body:    UpdateSessionRequest{Name: "scratch", Messages: []tables.PromptMessage{promptTestMessage}},
		},
		{
			name:    "deleteSession",
			invoke:  (*PromptsHandler).deleteSession,
			routeID: "1",
		},
		{
			name:    "renameSession",
			invoke:  (*PromptsHandler).renameSession,
			routeID: "1",
			body:    RenameSessionRequest{Name: "renamed"},
		},
		{
			name:    "commitSession",
			invoke:  (*PromptsHandler).commitSession,
			routeID: "1",
			body:    CommitSessionRequest{CommitMessage: "publish"},
		},
	}
}

func promptRequestCtx(t *testing.T, m promptMutation) *fasthttp.RequestCtx {
	t.Helper()
	ctx := &fasthttp.RequestCtx{}
	if m.routeID != "" {
		ctx.SetUserValue("id", m.routeID)
	}
	if m.body != nil {
		payload, err := json.Marshal(m.body)
		require.NoError(t, err)
		ctx.Request.SetBody(payload)
	}
	return ctx
}

// Every write endpoint has to reload, because in a cluster the reload is also
// what gossips the change to the other nodes. An endpoint that skips it leaves
// peers resolving prompts against a stale in-memory index.
func TestPromptMutationsReloadCache(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, m := range promptMutations() {
		t.Run(m.name, func(t *testing.T) {
			reloader := &countingPromptReloader{}
			handler := NewPromptsHandler(&promptReloadStore{}, reloader)
			ctx := promptRequestCtx(t, m)

			m.invoke(handler, ctx)

			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			require.Equal(t, 1, reloader.calls)
		})
	}
}

func TestPromptMutationsDoNotReloadOnStoreFailure(t *testing.T) {
	SetLogger(&mockLogger{})
	for _, m := range promptMutations() {
		t.Run(m.name, func(t *testing.T) {
			reloader := &countingPromptReloader{}
			handler := NewPromptsHandler(&promptReloadStore{writeErr: errors.New("database unavailable")}, reloader)
			ctx := promptRequestCtx(t, m)

			m.invoke(handler, ctx)

			require.Equal(t, fasthttp.StatusInternalServerError, ctx.Response.StatusCode())
			require.Zero(t, reloader.calls, "peers must not be told to reload a write that did not land")
		})
	}
}

// The reloader is nil when the prompts plugin is not loaded, and a reload error
// is logged rather than failing the write the user already made.
func TestPromptMutationsSurviveReloaderProblems(t *testing.T) {
	SetLogger(&mockLogger{})
	body := CreatePromptRequest{Name: "greeting"}
	payload, err := json.Marshal(body)
	require.NoError(t, err)

	t.Run("nil reloader", func(t *testing.T) {
		handler := NewPromptsHandler(&promptReloadStore{}, nil)
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetBody(payload)

		handler.createPrompt(ctx)

		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	})

	t.Run("failing reloader", func(t *testing.T) {
		reloader := &countingPromptReloader{err: errors.New("cluster unreachable")}
		handler := NewPromptsHandler(&promptReloadStore{}, reloader)
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetBody(payload)

		handler.createPrompt(ctx)

		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
		require.Equal(t, 1, reloader.calls)
	})
}
