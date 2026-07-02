package bifrost

import (
	"net"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestValidateExternalURL(t *testing.T) {
	tests := []struct {
		name                string
		url                 string
		allowPrivateNetwork bool
		wantErr             bool
		errMsg              string
	}{
		// Valid URLs
		{
			name:    "valid https URL",
			url:     "https://api.openai.com",
			wantErr: false,
		},
		{
			name:    "valid http URL",
			url:     "http://api.openai.com",
			wantErr: false,
		},
		{
			name:    "valid https URL with path",
			url:     "https://api.openai.com/v1",
			wantErr: false,
		},
		{
			name:    "valid https URL with port",
			url:     "https://api.openai.com:443",
			wantErr: false,
		},

		// Empty / malformed
		{
			name:    "empty URL",
			url:     "",
			wantErr: true,
			errMsg:  "URL cannot be empty",
		},
		{
			name:    "no scheme",
			url:     "api.openai.com",
			wantErr: true,
			errMsg:  "only https and http schemes are allowed",
		},
		{
			name:    "ftp scheme",
			url:     "ftp://api.openai.com",
			wantErr: true,
			errMsg:  "only https and http schemes are allowed",
		},
		{
			name:    "file scheme",
			url:     "file:///etc/passwd",
			wantErr: true,
			errMsg:  "only https and http schemes are allowed",
		},
		{
			name:    "missing hostname",
			url:     "http://",
			wantErr: true,
			errMsg:  "URL must have a hostname",
		},

		// Localhost / loopback — allowed for local deployments (Ollama, vLLM, SGL)
		{
			name:    "localhost",
			url:     "http://localhost",
			wantErr: false,
		},
		{
			name:    "localhost with port",
			url:     "http://localhost:8080",
			wantErr: false,
		},
		{
			name:    "loopback 127.0.0.1",
			url:     "http://127.0.0.1",
			wantErr: false,
		},
		{
			name:    "loopback ::1",
			url:     "http://[::1]",
			wantErr: false,
		},
		{
			name:    "all-zeros 0.0.0.0",
			url:     "http://0.0.0.0",
			wantErr: true,
			errMsg:  "unspecified IP addresses are not allowed",
		},

		// Private IP ranges (RFC 1918)
		{
			name:    "private 10.x.x.x",
			url:     "http://10.0.0.1",
			wantErr: true,
			errMsg:  "private IP addresses are not allowed",
		},
		{
			name:    "private 172.16.x.x",
			url:     "http://172.16.0.1",
			wantErr: true,
			errMsg:  "private IP addresses are not allowed",
		},
		{
			name:    "private 192.168.x.x",
			url:     "http://192.168.1.1",
			wantErr: true,
			errMsg:  "private IP addresses are not allowed",
		},

		// Link-local / cloud metadata — always blocked even with AllowPrivateNetwork
		{
			name:    "AWS metadata service",
			url:     "http://169.254.169.254",
			wantErr: true,
			errMsg:  "link-local IP addresses are not allowed",
		},
		{
			name:    "AWS metadata with path",
			url:     "http://169.254.169.254/latest/meta-data/",
			wantErr: true,
			errMsg:  "link-local IP addresses are not allowed",
		},
		{
			name:    "link-local 169.254.x.x",
			url:     "http://169.254.1.1",
			wantErr: true,
			errMsg:  "link-local IP addresses are not allowed",
		},

		// Query-parameter injection (the PoC vector from the advisory)
		{
			name:    "query param injection targeting metadata service",
			url:     "http://169.254.169.254/latest/meta-data/iam/security-credentials/role?x=",
			wantErr: true,
			errMsg:  "link-local IP addresses are not allowed",
		},
		{
			name:    "query param injection targeting internal host",
			url:     "http://10.0.0.1/arbitrary/path?x=",
			wantErr: true,
			errMsg:  "private IP addresses are not allowed",
		},

		// AllowPrivateNetwork=true: RFC 1918 allowed, link-local still blocked
		{
			name:                "allow_private_network permits 10.x.x.x",
			url:                 "http://10.0.0.5:8000",
			allowPrivateNetwork: true,
			wantErr:             false,
		},
		{
			name:                "allow_private_network permits 192.168.x.x",
			url:                 "http://192.168.1.50:11434",
			allowPrivateNetwork: true,
			wantErr:             false,
		},
		{
			name:                "allow_private_network still blocks 169.254.169.254",
			url:                 "http://169.254.169.254",
			allowPrivateNetwork: true,
			wantErr:             true,
			errMsg:              "link-local IP addresses are not allowed",
		},
		{
			name:                "allow_private_network still blocks 0.0.0.0",
			url:                 "http://0.0.0.0",
			allowPrivateNetwork: true,
			wantErr:             true,
			errMsg:              "unspecified IP addresses are not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExternalURL(tt.url, tt.allowPrivateNetwork)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errMsg)
					return
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, got %q", err.Error())
				}
			}
		})
	}
}

func TestIsLocalhost(t *testing.T) {
	tests := []struct {
		hostname string
		want     bool
	}{
		{"localhost", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"0.0.0.0", true},
		{"::", true},
		{"api.openai.com", false},
		{"10.0.0.1", false}, // private but not localhost — handled by IsPrivateIP
		{"169.254.169.254", false},
	}

	for _, tt := range tests {
		t.Run(tt.hostname, func(t *testing.T) {
			if got := network.IsLocalhost(tt.hostname); got != tt.want {
				t.Errorf("IsLocalhost(%q) = %v, want %v", tt.hostname, got, tt.want)
			}
		})
	}
}

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		// Private IPv4 (RFC 1918)
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.0.1", true},
		{"192.168.255.255", true},
		// Link-local
		{"169.254.0.1", true},
		{"169.254.169.254", true},
		// Loopback
		{"127.0.0.1", true},
		{"127.255.255.255", true},
		// Public IPv4
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"104.21.0.1", false},
		// Private IPv6
		{"::1", true},     // loopback
		{"fe80::1", true}, // link-local
		{"fc00::1", true}, // unique local
		{"fd00::1", true}, // unique local
		// Public IPv6
		{"2606:4700::1", false},
		// Unspecified addresses (fail-closed)
		{"0.0.0.0", true},                   // IPv4 unspecified
		{"0:0:0:0:0:0:0:0", true},           // IPv6 unspecified long form
		{"::", true},                         // IPv6 unspecified short form
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP %q", tt.ip)
			}
			if got := network.IsPrivateIP(ip); got != tt.want {
				t.Errorf("IsPrivateIP(%q) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestIsCustomProvider(t *testing.T) {
	tests := []struct {
		name   string
		config *schemas.ProviderConfig
		want   bool
	}{
		{
			name: "nil config",
		},
		{
			name:   "standard provider config",
			config: &schemas.ProviderConfig{},
		},
		{
			name: "custom provider with standard base provider",
			config: &schemas.ProviderConfig{
				CustomProviderConfig: &schemas.CustomProviderConfig{
					BaseProviderType: schemas.OpenAI,
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCustomProvider(tt.config); got != tt.want {
				t.Fatalf("IsCustomProvider() = %t, want %t", got, tt.want)
			}
		})
	}
}
