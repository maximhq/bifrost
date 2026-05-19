package schemas

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestApplyProviderNetworkConfigOverride_PartialOverrideKeepsDefaults(t *testing.T) {
	timeoutSeconds := 300
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
		ExtraHeaders:                   map[string]string{"x-tenant": "org1"},
		DefaultRequestTimeoutInSeconds: &timeoutSeconds,
		MaxRetries:                     &maxRetries,
	})

	if got.DefaultRequestTimeoutInSeconds != timeoutSeconds {
		t.Fatalf("DefaultRequestTimeoutInSeconds = %d, want %d", got.DefaultRequestTimeoutInSeconds, timeoutSeconds)
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

func TestBifrostRequestClone_CachedContentRequestsAreIndependentlyCovered(t *testing.T) {
	reqType := reflect.TypeOf(BifrostRequest{})
	covered := 0

	for i := 0; i < reqType.NumField(); i++ {
		field := reqType.Field(i)
		if !strings.HasPrefix(field.Name, "CachedContent") || !strings.HasSuffix(field.Name, "Request") {
			continue
		}
		if field.Type.Kind() != reflect.Pointer || field.Type.Elem().Kind() != reflect.Struct {
			t.Fatalf("%s has unexpected type %s", field.Name, field.Type)
		}
		covered++

		t.Run(field.Name, func(t *testing.T) {
			requestValue := reflect.New(field.Type.Elem())
			if providerField := requestValue.Elem().FieldByName("Provider"); providerField.IsValid() && providerField.CanSet() {
				providerField.Set(reflect.ValueOf(Gemini))
			}

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

			clone.SetProvider(Vertex)
			gotProvider, _, _ := req.GetRequestFields()
			if gotProvider != Gemini {
				t.Fatalf("original provider = %s, want %s", gotProvider, Gemini)
			}
		})
	}

	if covered == 0 {
		t.Fatal("expected at least one CachedContent*Request field on BifrostRequest")
	}
}
