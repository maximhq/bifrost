package handlers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/tracing"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// liveOwnerStore is a config store that only remembers provider objects, the way the real one
// records which virtual key opened a live session.
type liveOwnerStore struct {
	configstore.ConfigStore
	mu        sync.Mutex
	objects   map[string]*tables.TableProviderObject
	err       error // returned by every call when set
	upsertErr error // returned by writes alone when set
}

func (s *liveOwnerStore) UpsertProviderObject(_ context.Context, object *tables.TableProviderObject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if s.upsertErr != nil {
		return s.upsertErr
	}
	if s.objects == nil {
		s.objects = make(map[string]*tables.TableProviderObject)
	}
	if _, exists := s.objects[object.ID]; !exists {
		copied := *object
		s.objects[object.ID] = &copied
	}
	return nil
}

func (s *liveOwnerStore) GetProviderObjectsByIDs(_ context.Context, ids []string) ([]*tables.TableProviderObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	var objects []*tables.TableProviderObject
	for _, id := range ids {
		if object, ok := s.objects[id]; ok {
			objects = append(objects, object)
		}
	}
	return objects, nil
}

func (s *liveOwnerStore) GetProviderObjectsByObjectID(_ context.Context, kind, objectID string) ([]*tables.TableProviderObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	var objects []*tables.TableProviderObject
	for _, object := range s.objects {
		if object.Kind == kind && object.ObjectID == objectID {
			objects = append(objects, object)
		}
	}
	return objects, nil
}

func (s *liveOwnerStore) owner(kind, provider, id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if object, ok := s.objects[tables.ProviderObjectID(kind, provider, id)]; ok {
		return object.VirtualKeyID
	}
	return ""
}

// nopLiveIO is a session transport that drops every write.
type nopLiveIO struct{}

func (nopLiveIO) sendUpstream([]byte) error { return nil }
func (nopLiveIO) sendClient([]byte) error   { return nil }
func (nopLiveIO) abandonUpstream()          {}

// capturingLiveIO is a session transport that keeps every write.
type capturingLiveIO struct {
	mu       sync.Mutex
	upstream []string
	client   []string
}

func (r *capturingLiveIO) sendUpstream(message []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upstream = append(r.upstream, string(message))
	return nil
}

func (r *capturingLiveIO) sendClient(message []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.client = append(r.client, string(message))
	return nil
}

func (r *capturingLiveIO) abandonUpstream() {}

func (r *capturingLiveIO) sent() (upstream, client []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.upstream...), append([]string(nil), r.client...)
}

// newLiveOwnerTestGateway returns a gateway over a real client with one OpenAI key, so admission
// selects a key and opens billing the way the handlers do, and the given store for ownership.
// Each extra name is configured as a custom provider on OpenAI's API with its own key.
func newLiveOwnerTestGateway(t *testing.T, store configstore.ConfigStore, extra ...schemas.ModelProvider) *liveGateway {
	t.Helper()
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{EnforceVKObjectOwnership: true}, // the default; ClientConfig{} literals are off
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {Keys: []schemas.Key{{ID: "key-1", Name: "openai", Value: *schemas.NewSecretVar("sk-test"), Weight: 1, Models: []string{"gpt-live-1"}}}},
		},
	}
	for _, name := range extra {
		config.Providers[name] = configstore.ProviderConfig{
			Keys:                 []schemas.Key{{ID: "key-" + string(name), Name: string(name), Value: *schemas.NewSecretVar("sk-test"), Weight: 1, Models: []string{"gpt-live-1"}}},
			CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI},
		}
	}
	if store != nil {
		config.ConfigStore = store
	}
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: lib.NewBaseAccount(config),
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)
	config.SetBifrostClient(client)
	return &liveGateway{client: client, config: config, handlerStore: config}
}

// liveCallerCtx is the pre-request context of a caller, as the pre-request pipeline leaves it:
// with a Grant whose identity carries the resolved virtual key when the caller presented one.
func liveCallerCtx(t *testing.T, vkID string) *schemas.BifrostContext {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	t.Cleanup(cancel)
	ctx.SetGrant(grant.New())
	if vkID != "" {
		ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-"+vkID)
		ctx.Grant().SetIdentity(grant.NewIdentity(grant.NewCredential(grant.CredentialVirtualKey, "sk-bf-"+vkID), nil, &schemas.EntityRef{ID: vkID, Name: vkID}, nil, nil, nil, nil))
	}
	return ctx
}

