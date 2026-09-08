package governance

// What a credential resolved to decides whether a request may proceed at all, and this file asserts that
// through the hook a deployment actually calls rather than through the verdict behind it. Two things exist
// only on that path: what the caller is answered with (a short circuit carrying a status a client can act
// on rather than a decision struct) and the mark a refusal leaves on the request, which is what keeps a
// request that never reached a provider from being billed for the tokens it never spent, and which the
// allow path has to take back off again before a fallback retry is billed for the attempt that did run.
//
// The two levels genuinely answer differently, which is why the refusals are asserted at the outer one:
// the access checks allow a request no access was resolved for (an allowlist there is none of cannot deny
// anything) while the funnel in front of them refuses it, because only the funnel asks what the request
// carried rather than what it resolved to.

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusalTestUnknownVKValue is a key value no store in this file serves, standing in for one that was
// issued and has since been deleted or revoked.
const refusalTestUnknownVKValue = "sk-bf-refusal-revoked"

// refusalTestPlugin builds a plugin serving exactly one virtual key, with mutate free to make that key
// unusable. Mandatory authentication is off throughout this file, so nothing here is refused for
// failing to present a credential: every refusal below is the funnel answering for what the presented
// one resolved to.
func refusalTestPlugin(t *testing.T, mutate func(vk *configstoreTables.TableVirtualKey)) *GovernancePlugin {
	t.Helper()
	vk := buildVKForMCPStamping([]string{"read_file"})
	if mutate != nil {
		mutate(vk)
	}
	return newAccessTestPlugin(t, vk, nil)
}

// refusalTestRefusalFor runs a chat request through the LLM hook and returns the error the caller is
// answered with. Going through the hook is the point: it is the whole path a request takes, and the
// caller never sees the verdict behind the error.
func refusalTestRefusalFor(t *testing.T, plugin *GovernancePlugin, ctx *schemas.BifrostContext) *schemas.BifrostError {
	t.Helper()
	_, shortCircuit, err := plugin.PreLLMHook(ctx, newChatRequest())
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "the request reached the provider instead of being turned away")
	require.NotNil(t, shortCircuit.Error, "a refused request has to carry the reason it was refused")
	require.NotNil(t, shortCircuit.Error.Error)

	// Every refusal also marks the request, which is what keeps a request that never ran from being
	// billed for the tokens it never spent.
	assert.Equal(t, true, ctx.Value(governanceRejectedContextKey),
		"a refused request is marked refused for everything downstream")

	return shortCircuit.Error
}

// A credential that was presented and resolved to nothing is a failed authentication. Nothing resolved
// is not the same as nothing restricting the request: were it read as the latter, deleting a key would
// promote every request made with it to what a request carrying no credential at all has, which is
// unrestricted, the widest access there is.
func TestPreLLMHookRefusesACredentialThatResolvesToNothing(t *testing.T) {
	plugin := refusalTestPlugin(t, nil)
	ctx := presentCtx(refusalTestUnknownVKValue)

	bifrostErr := refusalTestRefusalFor(t, plugin, ctx)

	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, 401, *bifrostErr.StatusCode, "authentication failed rather than permission denied")
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionAccessNotFound), *bifrostErr.Type)
	assert.Contains(t, bifrostErr.Error.Message, "credential", "the caller is told the credential is the problem")

	assert.Nil(t, ctx.Grant().Access(),
		"nothing was resolved for the request, and that must not be recorded as an access permitting everything")
}

// A key switched off may not be used, and the refusal says so in words its holder can act on: the
// answer to a disabled key is not that some budget ran out, nor a bare 403.
func TestPreLLMHookRefusesAnInactiveGrant(t *testing.T) {
	plugin := refusalTestPlugin(t, func(vk *configstoreTables.TableVirtualKey) {
		inactive := false
		vk.IsActive = &inactive
	})

	bifrostErr := refusalTestRefusalFor(t, plugin, newPreRequestCtx(nil, nil))

	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, 403, *bifrostErr.StatusCode, "the credential is real, so this is a permission answer")
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionAccessBlocked), *bifrostErr.Type)
	assert.Contains(t, bifrostErr.Error.Message, "virtual key is inactive",
		"the refusal names what is inactive, so a deployment granting access through something else does not report it as a key")
}

// Expiry is the other half of the same gate, and is reported distinctly from being switched off: a key
// that ran out is renewed and a key that was disabled is re-enabled, and the holder needs to know which.
func TestPreLLMHookRefusesAnExpiredGrant(t *testing.T) {
	past := time.Now().UTC().Add(-time.Second)
	plugin := refusalTestPlugin(t, func(vk *configstoreTables.TableVirtualKey) { vk.ExpiresAt = &past })

	bifrostErr := refusalTestRefusalFor(t, plugin, newPreRequestCtx(nil, nil))

	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, 403, *bifrostErr.StatusCode)
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionAccessBlocked), *bifrostErr.Type)
	assert.Contains(t, bifrostErr.Error.Message, "virtual key has expired")
	assert.NotContains(t, bifrostErr.Error.Message, "inactive", "run out is not switched off")
}

// The contrast the refusals above must not swallow. A request presenting nothing, where nothing obliges
// it to, is what every deployment that has not turned on governance looks like: it is allowed, and it
// carries no access at all, which every consumer reads as unrestricted.
//
// It also has to come out of the hook unmarked, which is why it goes in marked. A request refused on its
// first attempt and then retried against a fallback arrives here still carrying the mark of the attempt
// that failed; were the allow path not to take it off, the attempt that did reach a provider would look
// like one that never ran, and the tokens it really spent would go uncharged.
func TestPreLLMHookAllowsARequestThatPresentsNothing(t *testing.T) {
	plugin := refusalTestPlugin(t, nil)
	ctx := emptyCtx()
	// The state a refused first attempt leaves behind before a fallback is tried.
	ctx.SetValue(governanceRejectedContextKey, true)

	_, shortCircuit, err := plugin.PreLLMHook(ctx, newChatRequest())

	require.NoError(t, err)
	require.Nil(t, shortCircuit, "nothing obliged the request to authenticate and nothing restricts it")
	assert.Nil(t, ctx.Value(governanceRejectedContextKey), "an allowed request does not stay marked refused")

	assert.Nil(t, ctx.Grant().Access(), "nothing granted it anything, so it carries no access")
	require.NotNil(t, ctx.Grant().Limits(), "and still answers to the deployment's own limits, settled on its grant")
}

// Why the refusals above are asserted at the funnel every hook calls rather than one level down: handed
// the same request, the access checks allow it. That is deliberate on both sides. The checks apply what a
// request may reach, and a request nothing was resolved for has no allowlist to be measured against, so
// they have nothing to deny it with: they answer for an access, never for the credential behind it, and
// telling a presented-but-unresolvable credential apart from an absent one needs what the request carried.
//
// This characterises that answer rather than a path production leans on: the funnel refuses such a request
// before the checks ever see it, so a later decision to fail closed here would be a hardening rather than
// a regression, but it would be one made deliberately, not by a check quietly changing its mind.
func TestTheAccessChecksAllowACredentialThatResolvedToNothing(t *testing.T) {
	plugin := refusalTestPlugin(t, nil)
	ctx := resolverCtx(plugin.store, refusalTestUnknownVKValue)
	require.Nil(t, ctx.Grant().Access(), "the store resolved no access for this credential")

	allowed := evaluateVirtualKey(plugin.resolver, ctx, refusalTestUnknownVKValue,
		schemas.OpenAI, "gpt-4o", schemas.ChatCompletionRequest, false, false)

	assertDecision(t, DecisionAllow, allowed)
}
