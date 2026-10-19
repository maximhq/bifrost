package handlers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/elevenlabs"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type scribeInMemoryStore struct{ *lib.Config }

func (s scribeInMemoryStore) GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig {
	return s.Providers // The test fixture never changes provider configuration.
}

func (s scribeInMemoryStore) GetConfiguredProviderNames() []string {
	names := make([]string, 0, len(s.Providers))
	for provider := range s.Providers {
		names = append(names, string(provider))
	}
	return names
}

// Real catalog, governance and provider code; only ElevenLabs' HTTP responses
// are mocked. The live model response deliberately omits all Scribe models.
func newElevenlabsScribeHandler(tb testing.TB, keys []schemas.Key, virtualKeys []configtables.TableVirtualKey) (*CompletionHandler, *modelcatalog.ModelCatalog, *atomic.Int32) {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "pricing.json")
	require.NoError(tb, os.WriteFile(path, []byte(`{
		"elevenlabs/scribe_v1": {"provider":"elevenlabs","mode":"audio_transcription","base_model":"scribe_v1"},
		"elevenlabs/scribe_v1_experimental": {"provider":"elevenlabs","mode":"audio_transcription","base_model":"scribe_v1_experimental"},
		"elevenlabs/scribe_v2": {"provider":"elevenlabs","mode":"audio_transcription","base_model":"scribe_v2"},
		"elevenlabs/eleven_turbo_v2_5": {"provider":"elevenlabs","mode":"audio_speech","base_model":"eleven_turbo_v2_5"},
		"openai/gpt-4o-mini": {"provider":"openai","mode":"chat","base_model":"gpt-4o-mini"}
	}`), 0o600))
	ds := datasheet.New(nil, nil, datasheet.Config{URL: "file://" + path})
	require.NoError(tb, ds.LoadFromURLIntoMemory(tb.Context()))
	catalog := modelcatalog.NewTestCatalogWithDatasheet(ds)
	catalog.SetKeyConfigForProvider(schemas.Elevenlabs, keys)
	// Seed the same per-key filtered responses that a pre-fix refresh cached.
	models := elevenlabs.ElevenlabsListModelsResponse{{ModelID: "eleven_turbo_v2_5", Name: "Upstream Turbo"}}
	for _, key := range keys {
		for _, unfiltered := range []bool{false, true} {
			catalog.UpsertLiveFromResponse(schemas.Elevenlabs, key.ID, unfiltered,
				models.ToBifrostListModelsResponse(schemas.Elevenlabs, key.Models, key.BlacklistedModels, key.Aliases, unfiltered))
		}
	}
	calls := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("Authorization") != "" {
				fmt.Fprint(w, `{"data":[{"id":"gpt-4o-mini","owned_by":"openai"}]}`)
			} else {
				fmt.Fprint(w, `[{"model_id":"eleven_turbo_v2_5","name":"Upstream Turbo"}]`)
			}
		case "/v1/speech-to-text":
			calls.Add(1)
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				tb.Errorf("upstream multipart: %v", err)
				w.WriteHeader(400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			if r.FormValue("model_id") != "scribe_v1" && r.FormValue("model_id") != "scribe_v2" {
				tb.Errorf("unexpected upstream model_id %q", r.FormValue("model_id"))
			}
			if r.Header.Get("xi-api-key") != "scribe-provider-key" {
				tb.Errorf("unexpected provider credential %q", r.Header.Get("xi-api-key"))
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				tb.Errorf("upstream file: %v", err)
				w.WriteHeader(400)
				return
			}
			defer file.Close()
			data, err := io.ReadAll(file)
			if err != nil || string(data) != "mock audio" || header.Filename != "sample.wav" {
				tb.Errorf("audio changed: filename=%q, body=%q, err=%v", header.Filename, data, err)
			}
			fmt.Fprint(w, `{"language_code":"en","text":"mock scribe transcription","words":[]}`)
		default:
			tb.Errorf("unexpected upstream request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	tb.Cleanup(upstream.Close)
	log := bifrost.NewNoOpLogger()
	SetLogger(log)
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{}, ModelCatalog: catalog,
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.Elevenlabs: {
				Keys:                     keys,
				NetworkConfig:            &schemas.NetworkConfig{BaseURL: upstream.URL, AllowPrivateNetwork: true},
				ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 10},
			},
			schemas.OpenAI: {
				Keys:                     []schemas.Key{{ID: "openai-key", Models: schemas.WhiteList{"*"}, Value: *schemas.NewSecretVar("openai-provider-key"), Weight: 1}},
				NetworkConfig:            &schemas.NetworkConfig{BaseURL: upstream.URL, AllowPrivateNetwork: true},
				ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 10},
			},
		},
	}
	catalog.SetKeyConfigForProvider(schemas.OpenAI, config.Providers[schemas.OpenAI].Keys)
	plugin, err := governance.Init(tb.Context(), nil, log, nil,
		&configstore.GovernanceConfig{VirtualKeys: virtualKeys}, catalog, nil, scribeInMemoryStore{config})
	require.NoError(tb, err)
	client, err := bifrost.Init(tb.Context(), schemas.BifrostConfig{
		Account: lib.NewBaseAccount(config), Logger: log, ModelCatalog: catalog,
		LLMPlugins: []schemas.LLMPlugin{plugin},
	})
	require.NoError(tb, err)
	tb.Cleanup(client.Shutdown)
	return NewInferenceHandler(nil, client, config), catalog, calls
}