// TestLiveSessionsBindToTheVirtualKeyThatOpenedThem pins that a live session belongs to the
// virtual key that opened it, as batches and files do: the provider's session id is recorded
// under that key when it becomes known, on the WebRTC create response and on the WebSocket
// session.started frame, and a route on the session from another virtual key is refused as not
// found before a key is selected or anything is dialed.
func TestLiveSessionsBindToTheVirtualKeyThatOpenedThem(t *testing.T) {
	t.Parallel()

	store := &liveOwnerStore{}
	g := newLiveOwnerTestGateway(t, store)
	const path = "/v1/live/sessions"
	open := func(t *testing.T, vkID string) *liveAdmission {
		t.Helper()
		target := liveTarget{provider: &fakeLiveWebRTCProvider{}, providerKey: schemas.OpenAI, voiceModel: "gpt-live-1"}
		admission, bifrostErr := g.admit(&authHeaders{}, liveCallerCtx(t, vkID), map[any]any{}, path, target, "bf-"+vkID)
		if bifrostErr != nil {
			t.Fatalf("admission refused: %d %s", *bifrostErr.StatusCode, bifrostErr.Error.Message)
		}
		t.Cleanup(admission.cancel)
		return admission
	}

	// A WebRTC session is bound when the create response names it.
	webrtc := open(t, "vk-a")
	created, _, bifrostErr := createLiveWebRTCSession(webrtc, []byte(`{"session":{"model":"gpt-live-1"},"transport":{"type":"webrtc","sdp":"offer"}}`), "bifrost offer")
	require.Nil(t, bifrostErr)
	assert.Equal(t, "vk-a", store.owner(tables.ProviderObjectKindLiveSession, "openai", created.Session.ID), "the WebRTC create binds the session to the key that opened it")

	// A WebSocket session is bound when session.started names it.
	websocket := open(t, "vk-a")
	websocket.controller(g, nopLiveIO{}).fromUpstream([]byte(`{"type":"session.started","session":{"id":"live_ws"}}`))
	assert.Equal(t, "vk-a", store.owner(tables.ProviderObjectKindLiveSession, "openai", "live_ws"), "session.started binds the session to the key that opened it")

	// A session opened with no virtual key is nobody's.
	anonymous := open(t, "")
	anonymous.controller(g, nopLiveIO{}).fromUpstream([]byte(`{"type":"session.started","session":{"id":"live_open"}}`))
	assert.Empty(t, store.owner(tables.ProviderObjectKindLiveSession, "openai", "live_open"))

	resolve := func(t *testing.T, g *liveGateway, route, vkID, sessionID string) (*fasthttp.RequestCtx, bool) {
		t.Helper()
		preReqCtx := liveCallerCtx(t, vkID)
		req := &liveRequest{path: path + "/" + sessionID + "/" + route, auth: &authHeaders{}, preReqCtx: preReqCtx, cancel: func() {}, middlewareValues: map[any]any{}}
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI(req.path)
		ctx.SetUserValue("session_id", sessionID)
		return ctx, g.resolveSession(ctx, req)
	}
	for _, route := range []string{"content", "attach"} {
		t.Run(route, func(t *testing.T) {
			for _, sessionID := range []string{created.Session.ID, "live_ws"} {
				ctx, ok := resolve(t, g, route, "vk-b", sessionID)
				assert.False(t, ok, "%s of %s by another key must be refused", route, sessionID)
				assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode(), "body=%s", ctx.Response.Body())
				assert.Contains(t, string(ctx.Response.Body()), "live session not found")

				ctx, ok = resolve(t, g, route, "vk-a", sessionID)
				assert.True(t, ok, "the key that opened %s keeps it; body=%s", sessionID, ctx.Response.Body())
			}
			// A caller with no virtual key is unrestricted, and a session nobody recorded stays reachable.
			_, ok := resolve(t, g, route, "", created.Session.ID)
			assert.True(t, ok)
			_, ok = resolve(t, g, route, "vk-b", "live_open")
			assert.True(t, ok)
			_, ok = resolve(t, g, route, "vk-b", "live_never_opened")
			assert.True(t, ok)
		})
	}

	// A virtual-key caller cannot be checked without a config store, or when the store fails.
	ctx, ok := resolve(t, newLiveOwnerTestGateway(t, nil), "content", "vk-b", "live_ws")
	assert.False(t, ok)
	assert.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode(), "body=%s", ctx.Response.Body())
	assert.Contains(t, string(ctx.Response.Body()), "config store")
	ctx, ok = resolve(t, newLiveOwnerTestGateway(t, &liveOwnerStore{err: errors.New("db down")}), "attach", "vk-b", "live_ws")
	assert.False(t, ok)
	assert.Equal(t, fasthttp.StatusInternalServerError, ctx.Response.StatusCode(), "body=%s", ctx.Response.Body())
	assert.NotContains(t, string(ctx.Response.Body()), "db down", "store errors stay in the server log")
}

// With client.enforce_vk_object_ownership turned off a live session is reachable by every
// virtual key, but its opener is still recorded, so turning the setting back on binds it. Recording
// does not depend on the setting: a virtual-key session that cannot be recorded (no config store,
// or a failed owner write) is refused either way, so no session exists that a later switch-on
// could not bind.
func TestLiveSessionOwnershipFollowsTheClientSetting(t *testing.T) {
	t.Parallel()

	const path = "/v1/live/sessions"
	target := liveTarget{provider: &fakeLiveWebRTCProvider{}, providerKey: schemas.OpenAI, voiceModel: "gpt-live-1"}
	offer := []byte(`{"session":{"model":"gpt-live-1"},"transport":{"type":"webrtc","sdp":"offer"}}`)
	gatewayOff := func(t *testing.T, store configstore.ConfigStore) *liveGateway {
		g := newLiveOwnerTestGateway(t, store)
		g.config.ClientConfig.EnforceVKObjectOwnership = false
		return g
	}
	resolve := func(t *testing.T, g *liveGateway, vkID, sessionID string) (*fasthttp.RequestCtx, bool) {
		t.Helper()
		req := &liveRequest{path: path + "/" + sessionID + "/content", auth: &authHeaders{}, preReqCtx: liveCallerCtx(t, vkID), cancel: func() {}, middlewareValues: map[any]any{}}
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI(req.path)
		ctx.SetUserValue("session_id", sessionID)
		return ctx, g.resolveSession(ctx, req)
	}

	store := &liveOwnerStore{}
	g := gatewayOff(t, store)
	admission, bifrostErr := g.admit(&authHeaders{}, liveCallerCtx(t, "vk-a"), map[any]any{}, path, target, "bf-off")
	require.Nil(t, bifrostErr)
	t.Cleanup(admission.cancel)
	created, _, bifrostErr := createLiveWebRTCSession(admission, offer, "bifrost offer")
	require.Nil(t, bifrostErr)
	assert.Equal(t, "vk-a", store.owner(tables.ProviderObjectKindLiveSession, "openai", created.Session.ID), "the opener is recorded while the setting is off")
	ctx, ok := resolve(t, g, "vk-b", created.Session.ID)
	assert.True(t, ok, "another key reaches the session while the setting is off; body=%s", ctx.Response.Body())

	g.config.ClientConfig.EnforceVKObjectOwnership = true
	ctx, ok = resolve(t, g, "vk-b", created.Session.ID)
	assert.False(t, ok, "another key is refused once the setting is on")
	assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode(), "body=%s", ctx.Response.Body())

	// No config store: a virtual-key session is refused at admission while the setting is off.
	noStore := gatewayOff(t, nil)
	admission, bifrostErr = noStore.admit(&authHeaders{}, liveCallerCtx(t, "vk-a"), map[any]any{}, path, target, "bf-off-nostore")
	if admission != nil {
		t.Cleanup(admission.cancel)
	}
	require.NotNil(t, bifrostErr, "a virtual-key session without a config store is refused while the setting is off")
	assert.Equal(t, 403, *bifrostErr.StatusCode)

	// A failed owner write ends the session while the setting is off.
	failing := gatewayOff(t, &liveOwnerStore{upsertErr: errors.New("db down")})
	admission, bifrostErr = failing.admit(&authHeaders{}, liveCallerCtx(t, "vk-a"), map[any]any{}, path, target, "bf-off-failing")
	require.Nil(t, bifrostErr)
	t.Cleanup(admission.cancel)
	_, _, bifrostErr = createLiveWebRTCSession(admission, offer, "bifrost offer")
	require.NotNil(t, bifrostErr, "a session whose owner cannot be recorded is refused while the setting is off")
}

