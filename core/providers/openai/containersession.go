package openai

import (
	"github.com/maximhq/bifrost/core/schemas"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
)

// stampContainerSessions records, on the response's usage, how many code-execution
// sandbox sessions this response owes payment for.
//
// A container created implicitly by the code interpreter tool never reaches the
// gateway as a container-create request, so nothing else on the path can see that a
// sandbox was provisioned. The only evidence is the container id echoed on each
// code_interpreter_call output item.
//
// The count is decided here, at response conversion, rather than in the pricing
// engine, because pricing runs several times per request (governance, telemetry and
// logging each call it independently) and a first-seen rule evaluated there would
// bill one caller and return zero to the rest. Deciding once and stamping the result
// also means RecalculateCosts replays the original decision instead of re-running it
// against a registry whose claims have long since expired.
//
// Only OpenAI and Azure are considered. Other providers reaching these shared
// handlers do not offer a hosted code interpreter, and at least one provider
// elsewhere in the tree populates ContainerID with a tool-use id rather than a real
// container, which would otherwise look like a fresh sandbox on every turn.
func stampContainerSessions(response *schemas.BifrostResponsesResponse, providerName schemas.ModelProvider, logger schemas.Logger) {
	if response == nil || !providerHostsCodeInterpreter(providerName) {
		return
	}

	sessions := 0
	for _, seen := range distinctContainerIDs(response) {
		key := string(providerName) + ":" + seen
		if providerUtils.ContainerSessions.Claim(key) {
			sessions++
			if logger != nil {
				logger.Debug("billing a code interpreter container session for %s", key)
			}
		} else if logger != nil {
			logger.Debug("skipping already-billed code interpreter container %s", key)
		}
	}
	if sessions == 0 {
		return
	}

	if response.Usage == nil {
		response.Usage = &schemas.ResponsesResponseUsage{}
	}
	if response.Usage.OutputTokensDetails == nil {
		response.Usage.OutputTokensDetails = &schemas.ResponsesResponseOutputTokens{}
	}
	response.Usage.OutputTokensDetails.NumContainerSessions = schemas.Ptr(sessions)
}

// providerHostsCodeInterpreter reports whether a provider runs OpenAI's hosted
// code interpreter, and therefore bills for its containers.
func providerHostsCodeInterpreter(providerName schemas.ModelProvider) bool {
	return providerName == schemas.OpenAI || providerName == schemas.Azure
}

// distinctContainerIDs collects the container ids named by the response's
// code_interpreter_call items, in first-seen order and without repeats. A single
// response can call the interpreter several times against one container, which is
// one session, not several.
func distinctContainerIDs(response *schemas.BifrostResponsesResponse) []string {
	var ids []string
	seen := make(map[string]struct{})
	for i := range response.Output {
		toolMessage := response.Output[i].ResponsesToolMessage
		if toolMessage == nil || toolMessage.ResponsesCodeInterpreterToolCall == nil {
			continue
		}
		id := toolMessage.ResponsesCodeInterpreterToolCall.ContainerID
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}
