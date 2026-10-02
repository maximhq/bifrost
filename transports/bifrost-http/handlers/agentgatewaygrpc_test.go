package handlers

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func grpcTestContext(md metadata.MD) context.Context {
	return metadata.NewIncomingContext(context.Background(), md)
}

func TestAgentGatewayGRPCAuthorityRouting(t *testing.T) {
	server := &AgentGatewayGRPCServer{baseDomain: "a2a.bifrost.example"}
	for _, tc := range []struct {
		name      string
		authority string
		wantName  string
	}{
		{"host and port", "accounting-agent.a2a.bifrost.example:9090", "accounting-agent"},
		{"host only", "accounting-agent.a2a.bifrost.example", "accounting-agent"},
		{"uppercase host", "Accounting-Agent.A2A.Bifrost.Example:9090", "accounting-agent"},
		{"trailing dot", "accounting-agent.a2a.bifrost.example.:9090", "accounting-agent"},
		{"foreign domain", "accounting-agent.other.example:9090", ""},
		{"base domain itself", "a2a.bifrost.example:9090", ""},
		{"nested label", "x.accounting-agent.a2a.bifrost.example:9090", ""},
		{"missing authority", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			md := metadata.MD{}
			if tc.authority != "" {
				md.Set(":authority", tc.authority)
			}
			name, err := server.agentNameFromAuthority(grpcTestContext(md))
			if tc.wantName == "" {
				require.Equal(t, codes.NotFound, status.Code(err))
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantName, name)
		})
	}
}

func TestResolveVirtualKeyFromGRPCMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		md       metadata.MD
		wantKey  string
		wantFrom string
	}{
		{"canonical verbatim", metadata.Pairs("x-bf-vk", "anything"), "anything", "x-bf-vk"},
		{"bearer with prefix", metadata.Pairs("authorization", "Bearer sk-bf-one"), "sk-bf-one", "authorization"},
		{"bearer without prefix ignored", metadata.Pairs("authorization", "Bearer upstream-token"), "", ""},
		{"x-api-key with prefix", metadata.Pairs("x-api-key", "sk-bf-two"), "sk-bf-two", "x-api-key"},
		{"alias without prefix ignored", metadata.Pairs("x-api-key", "provider-key"), "", ""},
		{"canonical beats bearer", metadata.Pairs("x-bf-vk", "sk-bf-one", "authorization", "Bearer sk-bf-two"), "sk-bf-one", "x-bf-vk"},
		{"empty", metadata.MD{}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, from := resolveVirtualKeyFromGRPCMetadata(tc.md)
			require.Equal(t, tc.wantKey, key)
			require.Equal(t, tc.wantFrom, from)
		})
	}
}

func TestSanitizeGRPCMetadataDropsTransportAndCredentialKeys(t *testing.T) {
	md := metadata.MD{
		":authority":    {"agent.a2a.example:9090"},
		"grpc-timeout":  {"5S"},
		"x-bf-vk":       {"sk-bf-one"},
		"a2a-version":   {"1.0"},
		"authorization": {"Bearer upstream-token"},
		"x-custom":      {"kept"},
	}
	cleaned := sanitizeGRPCMetadata(md, "authorization")
	require.Equal(t, metadata.MD{"x-custom": {"kept"}}, cleaned)

	// When no credential was consumed, upstream credentials survive.
	cleaned = sanitizeGRPCMetadata(md, "")
	require.Equal(t, metadata.MD{"authorization": {"Bearer upstream-token"}, "x-custom": {"kept"}}, cleaned)
}

func TestAgentGatewayGRPCAuthenticate(t *testing.T) {
	active := true
	vk := &tables.TableVirtualKey{ID: "vk-1", Value: *schemas.NewSecretVar("sk-bf-one"), IsActive: &active}
	store := &agentAuthStore{
		byID:    map[string]*tables.TableVirtualKey{"vk-1": vk},
		byValue: map[string]*tables.TableVirtualKey{"sk-bf-one": vk},
	}
	newServer := func(enforce bool) *AgentGatewayGRPCServer {
		return &AgentGatewayGRPCServer{
			config:     &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: enforce}},
			validator:  &agentAuthCache{store: store},
			baseDomain: "a2a.example",
		}
	}

	for _, tc := range []struct {
		name   string
		header string
		value  string
	}{
		{"canonical", "x-bf-vk", "sk-bf-one"},
		{"authorization alias", "authorization", "Bearer sk-bf-one"},
		{"x-api-key alias", "x-api-key", "sk-bf-one"},
		{"x-goog-api-key alias", "x-goog-api-key", "sk-bf-one"},
		{"api-key alias", "api-key", "sk-bf-one"},
	} {
		t.Run("accepted virtual key via "+tc.name+" is captured and stripped", func(t *testing.T) {
			authedCtx, cancel, err := newServer(true).authenticate(grpcTestContext(metadata.Pairs(tc.header, tc.value, "x-custom", "kept")))
			require.NoError(t, err)
			defer cancel()
			require.Equal(t, "sk-bf-one", authedCtx.Value(schemas.BifrostContextKeyVirtualKey))
			require.Equal(t, schemas.AcceptedBifrostCredential{Header: tc.header, Value: tc.value}, authedCtx.Value(schemas.BifrostContextKeyAcceptedCredential))
			md, ok := metadata.FromIncomingContext(authedCtx)
			require.True(t, ok)
			require.Empty(t, md.Get(tc.header))
			require.Equal(t, []string{"kept"}, md.Get("x-custom"))
		})
	}

	t.Run("invalid selected credential never falls back", func(t *testing.T) {
		ctx := grpcTestContext(metadata.Pairs("x-bf-vk", "sk-bf-wrong"))
		_, _, err := newServer(false).authenticate(ctx)
		require.Equal(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("validated user authorization is captured and stripped", func(t *testing.T) {
		server := newServer(true)
		server.identityResolver = func(_ context.Context, credential string) (string, bool) {
			return "user-1", credential == "Bearer valid-jwt"
		}
		authedCtx, cancel, err := server.authenticate(grpcTestContext(metadata.Pairs("authorization", "Bearer valid-jwt")))
		require.NoError(t, err)
		defer cancel()
		require.Equal(t, "user-1", authedCtx.Value(schemas.BifrostContextKeyUserID))
		require.Equal(t, schemas.AcceptedBifrostCredential{Header: "authorization", Value: "Bearer valid-jwt"}, authedCtx.Value(schemas.BifrostContextKeyAcceptedCredential))
		md, ok := metadata.FromIncomingContext(authedCtx)
		require.True(t, ok)
		require.Empty(t, md.Get("authorization"))
	})

	t.Run("explicit invalid virtual key never falls back to resolver", func(t *testing.T) {
		server := newServer(false)
		server.identityResolver = func(context.Context, string) (string, bool) { return "user-1", true }
		ctx := grpcTestContext(metadata.Pairs("x-bf-vk", "sk-bf-wrong", "authorization", "Bearer valid-jwt"))
		_, _, err := server.authenticate(ctx)
		require.Equal(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("anonymous rejected while enforcement is on", func(t *testing.T) {
		_, _, err := newServer(true).authenticate(grpcTestContext(metadata.MD{}))
		require.Equal(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("anonymous allowed while enforcement is off", func(t *testing.T) {
		authedCtx, cancel, err := newServer(false).authenticate(grpcTestContext(metadata.MD{}))
		require.NoError(t, err)
		defer cancel()
		require.Nil(t, authedCtx.Value(schemas.BifrostContextKeyVirtualKey))
	})
}
