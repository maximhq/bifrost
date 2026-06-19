// Run all:  go test -run TestConformance_Models -count=1 -v .
// Run one:  go test -run 'TestConformance_Models/wildcard_linked' -count=1 -v .
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/scripts/bifrost-migration-cli/litellm"
)

// ---- live test harness -----------------------------------------------------

// conformanceEnv builds the migration config from env.
func conformanceEnv(t *testing.T) MigrationRunConfig {
	t.Helper()
	for _, key := range []string{
		"LITELLM_MASTER_KEY",
		"LITELLM_URL",
		"LITELLM_CONFIG",
		"LITELLM_DB_URL",
		"BIFROST_URL",
		"BIFROST_API_KEY",
	} {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			t.Skipf("skipping LiteLLM→Bifrost conformance test: %s is not set", key)
		}
	}
	return NewMigrationRunConfig()
}

// keepEntities leaves seeded entities behind for inspection (CONF_NO_CLEANUP=1).
func keepEntities() bool { return os.Getenv("CONF_NO_CLEANUP") == "1" }

// rawRequest performs an HTTP request with an optional JSON body and bearer,
// returning the body, status code, and a non-2xx error.
func rawRequest(method, reqURL, apiKey string, body any) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, reqURL, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("%s %s: read body: %w", method, reqURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, resp.StatusCode, fmt.Errorf("%s %s: status %d: %s", method, reqURL, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return out, resp.StatusCode, nil
}

// doJSON performs a request and decodes the JSON response into T, failing on any
// transport or decode error.
func doJSON[T any](t *testing.T, method, reqURL, apiKey string, body any) T {
	t.Helper()
	out, _, err := rawRequest(method, reqURL, apiKey, body)
	if err != nil {
		t.Fatalf("%v", err)
	}
	var v T
	if len(out) > 0 {
		if err := json.Unmarshal(out, &v); err != nil {
			t.Fatalf("decode %s %s: %v (body: %s)", method, reqURL, err, string(out))
		}
	}
	return v
}

// ---- declarative case model ------------------------------------------------

// customBase is a real, resolvable host (Bifrost validates the base URL by
// resolving it). Reused across cases since each case cleans up after itself.
const customBase = "https://example-endpoint-production.up.railway.app/"

// seedCred describes one LiteLLM credential to create for a case.
type seedCred struct {
	name     string // case-local name; suffixed unique per run
	provider string // credential_info.custom_llm_provider (base provider)
	key      string // credential_values.api_key (env ref by default)
	base     string // credential_values.api_base; "" => no base URL
}

// seedModel describes one LiteLLM deployment. Only set fields are sent; cred
// wires a litellm_credential_name, the rest are inline litellm_params.
type seedModel struct {
	name           string  // public model_name
	model          string  // litellm_params.model, e.g. "openai/gpt-4o" or "openai/*"
	provider       string  // litellm_params.custom_llm_provider (self-hosted: LiteLLM only routes when the provider is here, not in credential_info)
	cred           string  // case-local credential name to link, or "" for none
	key            string  // inline api_key
	base           string  // inline api_base
	rpm, tpm       int     // inline per-deployment rate limits
	maxBudget      float64 // inline per-deployment budget cap
	budgetDuration string  // inline budget reset window, e.g. "30d"
}

// wantProvider is an expected Bifrost provider: base provider type, base URL
// ("" => standard provider whose name equals base), and one allow-list per key
// (order-insensitive). Custom providers must match keys exactly; standard
// providers need only contain the expected allow-lists (the real provider may
// carry unrelated keys).
type wantProvider struct {
	base string
	url  string
	keys [][]string
}

// wantModelConfig is an expected global Bifrost model config.
type wantModelConfig struct {
	provider    string  // "" => all providers (nil in Bifrost)
	model       string  // upstream model name, or "*"
	rpm, tpm    int64   // 0 => not expected
	budget      float64 // 0 => not expected
	budgetReset string  // expected reset_duration when budget set
}

type modelCase struct {
	name         string
	creds        []seedCred
	models       []seedModel
	providers    []wantProvider
	modelConfigs []wantModelConfig
}

func TestConformance_Models(t *testing.T) {
	env := conformanceEnv(t)

	// envKey backs linked credentials; the Bifrost key is named after the
	// credential, so the literal masked value is all that matters.
	const envKey = "os.environ/OPENAI_API_KEY"
	// inlineKey backs inline deployments. Bifrost resolves env-ref key values at
	// create time (so the var must be in Bifrost's environment) AND names the key
	// after the env var, which must not collide with an existing Bifrost key — so
	// this is a real env var that is not already configured as a Bifrost key.
	const inlineKey = "os.environ/GROQ_API_KEY"

	cases := []modelCase{
		// --- standard providers, linked credentials (expected PASS) ---------
		{
			name:      "standard_linked_openai",
			creds:     []seedCred{{"oa", "openai", envKey, ""}},
			models:    []seedModel{{name: "m", model: "openai/gpt-4o", cred: "oa"}},
			providers: []wantProvider{{"openai", "", [][]string{{"gpt-4o"}}}},
		},
		{
			name:      "standard_linked_anthropic",
			creds:     []seedCred{{"an", "anthropic", envKey, ""}},
			models:    []seedModel{{name: "m", model: "anthropic/claude-sonnet-4-20250514", cred: "an"}},
			providers: []wantProvider{{"anthropic", "", [][]string{{"claude-sonnet-4-20250514"}}}},
		},
		{
			name:      "standard_linked_gemini",
			creds:     []seedCred{{"gm", "gemini", envKey, ""}},
			models:    []seedModel{{name: "m", model: "gemini/gemini-2.0-flash", cred: "gm"}},
			providers: []wantProvider{{"gemini", "", [][]string{{"gemini-2.0-flash"}}}},
		},
		{
			name:  "standard_linked_two_credentials",
			creds: []seedCred{{"a", "openai", envKey, ""}, {"b", "openai", envKey, ""}},
			models: []seedModel{
				{name: "m1", model: "openai/gpt-4o", cred: "a"},
				{name: "m2", model: "openai/gpt-4o-mini", cred: "b"},
			},
			providers: []wantProvider{{"openai", "", [][]string{{"gpt-4o"}, {"gpt-4o-mini"}}}},
		},

		// --- linked credential, multiple models on one key (expected PASS) --
		{
			name:  "linked_credential_unions_models",
			creds: []seedCred{{"oa", "openai", envKey, ""}},
			models: []seedModel{
				{name: "m1", model: "openai/gpt-4o", cred: "oa"},
				{name: "m2", model: "openai/gpt-4o-mini", cred: "oa"},
				{name: "m3", model: "openai/o3", cred: "oa"},
			},
			providers: []wantProvider{{"openai", "", [][]string{{"gpt-4o", "gpt-4o-mini", "o3"}}}},
		},

		// --- custom providers, linked credentials (expected PASS) -----------
		{
			name:      "custom_linked_openai",
			creds:     []seedCred{{"c", "openai", envKey, customBase}},
			models:    []seedModel{{name: "m", model: "openai/gpt-4o", cred: "c"}},
			providers: []wantProvider{{"openai", customBase, [][]string{{"gpt-4o"}}}},
		},
		{
			name:      "custom_linked_anthropic",
			creds:     []seedCred{{"c", "anthropic", envKey, customBase}},
			models:    []seedModel{{name: "m", model: "anthropic/claude-3-5-haiku-20241022", cred: "c"}},
			providers: []wantProvider{{"anthropic", customBase, [][]string{{"claude-3-5-haiku-20241022"}}}},
		},
		{
			name: "custom_two_credentials_same_base",
			creds: []seedCred{
				{"c1", "openai", envKey, customBase},
				{"c2", "openai", envKey, customBase},
			},
			models: []seedModel{
				{name: "m1", model: "openai/gpt-4o", cred: "c1"},
				{name: "m2", model: "openai/gpt-4o-mini", cred: "c2"},
			},
			providers: []wantProvider{{"openai", customBase, [][]string{{"gpt-4o"}, {"gpt-4o-mini"}}}},
		},

		// --- wildcards (expected PASS) --------------------------------------
		{
			name:      "wildcard_linked",
			creds:     []seedCred{{"oa", "openai", envKey, ""}},
			models:    []seedModel{{name: "m", model: "openai/*", cred: "oa"}},
			providers: []wantProvider{{"openai", "", [][]string{{"*"}}}},
		},
		{
			name:      "wildcard_custom_linked",
			creds:     []seedCred{{"c", "openai", envKey, customBase}},
			models:    []seedModel{{name: "m", model: "openai/*", cred: "c"}},
			providers: []wantProvider{{"openai", customBase, [][]string{{"*"}}}},
		},

		// --- inline credentials (resolved from the LiteLLM DB, decrypted with the
		//     salt key, since /model/info masks/omits them). inlineKey is a real
		//     env-ref Bifrost resolves at create time. ------------------------
		{
			name:      "inline_key_standard",
			models:    []seedModel{{name: "m", model: "openai/gpt-4o", key: inlineKey}},
			providers: []wantProvider{{"openai", "", [][]string{{"gpt-4o"}}}},
		},
		{
			name:      "inline_base_url_custom",
			models:    []seedModel{{name: "m", model: "openai/gpt-4o", key: inlineKey, base: customBase}},
			providers: []wantProvider{{"openai", customBase, [][]string{{"gpt-4o"}}}},
		},
		{
			name:      "wildcard_inline",
			models:    []seedModel{{name: "m", model: "openai/*", key: inlineKey}},
			providers: []wantProvider{{"openai", "", [][]string{{"*"}}}},
		},

		// --- per-deployment budgets / rate limits. Rate limits come from
		//     /model/info (unencrypted); the budget (max_budget + budget_duration)
		//     comes from the deployment's litellm_params, resolved from the DB. ---
		{
			name:         "rate_limit_rpm_tpm",
			creds:        []seedCred{{"oa", "openai", envKey, ""}},
			models:       []seedModel{{name: "m", model: "openai/gpt-4o", cred: "oa", rpm: 200, tpm: 200000}},
			providers:    []wantProvider{{"openai", "", [][]string{{"gpt-4o"}}}},
			modelConfigs: []wantModelConfig{{provider: "openai", model: "gpt-4o", rpm: 200, tpm: 200000}},
		},
		{
			name:         "budget_30d",
			creds:        []seedCred{{"oa", "openai", envKey, ""}},
			models:       []seedModel{{name: "m", model: "openai/gpt-4o", cred: "oa", maxBudget: 50, budgetDuration: "30d"}},
			providers:    []wantProvider{{"openai", "", [][]string{{"gpt-4o"}}}},
			modelConfigs: []wantModelConfig{{provider: "openai", model: "gpt-4o", budget: 50, budgetReset: "30d"}},
		},
		{
			name:         "budget_and_rate_limit",
			creds:        []seedCred{{"an", "anthropic", envKey, ""}},
			models:       []seedModel{{name: "m", model: "anthropic/claude-sonnet-4-20250514", cred: "an", maxBudget: 10, budgetDuration: "1d", rpm: 50, tpm: 2000}},
			providers:    []wantProvider{{"anthropic", "", [][]string{{"claude-sonnet-4-20250514"}}}},
			modelConfigs: []wantModelConfig{{provider: "anthropic", model: "claude-sonnet-4-20250514", rpm: 50, tpm: 2000, budget: 10, budgetReset: "1d"}},
		},

		// --- self-hosted: vllm/ollama keep the standard provider and carry the
		//     server URL on a per-base-URL key. LiteLLM only routes these when the
		//     provider is in litellm_params (custom_llm_provider), not the
		//     credential, so the seed sets it there. ---------------------------
		{
			name:      "vllm_base_url",
			creds:     []seedCred{{"c", "vllm", envKey, customBase}},
			models:    []seedModel{{name: "m", model: "meta-llama/Llama-3.1-8B-Instruct", provider: "vllm", cred: "c"}},
			providers: []wantProvider{{"vllm", customBase, [][]string{{"meta-llama/Llama-3.1-8B-Instruct"}}}},
		},
		{
			name:      "ollama_base_url",
			creds:     []seedCred{{"c", "ollama", envKey, customBase}},
			models:    []seedModel{{name: "m", model: "llama3.1", provider: "ollama", cred: "c"}},
			providers: []wantProvider{{"ollama", customBase, [][]string{{"llama3.1"}}}},
		},
		// (LiteLLM has no sglang provider — it is served as an OpenAI-compatible
		// endpoint — so there is no self-hosted case beyond vllm/ollama.)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Unique suffix keeps names from colliding across runs; the local
			// names in the case wire up cred<->model links.
			suffix := nameSuffix(tc.name)
			for _, c := range tc.creds {
				env.seedCred(t, c.name+suffix, c.provider, c.key, c.base)
			}
			for _, m := range tc.models {
				if m.cred != "" {
					m.cred += suffix
				}
				m.name += suffix
				env.seedModel(t, m)
			}

			env.migrateModels(t, suffix)

			for _, w := range tc.providers {
				env.assertProvider(t, w)
			}
			for _, w := range tc.modelConfigs {
				env.assertModelConfig(t, w)
			}
		})
	}
}