func scribeVirtualKey(name string, allowed schemas.WhiteList, blocked schemas.BlackList, allProviders bool) configtables.TableVirtualKey {
	return configtables.TableVirtualKey{
		ID: name, Name: name, Value: *schemas.NewSecretVar("sk-bf-" + name), IsActive: new(true), AllowAllProviders: allProviders,
		ProviderConfigs: []configtables.TableVirtualKeyProviderConfig{{
			Provider: "elevenlabs", AllowedModels: allowed, BlacklistedModels: blocked, AllowAllKeys: true,
		}},
	}
}

func scribeProviderKey() schemas.Key {
	return schemas.Key{
		ID: "scribe-key", Models: schemas.WhiteList{"*"}, Value: *schemas.NewSecretVar("scribe-provider-key"), Weight: 1,
		Aliases: schemas.KeyAliases{"dictation": {ModelID: "scribe_v2"}},
	}
}

func scribeTranscriptionContext(t *testing.T, model, vk string) *fasthttp.RequestCtx {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", model))
	file, err := writer.CreateFormFile("file", "sample.wav")
	require.NoError(t, err)
	_, err = file.Write([]byte("mock audio"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/v1/audio/transcriptions")
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.SetContentType(writer.FormDataContentType())
	ctx.Request.Header.Set("x-bf-vk", vk)
	ctx.Request.SetBody(body.Bytes())
	t.Cleanup(ctx.Request.RemoveMultipartFormFiles)
	return ctx
}

func TestElevenlabsScribeTranscriptionVirtualKey(t *testing.T) {
	cases := []struct {
		name  string
		model string
		vk    configtables.TableVirtualKey
		allow bool
	}{
		{"wildcard v2", "elevenlabs/scribe_v2", scribeVirtualKey("wildcard", schemas.WhiteList{"*"}, nil, true), true},
		{"wildcard v1", "elevenlabs/scribe_v1", scribeVirtualKey("wildcard-v1", schemas.WhiteList{"*"}, nil, false), true},
		{"exact v2", "elevenlabs/scribe_v2", scribeVirtualKey("exact", schemas.WhiteList{"scribe_v2"}, nil, false), true},
		{"case insensitive exact permission", "elevenlabs/scribe_v2", scribeVirtualKey("case", schemas.WhiteList{"SCRIBE_V2"}, nil, false), true},
		{"wildcard alias", "elevenlabs/dictation", scribeVirtualKey("alias-wildcard", schemas.WhiteList{"*"}, nil, false), true},
		{"exact alias", "elevenlabs/dictation", scribeVirtualKey("alias-exact", schemas.WhiteList{"dictation"}, nil, false), true},
		{"regex scribe", "elevenlabs/scribe_v2", scribeVirtualKey("regex", schemas.WhiteList{"regex:^scribe_v[12]$"}, nil, false), true},
		{"exact excludes sibling", "elevenlabs/scribe_v1", scribeVirtualKey("sibling", schemas.WhiteList{"scribe_v2"}, nil, false), false},
		{"tts only despite allow all providers", "elevenlabs/scribe_v2", scribeVirtualKey("tts", schemas.WhiteList{"eleven_turbo_v2_5"}, nil, true), false},
		{"empty denies all", "elevenlabs/scribe_v2", scribeVirtualKey("empty", schemas.WhiteList{}, nil, true), false},
		{"blacklist wins", "elevenlabs/scribe_v2", scribeVirtualKey("blocked", schemas.WhiteList{"*"}, schemas.BlackList{"scribe_v2"}, true), false},
		{"blocked alias", "elevenlabs/dictation", scribeVirtualKey("alias-blocked", schemas.WhiteList{"*"}, schemas.BlackList{"dictation"}, false), false},
		{"unknown model", "elevenlabs/nonexistent-scribe", scribeVirtualKey("unknown", schemas.WhiteList{"*"}, nil, false), false},
		{"unrelated provider", "openai/scribe_v2", scribeVirtualKey("provider", schemas.WhiteList{"*"}, nil, false), false},
		{"unrelated provider under allow all", "openai/scribe_v2", scribeVirtualKey("provider-all", schemas.WhiteList{"*"}, nil, true), false},
	}
	vks := make([]configtables.TableVirtualKey, 0, len(cases))
	for _, tc := range cases {
		vks = append(vks, tc.vk)
	}
	handler, catalog, calls := newElevenlabsScribeHandler(t, []schemas.Key{scribeProviderKey()}, vks)
	require.Contains(t, catalog.GetUnfilteredModelsForProvider(schemas.Elevenlabs), "scribe_v2")
	openaiRouter := router.New()
	integrations.NewOpenAIRouter(handler.client, handler.config, nil, bifrost.NewNoOpLogger()).RegisterRoutes(openaiRouter)
	for _, tc := range cases {
		for _, route := range []string{"native", "openai drop-in"} {
			t.Run(tc.name+"/"+route, func(t *testing.T) {
				ctx := scribeTranscriptionContext(t, tc.model, tc.vk.Value.GetValue())
				before := calls.Load()
				if route == "native" {
					handler.transcription(ctx)
				} else {
					ctx.Request.SetRequestURI("/openai/v1/audio/transcriptions")
					openaiRouter.Handler(ctx)
				}
				if tc.allow {
					require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
					require.Contains(t, string(ctx.Response.Body()), "mock scribe transcription")
					require.Equal(t, before+1, calls.Load())
				} else {
					require.Equal(t, 403, ctx.Response.StatusCode(), string(ctx.Response.Body()))
					var failure schemas.BifrostError
					require.NoError(t, schemas.Unmarshal(ctx.Response.Body(), &failure))
					wantType := "model_blocked"
					if strings.HasPrefix(tc.model, "openai/") && !tc.vk.AllowAllProviders {
						wantType = "provider_blocked"
					}
					require.NotNil(t, failure.Type)
					require.Equal(t, wantType, *failure.Type)
					require.Equal(t, before, calls.Load(), "denied requests must not reach ElevenLabs")
				}
			})
		}
	}
	// Granting a supported model does not implement unrelated operations.
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, vks[0].Value.GetValue())
	lib.SettleIdentity(ctx)
	before := calls.Load()
	_, failure := handler.client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.Elevenlabs, Model: "scribe_v2",
		Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hello")}}},
	})
	require.NotNil(t, failure)
	require.Contains(t, failure.GetErrorString(), "not supported")
	require.Equal(t, before, calls.Load())
}

