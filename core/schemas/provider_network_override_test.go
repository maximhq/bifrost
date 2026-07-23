package schemas

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestApplyProviderNetworkConfigOverride_PartialOverrideKeepsDefaults(t *testing.T) {
	maxRetries := 2
	allowPrivateNetwork := true
	base := NetworkConfig{
		BaseURL:                        "https://api.example.com",
		ExtraHeaders:                   map[string]string{"x-static": "yes"},
		DefaultRequestTimeoutInSeconds: 30,
		MaxRetries:                     0,
		RetryBackoffInitial:            500 * time.Millisecond,
		RetryBackoffMax:                5 * time.Second,
		InsecureSkipVerify:             true,
		StreamIdleTimeoutInSeconds:     60,
		MaxConnsPerHost:                5000,
	}

	got := ApplyProviderNetworkConfigOverride(base, &ProviderNetworkConfigOverride{
		ExtraHeaders:        map[string]string{"x-tenant": "org1"},
		MaxRetries:          &maxRetries,
		AllowPrivateNetwork: &allowPrivateNetwork,
		BetaHeaderOverrides: map[string]bool{"redact-thinking-": false},
	})

	if got.DefaultRequestTimeoutInSeconds != base.DefaultRequestTimeoutInSeconds {
		t.Fatalf("DefaultRequestTimeoutInSeconds = %d, want base value %d (not request-overridable)", got.DefaultRequestTimeoutInSeconds, base.DefaultRequestTimeoutInSeconds)
	}
	if got.MaxRetries != maxRetries {
		t.Fatalf("MaxRetries = %d, want %d", got.MaxRetries, maxRetries)
	}
	if got.RetryBackoffInitial != base.RetryBackoffInitial || got.RetryBackoffMax != base.RetryBackoffMax {
		t.Fatalf("backoff defaults changed: got %s/%s want %s/%s", got.RetryBackoffInitial, got.RetryBackoffMax, base.RetryBackoffInitial, base.RetryBackoffMax)
	}
	if !got.InsecureSkipVerify || got.MaxConnsPerHost != base.MaxConnsPerHost || got.BaseURL != base.BaseURL {
		t.Fatalf("non-overridden fields changed: got %+v want base-derived %+v", got, base)
	}
	if !got.AllowPrivateNetwork {
		t.Fatalf("AllowPrivateNetwork = false, want true")
	}
	if got.ExtraHeaders["x-static"] != "yes" || got.ExtraHeaders["x-tenant"] != "org1" {
		t.Fatalf("ExtraHeaders = %+v, want merged static and tenant headers", got.ExtraHeaders)
	}
	if v, ok := got.BetaHeaderOverrides["redact-thinking-"]; !ok || v != false {
		t.Fatalf("BetaHeaderOverrides[\"redact-thinking-\"] = %v, want false", got.BetaHeaderOverrides["redact-thinking-"])
	}
}

