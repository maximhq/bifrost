package vectorstore

import "testing"

func TestHostWithLocalScheme(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{"localhost:5081", "http://localhost:5081"},
		{"localhost", "http://localhost"},
		{"127.0.0.1:5081", "http://127.0.0.1:5081"},
		{"[::1]:5081", "http://[::1]:5081"},
		{"::1", "http://::1"},
		{"http://localhost:5081", "http://localhost:5081"},         // scheme preserved
		{"https://index.pinecone.io", "https://index.pinecone.io"}, // scheme preserved
		{"index.pinecone.io", "index.pinecone.io"},                 // non-local untouched
		{"192.168.1.10:5081", "192.168.1.10:5081"},                 // private but not loopback
	}
	for _, tt := range tests {
		if got := hostWithLocalScheme(tt.host); got != tt.want {
			t.Errorf("hostWithLocalScheme(%q) = %q, want %q", tt.host, got, tt.want)
		}
	}
}
