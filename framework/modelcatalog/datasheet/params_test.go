package datasheet

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

func TestModelParameterCandidates(t *testing.T) {
	s := NewTestStore(map[string]string{
		"gpt-4o-2024-08-06": "gpt-4o",
	})
	s.mu.Lock()
	s.supportedParams = map[string][]string{
		"gpt-5.5":                          {"temperature"},
		"openrouter/moonshotai/kimi-k2.5":  {"temperature"},
		"openrouter/openai/gpt-5.5":        {"temperature"},
		"openrouter/moonshotai/kimi-k2.7":  {"temperature"},
		"openrouter/moonshotai2/kimi-k2.5": {"temperature"},
	}
	s.mu.Unlock()

	tests := []struct {
		name  string
		model string
		want  []string
	}{
		{
			name:  "bare model stays first",
			model: "gpt-5.5",
			want:  []string{"gpt-5.5", "openrouter/openai/gpt-5.5"},
		},
		{
			name:  "provider-qualified strips to bare",
			model: "openai/gpt-5.5",
			want:  []string{"openai/gpt-5.5", "gpt-5.5", "openrouter/openai/gpt-5.5"},
		},
		{
			name:  "double-qualified openrouter id strips progressively",
			model: "openrouter/openai/gpt-5.5",
			want:  []string{"openrouter/openai/gpt-5.5", "openai/gpt-5.5", "gpt-5.5"},
		},
		{
			name:  "bare alias finds qualified datasheet keys sorted",
			model: "kimi-k2.5",
			want:  []string{"kimi-k2.5", "openrouter/moonshotai/kimi-k2.5", "openrouter/moonshotai2/kimi-k2.5"},
		},
		{
			name:  "dated model includes canonical base name",
			model: "gpt-4o-2024-08-06",
			want:  []string{"gpt-4o-2024-08-06", "gpt-4o"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.modelParameterCandidates(tt.model)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("modelParameterCandidates(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

// A model-parameters sync must invalidate the capability cache, and must keep
// doing so after UpdateSyncConfig runs — operators change the sheet URL or
// interval from the config API at runtime and must not have to restart for
// sheet edits to take effect.
func TestApplyModelParameters_FiresAppliedHook(t *testing.T) {
	usable := map[string]json.RawMessage{
		"gpt-4o": json.RawMessage(`{"supports_reasoning":true}`),
	}

	t.Run("fires on a usable record", func(t *testing.T) {
		s := &Store{}
		fired := 0
		s.SetOnModelParametersApplied(func() { fired++ })
		if applied := s.applyModelParameters(usable); applied != 1 {
			t.Fatalf("applied = %d, want 1", applied)
		}
		if fired != 1 {
			t.Fatalf("applied hook fired %d times, want 1", fired)
		}
	})

	t.Run("survives a runtime sync-config update", func(t *testing.T) {
		s := &Store{}
		fired := 0
		s.SetOnModelParametersApplied(func() { fired++ })
		s.UpdateSyncConfig(Config{
			URL:                "file:///pricing.json",
			ModelParametersURL: "file:///params.json",
			SyncInterval:       time.Hour,
		})
		if applied := s.applyModelParameters(usable); applied != 1 {
			t.Fatalf("applied = %d, want 1", applied)
		}
		if fired != 1 {
			t.Fatalf("applied hook fired %d times after UpdateSyncConfig, want 1", fired)
		}
	})

	// A feed with nothing usable must leave the derived indexes alone too, or
	// the deployment is split: an empty parameter allowlist alongside capability
	// records that still describe the previous sheet.
	t.Run("leaves the derived indexes untouched on an empty feed", func(t *testing.T) {
		s := &Store{}
		if applied := s.applyModelParameters(usable); applied != 1 {
			t.Fatalf("seed applied = %d, want 1", applied)
		}
		s.mu.RLock()
		seeded := len(s.supportedParams)
		s.mu.RUnlock()

		if applied := s.applyModelParameters(map[string]json.RawMessage{
			"a": json.RawMessage(`{}`),
		}); applied != 0 {
			t.Fatalf("applied = %d, want 0", applied)
		}
		s.mu.RLock()
		after := len(s.supportedParams)
		s.mu.RUnlock()
		if after != seeded {
			t.Errorf("supportedParams size = %d after an empty feed, want %d (unchanged)", after, seeded)
		}
	})

	// The flush is what refills the cache, so a feed carrying nothing usable
	// must not drop records it cannot replace.
	t.Run("does not fire when every record is empty", func(t *testing.T) {
		s := &Store{}
		fired := 0
		s.SetOnModelParametersApplied(func() { fired++ })
		applied := s.applyModelParameters(map[string]json.RawMessage{
			"a": json.RawMessage(`{}`),
			"b": json.RawMessage(`{"provider":"openai"}`),
		})
		if applied != 0 {
			t.Fatalf("applied = %d, want 0 for empty records", applied)
		}
		if fired != 0 {
			t.Fatalf("applied hook fired %d times on an empty feed, want 0", fired)
		}
	})
}

// paramsOnlyConfigStore answers model-parameter reads and nothing else. The
// embedded interface is nil, so any other call panics rather than passing.
type paramsOnlyConfigStore struct {
	configstore.ConfigStore
	rows map[string]string // model key -> raw JSON
}

func (s paramsOnlyConfigStore) GetModelParametersByModel(_ context.Context, model string) (*configstoreTables.TableModelParameters, error) {
	data, ok := s.rows[model]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return &configstoreTables.TableModelParameters{Model: model, Data: data}, nil
}

// normalizeProvider folds every "*bedrock*" row provider onto "bedrock", so a
// bedrock_mantle lookup matched nothing and every capability check on that
// provider silently fell back — routing /v1/responses natively for models the
// sheet only lists on /v1/chat/completions.
func TestLoadModelCapabilities_MatchesBedrockMantleRows(t *testing.T) {
	const model = "openai.gpt-oss-safeguard-20b"
	rows := map[string]string{
		model:                     `{"provider":"bedrock","mode":"chat"}`,
		"bedrock_mantle/" + model: `{"provider":"bedrock_mantle","mode":"chat","supported_endpoints":["/v1/chat/completions"]}`,
	}
	s := NewTestStore(nil)
	s.configStore = paramsOnlyConfigStore{rows: rows}
	s.SetSupportedParamsForTest(map[string][]string{"bedrock_mantle/" + model: {"temperature"}})

	caps, err := s.LoadModelCapabilities(context.Background(), schemas.BedrockMantle, model)
	if err != nil {
		t.Fatalf("LoadModelCapabilities: %v", err)
	}
	if caps == nil {
		t.Fatal("no capabilities for a bedrock_mantle model the sheet has a row for")
	}
	if !slices.Equal(caps.SupportedEndpoints, []string{"/v1/chat/completions"}) {
		t.Fatalf("SupportedEndpoints = %v, want the bedrock_mantle row's", caps.SupportedEndpoints)
	}
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return caps })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
	if schemas.ResolveModelCaps(schemas.BedrockMantle, model).SupportsResponsesEndpoint(true) {
		t.Error("responses reported as supported for a chat-completions-only model")
	}

	// The plain bedrock row still resolves for a bedrock lookup.
	bedrockCaps, err := s.LoadModelCapabilities(context.Background(), schemas.Bedrock, model)
	if err != nil {
		t.Fatalf("LoadModelCapabilities(bedrock): %v", err)
	}
	if bedrockCaps == nil || len(bedrockCaps.SupportedEndpoints) != 0 {
		t.Errorf("bedrock lookup resolved to %+v, want the plain bedrock row", bedrockCaps)
	}
}

// server_side_model rides the model-parameters row (no pricing column, no
// migration), so it must survive the row -> capability record decode for the
// provider's runtime lookup.
func TestLoadModelCapabilities_CarriesServerSideModel(t *testing.T) {
	s := NewTestStore(nil)
	s.configStore = paramsOnlyConfigStore{rows: map[string]string{
		"deepseek/deepseek-v4-flash": `{"provider":"deepseek","mode":"chat","base_model":"deepseek-v4-flash","server_side_model":"deepseek-flash"}`,
	}}
	s.SetSupportedParamsForTest(map[string][]string{"deepseek/deepseek-v4-flash": {"temperature"}})

	caps, err := s.LoadModelCapabilities(context.Background(), schemas.DeepSeek, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("LoadModelCapabilities: %v", err)
	}
	if caps == nil || caps.ServerSideModel == nil || *caps.ServerSideModel != "deepseek-flash" {
		t.Fatalf("ServerSideModel = %+v, want deepseek-flash", caps)
	}
}

// A URL sync updates feed-owned rows, but DB-only qualified rows must remain
// discoverable after the derived indexes and capability cache are rebuilt.
func TestSyncModelParamsFromURL_PreservesDBOnlyRows(t *testing.T) {
	ctx := t.Context()
	cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, bifrost.NewNoOpLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(context.Background()) })
	const model = "custom-params-model"
	const customData = `{"provider":"custom-anthropic","max_output_tokens":32000,"supported_endpoints":["/v1/responses"],"supports_function_calling":true}`
	if err := cs.UpsertModelParametersBatch(ctx, []configstoreTables.TableModelParameters{
		{Model: "custom-anthropic/" + model, Data: customData},
		{Model: "other-anthropic/" + model, Data: `{"provider":"other-anthropic","max_output_tokens":64000}`},
		{Model: model, Data: `{"provider":"anthropic","max_output_tokens":8000}`},
	}); err != nil {
		t.Fatal(err)
	}
	feedPath := filepath.Join(t.TempDir(), "params.json")
	if err := os.WriteFile(feedPath, []byte(`{"custom-params-model":{"provider":"anthropic","max_output_tokens":16000}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(cs, nil, Config{ModelParametersURL: "file://" + feedPath})
	applied := 0
	s.SetOnModelParametersApplied(func() { applied++ })
	if _, err := s.LoadModelParamsFromDB(ctx); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"before sync", "after sync", "after repeated sync"} {
		if phase != "before sync" {
			if err := s.SyncModelParamsFromURL(ctx); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct {
			provider schemas.ModelProvider
			want     int
		}{
			{"custom-anthropic", 32000},
			{"other-anthropic", 64000},
			{schemas.Anthropic, 16000},
		} {
			want := tc.want
			if phase == "before sync" && tc.provider == schemas.Anthropic {
				want = 8000
			}
			caps, err := s.LoadModelCapabilities(ctx, tc.provider, model)
			if err != nil || caps == nil || caps.MaxOutputTokens == nil || *caps.MaxOutputTokens != want {
				t.Errorf("%s: capabilities for %s = %+v, err = %v, want max_output_tokens %d", phase, tc.provider, caps, err, want)
			}
		}
		if !slices.Contains(s.GetSupportedParameters("custom-anthropic/"+model), "tools") {
			t.Errorf("%s: DB-only supported parameters disappeared", phase)
		}
		if !s.IsRequestTypeSupported("custom-anthropic/"+model, schemas.ResponsesRequest) {
			t.Errorf("%s: DB-only supported response types disappeared", phase)
		}
		if caps, err := s.LoadModelCapabilities(ctx, "unrelated-provider", model); err != nil || caps != nil {
			t.Errorf("%s: unrelated provider resolved another provider's capabilities: %+v, %v", phase, caps, err)
		}
	}
	row, err := cs.GetModelParametersByModel(ctx, "custom-anthropic/"+model)
	if err != nil || row == nil || row.Data != customData {
		t.Fatalf("DB-only row changed: %+v, %v", row, err)
	}
	if applied != 3 {
		t.Fatalf("applied hook fired %d times, want 3", applied)
	}
}

type modelParamsReloadErrorStore struct {
	configstore.ConfigStore
	err      error
	upserted bool
}

// DB-only valid rows must not make an unusable URL feed look safe to apply.
func TestSyncModelParamsFromURL_UnusableFeedKeepsDBAndIndexes(t *testing.T) {
	for _, tc := range []struct {
		name string
		feed string
	}{
		{"empty feed", `{}`},
		{"null feed", `null`},
		{"empty record", `{"feed-model":{}}`},
		{"empty endpoints", `{"feed-model":{"supported_endpoints":[]}}`},
		{"empty parameters", `{"feed-model":{"model_parameters":[]}}`},
		{"empty tools", `{"feed-model":{"server_tools":{}}}`},
		{"empty collections", `{"feed-model":{"supported_endpoints":[],"server_tools":{}}}`},
		{"empty budget", `{"feed-model":{"reasoning_budget":{}}}`},
		{"null budget bounds", `{"feed-model":{"reasoning_budget":{"min":null,"max":null}}}`},
		{"null endpoints", `{"feed-model":{"supported_endpoints":[null]}}`},
		{"blank endpoints", `{"feed-model":{"supported_endpoints":[""]}}`},
		{"empty parameter descriptors", `{"feed-model":{"model_parameters":[{},null,{"id":""}]}}`},
		{"provider only", `{"feed-model":{"provider":"anthropic"}}`},
		{"null record", `{"feed-model":null}`},
		{"malformed capability", `{"feed-model":{"provider":"anthropic","max_output_tokens":"invalid"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
				Enabled: true,
				Type:    configstore.ConfigStoreTypeSQLite,
				Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
			}, bifrost.NewNoOpLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cs.Close(context.Background()) })
			const feedData = `{"provider":"anthropic","max_output_tokens":16000,"supported_endpoints":["/v1/responses"],"supports_function_calling":true}`
			if err := cs.UpsertModelParametersBatch(ctx, []configstoreTables.TableModelParameters{
				{Model: "feed-model", Data: feedData},
				{Model: "custom-anthropic/custom-model", Data: `{"provider":"custom-anthropic","max_output_tokens":32000}`},
			}); err != nil {
				t.Fatal(err)
			}
			feedPath := filepath.Join(t.TempDir(), "params.json")
			if err := os.WriteFile(feedPath, []byte(tc.feed), 0o600); err != nil {
				t.Fatal(err)
			}
			s := New(cs, nil, Config{ModelParametersURL: "file://" + feedPath})
			if _, err := s.LoadModelParamsFromDB(ctx); err != nil {
				t.Fatal(err)
			}
			fired := false
			s.SetOnModelParametersApplied(func() { fired = true })
			if err := s.SyncModelParamsFromURL(ctx); err != nil {
				t.Fatal(err)
			}
			if fired {
				t.Error("unusable feed flushed the capability cache")
			}
			if !slices.Contains(s.GetSupportedParameters("feed-model"), "tools") || !s.IsRequestTypeSupported("feed-model", schemas.ResponsesRequest) {
				t.Error("unusable feed replaced the existing indexes")
			}
			row, err := cs.GetModelParametersByModel(ctx, "feed-model")
			if err != nil || row == nil || row.Data != feedData {
				t.Errorf("unusable feed overwrote the DB row: %+v, %v", row, err)
			}
		})
	}
}