// TestApplyProviderNetworkConfigOverride_BetaHeaderOverridesMerge verifies the
// three important cases for BetaHeaderOverrides merging in ApplyProviderNetworkConfigOverride.
func TestApplyProviderNetworkConfigOverride_BetaHeaderOverridesMerge(t *testing.T) {
	t.Run("nil override leaves base intact", func(t *testing.T) {
		base := NetworkConfig{BetaHeaderOverrides: map[string]bool{"tool-examples-2025-10-29": true}}
		got := ApplyProviderNetworkConfigOverride(base, &ProviderNetworkConfigOverride{})
		if got.BetaHeaderOverrides["tool-examples-2025-10-29"] != true {
			t.Fatalf("BetaHeaderOverrides changed when override was nil: %v", got.BetaHeaderOverrides)
		}
	})

	t.Run("override key wins over base", func(t *testing.T) {
		base := NetworkConfig{BetaHeaderOverrides: map[string]bool{"redact-thinking-": true}}
		got := ApplyProviderNetworkConfigOverride(base, &ProviderNetworkConfigOverride{
			BetaHeaderOverrides: map[string]bool{"redact-thinking-": false},
		})
		if got.BetaHeaderOverrides["redact-thinking-"] != false {
			t.Fatalf("override key did not win: got true, want false")
		}
	})

	t.Run("base key absent from override is preserved", func(t *testing.T) {
		base := NetworkConfig{BetaHeaderOverrides: map[string]bool{"tool-examples-2025-10-29": true}}
		got := ApplyProviderNetworkConfigOverride(base, &ProviderNetworkConfigOverride{
			BetaHeaderOverrides: map[string]bool{"redact-thinking-": false},
		})
		if got.BetaHeaderOverrides["tool-examples-2025-10-29"] != true {
			t.Fatalf("base-only key was dropped after merge: %v", got.BetaHeaderOverrides)
		}
		if got.BetaHeaderOverrides["redact-thinking-"] != false {
			t.Fatalf("override key missing after merge: %v", got.BetaHeaderOverrides)
		}
	})

	t.Run("nil base gets override keys", func(t *testing.T) {
		base := NetworkConfig{}
		got := ApplyProviderNetworkConfigOverride(base, &ProviderNetworkConfigOverride{
			BetaHeaderOverrides: map[string]bool{"redact-thinking-": true},
		})
		if got.BetaHeaderOverrides["redact-thinking-"] != true {
			t.Fatalf("override not applied onto nil base: %v", got.BetaHeaderOverrides)
		}
	})

	t.Run("merge does not mutate base map", func(t *testing.T) {
		baseMap := map[string]bool{"tool-examples-2025-10-29": true}
		base := NetworkConfig{BetaHeaderOverrides: baseMap}
		ApplyProviderNetworkConfigOverride(base, &ProviderNetworkConfigOverride{
			BetaHeaderOverrides: map[string]bool{"redact-thinking-": false},
		})
		if _, ok := baseMap["redact-thinking-"]; ok {
			t.Fatal("ApplyProviderNetworkConfigOverride mutated the caller's base BetaHeaderOverrides map")
		}
	})
}

func TestBifrostRequestClone_ProviderNetworkConfigOverrideHeadersAreIndependent(t *testing.T) {
	req := &BifrostRequest{
		ChatRequest: &BifrostChatRequest{Provider: OpenAI, Model: "gpt-4o"},
		ProviderOverride: &ProviderOverride{
			NetworkConfig: &ProviderNetworkConfigOverride{
				ExtraHeaders: map[string]string{"x-tenant": "org1"},
			},
		},
	}

	clone := req.Clone()
	clone.ProviderOverride.NetworkConfig.ExtraHeaders["x-tenant"] = "org2"

	if req.ProviderOverride.NetworkConfig.ExtraHeaders["x-tenant"] != "org1" {
		t.Fatalf("original ProviderOverride.NetworkConfig.ExtraHeaders was mutated: %+v", req.ProviderOverride.NetworkConfig.ExtraHeaders)
	}
}

func TestApplyProviderNetworkConfigOverride_CanDisablePrivateNetwork(t *testing.T) {
	allowPrivateNetwork := false
	base := NetworkConfig{
		AllowPrivateNetwork: true,
	}

	got := ApplyProviderNetworkConfigOverride(base, &ProviderNetworkConfigOverride{
		AllowPrivateNetwork: &allowPrivateNetwork,
	})

	if got.AllowPrivateNetwork {
		t.Fatalf("AllowPrivateNetwork = true, want false")
	}
}