// A config save copies the whole client config in under Config.Mu while live routes read the
// ownership setting. The read must take the same lock, or the race detector reports the two.
func TestLiveSessionOwnershipReadIsOrderedWithConfigSaves(t *testing.T) {
	t.Parallel()

	g := newLiveOwnerTestGateway(t, &liveOwnerStore{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			updated := *g.config.ClientConfig
			updated.EnforceVKObjectOwnership = i%2 == 0
			g.config.Mu.Lock()
			*g.config.ClientConfig = updated
			g.config.Mu.Unlock()
		}
	}()
	for i := 0; i < 200; i++ {
		_ = g.ownershipEnforced()
	}
	<-done
}

// The control routes resolve their provider by the path and, off a provider route, prefer openai
// among the configured providers that serve live sessions. A session opened on another such
// provider (a custom provider on OpenAI's API) is recorded under that provider's name, and must
// still be found when the control route resolves to openai: the id names the same upstream session.
func TestLiveSessionOwnerIsFoundUnderEveryLiveProvider(t *testing.T) {
	t.Parallel()

	store := &liveOwnerStore{}
	g := newLiveOwnerTestGateway(t, store, "custom-live")
	require.NoError(t, store.UpsertProviderObject(context.Background(), &tables.TableProviderObject{
		ID:           tables.ProviderObjectID(tables.ProviderObjectKindLiveSession, "custom-live", "live_custom"),
		Kind:         tables.ProviderObjectKindLiveSession,
		Provider:     "custom-live",
		ObjectID:     "live_custom",
		VirtualKeyID: "vk-a",
	}))
	resolve := func(t *testing.T, vkID string) (*fasthttp.RequestCtx, bool) {
		t.Helper()
		req := &liveRequest{path: "/v1/live/sessions/live_custom/content", auth: &authHeaders{}, preReqCtx: liveCallerCtx(t, vkID), cancel: func() {}, middlewareValues: map[any]any{}}
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI(req.path)
		ctx.SetUserValue("session_id", "live_custom")
		ok := g.resolveSession(ctx, req)
		if ok {
			assert.Equal(t, schemas.OpenAI, req.providerKey, "the control route resolves to openai")
		}
		return ctx, ok
	}

	ctx, ok := resolve(t, "vk-b")
	assert.False(t, ok, "another key must not reach a session recorded under the custom provider's name; body=%s", ctx.Response.Body())
	assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode())

	_, ok = resolve(t, "vk-a")
	assert.True(t, ok, "the key that opened it keeps it")
}

// A session's owner record outlives the provider name it was recorded under. If the custom
// provider that opened a session is deleted while another provider still reaches the same
// upstream, the session must stay its opener's: the owner is looked up by the session id, not by
// the provider names configured now.
func TestLiveSessionOwnerSurvivesRemovalOfTheOpeningProvider(t *testing.T) {
	t.Parallel()

	store := &liveOwnerStore{}
	g := newLiveOwnerTestGateway(t, store) // custom-live is no longer configured; only openai is
	require.NoError(t, store.UpsertProviderObject(context.Background(), &tables.TableProviderObject{
		ID:           tables.ProviderObjectID(tables.ProviderObjectKindLiveSession, "custom-live", "live_orphaned"),
		Kind:         tables.ProviderObjectKindLiveSession,
		Provider:     "custom-live",
		ObjectID:     "live_orphaned",
		VirtualKeyID: "vk-a",
	}))
	resolve := func(t *testing.T, vkID string) (*fasthttp.RequestCtx, bool) {
		t.Helper()
		req := &liveRequest{path: "/v1/live/sessions/live_orphaned/content", auth: &authHeaders{}, preReqCtx: liveCallerCtx(t, vkID), cancel: func() {}, middlewareValues: map[any]any{}}
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI(req.path)
		ctx.SetUserValue("session_id", "live_orphaned")
		return ctx, g.resolveSession(ctx, req)
	}

	ctx, ok := resolve(t, "vk-b")
	assert.False(t, ok, "another key must not reach a session whose opening provider was removed; body=%s", ctx.Response.Body())
	assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode())

	_, ok = resolve(t, "vk-a")
	assert.True(t, ok, "the key that opened it keeps it")
}

