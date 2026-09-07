package grant

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A permit on its own, before anything folds it with another: what it permits, what governs it,
// and the projections that build one. Composition lives in access_test.go, limits in
// limit_test.go.

func TestNewPermit(t *testing.T) {
	providerPermits := []schemas.ProviderPermit{{Provider: "openai", AllowedModels: []string{"*"}, KeyIDs: []string{"key-1"}}}
	mcpPermits := []schemas.MCPPermit{{Client: "github-id", ClientName: "github", Tools: []string{"*"}}}

	t.Run("what it was built from is what the getters return", func(t *testing.T) {
		permit := NewPermit(PermitVirtualKey, "vk1", "Caller Key", true, true, providerPermits, mcpPermits)

		assert.Equal(t, string(PermitVirtualKey), permit.Type())
		assert.Equal(t, "vk1", permit.ID())
		assert.Equal(t, "Caller Key", permit.Name())
		assert.True(t, permit.IsActive())
		assert.True(t, permit.IsExpired())
		assert.Equal(t, providerPermits, permit.ProviderPermits())
		assert.Equal(t, mcpPermits, permit.MCPPermits())
	})

	t.Run("the lists are copied", func(t *testing.T) {
		// A resolver that keeps its slices cannot alter what an attempt was admitted under.
		mine := append([]schemas.ProviderPermit(nil), providerPermits...)
		myTools := append([]schemas.MCPPermit(nil), mcpPermits...)
		permit := NewPermit(PermitVirtualKey, "vk1", "Caller Key", true, false, mine, myTools)

		mine[0].Provider = "mutated"
		myTools[0].ClientName = "mutated"

		assert.Equal(t, "openai", permit.ProviderPermits()[0].Provider)
		assert.Equal(t, "github", permit.MCPPermits()[0].ClientName)
	})

	t.Run("the copy goes deep, not just the outer list", func(t *testing.T) {
		// Cloning the outer slice copies each entry's struct, but AllowedModels, BlacklistedModels,
		// KeyIDs, Tools, and the value behind Weight are themselves shared unless copied too. A
		// resolver mutating those after the permit was built must not alter what it already
		// admitted.
		weight := 0.4
		allowedModels := []string{"gpt-4o"}
		blacklistedModels := []string{"o3"}
		keyIDs := []string{"key-1"}
		tools := []string{"read_file"}
		permit := NewPermit(PermitVirtualKey, "vk1", "Caller Key", true, false,
			[]schemas.ProviderPermit{{
				Provider: "openai", AllowedModels: allowedModels, BlacklistedModels: blacklistedModels,
				KeyIDs: keyIDs, Weight: &weight,
			}},
			[]schemas.MCPPermit{{Client: "github-id", ClientName: "github", Tools: tools}},
		)

		allowedModels[0] = "mutated"
		blacklistedModels[0] = "mutated"
		keyIDs[0] = "mutated"
		tools[0] = "mutated"
		weight = 999

		gotProvider := permit.ProviderPermits()[0]
		assert.Equal(t, schemas.WhiteList{"gpt-4o"}, gotProvider.AllowedModels)
		assert.Equal(t, schemas.BlackList{"o3"}, gotProvider.BlacklistedModels)
		assert.Equal(t, schemas.WhiteList{"key-1"}, gotProvider.KeyIDs)
		require.NotNil(t, gotProvider.Weight)
		assert.Equal(t, 0.4, *gotProvider.Weight)

		assert.Equal(t, schemas.WhiteList{"read_file"}, permit.MCPPermits()[0].Tools)
	})

	t.Run("mutating what a getter returned does not alter what a later reader sees", func(t *testing.T) {
		// The permit is a snapshot for the whole attempt, read by every consumer that asks what it
		// permits. A getter handing back its own slice would let whichever reads first corrupt what
		// the rest read after it.
		weight := 0.4
		permit := NewPermit(PermitVirtualKey, "vk1", "Caller Key", true, false,
			[]schemas.ProviderPermit{{
				Provider: "openai", AllowedModels: []string{"gpt-4o"}, Weight: &weight,
			}},
			[]schemas.MCPPermit{{Client: "github-id", ClientName: "github", Tools: []string{"read_file"}}},
		)

		gotProvider := permit.ProviderPermits()
		gotProvider[0].Provider = "mutated"
		gotProvider[0].AllowedModels[0] = "mutated"
		*gotProvider[0].Weight = 999

		gotMCP := permit.MCPPermits()
		gotMCP[0].Client = "mutated"
		gotMCP[0].Tools[0] = "mutated"

		again := permit.ProviderPermits()[0]
		assert.Equal(t, "openai", again.Provider)
		assert.Equal(t, schemas.WhiteList{"gpt-4o"}, again.AllowedModels)
		require.NotNil(t, again.Weight)
		assert.Equal(t, 0.4, *again.Weight)

		assert.Equal(t, "github-id", permit.MCPPermits()[0].Client)
		assert.Equal(t, schemas.WhiteList{"read_file"}, permit.MCPPermits()[0].Tools)
	})

	t.Run("no lists is no lists", func(t *testing.T) {
		permit := NewPermit(PermitVirtualKey, "vk1", "Caller Key", true, false, nil, nil)
		assert.Empty(t, permit.ProviderPermits())
		assert.Empty(t, permit.MCPPermits())
	})
}