func TestElevenlabsScribeDiscoveryVirtualKey(t *testing.T) {
	vks := []configtables.TableVirtualKey{
		scribeVirtualKey("discovery-all", schemas.WhiteList{"*"}, nil, true),
		scribeVirtualKey("discovery-exact", schemas.WhiteList{"scribe_v2"}, nil, false),
		scribeVirtualKey("discovery-tts", schemas.WhiteList{"eleven_turbo_v2_5"}, nil, false),
		scribeVirtualKey("discovery-empty", schemas.WhiteList{}, nil, true),
		scribeVirtualKey("discovery-blocked", schemas.WhiteList{"*"}, schemas.BlackList{"scribe_v2"}, true),
	}
	handler, _, _ := newElevenlabsScribeHandler(t, []schemas.Key{scribeProviderKey()}, vks)
	for i, want := range [][]string{
		{"elevenlabs/eleven_turbo_v2_5", "elevenlabs/scribe_v1", "elevenlabs/scribe_v1_experimental", "elevenlabs/scribe_v2", "elevenlabs/dictation"},
		{"elevenlabs/scribe_v2"},
		{"elevenlabs/eleven_turbo_v2_5"},
		{},
		{"elevenlabs/eleven_turbo_v2_5", "elevenlabs/scribe_v1", "elevenlabs/scribe_v1_experimental", "elevenlabs/dictation"},
	} {
		t.Run(vks[i].Name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("/v1/models?provider=elevenlabs")
			ctx.Request.Header.Set("x-bf-vk", vks[i].Value.GetValue())
			handler.listModels(ctx)
			require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			var response schemas.BifrostListModelsResponse
			require.NoError(t, schemas.Unmarshal(ctx.Response.Body(), &response))
			ids := make([]string, 0, len(response.Data))
			for _, model := range response.Data {
				ids = append(ids, model.ID)
				if model.ID == "elevenlabs/eleven_turbo_v2_5" {
					require.Equal(t, "Upstream Turbo", *model.Name, "upstream metadata must be preserved")
				}
				if model.ID == "elevenlabs/dictation" {
					require.Equal(t, "scribe_v2", *model.Alias)
				}
			}
			require.ElementsMatch(t, want, ids)
		})
	}
	// The aggregate route still discovers an unrelated allowed provider normally.
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/v1/models")
	ctx.Request.Header.Set("x-bf-vk", vks[0].Value.GetValue())
	handler.listModels(ctx)
	require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	var response schemas.BifrostListModelsResponse
	require.NoError(t, schemas.Unmarshal(ctx.Response.Body(), &response))
	require.True(t, slices.ContainsFunc(response.Data, func(model schemas.Model) bool { return model.ID == "openai/gpt-4o-mini" }), "%s", ctx.Response.Body())
	require.True(t, slices.ContainsFunc(response.Data, func(model schemas.Model) bool { return model.ID == "elevenlabs/scribe_v2" }), "%s", ctx.Response.Body())
}

