package handlers

import (
	"testing"

	"github.com/valyala/fasthttp"
)

func TestResolveBaseURLUsesForwardedPort(t *testing.T) {
	h := &SkillsServingHandler{}
	tests := []struct {
		name   string
		proto  string
		host   string
		port   string
		direct string
		want   string
	}{
		{name: "non-default forwarded port", proto: "https", host: "gateway.example.com", port: "8443", want: "https://gateway.example.com:8443"},
		{name: "default https port", proto: "https", host: "gateway.example.com", port: "443", want: "https://gateway.example.com"},
		{name: "default http port", proto: "http", host: "gateway.example.com", port: "80", want: "http://gateway.example.com"},
		{name: "host already has a port", proto: "https", host: "gateway.example.com:8443", port: "9443", want: "https://gateway.example.com:8443"},
		{name: "ipv6 host already has a port", proto: "https", host: "[2001:db8::1]:8443", port: "9443", want: "https://[2001:db8::1]:8443"},
		{name: "ipv6 host without a port", proto: "https", host: "[2001:db8::1]", port: "8443", want: "https://[2001:db8::1]:8443"},
		{name: "no forwarded port", proto: "https", host: "gateway.example.com", want: "https://gateway.example.com"},
		{name: "first forwarded port", proto: "https", host: "gateway.example.com", port: "8443, 443", want: "https://gateway.example.com:8443"},
		{name: "direct host keeps its port", direct: "gateway.example.com:8443", want: "http://gateway.example.com:8443"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			if tt.proto != "" {
				ctx.Request.Header.Set("X-Forwarded-Proto", tt.proto)
			}
			if tt.host != "" {
				ctx.Request.Header.Set("X-Forwarded-Host", tt.host)
			}
			if tt.port != "" {
				ctx.Request.Header.Set("X-Forwarded-Port", tt.port)
			}
			if tt.direct != "" {
				ctx.Request.SetHost(tt.direct)
			}
			if got := h.resolveBaseURL(ctx); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