// A session whose opener cannot be recorded is refused, not opened unbound: a WebRTC create
// answers an error, and a WebSocket session is ended with an error to the client before the
// provider's session id is forwarded, so no other key can ever address it.
func TestLiveSessionIsRefusedWhenItsOwnerCannotBeRecorded(t *testing.T) {
	t.Parallel()

	store := &liveOwnerStore{upsertErr: errors.New("db down")}
	g := newLiveOwnerTestGateway(t, store)
	const path = "/v1/live/sessions"
	open := func(t *testing.T) *liveAdmission {
		t.Helper()
		target := liveTarget{provider: &fakeLiveWebRTCProvider{}, providerKey: schemas.OpenAI, voiceModel: "gpt-live-1"}
		admission, bifrostErr := g.admit(&authHeaders{}, liveCallerCtx(t, "vk-a"), map[any]any{}, path, target, "bf-refused")
		if bifrostErr != nil {
			t.Fatalf("admission refused: %d %s", *bifrostErr.StatusCode, bifrostErr.Error.Message)
		}
		t.Cleanup(admission.cancel)
		return admission
	}

	created, _, bifrostErr := createLiveWebRTCSession(open(t), []byte(`{"session":{"model":"gpt-live-1"},"transport":{"type":"webrtc","sdp":"offer"}}`), "bifrost offer")
	require.NotNil(t, bifrostErr, "a WebRTC session whose owner cannot be recorded is refused")
	assert.Nil(t, created)
	assert.Equal(t, 500, *bifrostErr.StatusCode)
	assert.NotContains(t, bifrostErr.Error.Message, "db down", "store errors stay in the server log")

	io := &capturingLiveIO{}
	controller := open(t).controller(g, io)
	controller.fromUpstream([]byte(`{"type":"session.started","session":{"id":"live_unrecorded"}}`))
	upstream, client := io.sent()
	require.Len(t, client, 1, "the client is told the session is ending")
	assert.Contains(t, client[0], `"type":"error"`)
	assert.NotContains(t, client[0], "db down")
	assert.Equal(t, []string{`{"type":"session.close"}`}, upstream, "the session is closed upstream")
	assert.Empty(t, store.owner(tables.ProviderObjectKindLiveSession, "openai", "live_unrecorded"))
}

// Without a config store a session cannot be bound to the virtual key that opened it, and the
// control routes refuse virtual keys there, so the opener could never reach its own session again.
// A virtual-key session is refused at admission, with the reason, before the provider is called or
// the session's billing is opened, so the refused caller is never charged the minimum; a session
// opened with no virtual key is nobody's and still opens.
func TestLiveSessionWithAVirtualKeyIsRefusedWithoutAConfigStore(t *testing.T) {
	t.Parallel()

	g := newLiveOwnerTestGateway(t, nil)
	const path = "/v1/live/sessions"
	provider := &fakeLiveWebRTCProvider{}
	target := liveTarget{provider: provider, providerKey: schemas.OpenAI, voiceModel: "gpt-live-1"}

	admission, bifrostErr := g.admit(&authHeaders{}, liveCallerCtx(t, "vk-a"), map[any]any{}, path, target, "bf-nostore-vk-a")
	if admission != nil {
		t.Cleanup(admission.cancel)
	}
	require.NotNil(t, bifrostErr, "a virtual-key session is refused at admission without a config store")
	assert.Nil(t, admission, "no admission, so no billing is opened and no provider is called")
	assert.Equal(t, 403, *bifrostErr.StatusCode)
	assert.Contains(t, bifrostErr.Error.Message, "config store")
	assert.Nil(t, provider.body, "the provider was never asked to open the session")

	admission, bifrostErr = g.admit(&authHeaders{}, liveCallerCtx(t, ""), map[any]any{}, path, target, "bf-nostore-none")
	require.Nil(t, bifrostErr, "a session opened with no virtual key is admitted without a config store")
	t.Cleanup(admission.cancel)
	created, _, bifrostErr := createLiveWebRTCSession(admission, []byte(`{"session":{"model":"gpt-live-1"},"transport":{"type":"webrtc","sdp":"offer"}}`), "bifrost offer")
	require.Nil(t, bifrostErr, "a session opened with no virtual key still opens without a config store")
	assert.NotNil(t, created)
}

func TestLiveBackendModelAndRewrite(t *testing.T) {
	t.Parallel()

	start := &schemas.LiveSession{
		Model: "openai/gpt-live-1",
		Delegation: &schemas.LiveDelegationConfig{
			Type:      schemas.LiveDelegationResponses,
			Responses: &schemas.LiveResponsesDelegation{Model: "openai/luna"},
		},
	}
	model, err := liveBackendModel(start, schemas.OpenAI)
	require.NoError(t, err)
	assert.Equal(t, "luna", model)

	key := schemas.Key{Aliases: schemas.KeyAliases{"luna": {ModelID: "gpt-5.6-luna"}}}
	frame := []byte(`{"type":"session.start","session":{"model":"openai/gpt-live-1","delegation":{"type":"responses","responses":{"model":"openai/luna","tools":[{"type":"web_search"}]}}}}`)
	rewritten, err := rewriteLiveModels(frame, start, key, "gpt-live-1", model)
	require.NoError(t, err)
	assert.Equal(t, `{"type":"session.start","session":{"model":"gpt-live-1","delegation":{"type":"responses","responses":{"model":"gpt-5.6-luna","tools":[{"type":"web_search"}]}}}}`, string(rewritten))

	// A bare, unaliased frame is sent exactly as received.
	bare := []byte(`{"type":"session.start","session":{"model":"gpt-live-1"}}`)
	unchanged, err := rewriteLiveModels(bare, &schemas.LiveSession{Model: "gpt-live-1"}, schemas.Key{}, "gpt-live-1", "")
	require.NoError(t, err)
	assert.Equal(t, string(bare), string(unchanged))

	// Client delegation has no backend on the socket.
	model, err = liveBackendModel(&schemas.LiveSession{Delegation: &schemas.LiveDelegationConfig{Type: schemas.LiveDelegationClient}}, schemas.OpenAI)
	require.NoError(t, err)
	assert.Empty(t, model)

	_, err = liveBackendModel(&schemas.LiveSession{Delegation: &schemas.LiveDelegationConfig{Type: schemas.LiveDelegationResponses, Responses: &schemas.LiveResponsesDelegation{Model: "anthropic/claude-opus-5"}}}, schemas.OpenAI)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "must be served by openai"))
}