// ---- seeding ---------------------------------------------------------------

// seedCred creates a LiteLLM credential and registers its deletion.
func (e MigrationRunConfig) seedCred(t *testing.T, name, provider, key, base string) {
	t.Helper()
	values := map[string]any{"api_key": key}
	if base != "" {
		values["api_base"] = base
	}
	doJSON[struct{}](t, http.MethodPost, e.LiteLLMClient.BaseURL+"/credentials", e.LiteLLMClient.APIKey, map[string]any{
		"credential_name":   name,
		"credential_info":   map[string]any{"custom_llm_provider": provider},
		"credential_values": values,
	})
	if keepEntities() {
		return
	}
	t.Cleanup(func() {
		_, _, _ = rawRequest(http.MethodDelete, e.LiteLLMClient.BaseURL+"/credentials/"+name, e.LiteLLMClient.APIKey, nil)
	})
}

// seedModel creates a LiteLLM deployment and registers its deletion by id.
func (e MigrationRunConfig) seedModel(t *testing.T, m seedModel) {
	t.Helper()
	params := map[string]any{"model": m.model}
	if m.provider != "" {
		params["custom_llm_provider"] = m.provider
	}
	if m.cred != "" {
		params["litellm_credential_name"] = m.cred
	}
	if m.key != "" {
		params["api_key"] = m.key
	}
	if m.base != "" {
		params["api_base"] = m.base
	}
	if m.rpm != 0 {
		params["rpm"] = m.rpm
	}
	if m.tpm != 0 {
		params["tpm"] = m.tpm
	}
	if m.maxBudget != 0 {
		params["max_budget"] = m.maxBudget
	}
	if m.budgetDuration != "" {
		params["budget_duration"] = m.budgetDuration
	}
	id := doJSON[struct {
		ModelID string `json:"model_id"`
	}](t, http.MethodPost, e.LiteLLMClient.BaseURL+"/model/new", e.LiteLLMClient.APIKey, map[string]any{
		"model_name":     m.name,
		"litellm_params": params,
	}).ModelID
	if id == "" {
		t.Fatalf("seed model %q: empty model_id in response", m.name)
	}
	if keepEntities() {
		return
	}
	t.Cleanup(func() {
		_, _, _ = rawRequest(http.MethodPost, e.LiteLLMClient.BaseURL+"/model/delete", e.LiteLLMClient.APIKey, map[string]any{"id": id})
	})
}