func TestElevenlabsScribeProviderKeyRestrictions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed schemas.WhiteList
		blocked schemas.BlackList
	}{
		{"tts-only key", schemas.WhiteList{"eleven_turbo_v2_5"}, nil},
		{"blacklisted scribe", schemas.WhiteList{"*"}, schemas.BlackList{"scribe_v2"}},
		{"empty key allowlist", schemas.WhiteList{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := schemas.Key{ID: "restricted-key", Models: tc.allowed, BlacklistedModels: tc.blocked, Value: *schemas.NewSecretVar("restricted-provider-key"), Weight: 1}
			vk := scribeVirtualKey("restricted", schemas.WhiteList{"*"}, nil, true)
			vk.ProviderConfigs[0].AllowAllKeys = false
			vk.ProviderConfigs[0].Keys = []configtables.TableKey{{KeyID: key.ID}}
			// A second key permits Scribe provider-wide, but the VK cannot use it.
			handler, _, calls := newElevenlabsScribeHandler(t, []schemas.Key{key, scribeProviderKey()}, []configtables.TableVirtualKey{vk})
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("/v1/models?provider=elevenlabs")
			ctx.Request.Header.Set("x-bf-vk", vk.Value.GetValue())
			handler.listModels(ctx)
			require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			var response schemas.BifrostListModelsResponse
			require.NoError(t, schemas.Unmarshal(ctx.Response.Body(), &response))
			require.False(t, slices.ContainsFunc(response.Data, func(model schemas.Model) bool { return model.ID == "elevenlabs/scribe_v2" }))
			ctx = scribeTranscriptionContext(t, "elevenlabs/scribe_v2", vk.Value.GetValue())
			handler.transcription(ctx)
			require.GreaterOrEqual(t, ctx.Response.StatusCode(), 400, string(ctx.Response.Body()))
			require.Contains(t, string(ctx.Response.Body()), "no keys found")
			require.Equal(t, int32(0), calls.Load(), "the inaccessible Scribe key must never be used")
		})
	}
}