// splitKeysAccount exposes OpenAI with a key per model and one key for every model, so key
// selection has a choice governance can narrow.
type splitKeysAccount struct{}

func (splitKeysAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}

// GetKeysForProvider honours the keys governance allowed on the context, as the transport's
// account does.
func (splitKeysAccount) GetKeysForProvider(ctx context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	if p != schemas.OpenAI {
		return nil, nil
	}
	keys := []schemas.Key{
		{ID: "voice-only", Name: "voice-only", Value: *schemas.NewSecretVar("sk-voice"), Models: schemas.WhiteList{"gpt-live-1"}, Weight: 1.0},
		{ID: "backend-only", Name: "backend-only", Value: *schemas.NewSecretVar("sk-backend"), Models: schemas.WhiteList{"gpt-5.6-luna"}, Weight: 1.0},
		{ID: "every-model", Name: "every-model", Value: *schemas.NewSecretVar("sk-every"), Models: schemas.WhiteList{"*"}, Weight: 1.0},
	}
	allowed, restricted := ctx.Value(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys).([]string)
	if !restricted {
		return keys, nil
	}
	return slices.DeleteFunc(keys, func(key schemas.Key) bool { return !slices.Contains(allowed, key.ID) }), nil
}

func (splitKeysAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if p != schemas.OpenAI {
		return nil, fmt.Errorf("unsupported provider %s", p)
	}
	return &schemas.ProviderConfig{
		NetworkConfig:            schemas.DefaultNetworkConfig,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}, nil
}

// allowedKeysPlugin narrows a request to the keys its virtual key may use, as governance does,
// and records the key each unit was billed on.
type allowedKeysPlugin struct {
	ids    []string
	billed *[]string
}

func (allowedKeysPlugin) GetName() string { return "allowed-keys" }
func (allowedKeysPlugin) Cleanup() error  { return nil }
func (allowedKeysPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (p allowedKeysPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	ctx.SetValue(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys, p.ids)
	return req, nil, nil
}
func (p allowedKeysPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	keyID, _ := ctx.Value(schemas.BifrostContextKeySelectedKeyID).(string)
	*p.billed = append(*p.billed, keyID)
	return resp, bifrostErr, nil
}

// A live session's key is chosen from the keys governance allows its virtual key, and only
// after governance has admitted the session's units, since that is when the allowed keys are known.
func TestLiveAdmitSelectsAmongGovernanceAllowedKeys(t *testing.T) {
	SetLogger(bifrost.NewDefaultLogger(schemas.LogLevelWarn))
	cases := []struct {
		name    string
		allowed []string
		backend string
		wantKey string
		wantErr string
	}{
		{name: "keys serving one model each refuse a two-model session", allowed: []string{"voice-only", "backend-only"}, backend: "gpt-5.6-luna", wantErr: "no keys found"},
		{name: "the allowed key serving both models is chosen", allowed: []string{"voice-only", "every-model"}, backend: "gpt-5.6-luna", wantKey: "every-model"},
		{name: "a voice-only session takes the allowed voice key", allowed: []string{"voice-only"}, wantKey: "voice-only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := tracing.NewTraceStore(5*time.Minute, nil)
			defer store.Stop()
			tracer := tracing.NewTracer(store, nil, nil)
			defer tracer.Stop()
			var billed []string
			client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
				Account:    splitKeysAccount{},
				LLMPlugins: []schemas.LLMPlugin{allowedKeysPlugin{ids: tc.allowed, billed: &billed}},
				Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
				Tracer:     tracer,
			})
			require.NoError(t, err)
			defer client.Shutdown()

			provider, ok := client.GetProviderByKey(schemas.OpenAI).(schemas.LiveProvider)
			require.True(t, ok, "the OpenAI provider serves live sessions")
			g := &liveGateway{client: client}
			target := liveTarget{provider: provider, providerKey: schemas.OpenAI, voiceModel: "gpt-live-1", backendModel: tc.backend}
			preReqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			admission, bifrostErr := g.admit(nil, preReqCtx, nil, "/v1/live/sessions", target, "live-admit-test")
			if tc.wantErr != "" {
				require.NotNil(t, bifrostErr, "a session no allowed key can serve must be refused")
				assert.Contains(t, bifrostErr.Error.Message, tc.wantErr)
				return
			}
			require.Nil(t, bifrostErr, "admission refused: %v", bifrostErr)
			defer admission.cancel()
			assert.Equal(t, tc.wantKey, admission.key.ID)
			// The units opened during admission bill on the selected key.
			admission.meter.abort(newRealtimeWireBifrostError(499, "cancelled", "test over"))
			require.NotEmpty(t, billed, "the admitted units ran their post-hooks")
			for _, keyID := range billed {
				assert.Equal(t, tc.wantKey, keyID)
			}
		})
	}
}

// aliasLiveRunner grants the caller aliases, not their provider model identifiers.
type aliasLiveRunner struct {
	inner   *fakeLiveRunner
	allowed []string
}

func (r aliasLiveRunner) RunRealtimeTurnPreHooks(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*bifrost.RealtimeTurnHooks, *schemas.BifrostError) {
	_, model, _ := req.GetRequestFields()
	if !slices.Contains(r.allowed, model) {
		return nil, newRealtimeWireBifrostError(403, "model_blocked", "model is not allowed")
	}
	return r.inner.RunRealtimeTurnPreHooks(ctx, req)
}

type aliasLiveIO struct{ messages [][]byte }