// ---- migration -------------------------------------------------------------

// migrateModels runs the real transform and writes the result to Bifrost,
// registering cleanup for every custom provider, standard-provider key and model
// config it creates. It migrates only the entities tagged with this run's
// suffix, so leftover entities from other runs (e.g. CONF_NO_CLEANUP) never leak
// in — each case is hermetic.
func (e MigrationRunConfig) migrateModels(t *testing.T, suffix string) {
	t.Helper()
	ctx := context.Background()

	all, err := e.LiteLLMClient.ListModelInfo(ctx)
	if err != nil {
		t.Fatalf("list model info: %v", err)
	}
	var models []litellm.LiteLLMModelInfo
	for _, m := range all {
		if strings.HasSuffix(m.ModelName, suffix) {
			models = append(models, m)
		}
	}

	allCreds, err := e.LiteLLMClient.ListCredentials(ctx)
	if err != nil {
		t.Fatalf("list credentials: %v", err)
	}

	credByName := map[string]*litellm.LiteLLMModelCredential{}
	for i := range allCreds {
		if strings.HasSuffix(allCreds[i].CredentialName, suffix) {
			credByName[allCreds[i].CredentialName] = &allCreds[i]
		}
	}

	// Inline credentials and budgets are masked/omitted by the API, so resolve
	// them from the LiteLLM database (decrypted with the salt key).
	store, err := litellm.NewCredentialStore(ctx, e.LiteLLMDBURL, nil, e.LiteLLMSaltKey)
	if err != nil {
		t.Fatalf("open credential store: %v", err)
	}
	defer store.Close()
	deployments, err := store.Deployments(ctx)
	if err != nil {
		t.Fatalf("resolve deployments: %v", err)
	}

	plans, modelConfigs, _ := LiteLLMModelsToBifrostProviders(models, credByName, deployments, e)

	for _, p := range plans {
		if err := e.BifrostClient.EnsureProvider(ctx, p); err != nil {
			t.Fatalf("ensure provider %q: %v", p.Name, err)
		}
		// A custom provider is disposable; deleting it removes its keys too. A
		// standard provider is shared, so clean up only the keys we added.
		if p.IsCustom {
			e.cleanupProvider(t, p.Name)
		}
		for _, k := range p.Keys {
			if err := e.BifrostClient.CreateProviderKey(ctx, p.Name, k); err != nil {
				t.Fatalf("create key %q/%q: %v", p.Name, k.Name, err)
			}
			if !p.IsCustom {
				e.cleanupStandardKey(t, p.Name, k.Name)
			}
		}
	}
	for _, mc := range modelConfigs {
		if err := e.BifrostClient.CreateModelConfig(ctx, mc); err != nil {
			t.Fatalf("create model config %q: %v", modelConfigSignature(mc), err)
		}
		e.cleanupModelConfig(t, mc.ModelName, mc.Provider)
	}
}

