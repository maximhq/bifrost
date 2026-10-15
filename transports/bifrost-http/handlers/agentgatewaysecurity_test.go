package handlers

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/maximhq/bifrost/core/agent"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// TestAgentGatewayBypassedCredentialOrigins verifies permission changes stop before any manager call.
func TestAgentGatewayBypassedCredentialOrigins(t *testing.T) {
	for _, operation := range []string{"create", "update"} {
		t.Run(operation, func(t *testing.T) {
			h := &AgentGatewayHandler{}
			ctx := &fasthttp.RequestCtx{}
			ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
			ctx.Request.SetBodyString(`{"name":"fixture","credential_origins":["https://runtime.example.com"]}`)
			if operation == "create" {
				h.create(ctx)
			} else {
				h.update(ctx)
			}
			require.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
		})
	}
}

// TestAgentGatewayReplacementCredentialOrigins preserves operator permissions on bypassed replacements.
func TestAgentGatewayReplacementCredentialOrigins(t *testing.T) {
	for _, bypassed := range []bool{false, true} {
		for _, field := range []string{"", `,"credential_origins":[]`, `,"credential_origins":["https://replacement.example.com"]`} {
			t.Run(fmt.Sprintf("bypassed=%t/field=%s", bypassed, field), func(t *testing.T) {
				origins := []string{"https://runtime.example.com"}
				store := &agentAuthStore{registrations: map[string]schemas.AgentRegistration{
					"fixture": {Name: "fixture", CredentialOrigins: origins},
				}}
				manager, err := agent.NewManager(context.Background(), store, nil, "", http.DefaultClient)
				require.NoError(t, err)
				defer manager.Close()
				recorder := &recordingAgentRegistrationManager{}
				h := NewAgentGatewayHandler(manager, recorder, &lib.Config{}, nil)
				ctx := &fasthttp.RequestCtx{}
				ctx.SetUserValue("name", "fixture")
				ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, bypassed)
				ctx.Request.SetBodyString(`{"agent_card_url":"https://agent.example.com/card","enabled":false` + field + `}`)
				h.update(ctx)
				if bypassed && field == `,"credential_origins":["https://replacement.example.com"]` {
					require.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
					require.Zero(t, recorder.updateCalls)
					return
				}
				require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
				require.Equal(t, 1, recorder.updateCalls)
				if bypassed {
					require.Equal(t, origins, recorder.updateRequest.CredentialOrigins)
				} else if field == `,"credential_origins":["https://replacement.example.com"]` {
					require.Equal(t, []string{"https://replacement.example.com"}, recorder.updateRequest.CredentialOrigins)
				} else {
					require.Empty(t, recorder.updateRequest.CredentialOrigins)
				}
			})
		}
	}
}

// TestAgentGatewayPublicPolicyComesFromAuthentication verifies JSON cannot override the server-owned policy.
func TestAgentGatewayPublicPolicyComesFromAuthentication(t *testing.T) {
	for _, bypassed := range []bool{false, true} {
		t.Run(fmt.Sprint(bypassed), func(t *testing.T) {
			store := &agentAuthStore{}
			manager, err := agent.NewManager(context.Background(), store, nil, "", http.DefaultClient)
			require.NoError(t, err)
			defer manager.Close()
			h := NewAgentGatewayHandler(manager, nil, &lib.Config{}, nil)
			ctx := &fasthttp.RequestCtx{}
			ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, bypassed)
			ctx.Request.SetBodyString(fmt.Sprintf(`{"name":"fixture","agent_card_url":"https://agent.example.com/card","enabled":false,"require_public_target":%t}`, !bypassed))
			h.create(ctx)
			require.Equal(t, fasthttp.StatusCreated, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			require.Equal(t, bypassed, store.registrations["fixture"].RequirePublicTarget)
		})
	}
}

// TestAgentGatewayBypassedSecretReferences rejects operator-owned credentials on each management entry point.
func TestAgentGatewayBypassedSecretReferences(t *testing.T) {
	for _, reference := range []string{"env.AGENT_SECRET", "vault.agent-secret", "<REDACTED>"} {
		for _, operation := range []string{"create", "update", "inspect"} {
			t.Run(operation+"/"+reference, func(t *testing.T) {
				h := &AgentGatewayHandler{}
				ctx := &fasthttp.RequestCtx{}
				ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
				ctx.Request.SetBodyString(fmt.Sprintf(`{"name":"fixture","agent_card_url":"https://agent.example.com/card","discovery_auth":{"type":"headers","headers":{"Authorization":%q}}}`, reference))
				switch operation {
				case "create":
					h.create(ctx)
				case "update":
					h.update(ctx)
				case "inspect":
					h.inspect(ctx)
				}
				require.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
			})
		}
	}
}

// TestAgentGatewayBypassedRuntimeAndTLSReferences covers credentials outside discovery headers.
func TestAgentGatewayBypassedRuntimeAndTLSReferences(t *testing.T) {
	for _, field := range []string{"runtime_auth", "discovery_auth"} {
		for _, auth := range []string{`{"type":"oauth","oauth":{"client_id":"env.AGENT_ID","client_secret":"vault.agent-secret"}}`, `{"type":"none","tls":{"client_cert_pem":"env.AGENT_CERT","client_key_pem":"vault.agent-key"}}`} {
			t.Run(field+"/"+auth, func(t *testing.T) {
				h := &AgentGatewayHandler{}
				ctx := &fasthttp.RequestCtx{}
				ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
				ctx.Request.SetBodyString(fmt.Sprintf(`{"name":"fixture",%q:%s}`, field, auth))
				h.create(ctx)
				require.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
			})
		}
	}
}