// A permit that is not there permits nothing and governs nothing. Every rule takes a nil permit
// because the sides of an Access are routinely empty (a caller holding no permit of their own, or
// nothing scoping the request) and the fold asks all of them unconditionally rather than guarding
// each call. A typed nil pointer has to read as nothing too, since that is how an empty side
// arrives through the interface.
func TestPermit_NilReceiver(t *testing.T) {
	var permit *Permit

	assert.Equal(t, "", permit.Type())
	assert.Equal(t, "", permit.ID())
	assert.Equal(t, "", permit.Name())
	assert.False(t, permit.IsActive())
	assert.False(t, permit.IsExpired())
	assert.Nil(t, permit.ProviderPermits())
	assert.Nil(t, permit.MCPPermits())

	assert.True(t, isNilPermit(permit))
	assert.True(t, isNilPermit(nil))
	assert.False(t, allowsProvider(permit, "openai"))
	assert.False(t, blacklistsModel(permit, "openai", "gpt-4o"))
	assert.False(t, allowsTool(permit, "github-read_file"))
	assert.False(t, allowsModelByName(permit, "openai", "gpt-4o"))
	assert.Nil(t, providerPermitFor(permit, "openai"))
	assert.Nil(t, weightedProviderPermitFor(permit, "openai"))

	visited := false
	assert.True(t, eachProviderPermit(permit, func(*schemas.ProviderPermit) bool {
		visited = true
		return true
	}), "nothing to walk is a walk that completed")
	assert.False(t, visited)

	// An empty list rather than nil: no tool may be executed, which a consumer must be able to
	// tell from no answer at all.
	entries := mcpEntries(permit)
	assert.NotNil(t, entries)
	assert.Empty(t, entries)
}

func TestAllowsModelByName(t *testing.T) {
	permit := newPermit(permitSpec{
		Type: PermitVirtualKey, ID: "vk1",
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: []string{"gpt-4o"}, BlacklistedModels: []string{"o3"}},
			{Provider: "anthropic", AllowedModels: []string{"*"}},
		},
	})

	assert.True(t, allowsModelByName(permit, "openai", "gpt-4o"))
	assert.False(t, allowsModelByName(permit, "openai", "gpt-4o-mini"), "outside the allowlist")
	assert.False(t, allowsModelByName(permit, "openai", "o3"), "blacklisted")
	assert.False(t, allowsModelByName(permit, "cohere", "command-r"), "provider not held")

	// No model is nothing to filter on, so holding the provider is the whole question. This is
	// what lets a listing ask "which providers are granted at all".
	assert.True(t, allowsModelByName(permit, "openai", ""))
	assert.True(t, allowsModelByName(permit, "anthropic", ""))
	assert.False(t, allowsModelByName(permit, "cohere", ""))

	t.Run("membership ignores case", func(t *testing.T) {
		assert.True(t, allowsModelByName(permit, "openai", "GPT-4o"))
		assert.False(t, allowsModelByName(permit, "openai", "O3"))
	})

	t.Run("a later provider permit's blacklist still blocks", func(t *testing.T) {
		// Two provider permits for the same provider are funded separately (two provider configs
		// on one key, say), but a blacklist on either one blocks the provider for that model
		// outright: the first entry allowing a model must not return before a later entry's
		// blacklist is checked.
		twoEntries := newPermit(permitSpec{
			Type: PermitVirtualKey, ID: "vk2",
			ProviderPermits: []schemas.ProviderPermit{
				{Provider: "openai", AllowedModels: []string{"*"}},
				{Provider: "openai", BlacklistedModels: []string{"gpt-4"}},
			},
		})
		assert.False(t, allowsModelByName(twoEntries, "openai", "gpt-4"))
		assert.True(t, allowsModelByName(twoEntries, "openai", "gpt-4o"))
	})
}

