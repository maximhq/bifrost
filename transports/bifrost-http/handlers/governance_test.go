package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/valyala/fasthttp"
)

// mockGovernanceManagerForVK embeds the interface so unimplemented methods panic.
// Only GetGovernanceData is needed for the getVirtualKeys handler path.
type mockGovernanceManagerForVK struct {
	GovernanceManager
}

func (m *mockGovernanceManagerForVK) GetGovernanceData() *governance.GovernanceData {
	return nil
}

// mockConfigStoreForVK embeds the interface so unimplemented methods panic.
// Only GetVirtualKeysPaginated is called in the non-from_memory path.
type mockConfigStoreForVK struct {
	configstore.ConfigStore
}

func (m *mockConfigStoreForVK) GetVirtualKeysPaginated(_ context.Context, _ configstore.VirtualKeyQueryParams) ([]configstoreTables.TableVirtualKey, int64, error) {
	return nil, 0, nil
}

func (m *mockConfigStoreForVK) GetVirtualKeys(_ context.Context) ([]configstoreTables.TableVirtualKey, error) {
	return nil, nil
}

func (m *mockConfigStoreForVK) GetAllPromptVersions(_ context.Context) ([]configstoreTables.TablePromptVersion, error) {
	return nil, nil
}

// TestGetVirtualKeys_PaginatedEndpoint_ResponseShape verifies the JSON response
// from the paginated virtual keys endpoint contains all expected fields.
func TestGetVirtualKeys_PaginatedEndpoint_ResponseShape(t *testing.T) {
	SetLogger(&mockLogger{})

	h := &GovernanceHandler{
		configStore:       &mockConfigStoreForVK{},
		governanceManager: &mockGovernanceManagerForVK{},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/governance/virtual-keys?limit=10&offset=0")

	h.getVirtualKeys(ctx)

	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("expected status 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to parse JSON response: %v", err)
	}

	// Assert expected fields exist with correct types
	requiredFields := []struct {
		key      string
		wantType string
	}{
		{"virtual_keys", "array"},
		{"total_count", "number"},
		{"count", "number"},
		{"limit", "number"},
		{"offset", "number"},
	}

	for _, f := range requiredFields {
		val, ok := resp[f.key]
		if !ok {
			t.Errorf("response missing required field %q", f.key)
			continue
		}
		switch f.wantType {
		case "array":
			if _, ok := val.([]interface{}); !ok {
				// nil decodes as nil, which is fine — JSON null for empty array
				if val != nil {
					t.Errorf("field %q: expected array, got %T", f.key, val)
				}
			}
		case "number":
			if _, ok := val.(float64); !ok {
				t.Errorf("field %q: expected number, got %T", f.key, val)
			}
		}
	}

	// Verify no unexpected extra top-level fields
	allowedKeys := map[string]bool{
		"virtual_keys": true,
		"total_count":  true,
		"count":        true,
		"limit":        true,
		"offset":       true,
	}
	for key := range resp {
		if !allowedKeys[key] {
			t.Errorf("unexpected field %q in response", key)
		}
	}
}

// TestGetVirtualKeys_PaginatedEndpoint_QueryParams verifies query parameters are
// parsed and reflected in the response.
func TestGetVirtualKeys_PaginatedEndpoint_QueryParams(t *testing.T) {
	SetLogger(&mockLogger{})

	h := &GovernanceHandler{
		configStore:       &mockConfigStoreForVK{},
		governanceManager: &mockGovernanceManagerForVK{},
	}

	tests := []struct {
		name       string
		uri        string
		wantLimit  float64
		wantOffset float64
	}{
		{
			name:       "explicit limit and offset",
			uri:        "/api/governance/virtual-keys?limit=10&offset=5",
			wantLimit:  10,
			wantOffset: 5,
		},
		{
			name:       "no params uses defaults",
			uri:        "/api/governance/virtual-keys",
			wantLimit:  0,
			wantOffset: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetMethod("GET")
			ctx.Request.SetRequestURI(tt.uri)

			h.getVirtualKeys(ctx)

			if ctx.Response.StatusCode() != 200 {
				t.Fatalf("expected status 200, got %d", ctx.Response.StatusCode())
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
				t.Fatalf("failed to parse JSON: %v", err)
			}

			if got := resp["limit"].(float64); got != tt.wantLimit {
				t.Errorf("limit: got %v, want %v", got, tt.wantLimit)
			}
			if got := resp["offset"].(float64); got != tt.wantOffset {
				t.Errorf("offset: got %v, want %v", got, tt.wantOffset)
			}
		})
	}
}

