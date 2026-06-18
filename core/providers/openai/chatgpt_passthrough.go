package openai

import (
	"encoding/base64"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	chatGPTAccountIDKey = "chatgpt_account_id"
	openAIAuthClaim     = "https://api.openai.com/auth"

	// ChatGPTCodexURL is the full upstream URL for ChatGPT subscription token requests.
	ChatGPTCodexURL = "https://chatgpt.com/backend-api/codex/responses"
)

// ParseChatGPTJWT parses a raw bearer token, checks for the ChatGPT subscription
// JWT claim, and returns the chatgpt_account_id. No signature verification is
// Returns ("", false) for any non-ChatGPT or malformed token.
func ParseChatGPTJWT(token string) (accountID string, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}

	// Extract the nested claim: {"https://api.openai.com/auth": {"chatgpt_account_id": "..."}}
	var claims map[string]interface{}
	if err := sonic.Unmarshal(payload, &claims); err != nil {
		return "", false
	}

	authClaim, ok := claims[openAIAuthClaim].(map[string]interface{})
	if !ok {
		return "", false
	}

	accountID, ok = authClaim[chatGPTAccountIDKey].(string)
	if !ok || accountID == "" {
		return "", false
	}

	return accountID, true
}

// IsChatGPTPassthrough reports whether the current request was auto-detected
// as a ChatGPT subscription token and should be routed to chatgpt.com.
func IsChatGPTPassthrough(ctx *schemas.BifrostContext) bool {
	v, _ := ctx.Value(schemas.BifrostContextKeyChatGPTPassthrough).(bool)
	return v
}