// Live unfiltered entries can include another key's aliases. Family metadata
// lookup is not proof that such an alias is a provider-native model ID.
func TestElevenlabsScribeDiscoveryDoesNotCopyOtherKeyAliases(t *testing.T) {
	key := scribeProviderKey()
	key.Aliases = nil
	other := scribeProviderKey()
	other.ID = "other-key"
	alias := "scribe_v2-2024-08-06"
	other.Aliases = schemas.KeyAliases{alias: {ModelID: "scribe_v2"}}
	vk := scribeVirtualKey("key-scoped-discovery", schemas.WhiteList{"*"}, nil, false)
	vk.ProviderConfigs[0].AllowAllKeys = false
	vk.ProviderConfigs[0].Keys = []configtables.TableKey{{KeyID: key.ID}}
	handler, catalog, _ := newElevenlabsScribeHandler(t, []schemas.Key{key, other}, []configtables.TableVirtualKey{vk})
	catalog.UpsertLive(schemas.Elevenlabs, other.ID, true, []string{alias})
	require.NotNil(t, catalog.GetModelInfo(schemas.Elevenlabs, alias), "metadata lookup resolves model families too")
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/v1/models?provider=elevenlabs")
	ctx.Request.Header.Set("x-bf-vk", vk.Value.GetValue())
	handler.listModels(ctx)
	require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	var response schemas.BifrostListModelsResponse
	require.NoError(t, schemas.Unmarshal(ctx.Response.Body(), &response))
	ids := make([]string, 0, len(response.Data))
	for _, model := range response.Data {
		ids = append(ids, model.ID)
	}
	require.ElementsMatch(t, []string{"elevenlabs/eleven_turbo_v2_5", "elevenlabs/scribe_v1", "elevenlabs/scribe_v1_experimental", "elevenlabs/scribe_v2"}, ids)
}

func TestElevenlabsScribeDiscoveryPreservesAliasOverride(t *testing.T) {
	for _, tc := range []struct{ alias, target string }{{"scribe_v1", "scribe_v2"}, {"scribe_v2", "scribe_v1"}} {
		t.Run(tc.alias, func(t *testing.T) {
			key := scribeProviderKey()
			key.Aliases = schemas.KeyAliases{tc.alias: {ModelID: tc.target}}
			vk := scribeVirtualKey("alias-override", schemas.WhiteList{tc.alias}, nil, false)
			handler, _, _ := newElevenlabsScribeHandler(t, []schemas.Key{key}, []configtables.TableVirtualKey{vk})
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("/v1/models?provider=elevenlabs")
			ctx.Request.Header.Set("x-bf-vk", vk.Value.GetValue())
			handler.listModels(ctx)
			require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			var response schemas.BifrostListModelsResponse
			require.NoError(t, schemas.Unmarshal(ctx.Response.Body(), &response))
			require.Len(t, response.Data, 1)
			require.Equal(t, "elevenlabs/"+tc.alias, response.Data[0].ID)
			require.NotNil(t, response.Data[0].Alias)
			require.Equal(t, tc.target, *response.Data[0].Alias)
			ctx = scribeTranscriptionContext(t, "elevenlabs/"+tc.alias, vk.Value.GetValue())
			handler.transcription(ctx)
			require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		})
	}
}

func BenchmarkElevenlabsScribeDiscovery(b *testing.B) {
	vk := scribeVirtualKey("bench", schemas.WhiteList{"*"}, nil, true)
	handler, _, _ := newElevenlabsScribeHandler(b, []schemas.Key{scribeProviderKey()}, []configtables.TableVirtualKey{vk})
	ctx := schemas.NewBifrostContext(b.Context(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, vk.Value.GetValue())
	lib.SettleIdentity(ctx)
	req := &schemas.BifrostListModelsRequest{Provider: schemas.Elevenlabs}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, failure := handler.client.ListModelsRequest(ctx, req)
		if failure != nil {
			b.Fatal(failure.GetErrorString())
		}
	}
}