func (io *aliasLiveIO) sendUpstream(message []byte) error {
	io.messages = append(io.messages, message)
	return nil
}
func (io *aliasLiveIO) sendClient(message []byte) error {
	io.messages = append(io.messages, message)
	return nil
}
func (*aliasLiveIO) abandonUpstream() {}

func TestLiveSessionUpdatedRetainsAdmittedAlias(t *testing.T) {
	t.Parallel()
	for _, initial := range []string{"terra", "gpt-5.6-luna"} {
		t.Run(initial, func(t *testing.T) {
			runner := &fakeLiveRunner{}
			meter := newTestLiveMeter(runner)
			meter.runner = aliasLiveRunner{inner: runner, allowed: []string{"gpt-live-1", "gpt-5.6-luna", "terra", "terra-other"}}
			require.Nil(t, meter.admit("gpt-live-1", initial))
			key := schemas.Key{ID: "key-1", Aliases: schemas.KeyAliases{
				"terra": {ModelID: "gpt-5.6-terra"}, "terra-other": {ModelID: "gpt-5.6-terra"},
				"forbidden-alias": {ModelID: "gpt-5.6-sol"},
			}}
			// The key is selected after the initial units have been admitted.
			meter.setKey(key)
			wire := &aliasLiveIO{}
			controller := newTestLiveController(fakeLiveModels{allowed: true}, meter, wire)
			controller.key = key
			defer controller.markUpstreamDone()
			defer meter.finish(0)
			update := []byte(`{"type":"session.update","session":{"delegation":{"responses":{"model":"openai/terra"}}}}`)
			checked, accepted := controller.fromClient(update)
			require.True(t, accepted)
			assert.Contains(t, string(checked), `"model":"gpt-5.6-terra"`)
			opens, _, _ := runner.snapshot()
			ack := []byte(`{"type":"session.updated","session":{"delegation":{"responses":{"model":"gpt-5.6-terra"}}}}`)
			for i := 0; i < 2; i++ {
				assert.False(t, controller.fromUpstream(ack))
			}
			after, _, _ := runner.snapshot()
			require.Empty(t, wire.messages, "an acknowledged alias must not send an error or session.close")
			assert.Len(t, after, len(opens), "acknowledgments do not open another billing lane")
			assert.Equal(t, "terra", meter.activeBackend)

			// Two admitted aliases can resolve to the same provider model. The active alias wins.
			_, accepted = controller.fromClient([]byte(`{"type":"session.update","session":{"delegation":{"responses":{"model":"terra-other"}}}}`))
			require.True(t, accepted)
			controller.fromUpstream(ack)
			assert.Equal(t, "terra-other", meter.activeBackend)
			require.Empty(t, wire.messages)

			// A refused update must not acquire an identity that later bypasses authorization.
			_, accepted = controller.fromClient([]byte(`{"type":"session.update","session":{"delegation":{"responses":{"model":"forbidden-alias"}}}}`))
			require.False(t, accepted)
			require.Len(t, wire.messages, 1)
			wire.messages = nil
			controller.fromUpstream(ack)
			assert.Equal(t, "terra-other", meter.activeBackend)
			require.Empty(t, wire.messages)
			controller.fromUpstream([]byte(`{"type":"session.updated","session":{"delegation":{"responses":{"model":"gpt-5.6-sol"}}}}`))
			assert.Equal(t, "terra-other", meter.activeBackend)
			require.Len(t, wire.messages, 2, "an unknown sideband model still triggers authorization and refusal")
			assert.Contains(t, string(wire.messages[0]), "model is not allowed")
			assert.Equal(t, `{"type":"session.close"}`, string(wire.messages[1]))
		})
	}
}

func TestLiveMeterAliasResponsesKeepTheirBillingLane(t *testing.T) {
	t.Parallel()
	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	meter.runner = aliasLiveRunner{inner: runner, allowed: []string{"gpt-live-1", "luna", "terra"}}
	require.Nil(t, meter.admit("gpt-live-1", "luna"))
	meter.setKey(schemas.Key{ID: "key-1", Aliases: schemas.KeyAliases{
		"luna": {ModelID: "gpt-5.6-luna"}, "terra": {ModelID: "gpt-5.6-terra"},
	}})
	require.Nil(t, meter.switchBackend("terra"))
	// An older response can finish after a backend switch, including a dated provider name.
	require.Nil(t, meter.onBackendResponse(&schemas.BifrostResponsesResponse{
		ID: new("resp_luna"), Model: "gpt-5.6-luna-2026-10-01",
		Usage: &schemas.ResponsesResponseUsage{InputTokens: 8, OutputTokens: 2, TotalTokens: 10},
	}, "item_luna", 0))
	_, posts, _ := runner.snapshot()
	require.Len(t, posts, 1)
	assert.Equal(t, "luna", posts[0].model, "the old response must not be billed to the new active alias")
	assert.Equal(t, "gpt-5.6-luna", posts[0].resp.Model)
	_, _, requested, resolved := bifrost.GetResponseFields(&schemas.BifrostResponse{ResponsesResponse: posts[0].resp}, nil)
	assert.Equal(t, "luna", requested)
	assert.Equal(t, "gpt-5.6-luna", resolved)
	assert.Equal(t, 10, posts[0].resp.Usage.TotalTokens)
	assert.Equal(t, "terra", meter.activeBackend)
	meter.finish(0)
	opens, _, cleanups := runner.snapshot()
	assert.Equal(t, len(opens), cleanups)
}