// TestBifrostRequestClone_AllRequestTypesAreIndependentlyCovered reflects over
// every *Request pointer field on BifrostRequest and pins three exhaustiveness
// contracts at once, so adding a new request type upstream without updating the
// fork's switches fails loudly instead of silently aliasing:
//   - Clone must produce a distinct inner-request pointer (missing Clone case →
//     the shallow struct copy shares the pointer).
//   - SetProvider must reach the inner Provider field when one exists (missing
//     SetProvider case → the write is silently dropped).
//   - SetModel must reach the inner Model field when one exists.
func TestBifrostRequestClone_AllRequestTypesAreIndependentlyCovered(t *testing.T) {
	reqType := reflect.TypeOf(BifrostRequest{})
	covered := 0

	for i := 0; i < reqType.NumField(); i++ {
		field := reqType.Field(i)
		if !strings.HasSuffix(field.Name, "Request") || field.Type.Kind() != reflect.Pointer || field.Type.Elem().Kind() != reflect.Struct {
			continue
		}
		covered++

		t.Run(field.Name, func(t *testing.T) {
			requestValue := reflect.New(field.Type.Elem())
			providerField := requestValue.Elem().FieldByName("Provider")
			hasProvider := providerField.IsValid() && providerField.CanSet() && providerField.Type() == reflect.TypeOf(ModelProvider(""))
			if hasProvider {
				providerField.Set(reflect.ValueOf(Gemini))
			}
			modelField := requestValue.Elem().FieldByName("Model")
			hasModel := modelField.IsValid() && modelField.CanSet() && modelField.Kind() == reflect.String

			req := &BifrostRequest{}
			reflect.ValueOf(req).Elem().FieldByName(field.Name).Set(requestValue)

			clone := req.Clone()
			cloneValue := reflect.ValueOf(&clone).Elem().FieldByName(field.Name)
			if cloneValue.IsNil() {
				t.Fatalf("clone field %s is nil", field.Name)
			}
			if cloneValue.Pointer() == requestValue.Pointer() {
				t.Fatalf("clone field %s shares the original inner request pointer; update BifrostRequest.Clone", field.Name)
			}

			if hasProvider {
				clone.SetProvider(Vertex)
				if got := cloneValue.Elem().FieldByName("Provider").Interface().(ModelProvider); got != Vertex {
					t.Fatalf("SetProvider did not reach %s.Provider (got %q); update BifrostRequest.SetProvider", field.Name, got)
				}
				if got := providerField.Interface().(ModelProvider); got != Gemini {
					t.Fatalf("SetProvider on clone mutated the original %s.Provider (got %q)", field.Name, got)
				}
			}
			if hasModel {
				clone.SetModel("model-sentinel")
				if got := cloneValue.Elem().FieldByName("Model").String(); got != "model-sentinel" {
					t.Fatalf("SetModel did not reach %s.Model (got %q); update BifrostRequest.SetModel", field.Name, got)
				}
				if got := modelField.String(); got != "" {
					t.Fatalf("SetModel on clone mutated the original %s.Model (got %q)", field.Name, got)
				}
			}
		})
	}

	// 45 request-type fields existed when this test was generalized; the floor
	// only guards against the reflection filter silently matching nothing.
	if covered < 40 {
		t.Fatalf("only %d *Request fields matched; reflection filter is broken", covered)
	}
}

// TestNetworkConfig_AllFieldsAccountedForInOverrideOrExclusionList enforces that
// every NetworkConfig field is either per-request-overridable (present in
// ProviderNetworkConfigOverride) or explicitly listed as construction-time-only
// (cannot change without rebuilding the HTTP client). Adding a field to
// NetworkConfig without updating ProviderNetworkConfigOverride or the exclusion
// list below fails this test — the author must consciously classify the new field.
func TestNetworkConfig_AllFieldsAccountedForInOverrideOrExclusionList(t *testing.T) {
	// constructionTimeOnly: NetworkConfig fields baked into the fasthttp.Client
	// (or TLS config) at NewProvider time. They cannot be overridden per-request
	// without rebuilding the client. The value is the reason — purely
	// documentation, but required: a blank reason is a sign the reviewer skipped
	// the classification.
	//
	// To add a new per-request-overridable field instead, add it to
	// ProviderNetworkConfigOverride + ApplyProviderNetworkConfigOverride.
	constructionTimeOnly := map[string]string{
		"base_url":                           "HTTP client BaseURL baked in at construction",
		"default_request_timeout_in_seconds": "transport ReadTimeout set at construction; per-request variant is ProviderNetworkConfigOverride.RequestTimeoutInSeconds",
		"insecure_skip_verify":               "TLS config applied at construction via ConfigureTLS",
		"ca_cert_pem":                        "TLS CA cert applied at construction via ConfigureTLS",
		"max_conns_per_host":                 "connection pool limit set at construction",
		"enforce_http2":                      "HTTP/2 protocol selection set at construction",
		"keep_alive_timeout_in_seconds":      "idle-connection keep-alive baked into the shared client's MaxIdleConnDuration at construction; pooled connections are shared across requests so a per-request value cannot apply",
	}

	// Sanity: every exclusion entry must have a non-empty reason.
	for field, reason := range constructionTimeOnly {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("constructionTimeOnly[%q] has an empty reason; document why it cannot be per-request-overridable", field)
		}
	}

	overrideFields := make(map[string]bool)
	overrideType := reflect.TypeOf(ProviderNetworkConfigOverride{})
	for i := 0; i < overrideType.NumField(); i++ {
		tag := overrideType.Field(i).Tag.Get("json")
		name := strings.SplitN(tag, ",", 2)[0]
		if name != "" && name != "-" {
			overrideFields[name] = true
		}
	}

	ncType := reflect.TypeOf(NetworkConfig{})
	for i := 0; i < ncType.NumField(); i++ {
		tag := ncType.Field(i).Tag.Get("json")
		name := strings.SplitN(tag, ",", 2)[0]
		if name == "" || name == "-" {
			continue
		}
		_, inOverride := overrideFields[name]
		_, inExclusion := constructionTimeOnly[name]
		if !inOverride && !inExclusion {
			t.Errorf("NetworkConfig field %q is unaccounted for.\n"+
				"Either:\n"+
				"  (a) add it to ProviderNetworkConfigOverride + ApplyProviderNetworkConfigOverride if it can be overridden per-request, or\n"+
				"  (b) add it to constructionTimeOnly in this test with a reason if it is baked into the HTTP client at construction.", name)
		}
	}
}