func TestProviderPermitAllowsModel(t *testing.T) {
	pp := &schemas.ProviderPermit{Provider: "openai", AllowedModels: []string{"gpt-4o"}, BlacklistedModels: []string{"gpt-4o-mini"}}

	assert.True(t, providerPermitAllowsModel(pp, "gpt-4o"))
	assert.False(t, providerPermitAllowsModel(pp, "gpt-4o-mini"))
	assert.False(t, providerPermitAllowsModel(pp, "o3"))
	assert.True(t, providerPermitAllowsModel(pp, ""), "no model is nothing to filter on")

	everything := &schemas.ProviderPermit{Provider: "openai", AllowedModels: []string{"*"}, BlacklistedModels: []string{"o3"}}
	assert.True(t, providerPermitAllowsModel(everything, "anything"))
	assert.False(t, providerPermitAllowsModel(everything, "o3"), "the blacklist wins over the wildcard")

	nothing := &schemas.ProviderPermit{Provider: "openai"}
	assert.False(t, providerPermitAllowsModel(nothing, "gpt-4o"), "an empty allowlist permits nothing")
}

func TestProviderPermitFor(t *testing.T) {
	permit := newPermit(permitSpec{
		Type: PermitVirtualKey, ID: "vk1",
		ProviderPermits: []schemas.ProviderPermit{
			{Provider: "openai", KeyIDs: []string{"key-a"}},
			{Provider: "openai", KeyIDs: []string{"key-b"}, Weight: ptr(0.4)},
			{Provider: "bedrock", KeyIDs: []string{"key-c"}},
		},
	})

	t.Run("the first provider permit for the provider", func(t *testing.T) {
		found := providerPermitFor(permit, "openai")
		require.NotNil(t, found)
		assert.Equal(t, schemas.WhiteList{"key-a"}, found.KeyIDs)
		assert.Nil(t, providerPermitFor(permit, "cohere"))
	})

	t.Run("the first that sets a weight", func(t *testing.T) {
		found := weightedProviderPermitFor(permit, "openai")
		require.NotNil(t, found)
		assert.Equal(t, schemas.WhiteList{"key-b"}, found.KeyIDs)
		assert.Nil(t, weightedProviderPermitFor(permit, "bedrock"), "held, and unweighted")
		assert.Nil(t, weightedProviderPermitFor(permit, "cohere"))
	})
}

// eachProviderPermit is the walk the fold shares, and it stops when the visitor says so: the
// coarse gate uses that to stop once it has an answer.
func TestEachProviderPermit(t *testing.T) {
	permit := permitWithProviders(PermitVirtualKey, "vk1", "Key", "openai", "anthropic", "bedrock")

	t.Run("visits every provider permit", func(t *testing.T) {
		seen := []string{}
		completed := eachProviderPermit(permit, func(pp *schemas.ProviderPermit) bool {
			seen = append(seen, pp.Provider)
			return true
		})
		assert.True(t, completed)
		assert.Equal(t, []string{"openai", "anthropic", "bedrock"}, seen)
	})

	t.Run("stops when the visitor says to, and reports it", func(t *testing.T) {
		seen := []string{}
		completed := eachProviderPermit(permit, func(pp *schemas.ProviderPermit) bool {
			seen = append(seen, pp.Provider)
			return pp.Provider != "anthropic"
		})
		assert.False(t, completed, "the caller has to know the walk was cut short")
		assert.Equal(t, []string{"openai", "anthropic"}, seen)
	})

	t.Run("a provider named by nothing but whitespace is skipped", func(t *testing.T) {
		// No comparison anywhere would match it, so it could only be selected and then fail
		// downstream.
		blank := newPermit(permitSpec{Type: PermitVirtualKey, ID: "vk2", ProviderPermits: []schemas.ProviderPermit{
			{Provider: "  ", AllowedModels: []string{"*"}},
			{Provider: "openai", AllowedModels: []string{"*"}},
		}})
		seen := []string{}
		eachProviderPermit(blank, func(pp *schemas.ProviderPermit) bool {
			seen = append(seen, pp.Provider)
			return true
		})
		assert.Equal(t, []string{"openai"}, seen)
	})
}

