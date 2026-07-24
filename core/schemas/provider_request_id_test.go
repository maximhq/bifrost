package schemas

import "testing"

func TestResolveProviderRequestIDHeader(t *testing.T) {
	tests := []struct {
		name     string
		provider ModelProvider
		config   *ProviderConfig
		want     string
		wantErr  bool
	}{
		{name: "disabled", provider: OpenAI, config: &ProviderConfig{}, want: ""},
		{name: "explicitly disabled", provider: OpenAI, config: &ProviderConfig{ProviderRequestID: &ProviderRequestIDConfig{Enabled: false, HeaderName: "x-request-id"}}, want: ""},
		{name: "openai default", provider: OpenAI, config: &ProviderConfig{ProviderRequestID: &ProviderRequestIDConfig{Enabled: true}}, want: "x-request-id"},
		{name: "anthropic default", provider: Anthropic, config: &ProviderConfig{ProviderRequestID: &ProviderRequestIDConfig{Enabled: true}}, want: "request-id"},
		{name: "azure default", provider: Azure, config: &ProviderConfig{ProviderRequestID: &ProviderRequestIDConfig{Enabled: true}}, want: "apim-request-id"},
		{name: "custom base default", provider: ModelProvider("proxy"), config: &ProviderConfig{CustomProviderConfig: &CustomProviderConfig{BaseProviderType: OpenAI}, ProviderRequestID: &ProviderRequestIDConfig{Enabled: true}}, want: "x-request-id"},
		{name: "explicit override canonicalized", provider: OpenAI, config: &ProviderConfig{ProviderRequestID: &ProviderRequestIDConfig{Enabled: true, HeaderName: " X-Trace-ID "}}, want: "x-trace-id"},
		{name: "unknown requires header", provider: ModelProvider("proxy"), config: &ProviderConfig{ProviderRequestID: &ProviderRequestIDConfig{Enabled: true}}, wantErr: true},
		{name: "invalid token", provider: OpenAI, config: &ProviderConfig{ProviderRequestID: &ProviderRequestIDConfig{Enabled: true, HeaderName: "bad header"}}, wantErr: true},
		{name: "sensitive header", provider: OpenAI, config: &ProviderConfig{ProviderRequestID: &ProviderRequestIDConfig{Enabled: true, HeaderName: "Authorization"}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveProviderRequestIDHeader(tt.provider, tt.config)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ResolveProviderRequestIDHeader() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ResolveProviderRequestIDHeader() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeProviderRequestIDConfig(t *testing.T) {
	config := &ProviderConfig{ProviderRequestID: &ProviderRequestIDConfig{Enabled: false, HeaderName: " X-Trace-ID "}}
	if err := NormalizeProviderRequestIDConfig(OpenAI, config); err != nil {
		t.Fatal(err)
	}
	if got := config.ProviderRequestID.HeaderName; got != "x-trace-id" {
		t.Fatalf("disabled config header = %q, want x-trace-id", got)
	}

	config.ProviderRequestID.Enabled = true
	config.ProviderRequestID.HeaderName = ""
	if err := NormalizeProviderRequestIDConfig(OpenAI, config); err != nil {
		t.Fatal(err)
	}
	if got := config.ProviderRequestID.HeaderName; got != "x-request-id" {
		t.Fatalf("enabled default header = %q, want x-request-id", got)
	}
}