// TestBifrostRequestClone_ParamsAreIndependentlyCopied is a regression test for
// the MCP tool-injection write-through: AddToolsToRequest assigns
// req.ChatRequest.Params.Tools (and the ResponsesRequest equivalent) through the
// Params pointer on every attempt. If Clone shared the Params pointer, a fallback
// attempt's tool injection would mutate the caller-owned original request.
func TestBifrostRequestClone_ParamsAreIndependentlyCopied(t *testing.T) {
	t.Run("ChatRequest", func(t *testing.T) {
		original := &BifrostRequest{
			ChatRequest: &BifrostChatRequest{Provider: OpenAI, Model: "gpt-4o", Params: &ChatParameters{}},
		}
		clone := original.Clone()
		if clone.ChatRequest.Params == original.ChatRequest.Params {
			t.Fatal("Clone shared the ChatRequest.Params pointer with the original")
		}
		clone.ChatRequest.Params.Tools = []ChatTool{{}}
		if len(original.ChatRequest.Params.Tools) != 0 {
			t.Fatal("assigning Tools on the clone's Params mutated the original request")
		}
	})

	t.Run("ResponsesRequest", func(t *testing.T) {
		original := &BifrostRequest{
			ResponsesRequest: &BifrostResponsesRequest{Provider: OpenAI, Model: "gpt-4o", Params: &ResponsesParameters{}},
		}
		clone := original.Clone()
		if clone.ResponsesRequest.Params == original.ResponsesRequest.Params {
			t.Fatal("Clone shared the ResponsesRequest.Params pointer with the original")
		}
		clone.ResponsesRequest.Params.Tools = []ResponsesTool{{}}
		if len(original.ResponsesRequest.Params.Tools) != 0 {
			t.Fatal("assigning Tools on the clone's Params mutated the original request")
		}
	})

	t.Run("TextCompletionRequest", func(t *testing.T) {
		original := &BifrostRequest{
			TextCompletionRequest: &BifrostTextCompletionRequest{Provider: OpenAI, Model: "gpt-3.5-turbo-instruct", Params: &TextCompletionParameters{}},
		}
		clone := original.Clone()
		if clone.TextCompletionRequest.Params == original.TextCompletionRequest.Params {
			t.Fatal("Clone shared the TextCompletionRequest.Params pointer with the original")
		}
		clone.TextCompletionRequest.Params.MaxTokens = Ptr(42)
		if original.TextCompletionRequest.Params.MaxTokens != nil {
			t.Fatal("setting MaxTokens on the clone's Params mutated the original request")
		}
	})
}
