package oauth2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchSingleAuthServerMetadata_PathIssuerUsesOIDCPathAppendFallback(t *testing.T) {
	SetLogger(bifrost.NewDefaultLogger(schemas.LogLevelError))

	var requestedPaths []string
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPaths = append(requestedPaths, r.URL.Path)
		if r.URL.Path != "/tenant/prod/oidc/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"` + issuer + `","authorization_endpoint":"https://issuer.example/authorize","token_endpoint":"https://issuer.example/token"}`))
	}))
	defer server.Close()
	issuer = server.URL + "/tenant/prod/oidc"

	metadata, err := fetchSingleAuthServerMetadata(context.Background(), issuer)
	require.NoError(t, err)
	require.NotNil(t, metadata)
	assert.Equal(t, "https://issuer.example/authorize", metadata.AuthorizationURL)
	assert.Equal(t, []string{
		"/.well-known/oauth-authorization-server/tenant/prod/oidc",
		"/.well-known/openid-configuration/tenant/prod/oidc",
		"/tenant/prod/oidc/.well-known/openid-configuration",
	}, requestedPaths)
}

func TestFetchSingleAuthServerMetadata_PathIssuerDoesNotTryHostLevelOIDCURL(t *testing.T) {
	SetLogger(bifrost.NewDefaultLogger(schemas.LogLevelError))

	var requestedPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPaths = append(requestedPaths, r.URL.Path)
		if r.URL.Path == "/.well-known/openid-configuration" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"issuer":"https://issuer.example","authorization_endpoint":"https://issuer.example/authorize","token_endpoint":"https://issuer.example/token"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	metadata, err := fetchSingleAuthServerMetadata(context.Background(), server.URL+"/tenant/prod/oidc")
	require.Error(t, err)
	assert.Nil(t, metadata)
	assert.Equal(t, []string{
		"/.well-known/oauth-authorization-server/tenant/prod/oidc",
		"/.well-known/openid-configuration/tenant/prod/oidc",
		"/tenant/prod/oidc/.well-known/openid-configuration",
		"/tenant/prod/oidc",
	}, requestedPaths)
}

func TestFetchSingleAuthServerMetadata_RejectsMismatchedIssuer(t *testing.T) {
	SetLogger(bifrost.NewDefaultLogger(schemas.LogLevelError))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server/tenant/prod/oidc" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"https://issuer.example/other-tenant","authorization_endpoint":"https://issuer.example/authorize","token_endpoint":"https://issuer.example/token"}`))
	}))
	defer server.Close()

	metadata, err := fetchSingleAuthServerMetadata(context.Background(), server.URL+"/tenant/prod/oidc")
	require.Error(t, err)
	assert.Nil(t, metadata)
}
