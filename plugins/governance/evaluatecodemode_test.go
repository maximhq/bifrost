package governance

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codeModeMetaTools are the names core records on BifrostContextKeyMCPAddedTools when it
// injects the code mode meta-tools. They are bare names, not "<client>-<tool>", and belong to
// no client, so no grant can list them.
var codeModeMetaTools = []string{"listToolFiles", "readToolFile", "getToolDocs", "executeToolCode"}

// evaluateWithAddedMCPTools evaluates a request for a key granted grantedTools on the "sentry"
// client, after injection has put the given tools on BifrostContextKeyMCPAddedTools: the state
// the tool check in Evaluate sees.
func evaluateWithAddedMCPTools(t *testing.T, grantedTools []string, added ...string) (*EvaluationResult, *schemas.BifrostError) {
	t.Helper()
	plugin := newPluginForMCPStamping(t, buildVKForMCPStamping(grantedTools), false)
	ctx := presentCtx(mcpTestVKValue)
	for _, tool := range added {
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyMCPAddedTools, tool)
	}
	return plugin.Evaluate(ctx, &EvaluationRequest{Provider: schemas.OpenAI, Model: "gpt-4o"})
}

// A grant lists "<client>-<tool>" entries, which a bare meta-tool name can never match. Checking
// the meta-tools against it would refuse every governed request that has code mode enabled, even
// when the grant permits every tool the request is allowed to reach.
func TestEvaluate_CodeModeMetaToolsDoNotBlockAGrantedRequest(t *testing.T) {
	added := append([]string{"sentry-search"}, codeModeMetaTools...)

	result, bifrostErr := evaluateWithAddedMCPTools(t, []string{"search"}, added...)

	require.Nil(t, bifrostErr)
	assertDecision(t, DecisionAllow, result)
}

func TestEvaluate_CodeModeMetaToolsAloneDoNotBlock(t *testing.T) {
	result, bifrostErr := evaluateWithAddedMCPTools(t, []string{"search"}, codeModeMetaTools...)

	require.Nil(t, bifrostErr)
	assertDecision(t, DecisionAllow, result)
}

// Skipping the meta-tools must not widen anything else: a client tool the grant does not name is
// still refused, and the refusal names that tool rather than a meta-tool.
func TestEvaluate_UngrantedClientToolIsStillBlockedAlongsideCodeModeMetaTools(t *testing.T) {
	added := append([]string{"sentry-search", "sentry-delete"}, codeModeMetaTools...)

	result, bifrostErr := evaluateWithAddedMCPTools(t, []string{"search"}, added...)

	require.NotNil(t, bifrostErr)
	assertDecision(t, DecisionMCPToolBlocked, result)
	assert.Contains(t, result.Reason, "sentry-delete")
	for _, metaTool := range codeModeMetaTools {
		assert.NotContains(t, result.Reason, metaTool)
	}
}