// Ensure mockLogger satisfies schemas.Logger (already defined in middlewares_test.go
// but we reference it here — same package, so no redeclaration needed).
var _ schemas.Logger = (*mockLogger)(nil)

func TestBudgetRemovalRequestDetection(t *testing.T) {
	tests := []struct {
		name string
		req  *UpdateBudgetRequest
		want bool
	}{
		{
			name: "nil request is not removal",
			req:  nil,
			want: false,
		},
		{
			name: "empty object is removal",
			req:  &UpdateBudgetRequest{},
			want: true,
		},
		{
			name: "max limit present is not removal",
			req:  &UpdateBudgetRequest{MaxLimit: bifrostFloat(10)},
			want: false,
		},
		{
			name: "reset duration only is not removal",
			req:  &UpdateBudgetRequest{ResetDuration: bifrostString("1h")},
			want: false,
		},
		{
			name: "calendar aligned only is treated as removal",
			req:  &UpdateBudgetRequest{CalendarAligned: bifrostBool(true)},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBudgetRemovalRequest(tt.req); got != tt.want {
				t.Fatalf("isBudgetRemovalRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRateLimitRemovalRequestDetection(t *testing.T) {
	tests := []struct {
		name string
		req  *UpdateRateLimitRequest
		want bool
	}{
		{
			name: "nil request is not removal",
			req:  nil,
			want: false,
		},
		{
			name: "empty object is removal",
			req:  &UpdateRateLimitRequest{},
			want: true,
		},
		{
			name: "token limit present is not removal",
			req:  &UpdateRateLimitRequest{TokenMaxLimit: bifrostInt64(100)},
			want: false,
		},
		{
			name: "request limit present is not removal",
			req:  &UpdateRateLimitRequest{RequestMaxLimit: bifrostInt64(10)},
			want: false,
		},
		{
			name: "durations only is not removal",
			req: &UpdateRateLimitRequest{
				TokenResetDuration:   bifrostString("1h"),
				RequestResetDuration: bifrostString("1h"),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRateLimitRemovalRequest(tt.req); got != tt.want {
				t.Fatalf("isRateLimitRemovalRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCollectProviderConfigDeleteIDs(t *testing.T) {
	budgetID := "budget-1"
	rateLimitID := "rate-limit-1"

	tests := []struct {
		name             string
		config           configstoreTables.TableVirtualKeyProviderConfig
		initialBudgetIDs []string
		initialRateIDs   []string
		wantBudgetIDs    []string
		wantRateIDs      []string
	}{
		{
			name: "collects both IDs",
			config: configstoreTables.TableVirtualKeyProviderConfig{
				BudgetID:    &budgetID,
				RateLimitID: &rateLimitID,
			},
			wantBudgetIDs: []string{budgetID},
			wantRateIDs:   []string{rateLimitID},
		},
		{
			name: "appends to existing slices",
			config: configstoreTables.TableVirtualKeyProviderConfig{
				BudgetID:    &budgetID,
				RateLimitID: &rateLimitID,
			},
			initialBudgetIDs: []string{"budget-0"},
			initialRateIDs:   []string{"rate-limit-0"},
			wantBudgetIDs:    []string{"budget-0", budgetID},
			wantRateIDs:      []string{"rate-limit-0", rateLimitID},
		},
		{
			name:   "ignores missing IDs",
			config: configstoreTables.TableVirtualKeyProviderConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBudgetIDs, gotRateIDs := collectProviderConfigDeleteIDs(tt.config, tt.initialBudgetIDs, tt.initialRateIDs)

			if len(gotBudgetIDs) != len(tt.wantBudgetIDs) {
				t.Fatalf("budget IDs length = %d, want %d", len(gotBudgetIDs), len(tt.wantBudgetIDs))
			}
			for i := range gotBudgetIDs {
				if gotBudgetIDs[i] != tt.wantBudgetIDs[i] {
					t.Fatalf("budget IDs[%d] = %q, want %q", i, gotBudgetIDs[i], tt.wantBudgetIDs[i])
				}
			}

			if len(gotRateIDs) != len(tt.wantRateIDs) {
				t.Fatalf("rate limit IDs length = %d, want %d", len(gotRateIDs), len(tt.wantRateIDs))
			}
			for i := range gotRateIDs {
				if gotRateIDs[i] != tt.wantRateIDs[i] {
					t.Fatalf("rate limit IDs[%d] = %q, want %q", i, gotRateIDs[i], tt.wantRateIDs[i])
				}
			}
		})
	}
}

func bifrostFloat(v float64) *float64 {
	return &v
}

func bifrostInt64(v int64) *int64 {
	return &v
}

func bifrostString(v string) *string {
	return &v
}

func bifrostBool(v bool) *bool {
	return &v
}