func TestLiveMeterSharedAliasResponseStaysOnItsProviderModel(t *testing.T) {
	t.Parallel()
	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	meter.runner = aliasLiveRunner{inner: runner, allowed: []string{"gpt-live-1", "luna", "terra", "terra-other"}}
	require.Nil(t, meter.admit("gpt-live-1", "terra"))
	meter.setKey(schemas.Key{ID: "key-1", Aliases: schemas.KeyAliases{
		"luna": {ModelID: "gpt-5.6-luna"}, "terra": {ModelID: "gpt-5.6-terra"}, "terra-other": {ModelID: "gpt-5.6-terra"},
	}})
	require.Nil(t, meter.switchBackend("terra-other"))
	require.Nil(t, meter.switchBackend("luna"))
	// A Terra response finishing after the switch names the identifier two admitted aliases share.
	require.Nil(t, meter.onBackendResponse(&schemas.BifrostResponsesResponse{
		ID: new("resp_terra"), Model: "gpt-5.6-terra",
		Usage: &schemas.ResponsesResponseUsage{InputTokens: 8, OutputTokens: 2, TotalTokens: 10},
	}, "item_terra", 0))
	_, posts, _ := runner.snapshot()
	require.Len(t, posts, 1)
	assert.Contains(t, []string{"terra", "terra-other"}, posts[0].model, "billed on an admitted Terra alias, never the active Luna")
	assert.Equal(t, "gpt-5.6-terra", posts[0].resp.Model, "the response keeps its provider model")
	assert.Equal(t, 10, posts[0].resp.Usage.TotalTokens)
	assert.Equal(t, "luna", meter.activeBackend)

	// The upstream echo of a sideband switch to the shared identifier confirms an admitted alias.
	require.Nil(t, meter.confirmBackend("gpt-5.6-terra"), "the raw provider name must not go through admission")
	_, raw := meter.backends["gpt-5.6-terra"]
	assert.False(t, raw, "the raw provider model is never a lane of its own")
	assert.Contains(t, []string{"terra", "terra-other"}, meter.activeBackend)
	meter.finish(0)
	opens, _, cleanups := runner.snapshot()
	assert.Equal(t, len(opens), cleanups)
}

func TestLiveMeterDatedResponsePrefersTheLongestAdmittedModel(t *testing.T) {
	t.Parallel()
	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	meter.runner = aliasLiveRunner{inner: runner, allowed: []string{"gpt-live-1", "terra", "mini"}}
	require.Nil(t, meter.admit("gpt-live-1", "mini"))
	meter.setKey(schemas.Key{ID: "key-1", Aliases: schemas.KeyAliases{
		"terra": {ModelID: "gpt-5.6-terra"}, "mini": {ModelID: "gpt-5.6-terra-mini"},
	}})
	require.Nil(t, meter.switchBackend("terra"))
	// A dated Mini response lands while Terra is active; "gpt-5.6-terra-" is a prefix of it too.
	require.Nil(t, meter.onBackendResponse(&schemas.BifrostResponsesResponse{
		ID: new("resp_mini"), Model: "gpt-5.6-terra-mini-2026-10-01",
		Usage: &schemas.ResponsesResponseUsage{InputTokens: 8, OutputTokens: 2, TotalTokens: 10},
	}, "item_mini", 0))
	_, posts, _ := runner.snapshot()
	require.Len(t, posts, 1)
	assert.Equal(t, "mini", posts[0].model, "a dated name belongs to the longest admitted provider model, not the active Terra")
	assert.Equal(t, "gpt-5.6-terra-mini", posts[0].resp.Model)
	assert.Equal(t, 10, posts[0].resp.Usage.TotalTokens)
	assert.Equal(t, "terra", meter.activeBackend)
	meter.finish(0)
	opens, _, cleanups := runner.snapshot()
	assert.Equal(t, len(opens), cleanups)
}

type liveAliasMetadataPlugin struct {
	allowedKeysPlugin
	seen [2]string
}

func (p *liveAliasMetadataPlugin) PostLLMHook(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	_, _, requested, resolved := bifrost.GetResponseFields(result, err)
	p.seen = [2]string{requested, resolved}
	return result, err, nil
}

func TestLiveAliasedPostHooksPreserveModelIdentity(t *testing.T) {
	plugin := &liveAliasMetadataPlugin{allowedKeysPlugin: allowedKeysPlugin{ids: []string{"every-model"}}}
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: splitKeysAccount{}, LLMPlugins: []schemas.LLMPlugin{plugin},
		Logger: bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	defer client.Shutdown()
	for _, requestType := range []schemas.RequestType{schemas.LiveRequest, schemas.RealtimeRequest} {
		t.Run(string(requestType), func(t *testing.T) {
			for _, failed := range []bool{false, true} {
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: "terra", Config: &schemas.AliasConfig{ModelID: "gpt-5.6-terra"}})
				hooks, bifrostErr := client.RunRealtimeTurnPreHooks(ctx, &schemas.BifrostRequest{
					RequestType: requestType, ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "terra"},
				})
				require.Nil(t, bifrostErr)
				defer hooks.Cleanup()
				result := &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Model: "gpt-5.6-terra"}}
				var unitErr *schemas.BifrostError
				if failed {
					result = nil
					unitErr = newRealtimeWireBifrostError(502, "server_error", "backend failed")
				}
				result, unitErr = hooks.PostHookRunner(ctx, result, unitErr)
				resolved := "terra"
				if requestType == schemas.LiveRequest {
					resolved = "gpt-5.6-terra"
				}
				assert.Equal(t, [2]string{"terra", resolved}, plugin.seen, "post-hooks receive both identities; Realtime keeps its existing behavior")
				_, _, requested, actual := bifrost.GetResponseFields(result, unitErr)
				assert.Equal(t, "terra", requested)
				assert.Equal(t, resolved, actual)
			}
		})
	}
}

