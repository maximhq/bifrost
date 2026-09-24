package governance

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/stretchr/testify/assert"
)

// gatewayCtx builds an MCP-gateway request context with the given Virtual MCPs recorded as
// addressable, the way resolution records them during a real /mcp request.
func gatewayCtx(vmcps ...configstoreTables.TableVirtualMCP) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	ctx.SetValue(schemas.BifrostContextKeyIsMCPGateway, true)
	for i := range vmcps {
		RecordGrantedVirtualMCP(ctx, &vmcps[i])
	}
	return ctx
}

// TestVirtualMCPToolAccess covers the /mcp/<slug> resolution: the slug's Virtual MCP must have been
// recorded as addressable during resolution (else refused), its tools are named through the
// client-name map, and are narrowed to what the request's access permits.
func TestVirtualMCPToolAccess(t *testing.T) {
	names := map[string]string{"cA": "alpha", "cB": "beta"}
	store := func(clientNames map[string]string) *LocalGovernanceStore {
		return &LocalGovernanceStore{inMemoryStore: &mockInMemoryStore{clientNames: clientNames}, logger: NewMockLogger()}
	}
	// The addressable vMCP "g" grants alpha read+write and every tool of beta.
	assigned := configstoreTables.TableVirtualMCP{
		Name: "grp", EndpointSlug: "g", Enabled: true,
		ParsedTools: []configstoreTables.MCPToolSpec{spec("cA", "read", "write"), spec("cB", grant.Wildcard)},
	}

	t.Run("a slug not recorded as addressable is refused", func(t *testing.T) {
		served, _, ok := store(names).VirtualMCPToolAccess(gatewayCtx(assigned), "unknown", nil)
		assert.False(t, ok)
		assert.Nil(t, served)
	})

	t.Run("a request with nothing recorded is refused", func(t *testing.T) {
		served, _, ok := store(names).VirtualMCPToolAccess(gatewayCtx(), "g", nil)
		assert.False(t, ok)
		assert.Nil(t, served)
	})

	t.Run("nil access serves the vMCP's own tools, named through the client map", func(t *testing.T) {
		served, _, ok := store(names).VirtualMCPToolAccess(gatewayCtx(assigned), "g", nil)
		assert.True(t, ok)
		assert.ElementsMatch(t, []string{"alpha-read", "alpha-write", "beta-" + grant.Wildcard}, served)
	})

	t.Run("access narrows the served tools to what the request may reach", func(t *testing.T) {
		// The request's access permits only alpha-read.
		accessPermit := permitFor(buildVKNoMCPConfigs(), []configstoreTables.TableVirtualMCP{
			{Name: "acc", EndpointSlug: "acc", Enabled: true, ParsedTools: []configstoreTables.MCPToolSpec{spec("cA", "read")}},
		}, names, nil)
		access := grant.NewAccess([]schemas.Permit{accessPermit}, nil, "", nil)

		served, _, ok := store(names).VirtualMCPToolAccess(gatewayCtx(assigned), "g", access)
		assert.True(t, ok)
		assert.Equal(t, []string{"alpha-read"}, served, "alpha-write and beta are excluded by the access")
	})

	t.Run("a client no longer in the name map is skipped", func(t *testing.T) {
		served, _, ok := store(map[string]string{"cA": "alpha"}).VirtualMCPToolAccess(gatewayCtx(assigned), "g", nil)
		assert.True(t, ok)
		assert.ElementsMatch(t, []string{"alpha-read", "alpha-write"}, served, "beta (cB) is dropped: no longer configured")
	})

	t.Run("recording is a no-op off the gateway path", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
		RecordGrantedVirtualMCP(ctx, &assigned) // no IsMCPGateway flag: must not record
		served, _, ok := store(names).VirtualMCPToolAccess(ctx, "g", nil)
		assert.False(t, ok)
		assert.Nil(t, served)
	})
}

// TestVirtualMCPInstructionsResolution covers the half the gateway handler fakes.
func TestVirtualMCPInstructionsResolution(t *testing.T) {
	names := map[string]string{"cA": "alpha"}
	store := func() *LocalGovernanceStore {
		return &LocalGovernanceStore{inMemoryStore: &mockInMemoryStore{clientNames: names}, logger: NewMockLogger()}
	}
	vmcp := func(text *string, mode string) configstoreTables.TableVirtualMCP {
		return configstoreTables.TableVirtualMCP{
			Name: "grp", EndpointSlug: "g", Enabled: true,
			Instructions: text, InstructionsMode: mode,
			ParsedTools: []configstoreTables.MCPToolSpec{spec("cA", "read")},
		}
	}
	text := "Read-only lookups here."

	// Both modes explicitly: a stored "append" takes a different path from an empty one.
	for _, mode := range []schemas.MCPVirtualInstructionsMode{
		schemas.MCPVirtualInstructionsModeAppend,
		schemas.MCPVirtualInstructionsModeReplace,
	} {
		t.Run("text and mode reach the caller: "+string(mode), func(t *testing.T) {
			_, instructions, ok := store().VirtualMCPToolAccess(gatewayCtx(vmcp(&text, string(mode))), "g", nil)
			assert.True(t, ok)
			assert.Equal(t, "grp", instructions.Name)
			assert.Equal(t, text, instructions.Text)
			assert.Equal(t, mode, instructions.Mode)
		})
	}

	t.Run("an unset mode defaults to append, never empty", func(t *testing.T) {
		// A row written before the column existed has mode "", which must not reach the
		// combiner as an unrecognized value.
		_, instructions, ok := store().VirtualMCPToolAccess(gatewayCtx(vmcp(&text, "")), "g", nil)
		assert.True(t, ok)
		assert.Equal(t, schemas.MCPVirtualInstructionsModeAppend, instructions.Mode)
	})

	t.Run("no text means inherit only", func(t *testing.T) {
		_, instructions, ok := store().VirtualMCPToolAccess(gatewayCtx(vmcp(nil, "append")), "g", nil)
		assert.True(t, ok)
		assert.Empty(t, instructions.Text)
	})

	t.Run("a refused slug carries no instructions", func(t *testing.T) {
		_, instructions, ok := store().VirtualMCPToolAccess(gatewayCtx(vmcp(&text, "replace")), "unknown", nil)
		assert.False(t, ok)
		assert.Empty(t, instructions.Text, "a refusal must not leak the text of a vMCP the caller cannot reach")
	})
}
