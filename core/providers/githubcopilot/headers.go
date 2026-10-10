package githubcopilot

import (
	"github.com/google/uuid"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// Editor identity. Copilot validates that a caller looks like a supported editor and
// rejects requests that look like generic API clients.
//
// These three move together. copilot-integration-id selects the integration Copilot
// thinks it is serving, and the two version strings must be consistent with it and with
// the User-Agent below. Bump them as one unit when GitHub tightens validation; changing
// one alone produces a 4xx that reads like an auth failure.
//
// None of this is covered by a published GitHub contract. It is derived from the VS Code
// Copilot Chat extension's own request shape.
const (
	copilotIntegrationID = "vscode-chat"
	editorVersion        = "vscode/1.111.0"
	editorPluginVersion  = "copilot-chat/0.40.0"
	copilotUserAgent     = "GitHubCopilotChat/0.40.0"
)

const (
	openAIIntent     = "conversation-panel"
	githubAPIVersion = "2025-04-01"
)

// buildAuthHeaders returns the bearer token plus the editor identity headers.
//
// These belong in the handlers' authHeader map rather than in extraHeaders. Handlers
// apply extraHeaders through providerUtils.SetExtraHeaders, which only sets a header when
// it is absent and runs before the authHeader loop, so an extraHeaders value can be
// silently dropped. A wrong editor-version breaks every request, so these must not be
// overridable by accident.
//
// hasImageContent adds copilot-vision-request, which Copilot requires on turns carrying
// image input and which must be absent otherwise.
func buildAuthHeaders(creds *copilotCredentials, hasImageContent bool) map[string]string {
	headers := map[string]string{
		"Authorization":          "Bearer " + creds.Token,
		"Copilot-Integration-Id": copilotIntegrationID,
		"Editor-Version":         editorVersion,
		"Editor-Plugin-Version":  editorPluginVersion,
		"User-Agent":             copilotUserAgent,
		"Openai-Intent":          openAIIntent,
		"X-Github-Api-Version":   githubAPIVersion,
		"X-Request-Id":           uuid.NewString(),
	}
	if hasImageContent {
		headers["Copilot-Vision-Request"] = "true"
	}
	return headers
}

// chatRequestHasImageContent reports whether any message in the request carries an image
// block. Only content blocks can hold images; a plain string content never does.
func chatRequestHasImageContent(request *schemas.BifrostChatRequest) bool {
	if request == nil {
		return false
	}
	for _, message := range request.Input {
		if message.Content == nil {
			continue
		}
		for _, block := range message.Content.ContentBlocks {
			if block.Type == schemas.ChatContentBlockTypeImage {
				return true
			}
		}
	}
	return false
}