func TestLiveMeterAliasResponsePrefersExactModelOverSnapshot(t *testing.T) {
	t.Parallel()
	meter := newTestLiveMeter(&fakeLiveRunner{})
	require.Nil(t, meter.admit("gpt-live-1", "mini"))
	meter.setKey(schemas.Key{Aliases: schemas.KeyAliases{
		"terra": {ModelID: "gpt-5.6-terra"}, "mini": {ModelID: "gpt-5.6-terra-mini"},
	}})
	require.Nil(t, meter.switchBackend("terra"))
	defer meter.finish(0)
	assert.Equal(t, "mini", meter.backendLaneForLocked("gpt-5.6-terra-mini").model,
		"an exact admitted model must win over another model's snapshot prefix")
}

// twoLiveProvidersAccount adds a custom OpenAI-based provider and a provider without live
// support next to splitKeysAccount's OpenAI, so a bare control route has a choice to make.
type twoLiveProvidersAccount struct{ splitKeysAccount }

func (twoLiveProvidersAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI, "openai-custom", schemas.Anthropic}, nil
}

func (a twoLiveProvidersAccount) GetKeysForProvider(ctx context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	switch p {
	case "openai-custom", schemas.Anthropic:
		return []schemas.Key{{ID: string(p) + "-key", Name: string(p) + "-key", Value: *schemas.NewSecretVar("sk-" + string(p)), Models: schemas.WhiteList{"*"}, Weight: 1.0}}, nil
	}
	return a.splitKeysAccount.GetKeysForProvider(ctx, p)
}

func (a twoLiveProvidersAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	switch p {
	case "openai-custom":
		return &schemas.ProviderConfig{NetworkConfig: schemas.DefaultNetworkConfig, ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize, CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI}}, nil
	case schemas.Anthropic:
		return &schemas.ProviderConfig{NetworkConfig: schemas.DefaultNetworkConfig, ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize}, nil
	}
	return a.splitKeysAccount.GetConfigForProvider(p)
}

// TestLiveControlProviderHonoursTheNamedProvider: a route that only carries a session id cannot
// tell which provider opened the session, so the caller may name it as on other id-only routes.
func TestLiveControlProviderHonoursTheNamedProvider(t *testing.T) {
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{Account: twoLiveProvidersAccount{}, Logger: bifrost.NewDefaultLogger(schemas.LogLevelError)})
	require.NoError(t, err)
	defer client.Shutdown()
	g := &liveGateway{client: client}
	for _, tc := range []struct{ name, path, named, want, wantErr string }{
		{"bare route defaults to openai", "/v1/live/sessions/live_1/attach", "", "openai", ""},
		{"named custom provider on the bare route", "/v1/live/sessions/live_1/attach", "openai-custom", "openai-custom", ""},
		{"integration prefix wins over the name", "/openai/v1/live/sessions/live_1/content", "openai-custom", "openai", ""},
		{"named provider without live support", "/v1/live/sessions/live_1/content", "anthropic", "", "does not support live sessions: anthropic"},
		{"unknown provider", "/v1/live/sessions/live_1/content", "nope", "", "not configured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, providerKey, bifrostErr := g.controlProvider(tc.path, tc.named)
			if tc.wantErr != "" {
				require.NotNil(t, bifrostErr)
				assert.Contains(t, bifrostErr.Error.Message, tc.wantErr)
				return
			}
			require.Nil(t, bifrostErr, "refused: %v", bifrostErr)
			assert.NotNil(t, provider)
			assert.Equal(t, schemas.ModelProvider(tc.want), providerKey)
		})
	}
}

func TestLiveControlProviderNameReadsQueryThenHeader(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/v1/live/sessions/live_1/content")
	assert.Equal(t, "", liveControlProviderName(ctx))
	ctx.Request.Header.Set("x-model-provider", "openai-custom")
	assert.Equal(t, "openai-custom", liveControlProviderName(ctx))
	ctx.Request.SetRequestURI("/v1/live/sessions/live_1/content?provider=openai")
	assert.Equal(t, "openai", liveControlProviderName(ctx), "the query parameter wins over the header")
}

// BenchmarkLiveSessionControlRoute measures resolving a control route on an existing live session
// (GET /v1/live/sessions/{id}/content) for a caller with a virtual key, against a real SQLite
// config store: the route's provider resolution and the session ownership check.
func BenchmarkLiveSessionControlRoute(b *testing.B) {
	SetLogger(&mockLogger{})
	store, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: b.TempDir() + "/bench.db"},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close(context.Background())
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{},
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {Keys: []schemas.Key{{ID: "key-1", Name: "openai", Value: *schemas.NewSecretVar("sk-test"), Weight: 1, Models: []string{"gpt-live-1"}}}},
		},
		ConfigStore: store,
	}
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{Account: lib.NewBaseAccount(config), Logger: bifrost.NewDefaultLogger(schemas.LogLevelError)})
	if err != nil {
		b.Fatal(err)
	}
	defer client.Shutdown()
	config.SetBifrostClient(client)
	g := &liveGateway{client: client, config: config, handlerStore: config}
	const path = "/v1/live/sessions/live_bench/content"
	// "on" is the default and runs the ownership lookup; "off" is client.enforce_vk_object_ownership: false.
	for _, mode := range []string{"off", "on"} {
		b.Run(mode, func(b *testing.B) {
			config.ClientConfig.EnforceVKObjectOwnership = mode == "on"
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				preReqCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
				preReqCtx.SetGrant(grant.New())
				preReqCtx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-vk-a")
				preReqCtx.Grant().SetIdentity(grant.NewIdentity(grant.NewCredential(grant.CredentialVirtualKey, "sk-bf-vk-a"), nil, &schemas.EntityRef{ID: "vk-a", Name: "vk-a"}, nil, nil, nil, nil))
				req := &liveRequest{path: path, auth: &authHeaders{}, preReqCtx: preReqCtx, cancel: func() {}, middlewareValues: map[any]any{}}
				ctx := &fasthttp.RequestCtx{}
				ctx.Request.SetRequestURI(path)
				ctx.SetUserValue("session_id", "live_bench")
				g.resolveSession(ctx, req)
				cancel()
			}
		})
	}
}