// mcpEntries expands a permit's MCP permits into the tool patterns it permits. The rules that are
// easy to get wrong: which MCP permit decides for a client, and what an unrestricted client
// expands to.
func TestMCPEntries(t *testing.T) {
	t.Run("specific tools become one entry each", func(t *testing.T) {
		permit := permitWithTools(PermitVirtualKey, "vk1", "Key", "github", "read_file", "list_issues")
		assert.Equal(t, []string{"github-read_file", "github-list_issues"}, mcpEntries(permit))
	})

	t.Run("an unrestricted client becomes a single wildcard", func(t *testing.T) {
		permit := permitWithTools(PermitVirtualKey, "vk1", "Key", "github", "*")
		assert.Equal(t, []string{"github-*"}, mcpEntries(permit))
	})

	t.Run("a client granted no tool contributes nothing", func(t *testing.T) {
		permit := permitWithTools(PermitVirtualKey, "vk1", "Key", "github")
		assert.Empty(t, mcpEntries(permit))
	})

	t.Run("the first MCP permit holding a client decides for it", func(t *testing.T) {
		// A second MCP permit for the same client cannot widen the first, which is what stops a
		// permissive duplicate from reopening a narrowed client.
		permit := newPermit(permitSpec{Type: PermitVirtualKey, ID: "vk1", MCPPermits: []schemas.MCPPermit{
			{Client: "github-id", ClientName: "github", Tools: []string{"read_file"}},
			{Client: "github-id", ClientName: "github", Tools: []string{"*"}},
		}})
		assert.Equal(t, []string{"github-read_file"}, mcpEntries(permit))
	})

	t.Run("a client is identified by its id, so a rename does not split it", func(t *testing.T) {
		permit := newPermit(permitSpec{Type: PermitVirtualKey, ID: "vk1", MCPPermits: []schemas.MCPPermit{
			{Client: "github-id", ClientName: "github", Tools: []string{"read_file"}},
			{Client: "github-id", ClientName: "github-renamed", Tools: []string{"*"}},
		}})
		assert.Equal(t, []string{"github-read_file"}, mcpEntries(permit),
			"the same client under two names is still one client")
	})

	t.Run("with no id, the name identifies the client", func(t *testing.T) {
		permit := newPermit(permitSpec{Type: PermitVirtualKey, ID: "vk1", MCPPermits: []schemas.MCPPermit{
			{ClientName: "github", Tools: []string{"read_file"}},
			{ClientName: "github", Tools: []string{"*"}},
		}})
		assert.Equal(t, []string{"github-read_file"}, mcpEntries(permit))
	})

	t.Run("an MCP permit naming no client is skipped", func(t *testing.T) {
		// Its entries would be "-tool", which matches no client anywhere.
		permit := newPermit(permitSpec{Type: PermitVirtualKey, ID: "vk1", MCPPermits: []schemas.MCPPermit{
			{Client: "orphan-id", Tools: []string{"read_file"}},
			{Client: "github-id", ClientName: "github", Tools: []string{"read_file"}},
		}})
		assert.Equal(t, []string{"github-read_file"}, mcpEntries(permit))
	})

	t.Run("an unnamed tool is skipped", func(t *testing.T) {
		permit := newPermit(permitSpec{Type: PermitVirtualKey, ID: "vk1", MCPPermits: []schemas.MCPPermit{
			{Client: "github-id", ClientName: "github", Tools: []string{"read_file", "", "list_issues"}},
		}})
		assert.Equal(t, []string{"github-read_file", "github-list_issues"}, mcpEntries(permit))
	})

	t.Run("duplicate entries collapse", func(t *testing.T) {
		permit := newPermit(permitSpec{Type: PermitVirtualKey, ID: "vk1", MCPPermits: []schemas.MCPPermit{
			{Client: "github-id", ClientName: "github", Tools: []string{"read_file", "read_file"}},
		}})
		assert.Equal(t, []string{"github-read_file"}, mcpEntries(permit))
	})
}