// ---- assertions ------------------------------------------------------------

// assertProvider checks the migrated provider's type, base URL and key allow-
// lists. Custom providers require an exact key match; standard providers (incl.
// the self-hosted keyless ones) only require the expected allow-lists to be
// present. For self-hosted providers w.url is the per-key server URL, so the
// matching keys' *_key_config is checked too.
func (e MigrationRunConfig) assertProvider(t *testing.T, w wantProvider) {
	t.Helper()
	selfHosted := selfHostedURLProviders[w.base]
	custom := w.url != "" && !selfHosted
	name := w.base
	if custom {
		name = customProviderName(w.base, w.url)
	}

	prov, found := e.fetchProvider(t, name)
	if !found {
		t.Errorf("provider %q not found after migration", name)
		return
	}
	if custom {
		if prov.CustomProviderConfig == nil || prov.CustomProviderConfig.BaseProviderType != w.base {
			t.Errorf("provider %q: custom_provider_config = %+v, want base_provider_type %q", name, prov.CustomProviderConfig, w.base)
		}
		if prov.NetworkConfig.BaseURL != w.url {
			t.Errorf("provider %q: base_url = %q, want %q", name, prov.NetworkConfig.BaseURL, w.url)
		}
	} else if prov.CustomProviderConfig != nil {
		t.Errorf("provider %q: expected standard provider, got custom %+v", name, prov.CustomProviderConfig)
	}

	keys := e.fetchProviderKeys(t, name)
	got := normalizeLists(allowlistsOf(keys))
	want := normalizeLists(w.keys)
	if custom {
		if !reflect.DeepEqual(want, got) {
			t.Errorf("provider %q key allow-lists = %v, want %v", name, got, want)
		}
		return
	}
	for _, wl := range want {
		if !containsList(got, wl) {
			t.Errorf("provider %q: missing expected key allow-list %v (got %v)", name, wl, got)
		}
	}
	if selfHosted {
		e.assertSelfHostedKeyURLs(t, w, keys)
	}
}

