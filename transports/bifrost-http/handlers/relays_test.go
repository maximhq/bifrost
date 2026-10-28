package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"

	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func newRelaysTestHandler(t *testing.T) (*RelaysHandler, configstore.ConfigStore, *relay.Manager) {
	t.Helper()
	store, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "pte.db")},
	}, &mockLogger{})
	require.NoError(t, err)
	manager := relay.NewManager(store, nil)
	return NewRelaysHandler(manager, nil, nil, nil), store, manager
}

func pteRequest(method, body string, name string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(method)
	ctx.Request.SetBodyString(body)
	if name != "" {
		ctx.SetUserValue("name", name)
	}
	return ctx
}

func TestRelaysHandlerCRUD(t *testing.T) {
	h, store, manager := newRelaysTestHandler(t)

	create := pteRequest(fasthttp.MethodPost, `{"name":"tavily","base_url":"https://search.example.com","auth":{"type":"bearer"},"credential":{"value":"tvly-secret-123456789"},"usage":{"cost_per_request":0.001},"routes":[{"method":"POST","path":"/search","usage":{"units_path":"usage.credits","cost_per_unit":0.008}}]}`, "")
	h.create(create)
	require.Equal(t, fasthttp.StatusCreated, create.Response.StatusCode(), string(create.Response.Body()))
	assert.NotContains(t, string(create.Response.Body()), "tvly-secret-123456789", "the credential is redacted in responses")
	assert.Equal(t, []string{"tavily"}, manager.Names(), "a created endpoint is live")

	dup := pteRequest(fasthttp.MethodPost, `{"name":"tavily","base_url":"https://search.example.com","credential":{"value":"x"}}`, "")
	h.create(dup)
	assert.Equal(t, fasthttp.StatusConflict, dup.Response.StatusCode())

	invalid := pteRequest(fasthttp.MethodPost, `{"name":"nocred","base_url":"https://search.example.com"}`, "")
	h.create(invalid)
	assert.Equal(t, fasthttp.StatusBadRequest, invalid.Response.StatusCode(), "a bearer endpoint without a credential is refused")

	exa := pteRequest(fasthttp.MethodPost, `{"name":"exa","base_url":"https://docs.example.com","auth":{"type":"none"}}`, "")
	h.create(exa)
	require.Equal(t, fasthttp.StatusCreated, exa.Response.StatusCode(), string(exa.Response.Body()))
	// Relays are listed oldest first, so a page of one is tavily.
	for query, want := range map[string][]string{"": {"exa", "tavily"}, "search=TAV": {"tavily"}, "limit=1": {"tavily"}, "limit=1&offset=1": {"exa"}} {
		list := pteRequest(fasthttp.MethodGet, "", "")
		list.Request.URI().SetQueryString(query)
		h.list(list)
		require.Equal(t, fasthttp.StatusOK, list.Response.StatusCode(), string(list.Response.Body()))
		var page struct {
			Relays     []struct{ Name string } `json:"relays"`
			TotalCount int                     `json:"total_count"`
		}
		require.NoError(t, json.Unmarshal(list.Response.Body(), &page))
		names := []string{}
		for _, r := range page.Relays {
			names = append(names, r.Name)
		}
		assert.ElementsMatch(t, want, names, "list ?%s", query)
	}
	bad := pteRequest(fasthttp.MethodGet, "", "")
	bad.Request.URI().SetQueryString("limit=0")
	h.list(bad)
	assert.Equal(t, fasthttp.StatusBadRequest, bad.Response.StatusCode())

	get := pteRequest(fasthttp.MethodGet, "", "tavily")
	h.get(get)
	require.Equal(t, fasthttp.StatusOK, get.Response.StatusCode())
	var view map[string]any
	require.NoError(t, json.Unmarshal(get.Response.Body(), &view))
	assert.Equal(t, "/relay/tavily", view["url"])

	// The UI sends back what it read: the redacted credential keeps the stored secret.
	view["description"] = "edited"
	edited, _ := json.Marshal(view)
	update := pteRequest(fasthttp.MethodPut, string(edited), "tavily")
	h.update(update)
	require.Equal(t, fasthttp.StatusOK, update.Response.StatusCode(), string(update.Response.Body()))
	stored, err := store.GetRelay(context.Background(), "tavily")
	require.NoError(t, err)
	assert.Equal(t, "edited", stored.Description)
	assert.Equal(t, "tvly-secret-123456789", stored.Credential.Value.GetValue())
	require.Len(t, stored.Routes, 1)
	assert.InDelta(t, 0.008, *stored.Routes[0].Usage.CostPerUnit, 1e-12)

	rename := pteRequest(fasthttp.MethodPut, `{"name":"other","base_url":"https://search.example.com","credential":{"value":"x"}}`, "tavily")
	h.update(rename)
	assert.Equal(t, fasthttp.StatusBadRequest, rename.Response.StatusCode(), "names are immutable")

	preview := pteRequest(fasthttp.MethodPost, `{"name":"tavily","sample":{"method":"POST","path":"/search","response_content_type":"application/json","response_body":"{\"usage\":{\"credits\":4}}"}}`, "")
	h.preview(preview)
	require.Equal(t, fasthttp.StatusOK, preview.Response.StatusCode(), string(preview.Response.Body()))
	var out relay.UsagePreview
	require.NoError(t, json.Unmarshal(preview.Response.Body(), &out))
	assert.InDelta(t, 0.001+4*0.008, out.Cost, 1e-12, "the preview prices a saved endpoint's route override")

	del := pteRequest(fasthttp.MethodDelete, "", "tavily")
	h.delete(del)
	assert.Equal(t, fasthttp.StatusNoContent, del.Response.StatusCode())
	assert.Equal(t, []string{"exa"}, manager.Names(), "a deleted relay stops serving; the other keeps serving")
	missing := pteRequest(fasthttp.MethodGet, "", "tavily")
	h.get(missing)
	assert.Equal(t, fasthttp.StatusNotFound, missing.Response.StatusCode())
}