// allowsTool answers for one pattern rather than expanding the whole list, and the two have to
// agree. A wildcard pattern asks whether the client is granted anything at all.
func TestAllowsTool(t *testing.T) {
	permit := newPermit(permitSpec{Type: PermitVirtualKey, ID: "vk1", MCPPermits: []schemas.MCPPermit{
		{Client: "github-id", ClientName: "github", Tools: []string{"read_file"}},
		{Client: "slack-id", ClientName: "slack", Tools: []string{"*"}},
		{Client: "jira-id", ClientName: "jira", Tools: []string{}},
		{Client: "orphan-id", Tools: []string{"*"}},
	}})

	assert.True(t, allowsTool(permit, "github-read_file"))
	assert.False(t, allowsTool(permit, "github-delete_repo"))
	assert.True(t, allowsTool(permit, "github-*"), "the client is granted some tool")

	assert.True(t, allowsTool(permit, "slack-anything"), "an unrestricted client permits every tool")
	assert.True(t, allowsTool(permit, "slack-*"))

	assert.False(t, allowsTool(permit, "jira-create_issue"), "granted no tool")
	assert.False(t, allowsTool(permit, "jira-*"))

	assert.False(t, allowsTool(permit, "unknown-tool"), "client not held")
	assert.False(t, allowsTool(permit, ""))
	assert.False(t, allowsTool(permit, "github"), "a bare client name is not a tool pattern")

	// An MCP permit naming no client can never match, so it cannot grant through the empty prefix.
	assert.False(t, allowsTool(permit, "-read_file"))

	t.Run("a client is identified by its id, so a rename does not split it, the same as mcpEntries", func(t *testing.T) {
		// Names chosen so neither is a string-prefix of the other's tool patterns: the point is
		// the stable id deciding precedence, not an incidental prefix match papering over it.
		renamed := newPermit(permitSpec{Type: PermitVirtualKey, ID: "vk2", MCPPermits: []schemas.MCPPermit{
			{Client: "acme-id", ClientName: "acme", Tools: []string{"read_file"}},
			{Client: "acme-id", ClientName: "bigco", Tools: []string{"*"}},
		}})
		assert.True(t, allowsTool(renamed, "acme-read_file"), "the first entry for the client still grants what it grants")
		assert.False(t, allowsTool(renamed, "acme-delete_repo"), "the first entry's narrower grant is not widened by the later, renamed one")
		assert.False(t, allowsTool(renamed, "bigco-read_file"),
			"the renamed entry never gets asked: the first entry already decided for this client, matching mcpEntries")
	})
}

// A refusal is read by whoever made the request, so no kind this package declares may render as a
// machine identifier. The switch translates the ones whose value does not read as prose; the rest
// fall through to their own value, which is fine only while that value is a word.
//
// The assertion that carries this is the underscore one: it is what fails if a kind is declared
// with an underscored value and nobody adds it to the switch, which is the way this actually goes
// wrong.
func TestPermitTypePrettyStringNeverRendersAnIdentifier(t *testing.T) {
	for _, tc := range []struct {
		kind PermitType
		want string
	}{
		{PermitVirtualKey, "virtual key"},
		{PermitAccessProfile, "access profile"},
		// Served by the default, because "project" is already the word a refusal should say.
		{PermitProject, "project"},
	} {
		assert.Equal(t, tc.want, tc.kind.PrettyString())
		assert.NotContains(t, tc.kind.PrettyString(), "_", "a refusal must not read as an identifier")
	}

	// A kind nobody declared still renders, because a refusal that loses its subject cannot be
	// acted on at all.
	assert.Equal(t, "something_else", PermitType("something_else").PrettyString())
}