// assertSelfHostedKeyURLs verifies that, for each expected allow-list, a key
// exists carrying a per-key server URL in the provider-matching *_key_config
// (and, for vllm, the served model in model_name). Bifrost masks the URL value
// on read, so the URL is asserted present (non-empty, not env-sourced) rather
// than equal to w.url.
func (e MigrationRunConfig) assertSelfHostedKeyURLs(t *testing.T, w wantProvider, keys providerKeysResponse) {
	t.Helper()
	for _, wl := range normalizeLists(w.keys) {
		matched := false
		for _, k := range keys.Keys {
			km := append([]string(nil), k.Models...)
			sort.Strings(km)
			if !reflect.DeepEqual(km, wl) {
				continue
			}
			var gotURL maskedValue
			var gotModel string
			present := false
			switch w.base {
			case "vllm":
				if k.VLLMKeyConfig != nil {
					present, gotURL, gotModel = true, k.VLLMKeyConfig.URL, k.VLLMKeyConfig.ModelName
				}
			case "ollama":
				if k.OllamaKeyConfig != nil {
					present, gotURL = true, k.OllamaKeyConfig.URL
				}
			}
			if !present || gotURL.Value == "" || gotURL.FromEnv {
				continue
			}
			if w.base == "vllm" && len(wl) == 1 && gotModel != wl[0] {
				continue
			}
			matched = true
			break
		}
		if !matched {
			t.Errorf("provider %q: no key with allow-list %v carrying a %s per-key url", w.base, wl, w.base)
		}
	}
}