// relayChangeRecorder stands in for the virtual-key reloader and records what it was asked to
// reload; a failed reload is logged, never surfaced, as for agent grants.
type relayChangeRecorder struct {
	reloads     []string
	failReloads bool
}

func (r *relayChangeRecorder) ReloadVirtualKey(_ context.Context, id string) (*tables.TableVirtualKey, error) {
	r.reloads = append(r.reloads, id)
	if r.failReloads {
		return nil, errors.New("governance store unavailable")
	}
	return nil, nil
}

// A delete reloads every key whose grant it took away, and a failed reload does not fail the
// delete or block the name, exactly as the Agent Gateway handler treats agent grants.
func TestRelaysHandlerReloadsGrantedKeysOnDelete(t *testing.T) {
	_, store, _ := newRelaysTestHandler(t)
	recorder := &relayChangeRecorder{failReloads: true}
	h := NewRelaysHandler(relay.NewManager(store, nil), nil, nil, recorder)
	h.create(pteRequest("POST", `{"name":"tavily","base_url":"https://api.tavily.com","auth":{"type":"bearer"},"credential":{"value":"tvly-1"}}`, ""))
	require.NoError(t, store.CreateVirtualKey(context.Background(), &tables.TableVirtualKey{ID: "vk-granted", Name: "granted", Value: *schemas.NewSecretVar("sk-bf-granted"), IsActive: schemas.Ptr(true), RelayGrants: []tables.TableVirtualKeyRelayGrant{{VirtualKeyID: "vk-granted", RelayName: "tavily"}}}))
	del := pteRequest("DELETE", "", "tavily")
	h.delete(del)
	assert.Equal(t, fasthttp.StatusNoContent, del.Response.StatusCode(), "a failed key reload does not fail the delete")
	assert.Equal(t, []string{"vk-granted"}, recorder.reloads, "the granted key is reloaded")

	ctx := pteRequest("POST", `{"name":"tavily","base_url":"https://api.tavily.com","auth":{"type":"bearer"},"credential":{"value":"tvly-2"}}`, "")
	h.create(ctx)
	assert.Equal(t, fasthttp.StatusCreated, ctx.Response.StatusCode(), "the name is free to reuse at once")
}