// A mixed feed updates usable records without replacing existing rows with unusable data.
func TestSyncModelParamsFromURL_MixedFeedKeepsUnusableRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
	}{
		{"empty record", `{}`},
		{"empty endpoints", `{"supported_endpoints":[]}`},
		{"empty parameters", `{"model_parameters":[]}`},
		{"empty tools", `{"server_tools":{}}`},
		{"empty collections", `{"supported_endpoints":[],"server_tools":{}}`},
		{"empty budget", `{"reasoning_budget":{}}`},
		{"null budget bounds", `{"reasoning_budget":{"min":null,"max":null}}`},
		{"null endpoints", `{"supported_endpoints":[null]}`},
		{"blank endpoints", `{"supported_endpoints":[""]}`},
		{"empty parameter descriptors", `{"model_parameters":[{},null,{"id":""}]}`},
		{"provider only", `{"provider":"custom-anthropic"}`},
		{"null record", `null`},
		{"malformed capability", `{"provider":"custom-anthropic","max_output_tokens":"invalid"}`},
		{"array record", `[]`},
		{"boolean record", `false`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
				Enabled: true,
				Type:    configstore.ConfigStoreTypeSQLite,
				Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
			}, bifrost.NewNoOpLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cs.Close(context.Background()) })
			const model = "custom-params-model"
			const customData = `{"provider":"custom-anthropic","max_output_tokens":32000,"supported_endpoints":["/v1/responses"],"supports_function_calling":true}`
			if err := cs.UpsertModelParametersBatch(ctx, []configstoreTables.TableModelParameters{
				{Model: "custom-anthropic/" + model, Data: customData},
				{Model: model, Data: `{"provider":"anthropic","max_output_tokens":8000}`},
			}); err != nil {
				t.Fatal(err)
			}
			feedPath := filepath.Join(t.TempDir(), "params.json")
			feed := `{"custom-anthropic/custom-params-model":` + tc.data + `,"custom-params-model":{"provider":"anthropic","max_output_tokens":16000}}`
			if err := os.WriteFile(feedPath, []byte(feed), 0o600); err != nil {
				t.Fatal(err)
			}
			s := New(cs, nil, Config{ModelParametersURL: "file://" + feedPath})
			if _, err := s.LoadModelParamsFromDB(ctx); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := s.SyncModelParamsFromURL(ctx); err != nil {
					t.Fatal(err)
				}
				row, err := cs.GetModelParametersByModel(ctx, "custom-anthropic/"+model)
				if err != nil || row == nil || row.Data != customData {
					t.Errorf("mixed feed overwrote the good custom row: %+v, %v", row, err)
				}
				for _, want := range []struct {
					provider schemas.ModelProvider
					limit    int
				}{{"custom-anthropic", 32000}, {schemas.Anthropic, 16000}} {
					caps, err := s.LoadModelCapabilities(ctx, want.provider, model)
					if err != nil || caps == nil || caps.MaxOutputTokens == nil || *caps.MaxOutputTokens != want.limit {
						t.Errorf("capabilities for %s = %+v, %v; want max_output_tokens %d", want.provider, caps, err, want.limit)
					}
				}
				if !slices.Contains(s.GetSupportedParameters("custom-anthropic/"+model), "tools") || !s.IsRequestTypeSupported("custom-anthropic/"+model, schemas.ResponsesRequest) {
					t.Error("mixed feed removed the existing parameter or response-type index")
				}
			}
		})
	}
}