// assertModelConfig checks a global model config exists with the expected
// budget / rate-limit fields.
func (e MigrationRunConfig) assertModelConfig(t *testing.T, w wantModelConfig) {
	t.Helper()
	mc, found := e.fetchModelConfig(t, w.model, w.provider)
	if !found {
		t.Errorf("model config %s/%s not found after migration", orAll(w.provider), w.model)
		return
	}
	if w.budget > 0 {
		if len(mc.Budgets) == 0 {
			t.Errorf("model config %s/%s: no budget, want %v/%s", orAll(w.provider), w.model, w.budget, w.budgetReset)
		} else if mc.Budgets[0].MaxLimit != w.budget || mc.Budgets[0].ResetDuration != w.budgetReset {
			t.Errorf("model config %s/%s: budget = %v/%s, want %v/%s", orAll(w.provider), w.model,
				mc.Budgets[0].MaxLimit, mc.Budgets[0].ResetDuration, w.budget, w.budgetReset)
		}
	}
	if w.rpm > 0 {
		assertLimit(t, "rpm", w.rpm, rateLimitField(mc.RateLimit, func(r bifrostRateLimit) *int64 { return r.RequestMaxLimit }))
	}
	if w.tpm > 0 {
		assertLimit(t, "tpm", w.tpm, rateLimitField(mc.RateLimit, func(r bifrostRateLimit) *int64 { return r.TokenMaxLimit }))
	}
}

func assertLimit(t *testing.T, name string, want int64, got *int64) {
	t.Helper()
	switch {
	case got == nil:
		t.Errorf("model config %s = unset, want %d", name, want)
	case *got != want:
		t.Errorf("model config %s = %d, want %d", name, *got, want)
	}
}

// ---- Bifrost reads ---------------------------------------------------------

type providerResponse struct {
	Name          string `json:"name"`
	NetworkConfig struct {
		BaseURL string `json:"base_url"`
	} `json:"network_config"`
	CustomProviderConfig *struct {
		BaseProviderType string `json:"base_provider_type"`
	} `json:"custom_provider_config"`
}

// maskedValue is Bifrost's wrapper for a secret-bearing field (key value, per-key
// URL): the plaintext is masked on read, so only presence / env-ref metadata is
// assertable.
type maskedValue struct {
	Value   string `json:"value"`
	EnvVar  string `json:"env_var"`
	FromEnv bool   `json:"from_env"`
}

type providerKeysResponse struct {
	Keys []struct {
		ID            string   `json:"id"`
		Name          string   `json:"name"`
		Models        []string `json:"models"`
		VLLMKeyConfig *struct {
			URL       maskedValue `json:"url"`
			ModelName string      `json:"model_name"`
		} `json:"vllm_key_config"`
		OllamaKeyConfig *struct {
			URL maskedValue `json:"url"`
		} `json:"ollama_key_config"`
	} `json:"keys"`
}

type bifrostRateLimit struct {
	TokenMaxLimit   *int64 `json:"token_max_limit"`
	RequestMaxLimit *int64 `json:"request_max_limit"`
}

type bifrostModelConfig struct {
	ID        string  `json:"id"`
	ModelName string  `json:"model_name"`
	Provider  *string `json:"provider"`
	Budgets   []struct {
		MaxLimit      float64 `json:"max_limit"`
		ResetDuration string  `json:"reset_duration"`
	} `json:"budgets"`
	RateLimit *bifrostRateLimit `json:"rate_limit"`
}

