//go:build !tinygo && !wasm

package schemas

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestMCPAuthURLHasTempTokenFragment(t *testing.T) {
	tests := []struct {
		name    string
		authURL string
		want    bool
	}{
		{
			name:    "url with temp-token fragment",
			authURL: "https://host/workspace/mcp-sessions/auth?flow=abc123#t=xyz",
			want:    true,
		},
		{
			name:    "url with no fragment",
			authURL: "https://host/workspace/mcp-sessions/auth?flow=abc123",
			want:    false,
		},
		{
			name:    "url with a different fragment",
			authURL: "https://host/workspace/mcp-sessions/auth?flow=abc123#nope",
			want:    false,
		},
		{
			name:    "empty string",
			authURL: "",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MCPAuthURLHasTempTokenFragment(tt.authURL); got != tt.want {
				t.Errorf("MCPAuthURLHasTempTokenFragment(%q) = %v, want %v", tt.authURL, got, tt.want)
			}
		})
	}
}

func TestMCPCodeModeLimitsWithDefaults(t *testing.T) {
	got := MCPCodeModeLimits{MaxSteps: 5, MaxToolCalls: 500}.WithDefaults()
	want := MCPCodeModeLimits{
		MaxSourceBytes:  DefaultCodeModeMaxSourceBytes,
		MaxSteps:        5,
		MaxMemoryBytes:  DefaultCodeModeMaxMemoryBytes,
		MaxLogBytes:     DefaultCodeModeMaxLogBytes,
		MaxToolCalls:    500,
		MaxValueBytes:   DefaultCodeModeMaxValueBytes,
		MaxNestingDepth: DefaultCodeModeMaxNestingDepth,
	}
	if got != want {
		t.Fatalf("WithDefaults() = %+v, want %+v", got, want)
	}
}

func TestMCPCodeModeLimitsValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limits  MCPCodeModeLimits
		wantErr bool
	}{
		{name: "zero uses defaults", limits: MCPCodeModeLimits{}},
		{name: "large limits", limits: MCPCodeModeLimits{MaxSteps: 1 << 40, MaxMemoryBytes: 1 << 34, MaxToolCalls: 100000, MaxValueBytes: 1 << 30, MaxNestingDepth: MaxCodeModeNestingDepth}},
		{name: "negative steps", limits: MCPCodeModeLimits{MaxSteps: -1}, wantErr: true},
		{name: "negative source", limits: MCPCodeModeLimits{MaxSourceBytes: -1}, wantErr: true},
		{name: "negative memory", limits: MCPCodeModeLimits{MaxMemoryBytes: -1}, wantErr: true},
		{name: "negative logs", limits: MCPCodeModeLimits{MaxLogBytes: -1}, wantErr: true},
		{name: "negative tool calls", limits: MCPCodeModeLimits{MaxToolCalls: -1}, wantErr: true},
		{name: "negative value", limits: MCPCodeModeLimits{MaxValueBytes: -1}, wantErr: true},
		{name: "negative depth", limits: MCPCodeModeLimits{MaxNestingDepth: -1}, wantErr: true},
		{name: "value below minimum", limits: MCPCodeModeLimits{MaxValueBytes: MinCodeModeValueBytes - 1}, wantErr: true},
		{name: "depth above recursion bound", limits: MCPCodeModeLimits{MaxNestingDepth: MaxCodeModeNestingDepth + 1}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.limits.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestMCPConnectionTypeOpenAPI_OTelNetworkTransport(t *testing.T) {
	if got := MCPConnectionTypeOpenAPI.OTelNetworkTransport(); got != "tcp" {
		t.Fatalf("openapi tools reach their upstream over HTTP; OTelNetworkTransport() = %q, want tcp", got)
	}
	if got := MCPConnectionTypeInProcess.OTelNetworkTransport(); got != "" {
		t.Fatalf("inprocess must stay attribute-less, got %q", got)
	}
}

func TestMCPOpenAPIConfig_HasSpecSource(t *testing.T) {
	str := func(s string) *string { return &s }
	for _, tc := range []struct {
		name string
		cfg  *MCPOpenAPIConfig
		want bool
	}{
		{name: "nil", cfg: nil},
		{name: "empty", cfg: &MCPOpenAPIConfig{}},
		{name: "whitespace only", cfg: &MCPOpenAPIConfig{Spec: "  \n", SpecURL: str(" "), SpecFile: str("")}},
		{name: "inline", cfg: &MCPOpenAPIConfig{Spec: "openapi: 3.0.0"}, want: true},
		{name: "url", cfg: &MCPOpenAPIConfig{SpecURL: str("https://example.com/openapi.json")}, want: true},
		{name: "file", cfg: &MCPOpenAPIConfig{SpecFile: str("specs/petstore.yaml")}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.HasSpecSource(); got != tc.want {
				t.Fatalf("HasSpecSource() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMCPClientConfig_OpenAPIConfigRoundTrips pins the wire shape of openapi_config
// through MCPClientConfig's custom (Un)MarshalJSON: nested credentials keep their
// SecretVar form and the server-computed metadata survives unchanged.
func TestMCPClientConfig_OpenAPIConfigRoundTrips(t *testing.T) {
	in := `{
		"name": "petstore",
		"connection_type": "openapi",
		"auth_type": "none",
		"openapi_config": {
			"spec": "openapi: 3.0.3\ninfo: {title: Petstore, version: 1.0.0}\npaths: {}",
			"spec_url": "https://petstore.example.com/openapi.yaml",
			"base_url": "https://petstore.example.com/v1",
			"security_credentials": {
				"ApiKeyAuth": {"value": "env.PETSTORE_KEY"},
				"BasicAuth": {"username": "svc", "password": "env.PETSTORE_PASSWORD"}
			},
			"include_deprecated": true,
			"max_response_bytes": 1024,
			"spec_size": 71,
			"spec_hash": "abc",
			"spec_title": "Petstore",
			"openapi_version": "3.0.3",
			"operation_count": 0
		}
	}`
	var cfg MCPClientConfig
	if err := json.Unmarshal([]byte(in), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.ConnectionType != MCPConnectionTypeOpenAPI {
		t.Fatalf("connection_type = %q", cfg.ConnectionType)
	}
	oc := cfg.OpenAPIConfig
	if oc == nil {
		t.Fatal("openapi_config was dropped")
	}
	if oc.SpecURL == nil || *oc.SpecURL != "https://petstore.example.com/openapi.yaml" || oc.BaseURL == nil || *oc.BaseURL != "https://petstore.example.com/v1" {
		t.Fatalf("spec_url/base_url not preserved: %+v", oc)
	}
	if !oc.IncludeDeprecated || oc.MaxResponseBytes != 1024 || oc.SpecSize != 71 || oc.SpecHash != "abc" || oc.SpecTitle != "Petstore" || oc.OpenAPIVersion != "3.0.3" {
		t.Fatalf("scalar fields not preserved: %+v", oc)
	}
	if got := oc.SecurityCredentials["ApiKeyAuth"].Value.GetValue(); got != "env.PETSTORE_KEY" && !oc.SecurityCredentials["ApiKeyAuth"].Value.IsFromSecret() {
		t.Fatalf("ApiKeyAuth value lost: %q", got)
	}
	if oc.SecurityCredentials["BasicAuth"].Username.GetValue() != "svc" {
		t.Fatalf("BasicAuth username lost: %+v", oc.SecurityCredentials["BasicAuth"])
	}

	out, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back MCPClientConfig
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if back.OpenAPIConfig == nil || back.OpenAPIConfig.Spec != oc.Spec || len(back.OpenAPIConfig.SecurityCredentials) != 2 {
		t.Fatalf("round trip lost data: %s", out)
	}
}

// TestExchangesAsSSOApplication checks id_jag always exchanges as the SSO
// application, while the direct profile follows use_idp_credentials.
func TestExchangesAsSSOApplication(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  *MCPTokenExchangeConfig
		want bool
	}{
		"nil":                       {nil, false},
		"direct, dedicated app":     {&MCPTokenExchangeConfig{}, false},
		"direct, SSO app":           {&MCPTokenExchangeConfig{UseIdPCredentials: true}, true},
		"id_jag, flag left off":     {&MCPTokenExchangeConfig{Profile: TokenExchangeProfileIDJAG}, true},
		"id_jag, dedicated app set": {&MCPTokenExchangeConfig{Profile: TokenExchangeProfileIDJAG, ClientID: NewSecretVar("dedicated")}, true},
	} {
		if got := tc.cfg.ExchangesAsSSOApplication(); got != tc.want {
			t.Errorf("%s: ExchangesAsSSOApplication() = %v, want %v", name, got, tc.want)
		}
	}
}

// TestMCPClientConfigCodeModeFor pins that server_type_on_slug decides only on the client's own /mcp/<slug>;
// every other request, other slugs included, follows is_code_mode_client.
func TestMCPClientConfigCodeModeFor(t *testing.T) {
	onSlug := func(name string) context.Context {
		return context.WithValue(context.Background(), BifrostContextKeyMCPGatewayClient, name)
	}
	cases := []struct {
		name                                                 string
		codeMode                                             bool
		slug                                                 MCPServerTypeOnSlug
		plain, ownSlug, otherSlug                            bool
		usesCodeModeGlobalOrSlug, servesOwnToolsGlobalOrSlug bool
	}{
		{"code mode, inherit", true, MCPServerTypeOnSlugInherit, true, true, true, true, false},
		{"code mode, slug disabled", true, MCPServerTypeOnSlugDisabled, true, false, true, true, true},
		{"direct, inherit", false, MCPServerTypeOnSlugInherit, false, false, false, false, true},
		{"direct, slug enabled", false, MCPServerTypeOnSlugEnabled, false, true, false, true, true},
		{"code mode, slug enabled", true, MCPServerTypeOnSlugEnabled, true, true, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &MCPClientConfig{Name: "github", IsCodeModeClient: tc.codeMode, ServerTypeOnSlug: tc.slug}
			if got := cfg.CodeModeFor(context.Background()); got != tc.plain {
				t.Errorf("/mcp: want %v, got %v", tc.plain, got)
			}
			if got := cfg.CodeModeFor(onSlug("github")); got != tc.ownSlug {
				t.Errorf("own slug: want %v, got %v", tc.ownSlug, got)
			}
			if got := cfg.CodeModeFor(onSlug("slack")); got != tc.otherSlug {
				t.Errorf("other slug: want %v, got %v", tc.otherSlug, got)
			}
			if got := cfg.UsesCodeModeGlobalOrSlug(); got != tc.usesCodeModeGlobalOrSlug {
				t.Errorf("UsesCodeModeGlobalOrSlug: want %v, got %v", tc.usesCodeModeGlobalOrSlug, got)
			}
			if got := cfg.ServesOwnToolsGlobalOrSlug(); got != tc.servesOwnToolsGlobalOrSlug {
				t.Errorf("ServesOwnToolsGlobalOrSlug: want %v, got %v", tc.servesOwnToolsGlobalOrSlug, got)
			}
		})
	}
	if (&MCPClientConfig{Name: "github", IsCodeModeClient: true}).CodeModeFor(nil) != true {
		t.Error("a nil context must follow is_code_mode_client")
	}
}

func TestMCPProtocolVersionsUpTo(t *testing.T) {
	for _, tc := range []struct {
		maxVersion string
		want       []string
		cimd       bool
	}{
		{"", nil, true},
		{MCPMaxProtocolVersionAuto, nil, true},
		{"latest", nil, true},
		{MCPProtocolVersion20260728, []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}, true},
		{MCPProtocolVersion20251125, []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}, true},
		{MCPProtocolVersion20250618, []string{"2025-06-18", "2025-03-26", "2024-11-05"}, false},
	} {
		got := MCPProtocolVersionsUpTo(tc.maxVersion)
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("MCPProtocolVersionsUpTo(%q) = %v, want %v", tc.maxVersion, got, tc.want)
		}
		if MCPAllowsClientMetadataDocuments(tc.maxVersion) != tc.cimd {
			t.Errorf("MCPAllowsClientMetadataDocuments(%q) = %v, want %v", tc.maxVersion, !tc.cimd, tc.cimd)
		}
	}
}

func TestMCPClientCredentialsConfigValidate(t *testing.T) {
	secret := func() *MCPClientCredentialsConfig {
		return &MCPClientCredentialsConfig{ClientID: NewSecretVar("bifrost"), ClientSecret: NewSecretVar("s")}
	}
	if err := secret().Validate(); err != nil {
		t.Fatalf("secret config: %v", err)
	}
	for name, tc := range map[string]struct {
		mutate func(*MCPClientCredentialsConfig)
		want   string
	}{
		"nil block":                {func(c *MCPClientCredentialsConfig) { *c = MCPClientCredentialsConfig{} }, "client_credentials.client_id is required"},
		"no credential":            {func(c *MCPClientCredentialsConfig) { c.ClientSecret = nil }, "client_credentials: set client_secret or private_key"},
		"secret and key":           {func(c *MCPClientCredentialsConfig) { c.PrivateKey = NewSecretVar("pem") }, "client_credentials: set either client_secret or private_key, not both"},
		"key settings without key": {func(c *MCPClientCredentialsConfig) { c.KeyID = "k" }, "client_credentials.key_id, signing_algorithm and assertion_audience require private_key"},
		"unknown auth method":      {func(c *MCPClientCredentialsConfig) { c.AuthMethod = "tls_client_auth" }, `client_credentials.auth_method must be empty, "client_secret_basic" or "client_secret_post"`},
		"http token url":           {func(c *MCPClientCredentialsConfig) { c.TokenURL = "http://as.example.com/token" }, "client_credentials.token_url must be an https URL"},
		"scope with a space":       {func(c *MCPClientCredentialsConfig) { c.Scopes = []string{"a b"} }, "client_credentials.scopes[0] must be a single non-empty scope"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := secret()
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil || err.Error() != tc.want {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
	key := &MCPClientCredentialsConfig{ClientID: NewSecretVar("bifrost"), PrivateKey: NewSecretVar("pem"), SigningAlgorithm: "ES256"}
	if err := key.Validate(); err != nil {
		t.Fatalf("key config: %v", err)
	}
	if err := (*MCPClientCredentialsConfig)(nil).Validate(); err == nil {
		t.Fatal("expected an error for a missing block")
	}
}

// TestMCPAWSSigV4ConfigValidate_SecretReferences: a region or session name from an
// env reference is checked as it resolves, and one that resolves to nothing says
// which reference to set.
func TestMCPAWSSigV4ConfigValidate_SecretReferences(t *testing.T) {
	t.Setenv("SIGV4_TEST_REGION", "us-gov-west-1")
	t.Setenv("SIGV4_TEST_SESSION", "bifrost-gov")
	t.Setenv("SIGV4_TEST_BAD_REGION", "US West")
	t.Setenv("SIGV4_TEST_ROLE", "arn:aws:iam::123456789012:role/from-env")
	t.Setenv("SIGV4_TEST_BAD_ROLE", "gateway")
	role := NewSecretVar("arn:aws-us-gov:iam::123456789012:role/gateway")
	ok := &MCPAWSSigV4Config{Region: NewSecretVar("env.SIGV4_TEST_REGION"), RoleARN: role, SessionName: NewSecretVar("env.SIGV4_TEST_SESSION")}
	if err := ok.Validate(); err != nil {
		t.Fatalf("resolved references: %v", err)
	}
	if got := ok.Region.GetValue(); got != "us-gov-west-1" {
		t.Fatalf("region resolved to %q", got)
	}
	if err := (&MCPAWSSigV4Config{Region: NewSecretVar("us-east-1"), RoleARN: NewSecretVar("env.SIGV4_TEST_ROLE")}).Validate(); err != nil {
		t.Fatalf("a role from env: %v", err)
	}
	for name, tc := range map[string]struct {
		cfg  *MCPAWSSigV4Config
		want string
	}{
		"unset env":                {&MCPAWSSigV4Config{Region: NewSecretVar("env.SIGV4_TEST_UNSET_REGION")}, "aws_sigv4.region references env.SIGV4_TEST_UNSET_REGION, which does not resolve to an AWS region"},
		"env holding a non-region": {&MCPAWSSigV4Config{Region: NewSecretVar("env.SIGV4_TEST_BAD_REGION")}, "aws_sigv4.region references env.SIGV4_TEST_BAD_REGION, which does not resolve to an AWS region"},
		"missing region":           {&MCPAWSSigV4Config{}, "aws_sigv4.region must be an AWS region such as us-east-1"},
		// An unresolved role would otherwise sign as Bifrost's own identity.
		"unset env role":        {&MCPAWSSigV4Config{Region: NewSecretVar("us-east-1"), RoleARN: NewSecretVar("env.SIGV4_TEST_UNSET_ROLE")}, "aws_sigv4.role_arn references env.SIGV4_TEST_UNSET_ROLE, which does not resolve to an IAM role ARN"},
		"env holding a non-ARN": {&MCPAWSSigV4Config{Region: NewSecretVar("us-east-1"), RoleARN: NewSecretVar("env.SIGV4_TEST_BAD_ROLE")}, "aws_sigv4.role_arn references env.SIGV4_TEST_BAD_ROLE, which does not resolve to an IAM role ARN"},
	} {
		if err := tc.cfg.Validate(); err == nil || err.Error() != tc.want {
			t.Errorf("%s: expected %q, got %v", name, tc.want, err)
		}
	}
}

func TestMCPAWSSigV4ConfigValidate(t *testing.T) {
	valid := func() *MCPAWSSigV4Config {
		return &MCPAWSSigV4Config{Region: NewSecretVar("us-gov-west-1")}
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("region only: %v", err)
	}
	for name, cfg := range map[string]*MCPAWSSigV4Config{
		"every allowed service": {Region: NewSecretVar("us-east-1"), Service: "execute-api"},
		"aws-mcp":               {Region: NewSecretVar("eu-central-1"), Service: "aws-mcp"},
		"lambda":                {Region: NewSecretVar("ap-southeast-2"), Service: "lambda"},
		"role with all options": {Region: NewSecretVar("us-east-1"), RoleARN: NewSecretVar("arn:aws:iam::123456789012:role/path/gateway"), ExternalID: NewSecretVar("ext"), SessionName: NewSecretVar("bifrost@mcp")},
		"govcloud role":         {Region: NewSecretVar("us-gov-west-1"), RoleARN: NewSecretVar("arn:aws-us-gov:iam::123456789012:role/gateway")},
		"role from env":         {Region: NewSecretVar("us-east-1"), RoleARN: &SecretVar{Val: "", SecretType: SecretTypeEnv}},
	} {
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, tc := range map[string]struct {
		mutate func(*MCPAWSSigV4Config)
		want   string
	}{
		"no region":                  {func(c *MCPAWSSigV4Config) { c.Region = NewSecretVar("") }, "aws_sigv4.region must be an AWS region such as us-east-1"},
		"malformed region":           {func(c *MCPAWSSigV4Config) { c.Region = NewSecretVar("us east 1") }, "aws_sigv4.region must be an AWS region such as us-east-1"},
		"service outside allowlist":  {func(c *MCPAWSSigV4Config) { c.Service = "s3" }, "aws_sigv4.service must be one of bedrock-agentcore, aws-mcp, lambda, execute-api"},
		"secretsmanager":             {func(c *MCPAWSSigV4Config) { c.Service = "secretsmanager" }, "aws_sigv4.service must be one of bedrock-agentcore, aws-mcp, lambda, execute-api"},
		"user ARN instead of a role": {func(c *MCPAWSSigV4Config) { c.RoleARN = NewSecretVar("arn:aws:iam::123456789012:user/alice") }, "aws_sigv4.role_arn must be an IAM role ARN such as arn:aws:iam::123456789012:role/name"},
		"short account id":           {func(c *MCPAWSSigV4Config) { c.RoleARN = NewSecretVar("arn:aws:iam::1234:role/r") }, "aws_sigv4.role_arn must be an IAM role ARN such as arn:aws:iam::123456789012:role/name"},
		"external id without role":   {func(c *MCPAWSSigV4Config) { c.ExternalID = NewSecretVar("ext") }, "aws_sigv4.external_id requires role_arn"},
		"session name without role":  {func(c *MCPAWSSigV4Config) { c.SessionName = NewSecretVar("mcp") }, "aws_sigv4.session_name requires role_arn"},
		"session name with a space": {func(c *MCPAWSSigV4Config) {
			c.RoleARN = NewSecretVar("arn:aws:iam::123456789012:role/r")
			c.SessionName = NewSecretVar("two words")
		}, "aws_sigv4.session_name must be 2 to 64 letters, digits or +=,.@_- characters"},
		"session name of one char": {func(c *MCPAWSSigV4Config) {
			c.RoleARN = NewSecretVar("arn:aws:iam::123456789012:role/r")
			c.SessionName = NewSecretVar("a")
		}, "aws_sigv4.session_name must be 2 to 64 letters, digits or +=,.@_- characters"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := valid()
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil || err.Error() != tc.want {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
	if err := (*MCPAWSSigV4Config)(nil).Validate(); err == nil || err.Error() != "aws_sigv4 is required for aws_sigv4 auth type" {
		t.Fatalf("expected an error for a missing block, got %v", err)
	}
}

func TestMCPAWSSigV4ConfigEqualAndService(t *testing.T) {
	base := &MCPAWSSigV4Config{Region: NewSecretVar("us-east-1"), RoleARN: NewSecretVar("arn:aws:iam::123456789012:role/r")}
	if got := base.SigningService(); got != DefaultAWSSigV4Service {
		t.Fatalf("empty service signs as %q, want %q", got, DefaultAWSSigV4Service)
	}
	explicitDefault := *base
	explicitDefault.Service = DefaultAWSSigV4Service
	if !base.Equal(&explicitDefault) {
		t.Fatal("an empty service and the default one are the same setting")
	}
	for name, mutate := range map[string]func(*MCPAWSSigV4Config){
		"region":          func(c *MCPAWSSigV4Config) { c.Region = NewSecretVar("us-west-2") },
		"service":         func(c *MCPAWSSigV4Config) { c.Service = "lambda" },
		"role":            func(c *MCPAWSSigV4Config) { c.RoleARN = NewSecretVar("arn:aws:iam::123456789012:role/other") },
		"external id":     func(c *MCPAWSSigV4Config) { c.ExternalID = NewSecretVar("ext") },
		"session name":    func(c *MCPAWSSigV4Config) { c.SessionName = NewSecretVar("mcp") },
		"region from env": func(c *MCPAWSSigV4Config) { c.Region = NewSecretVar("env.SIGV4_EQUAL_REGION") },
	} {
		changed := *base
		mutate(&changed)
		if base.Equal(&changed) {
			t.Errorf("a different %s must not compare equal", name)
		}
	}
	if !(*MCPAWSSigV4Config)(nil).Equal(nil) || base.Equal(nil) {
		t.Fatal("nil compares equal only to nil")
	}
}

// TestMCPAWSSigV4ConfigRedaction: a read shows literal identifiers, masks the external
// ID and any secret reference; an update that sends the mask back keeps the stored reference.
func TestMCPAWSSigV4ConfigRedaction(t *testing.T) {
	t.Setenv("SIGV4_SCHEMA_ROLE", "arn:aws:iam::123456789012:role/from-env")
	t.Setenv("SIGV4_SCHEMA_REGION", "us-gov-west-1")
	stored := &MCPAWSSigV4Config{Region: NewSecretVar("env.SIGV4_SCHEMA_REGION"), RoleARN: NewSecretVar("env.SIGV4_SCHEMA_ROLE"), ExternalID: NewSecretVar("literal-ext"), SessionName: NewSecretVar("bifrost")}
	redacted := stored.Redacted()
	if redacted.Region.GetValue() == "us-gov-west-1" || !redacted.Region.IsFromEnv() {
		t.Fatalf("a region from env must be masked and keep its reference, got %+v", redacted.Region)
	}
	if redacted.SessionName.GetValue() != "bifrost" {
		t.Fatalf("a literal session name reads back as set, got %q", redacted.SessionName.GetValue())
	}
	if redacted.RoleARN.GetValue() == "arn:aws:iam::123456789012:role/from-env" || !redacted.RoleARN.IsFromEnv() {
		t.Fatalf("a role ARN from env must be masked and keep its reference, got %+v", redacted.RoleARN)
	}
	if redacted.ExternalID.GetValue() == "literal-ext" || !redacted.ExternalID.IsMaskedPlaceholder() {
		t.Fatalf("a literal external ID must be masked, got %q", redacted.ExternalID.GetValue())
	}
	if stored.RoleARN.GetValue() != "arn:aws:iam::123456789012:role/from-env" {
		t.Fatal("redaction must not mutate the stored block")
	}

	// A client that sends back only the masked text it read.
	update := &MCPAWSSigV4Config{Region: NewSecretVar(redacted.Region.GetValue()), RoleARN: NewSecretVar(redacted.RoleARN.GetValue()), ExternalID: NewSecretVar("new-ext")}
	update.KeepMaskedFrom(stored)
	if !update.Region.IsFromEnv() || update.Region.GetValue() != "us-gov-west-1" {
		t.Fatalf("a masked region keeps the stored reference, got %+v", update.Region)
	}
	if !update.RoleARN.IsFromEnv() || update.RoleARN.GetValue() != "arn:aws:iam::123456789012:role/from-env" {
		t.Fatalf("a masked role ARN keeps the stored reference, got %+v", update.RoleARN)
	}
	if update.ExternalID.GetValue() != "new-ext" {
		t.Fatal("a value that was sent replaces the stored one")
	}
	resent := &MCPAWSSigV4Config{ExternalID: NewSecretVar(redacted.ExternalID.GetValue())}
	resent.KeepMaskedFrom(stored)
	if resent.ExternalID.GetValue() != "literal-ext" {
		t.Fatalf("a masked external ID keeps the stored value, got %q", resent.ExternalID.GetValue())
	}
	if (*MCPAWSSigV4Config)(nil).Redacted() != nil {
		t.Fatal("a nil block redacts to nil")
	}
	update.KeepMaskedFrom(nil)
}

func TestCheckAWSSigV4Headers(t *testing.T) {
	if err := CheckAWSSigV4Headers(map[string]SecretVar{"X-Api-Version": *NewSecretVar("2")}, WhiteList{"x-tenant-id", "*"}); err != nil {
		t.Fatalf("ordinary headers are allowed: %v", err)
	}
	if err := CheckAWSSigV4Headers(map[string]SecretVar{"x-amz-date": *NewSecretVar("now")}, nil); err == nil {
		t.Fatal("a configured x-amz-* header must be refused")
	}
	if err := CheckAWSSigV4Headers(nil, WhiteList{"AUTHORIZATION"}); err == nil {
		t.Fatal("forwarding Authorization must be refused")
	}
}

func TestIsAWSSigV4ReservedHeader(t *testing.T) {
	for name, want := range map[string]bool{
		"Authorization":          true,
		"authorization":          true,
		"X-Amz-Date":             true,
		"x-amz-security-token":   true,
		"X-Amz-Target":           true,
		" X-Amz-Content-Sha256 ": true,
		"X-Amzn-Trace-Id":        false,
		"X-Api-Key":              false,
		"Mcp-Session-Id":         false,
	} {
		if got := IsAWSSigV4ReservedHeader(name); got != want {
			t.Errorf("IsAWSSigV4ReservedHeader(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestMCPClientCredentialsConfigMasking: a read masks the credentials, and an
// update that sends the masks back keeps the stored values.
func TestMCPClientCredentialsConfigMasking(t *testing.T) {
	stored := &MCPClientCredentialsConfig{TokenURL: "https://as.example.com/token", ClientID: NewSecretVar("bifrost-client"), ClientSecret: NewSecretVar("super-secret-value"), Scopes: []string{"mcp"}}
	redacted := stored.Redacted()
	if redacted.ClientSecret.GetValue() == stored.ClientSecret.GetValue() || !redacted.ClientSecret.IsMaskedPlaceholder() {
		t.Fatalf("client_secret not masked: %q", redacted.ClientSecret.GetValue())
	}
	if stored.ClientSecret.GetValue() != "super-secret-value" {
		t.Fatal("Redacted mutated the stored block")
	}
	update := *redacted
	update.KeepMaskedFrom(stored)
	if !update.Equal(stored) {
		t.Fatalf("masked round trip did not keep the stored values: %+v", update)
	}
	if update.TargetChangedFrom(stored) {
		t.Fatal("unchanged token_url counted as a target change")
	}
	update.TokenURL = ""
	if !update.TargetChangedFrom(stored) {
		t.Fatal("a cleared token_url is discovered again, so it is a target change")
	}
}

func TestMCPClientCredentialsConfigResourceFor(t *testing.T) {
	cfg := &MCPClientCredentialsConfig{}
	if got := cfg.ResourceFor("https://mcp.example.com/mcp"); got != "https://mcp.example.com/mcp" {
		t.Fatalf("unset resource should default to the server URL, got %q", got)
	}
	empty := ""
	cfg.Resource = &empty
	if got := cfg.ResourceFor("https://mcp.example.com/mcp"); got != "" {
		t.Fatalf("an empty resource is omitted, got %q", got)
	}
}