func (s *modelParamsReloadErrorStore) UpsertModelParametersBatch(context.Context, []configstoreTables.TableModelParameters, ...*gorm.DB) error {
	s.upserted = true
	return nil
}

func (s *modelParamsReloadErrorStore) GetModelParameters(context.Context) ([]configstoreTables.TableModelParameters, error) {
	return nil, s.err
}

func TestSyncModelParamsFromURL_DBReadErrorKeepsIndexes(t *testing.T) {
	feedPath := filepath.Join(t.TempDir(), "params.json")
	if err := os.WriteFile(feedPath, []byte(`{"new-model":{"provider":"anthropic","max_output_tokens":16000}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dbErr := errors.New("database unavailable")
	cs := &modelParamsReloadErrorStore{err: dbErr}
	s := New(cs, nil, Config{ModelParametersURL: "file://" + feedPath})
	s.applyModelParameters(map[string]json.RawMessage{
		"custom-anthropic/old-model": json.RawMessage(`{"provider":"custom-anthropic","max_output_tokens":32000,"supports_function_calling":true}`),
	})
	fired := false
	s.SetOnModelParametersApplied(func() { fired = true })
	if err := s.SyncModelParamsFromURL(t.Context()); !errors.Is(err, dbErr) {
		t.Errorf("sync error = %v, want database reload error", err)
	}
	if cs.upserted {
		t.Error("failed snapshot read must not persist the feed")
	}
	if fired || !slices.Contains(s.GetSupportedParameters("custom-anthropic/old-model"), "tools") {
		t.Fatal("failed DB reload must preserve the existing indexes and capability cache")
	}
}

func TestSyncModelParamsFromURL_WithoutConfigStore(t *testing.T) {
	feedPath := filepath.Join(t.TempDir(), "params.json")
	if err := os.WriteFile(feedPath, []byte(`{"feed-model":{"provider":"anthropic","supported_endpoints":["/v1/responses"],"supports_function_calling":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(nil, nil, Config{ModelParametersURL: "file://" + feedPath})
	if err := s.SyncModelParamsFromURL(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(s.GetSupportedParameters("feed-model"), "tools") || !s.IsRequestTypeSupported("feed-model", schemas.ResponsesRequest) {
		t.Fatal("URL-only sync did not apply the feed")
	}
}

// Pause the first snapshot after reading it, while another sync tries to add a
// different row. Both committed rows must remain in the published indexes.
type modelParamsSnapshotStore struct {
	configstore.ConfigStore
	mu           sync.Mutex
	rows         map[string]string
	reads        int
	firstRead    chan struct{}
	secondRead   chan struct{}
	releaseFirst chan struct{}
}

func (s *modelParamsSnapshotStore) GetModelParameters(context.Context) ([]configstoreTables.TableModelParameters, error) {
	s.mu.Lock()
	rows := make([]configstoreTables.TableModelParameters, 0, len(s.rows))
	for model, data := range s.rows {
		rows = append(rows, configstoreTables.TableModelParameters{Model: model, Data: data})
	}
	s.reads++
	read := s.reads
	s.mu.Unlock()
	if read == 1 {
		close(s.firstRead)
		<-s.releaseFirst
	}
	if read == 2 {
		close(s.secondRead)
	}
	return rows, nil
}

func (s *modelParamsSnapshotStore) UpsertModelParametersBatch(_ context.Context, rows []configstoreTables.TableModelParameters, _ ...*gorm.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		s.rows[row.Model] = row.Data
	}
	return nil
}

func TestSyncModelParamsFromURL_ConcurrentSnapshotsKeepBothRows(t *testing.T) {
	cs := &modelParamsSnapshotStore{rows: make(map[string]string), firstRead: make(chan struct{}), secondRead: make(chan struct{}), releaseFirst: make(chan struct{})}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(cs.releaseFirst) }) })
	dir := t.TempDir()
	for _, model := range []string{"first-model", "second-model"} {
		if err := os.WriteFile(filepath.Join(dir, model+".json"), []byte(`{"`+model+`":{"provider":"anthropic","supports_function_calling":true}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := New(cs, nil, Config{ModelParametersURL: "file://" + filepath.Join(dir, "first-model.json")})
	done := make(chan error, 2)
	go func() { done <- s.SyncModelParamsFromURL(t.Context()) }()
	select {
	case <-cs.firstRead:
	case <-time.After(5 * time.Second):
		t.Fatal("first sync never read its snapshot")
	}
	s.UpdateSyncConfig(Config{ModelParametersURL: "file://" + filepath.Join(dir, "second-model.json")})
	go func() { done <- s.SyncModelParamsFromURL(t.Context()) }()
	// Without serialization the second sync can finish before the stale first
	// snapshot publishes. With serialization it waits until the first commits.
	select {
	case <-cs.secondRead:
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		release.Do(func() { close(cs.releaseFirst) })
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		release.Do(func() { close(cs.releaseFirst) })
		for range 2 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, model := range []string{"first-model", "second-model"} {
		if !slices.Contains(s.GetSupportedParameters(model), "tools") {
			t.Errorf("committed model %s missing from indexes", model)
		}
	}
}