// fetchProvider returns the provider and whether it exists (404 => not found).
func (e MigrationRunConfig) fetchProvider(t *testing.T, name string) (providerResponse, bool) {
	t.Helper()
	body, status, err := e.BifrostClient.doRequest(context.Background(), http.MethodGet, "/api/providers/"+name, nil)
	if status == http.StatusNotFound {
		return providerResponse{}, false
	}
	if err != nil {
		t.Fatalf("get provider %q: %v", name, err)
	}
	var p providerResponse
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode provider %q: %v", name, err)
	}
	return p, true
}

func (e MigrationRunConfig) fetchProviderKeys(t *testing.T, name string) providerKeysResponse {
	t.Helper()
	body, err := e.BifrostClient.getJSON(context.Background(), "/api/providers/"+name+"/keys", "get provider keys "+name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	var out providerKeysResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode provider keys %q: %v (body: %s)", name, err, string(body))
	}
	return out
}

// fetchModelConfig returns the model config matching model+provider, if any.
func (e MigrationRunConfig) fetchModelConfig(t *testing.T, model, provider string) (bifrostModelConfig, bool) {
	t.Helper()
	body, err := e.BifrostClient.getJSON(context.Background(), "/api/governance/model-configs?search="+url.QueryEscape(model), "get model config "+model)
	if err != nil {
		t.Fatalf("%v", err)
	}
	var resp struct {
		ModelConfigs []bifrostModelConfig `json:"model_configs"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode model configs for %q: %v (body: %s)", model, err, string(body))
	}
	for _, mc := range resp.ModelConfigs {
		if mc.ModelName == model && providerEq(mc.Provider, provider) {
			return mc, true
		}
	}
	return bifrostModelConfig{}, false
}

// ---- Bifrost cleanup -------------------------------------------------------

func (e MigrationRunConfig) cleanupProvider(t *testing.T, name string) {
	if keepEntities() {
		return
	}
	t.Cleanup(func() {
		_, _, _ = e.BifrostClient.doRequest(context.Background(), http.MethodDelete, "/api/providers/"+name, nil)
	})
}

func (e MigrationRunConfig) cleanupStandardKey(t *testing.T, provider, keyName string) {
	if keepEntities() {
		return
	}
	t.Cleanup(func() {
		for _, k := range e.fetchProviderKeys(t, provider).Keys {
			if k.Name == keyName {
				_, _, _ = e.BifrostClient.doRequest(context.Background(), http.MethodDelete, "/api/providers/"+provider+"/keys/"+k.ID, nil)
			}
		}
	})
}

func (e MigrationRunConfig) cleanupModelConfig(t *testing.T, model string, provider *string) {
	if keepEntities() {
		return
	}
	want := ""
	if provider != nil {
		want = *provider
	}
	t.Cleanup(func() {
		if mc, ok := e.fetchModelConfig(t, model, want); ok {
			_, _, _ = e.BifrostClient.doRequest(context.Background(), http.MethodDelete, "/api/governance/model-configs/"+mc.ID, nil)
		}
	})
}

// ---- small helpers ---------------------------------------------------------

// nameSuffix returns a per-case unique suffix for seeded entity names.
func nameSuffix(name string) string {
	return "-" + name + "-" + time.Now().Format("150405.000000000")
}

func allowlistsOf(keys providerKeysResponse) [][]string {
	out := make([][]string, 0, len(keys.Keys))
	for _, k := range keys.Keys {
		out = append(out, k.Models)
	}
	return out
}

// normalizeLists sorts each allow-list and then the set of lists, so comparison
// ignores ordering on both axes.
func normalizeLists(lists [][]string) [][]string {
	out := make([][]string, 0, len(lists))
	for _, l := range lists {
		c := append([]string(nil), l...)
		sort.Strings(c)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.Join(out[i], ",") < strings.Join(out[j], ",")
	})
	return out
}

func containsList(haystack [][]string, needle []string) bool {
	for _, h := range haystack {
		if reflect.DeepEqual(h, needle) {
			return true
		}
	}
	return false
}

func providerEq(got *string, want string) bool {
	if want == "" {
		return got == nil
	}
	return got != nil && *got == want
}

func rateLimitField(rl *bifrostRateLimit, pick func(bifrostRateLimit) *int64) *int64 {
	if rl == nil {
		return nil
	}
	return pick(*rl)
}

func orAll(provider string) string {
	if provider == "" {
		return "*"
	}
	return provider
}
