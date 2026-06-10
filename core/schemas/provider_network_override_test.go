package schemas

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestApplyProviderNetworkConfigOverride_PartialOverrideKeepsDefaults(t *testing.T) {
	maxRetries := 2
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
		ExtraHeaders: map[string]string{"x-tenant": "org1"},
		MaxRetries:   &maxRetries,
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
	if got.ExtraHeaders["x-static"] != "yes" || got.ExtraHeaders["x-tenant"] != "org1" {
		t.Fatalf("ExtraHeaders = %+v, want merged static and tenant headers", got.ExtraHeaders)
	}
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