// The listing hands the narrowing to whoever can answer what the request may reach, so the
// providers it publishes are the ones that answer grants.
func TestApplyListModelsProviderFilterDelegatesToTheModelsManager(t *testing.T) {
	permit := grant.NewPermit(grant.PermitVirtualKey, "vk-test", "Test VK", true, false, []schemas.ProviderPermit{
		{Provider: "openai", AllowedModels: []string{"gpt-4o"}},
		// A provider granted no model at all is still asked: the fan-out decides who can
		// serve the request, and the response is filtered per model afterwards.
		{Provider: "anthropic"},
	}, nil)
	manager := &mockModelsManager{access: grant.NewAccess([]schemas.Permit{permit}, nil, "", nil)}
	h := &CompletionHandler{modelsManager: manager}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	h.applyListModelsProviderFilter(bifrostCtx)

	if manager.narrowCalls != 1 {
		t.Fatalf("narrow calls = %d, want 1", manager.narrowCalls)
	}
	got, ok := bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders).([]schemas.ModelProvider)
	if !ok {
		t.Fatalf("expected available providers to be published as []schemas.ModelProvider, got %#v",
			bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders))
	}
	want := []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic}
	if len(got) != len(want) {
		t.Fatalf("expected providers %#v, got %#v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected providers %#v, got %#v", want, got)
		}
	}
}

// Nothing resolved must leave the fan-out alone. Publishing an empty list here would mean "no
// provider may serve this", turning an unrestricted request into one that lists nothing.
func TestApplyListModelsProviderFilterLeavesFanOutAloneWhenNothingResolved(t *testing.T) {
	manager := &mockModelsManager{}
	h := &CompletionHandler{modelsManager: manager}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	h.applyListModelsProviderFilter(bifrostCtx)

	if manager.narrowCalls != 1 {
		t.Fatalf("narrow calls = %d, want 1", manager.narrowCalls)
	}
	if got := bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders); got != nil {
		t.Fatalf("expected nothing to be published, got %#v", got)
	}
}

// Narrowing is an optimization, not a permission check, so a handler wired without a models
// manager falls through instead of panicking on the request path.
func TestApplyListModelsProviderFilterWithoutModelsManager(t *testing.T) {
	h := &CompletionHandler{}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	h.applyListModelsProviderFilter(bifrostCtx)

	if got := bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders); got != nil {
		t.Fatalf("expected nothing to be published, got %#v", got)
	}
}

// OpenAI SDKs percent-encode the id ("openai%2Fgpt-4o-mini") and the router hands the
// catch-all over still encoded, so the target must be read after decoding it once.
func TestModelRetrieveTarget(t *testing.T) {
	for _, test := range []struct {
		name         string
		pathModel    string
		query        string
		wantProvider schemas.ModelProvider
		wantModel    string
		wantErr      string
	}{
		{name: "literal provider/model", pathModel: "openai/gpt-4o-mini", wantProvider: schemas.OpenAI, wantModel: "gpt-4o-mini"},
		{name: "encoded provider/model", pathModel: "openai%2Fgpt-4o-mini", wantProvider: schemas.OpenAI, wantModel: "gpt-4o-mini"},
		{name: "encoded namespaced id", pathModel: "groq%2Fopenai%2Fgpt-oss-120b", wantProvider: schemas.Groq, wantModel: "openai/gpt-oss-120b"},
		{name: "encoded id under ?provider=", pathModel: "openai%2Fgpt-oss-120b", query: "provider=groq", wantProvider: schemas.Groq, wantModel: "openai/gpt-oss-120b"},
		{name: "bare model without a provider", pathModel: "gpt-4o-mini", wantErr: "provider is required"},
		{name: "no model", pathModel: "%2F", wantErr: "model is required"},
		{name: "malformed encoding", pathModel: "openai%2", wantErr: "invalid model encoding"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("/v1/models/x?" + test.query)
			ctx.SetUserValue("model", test.pathModel)

			provider, model, err := modelRetrieveTarget(ctx)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if provider != test.wantProvider || model != test.wantModel {
				t.Errorf("got (%q, %q), want (%q, %q)", provider, model, test.wantProvider, test.wantModel)
			}
		})
	}
}
