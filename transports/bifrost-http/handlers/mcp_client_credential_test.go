package handlers

import (
	"reflect"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

func TestParseOauthScopesJSON(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty string", raw: "", want: nil},
		{name: "whitespace only", raw: "   ", want: nil},
		{name: "json null (provider omitted scope)", raw: "null", want: nil},
		{name: "empty array", raw: "[]", want: nil},
		{name: "malformed", raw: "[repo", want: nil},
		{name: "wrong shape", raw: `{"scope":"repo"}`, want: nil},
		{name: "list", raw: `["repo","read:org"]`, want: []string{"repo", "read:org"}},
		{name: "drops blank entries", raw: `["repo",""," "]`, want: []string{"repo"}},
		{name: "trims entries", raw: `[" openid "]`, want: []string{"openid"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseOauthScopesJSON(tc.raw); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseOauthScopesJSON(%q) = %#v, want %#v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestTokenRow_TokenOnlyFields(t *testing.T) {
	row := tokenRow(configstoreTables.TableMCPOauthToken{ID: "t1", AuthMode: "user", Scopes: `["repo","read:org"]`, RefreshToken: "r"})
	if want := []string{"repo", "read:org"}; !reflect.DeepEqual(row.Scopes, want) {
		t.Fatalf("scopes = %#v, want %#v", row.Scopes, want)
	}
	if row.HasRefreshToken == nil || !*row.HasRefreshToken {
		t.Fatalf("has_refresh_token = %v, want true", row.HasRefreshToken)
	}
	empty := tokenRow(configstoreTables.TableMCPOauthToken{ID: "t2", AuthMode: "vk", Scopes: "null"})
	if empty.Scopes != nil {
		t.Fatalf("scopes for a null column = %#v, want nil so the wire field is omitted", empty.Scopes)
	}
	// False, not omitted: a token row always answers the question, only
	// header and flow rows leave it out.
	if empty.HasRefreshToken == nil || *empty.HasRefreshToken {
		t.Fatalf("has_refresh_token = %v, want false", empty.HasRefreshToken)
	}
	if flow := flowRow(configstoreTables.TableMCPOauthFlow{ID: "f1", FlowMode: "user"}); flow.HasRefreshToken != nil || flow.Scopes != nil {
		t.Fatalf("flow rows must omit token-only fields, got scopes=%#v has_refresh_token=%v", flow.Scopes, flow.HasRefreshToken)
	}
}

// TestMCPClientCredentialResponse checks that the sheet's credential block is
// projected from the same row projectMCPCredentialState reads for each auth
// type, that secret material is reduced to presence or names, and that a
// missing row or an auth type without a self-held credential yields no block.
func TestMCPClientCredentialResponse(t *testing.T) {
	created := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	updated := created.Add(time.Hour)
	expires := updated.Add(30 * time.Minute)
	refreshed := updated

	adminToken := &configstoreTables.TableMCPOauthToken{
		ID: "admin", AuthMode: "admin", Status: "needs_reauth",
		StatusReason: "provider rejected the refresh (HTTP 400, invalid_grant: Token has been expired or revoked.)",
		AccessToken:  "secret", RefreshToken: "",
		Scopes: "null", CreatedAt: created, UpdatedAt: updated,
	}
	sharedToken := &configstoreTables.TableMCPOauthToken{
		ID: "shared", AuthMode: "shared", Status: "active",
		AccessToken: "secret", RefreshToken: "refresh-secret",
		ExpiresAt: &expires, LastRefreshedAt: &refreshed,
		Scopes: `["repo","read:org"]`, CreatedAt: created, UpdatedAt: updated,
	}
	adminCred := &configstoreTables.TableMCPPerUserHeaderCredential{
		ID: "cred", AuthMode: "admin", Status: "needs_update", CreatedAt: created, UpdatedAt: updated,
	}
	if err := adminCred.SetHeaders(map[string]string{"X-Tenant-ID": "acme", "X-API-Key": "k"}); err != nil {
		t.Fatal(err)
	}

	sharedWant := &MCPClientCredentialResponse{
		Kind: "oauth", Status: "active", HasRefreshToken: true,
		ExpiresAt:       ptrString("2026-08-12T11:30:00Z"),
		LastRefreshedAt: ptrString("2026-08-12T11:00:00Z"),
		Scopes:          []string{"repo", "read:org"},
		CreatedAt:       "2026-08-12T10:00:00Z", UpdatedAt: "2026-08-12T11:00:00Z",
	}
	adminWant := &MCPClientCredentialResponse{
		Kind: "oauth", Status: "needs_reauth", HasRefreshToken: false,
		StatusReason: "provider rejected the refresh (HTTP 400, invalid_grant: Token has been expired or revoked.)",
		CreatedAt:    "2026-08-12T10:00:00Z", UpdatedAt: "2026-08-12T11:00:00Z",
	}
	headersWant := &MCPClientCredentialResponse{
		Kind: "headers", Status: "needs_update",
		HeaderKeys: []string{"X-API-Key", "X-Tenant-ID"},
		CreatedAt:  "2026-08-12T10:00:00Z", UpdatedAt: "2026-08-12T11:00:00Z",
	}

	tests := []struct {
		name     string
		authType schemas.MCPAuthType
		want     *MCPClientCredentialResponse
	}{
		{name: "oauth reads the shared token, not the admin one", authType: schemas.MCPAuthTypeOauth, want: sharedWant},
		{name: "per_user_oauth reads the admin token, not the shared one", authType: schemas.MCPAuthTypePerUserOauth, want: adminWant},
		{name: "token_exchange reads the admin token", authType: schemas.MCPAuthTypeTokenExchange, want: adminWant},
		{name: "per_user_headers reads the admin header credential with sorted key names", authType: schemas.MCPAuthTypePerUserHeaders, want: headersWant},
		{name: "none has no self-held credential", authType: schemas.MCPAuthTypeNone, want: nil},
		{name: "headers has no self-held credential", authType: schemas.MCPAuthTypeHeaders, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mcpClientCredentialResponse(tc.authType, adminToken, adminCred, sharedToken)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}

	t.Run("missing rows yield no block", func(t *testing.T) {
		for _, at := range []schemas.MCPAuthType{schemas.MCPAuthTypeOauth, schemas.MCPAuthTypePerUserOauth, schemas.MCPAuthTypeTokenExchange, schemas.MCPAuthTypePerUserHeaders} {
			if got := mcpClientCredentialResponse(at, nil, nil, nil); got != nil {
				t.Fatalf("%s: got %+v, want nil", at, got)
			}
		}
	})

	t.Run("unreadable header values still project status and timestamps", func(t *testing.T) {
		broken := &configstoreTables.TableMCPPerUserHeaderCredential{Status: "active", HeadersJSON: "not-json", CreatedAt: created, UpdatedAt: updated}
		got := mcpClientCredentialResponse(schemas.MCPAuthTypePerUserHeaders, nil, broken, nil)
		if got == nil || got.Status != "active" || got.HeaderKeys != nil {
			t.Fatalf("got %+v, want active block with no header keys", got)
		}
	})
}

func ptrString(s string) *string { return &s }
