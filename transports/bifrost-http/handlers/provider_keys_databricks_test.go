package handlers

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// Simulates the real UI round trip: stored key -> redacted GET -> client echoes it
// back on update -> merge -> validate.
func TestDatabricksKey_RedactedUpdateRoundTrip(t *testing.T) {
	h := &ProviderHandler{}
	sv := func(v string) schemas.SecretVar { return *schemas.NewSecretVar(v) }
	svp := func(v string) *schemas.SecretVar { return schemas.NewSecretVar(v) }

	stored := schemas.Key{
		ID: "k1", Name: "dbx", Weight: 1,
		Value: sv(""),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL:       sv("https://dbc-1.cloud.databricks.com"),
			APIFormat:          schemas.DatabricksAPIFormatAIGateway,
			ClientID:           svp("dbx-client-id-value"),
			ClientSecret:       svp("dbx-client-secret-value"),
			ForwardGatewayTags: true,
		},
	}

	pc := &configstore.ProviderConfig{Keys: []schemas.Key{stored}}
	redacted := pc.Redacted().Keys[0]

	if redacted.DatabricksKeyConfig == nil {
		t.Fatal("GET dropped databricks_key_config entirely")
	}
	t.Logf("redacted workspace_url=%q api_format=%q tags=%v client_id=%q",
		redacted.DatabricksKeyConfig.WorkspaceURL.GetValue(),
		redacted.DatabricksKeyConfig.APIFormat,
		redacted.DatabricksKeyConfig.ForwardGatewayTags,
		redacted.DatabricksKeyConfig.ClientID.GetValue())

	merged, err := h.mergeUpdatedKey(stored, redacted)
	if err != nil {
		t.Fatalf("merge of an untouched redacted key failed: %v", err)
	}
	cfg := merged.DatabricksKeyConfig
	if cfg == nil {
		t.Fatal("merge dropped the config")
	}
	if got := cfg.WorkspaceURL.GetValue(); got != "https://dbc-1.cloud.databricks.com" {
		t.Errorf("workspace_url: got %q", got)
	}
	if got := cfg.ClientID.GetValue(); got != "dbx-client-id-value" {
		t.Errorf("client_id: got %q", got)
	}
	if got := cfg.ClientSecret.GetValue(); got != "dbx-client-secret-value" {
		t.Errorf("client_secret: got %q", got)
	}
	if cfg.APIFormat != schemas.DatabricksAPIFormatAIGateway {
		t.Errorf("api_format: got %q", cfg.APIFormat)
	}
	if !cfg.ForwardGatewayTags {
		t.Error("forward_gateway_tags lost on round trip")
	}
	if err := validateProviderKeyURL(schemas.Databricks, merged); err != nil {
		t.Errorf("round-tripped key failed validation: %v", err)
	}
}

func TestDatabricksKey_Validate(t *testing.T) {
	sv := func(v string) schemas.SecretVar { return *schemas.NewSecretVar(v) }
	svp := func(v string) *schemas.SecretVar { return schemas.NewSecretVar(v) }
	url := sv("https://dbc-1.cloud.databricks.com")

	cases := []struct {
		name    string
		key     schemas.Key
		wantErr string
	}{
		{"pat ok", schemas.Key{Value: sv("dapi"), DatabricksKeyConfig: &schemas.DatabricksKeyConfig{WorkspaceURL: url}}, ""},
		{"oauth ok", schemas.Key{DatabricksKeyConfig: &schemas.DatabricksKeyConfig{WorkspaceURL: url, ClientID: svp("a"), ClientSecret: svp("b")}}, ""},
		{"nil config", schemas.Key{Value: sv("dapi")}, "workspace_url"},
		{"no workspace url", schemas.Key{Value: sv("dapi"), DatabricksKeyConfig: &schemas.DatabricksKeyConfig{}}, "workspace_url"},
		{"no auth at all", schemas.Key{DatabricksKeyConfig: &schemas.DatabricksKeyConfig{WorkspaceURL: url}}, "either a personal access token"},
		{"half a service principal", schemas.Key{DatabricksKeyConfig: &schemas.DatabricksKeyConfig{WorkspaceURL: url, ClientID: svp("a")}}, "either a personal access token"},
		{"half a pair alongside a pat", schemas.Key{Value: sv("dapi"), DatabricksKeyConfig: &schemas.DatabricksKeyConfig{WorkspaceURL: url, ClientID: svp("a")}}, "must be set together"},
		{"bad api_format", schemas.Key{Value: sv("dapi"), DatabricksKeyConfig: &schemas.DatabricksKeyConfig{WorkspaceURL: url, APIFormat: "nope"}}, "api_format"},
		{"env-ref workspace url", schemas.Key{Value: sv("dapi"), DatabricksKeyConfig: &schemas.DatabricksKeyConfig{WorkspaceURL: sv("env.DBX_URL")}}, ""},
		{"env-ref pat", schemas.Key{Value: sv("env.DBX_TOKEN"), DatabricksKeyConfig: &schemas.DatabricksKeyConfig{WorkspaceURL: url}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProviderKeyURL(schemas.Databricks, tc.key)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// Switching an OAuth key to a personal access token drops the service principal
// from the payload; the merge must not resurrect it.
func TestDatabricksKey_SwitchToPAT(t *testing.T) {
	h := &ProviderHandler{}
	sv := func(v string) schemas.SecretVar { return *schemas.NewSecretVar(v) }
	svp := func(v string) *schemas.SecretVar { return schemas.NewSecretVar(v) }

	stored := schemas.Key{
		Value: sv(""),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: sv("https://dbc-1.cloud.databricks.com"),
			ClientID:     svp("id"), ClientSecret: svp("secret"),
		},
	}
	update := schemas.Key{
		Value: sv("dapi-new-token"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: sv("https://dbc-1.cloud.databricks.com"),
		},
	}
	merged, err := h.mergeUpdatedKey(stored, update)
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if merged.DatabricksKeyConfig.ClientID != nil || merged.DatabricksKeyConfig.ClientSecret != nil {
		t.Error("service principal survived the switch to a personal access token")
	}
	if merged.Value.GetValue() != "dapi-new-token" {
		t.Errorf("token not applied: %q", merged.Value.GetValue())
	}
	if err := validateProviderKeyURL(schemas.Databricks, merged); err != nil {
		t.Errorf("validation after switch: %v", err)
	}
}