// relayMutationRecorder stands in for a clustered server's RelayManager: it mutates locally and
// records what it would broadcast, so the handler's delegation can be checked.
type relayMutationRecorder struct {
	localRelayManager
	changes []string
}

func (r *relayMutationRecorder) CreateRelay(ctx context.Context, cfg *schemas.RelayConfig) (*schemas.RelayConfig, error) {
	stored, err := r.localRelayManager.CreateRelay(ctx, cfg)
	if err == nil {
		r.changes = append(r.changes, "create:"+stored.Name)
	}
	return stored, err
}

func (r *relayMutationRecorder) UpdateRelay(ctx context.Context, name string, cfg *schemas.RelayConfig) (*schemas.RelayConfig, error) {
	updated, err := r.localRelayManager.UpdateRelay(ctx, name, cfg)
	if err == nil {
		r.changes = append(r.changes, "update:"+name)
	}
	return updated, err
}

func (r *relayMutationRecorder) DeleteRelay(ctx context.Context, name string) ([]string, error) {
	ids, err := r.localRelayManager.DeleteRelay(ctx, name)
	if err == nil {
		r.changes = append(r.changes, "delete:"+name+":"+strings.Join(ids, ","))
	}
	return ids, err
}

// Every relay mutation goes through the RelayManager, so a clustered deployment can wrap it with a
// broadcast; a peer told of one brings its live relay in line with the store: upserted while the
// row exists, removed once it is gone.
func TestRelaysHandlerDelegatesMutationsAndReloads(t *testing.T) {
	_, store, _ := newRelaysTestHandler(t)
	manager := relay.NewManager(store, nil)
	recorder := &relayMutationRecorder{localRelayManager: localRelayManager{manager: manager}}
	h := NewRelaysHandler(manager, recorder, nil, nil)

	h.create(pteRequest("POST", `{"name":"tavily","base_url":"https://api.tavily.com","auth":{"type":"bearer"},"credential":{"value":"tvly-1"}}`, ""))
	h.update(pteRequest("PUT", `{"name":"tavily","base_url":"https://api.tavily.com","auth":{"type":"bearer"},"credential":{"value":"tvly-1"},"description":"edited"}`, "tavily"))
	require.NoError(t, store.CreateVirtualKey(context.Background(), &tables.TableVirtualKey{ID: "vk-granted", Name: "granted", Value: *schemas.NewSecretVar("sk-bf-granted"), IsActive: schemas.Ptr(true), RelayGrants: []tables.TableVirtualKeyRelayGrant{{VirtualKeyID: "vk-granted", RelayName: "tavily"}}}))
	h.delete(pteRequest("DELETE", "", "tavily"))
	assert.Equal(t, []string{"create:tavily", "update:tavily", "delete:tavily:vk-granted"}, recorder.changes)
	assert.Empty(t, manager.Names(), "the local runtime followed the delete")

	// A peer replays the change from the store.
	peer := relay.NewManager(store, nil)
	peerHandler := NewRelaysHandler(peer, nil, nil, nil)
	require.NoError(t, store.CreateRelay(context.Background(), &schemas.RelayConfig{Name: "docs", BaseURL: "https://docs.example.com", Auth: schemas.RelayAuthConfig{Type: schemas.RelayAuthBearer}, Credential: &schemas.RelayCredential{Value: schemas.NewSecretVar("k")}}, ""))
	require.NoError(t, peerHandler.ReloadRelay(context.Background(), "docs"))
	assert.Contains(t, peer.Names(), "docs", "a created relay is served on the peer")
	require.NoError(t, store.DeleteRelay(context.Background(), "docs"))
	assert.ErrorIs(t, peerHandler.ReloadRelay(context.Background(), "docs"), configstore.ErrNotFound, "a reload of a deleted relay reports the missing row")
	peerHandler.RemoveRelay("docs")
	assert.NotContains(t, peer.Names(), "docs", "a deleted relay stops being served on the peer")
}
