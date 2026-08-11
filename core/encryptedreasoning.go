package bifrost

import (
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// encryptedContentErrorCode is the error code OpenAI returns when a replayed
// reasoning item's encrypted_content cannot be verified for the request's upstream
// identity. The accompanying reason varies ("Encrypted content could not be
// decrypted or parsed", "Encrypted content item_id did not match the target item
// id"), so the code is the stable signal.
const encryptedContentErrorCode = "invalid_encrypted_content"

// isEncryptedReasoningRejection reports whether err is an upstream refusal to accept
// replayed encrypted reasoning content.
//
// encrypted_content is bound to the identity that minted it: the item id it was
// issued with, the API key's organization, and the serving endpoint. A gateway
// legitimately changes any of those between turns of one conversation -- key
// rotation across a multi-key pool, a fallback that served an earlier turn from a
// different provider, or a client whose traffic starts (or stops) being routed
// through Bifrost mid-session. The ciphertext arrives byte-perfect and is still
// undecryptable, and no amount of retrying the same payload will fix it.
//
// Older deployments return no code field, so the message text is checked as a
// fallback. Both checks are gated on 400: a 5xx carrying similar text is a
// transient upstream fault the normal retry path already covers.
func isEncryptedReasoningRejection(err *schemas.BifrostError) bool {
	if err == nil || err.Error == nil {
		return false
	}
	if err.StatusCode == nil || *err.StatusCode != 400 {
		return false
	}
	if err.Error.Code != nil && strings.Contains(*err.Error.Code, encryptedContentErrorCode) {
		return true
	}
	message := strings.ToLower(err.Error.Message)
	// Anthropic sends no code and names the offending block instead:
	// "messages.1.content.0: Invalid `data` in `redacted_thinking` block".
	return strings.Contains(message, encryptedContentErrorCode) ||
		(strings.Contains(message, "encrypted content") && strings.Contains(message, "could not be verified")) ||
		(strings.Contains(message, "invalid `data`") && strings.Contains(message, "`redacted_thinking` block"))
}

// stripResponsesEncryptedContent removes encrypted_content from every reasoning item
// in a Responses request, reporting whether anything changed. Reasoning items left
// with nothing to say -- no summary and no content blocks -- are dropped entirely
// rather than forwarded as bare ids the upstream never issued.
//
// Summaries, ids, and every other item are preserved: the model loses the verbatim
// chain of thought from earlier turns but keeps the visible narrative, which is what
// a client that never captured encrypted reasoning would have sent anyway.
//
// Both wire forms are handled, because the answer must be truthful for the caller to
// decide whether a retry is worth an upstream call:
//
//   - Typed input, the normal path. The slice and the reasoning structs it points at
//     are shared with the caller (plugins and the transport layer hold the same
//     pointers), so the rewrite builds a new slice and clones each reasoning struct it
//     touches instead of mutating in place.
//   - Raw request body passthrough, where the typed input is not what reaches the
//     provider. Items are filtered by their verbatim JSON so unknown fields survive.
//
// Large-payload mode is the one case that returns false with work left undone: the
// body streams straight from a reader that core never parsed and cannot rewrite, so
// claiming a change would buy a second identical upstream call.
func stripResponsesEncryptedContent(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) bool {
	if req == nil || req.ResponsesRequest == nil {
		return false
	}
	if ctx != nil {
		if isLargePayload, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool); ok && isLargePayload {
			return false
		}
		if useRawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && useRawBody {
			return stripRawResponsesEncryptedContent(req.ResponsesRequest)
		}
	}

	if len(req.ResponsesRequest.Input) == 0 {
		return false
	}

	input := req.ResponsesRequest.Input
	stripped := make([]schemas.ResponsesMessage, 0, len(input))
	changed := false

	for _, message := range input {
		if message.ResponsesReasoning == nil || message.ResponsesReasoning.EncryptedContent == nil {
			stripped = append(stripped, message)
			continue
		}

		changed = true
		reasoningCopy := *message.ResponsesReasoning
		reasoningCopy.EncryptedContent = nil
		message.ResponsesReasoning = &reasoningCopy

		if len(reasoningCopy.Summary) == 0 &&
			(message.Content == nil || (len(message.Content.ContentBlocks) == 0 && message.Content.ContentStr == nil)) {
			continue
		}
		stripped = append(stripped, message)
	}

	if !changed {
		return false
	}

	req.ResponsesRequest.Input = stripped
	return true
}

// stripRawResponsesEncryptedContent applies the same rewrite to a buffered raw request
// body, which is what reaches the provider when the caller opted into passthrough.
// Each surviving item keeps its original bytes (minus the deleted field) so fields
// Bifrost's schema does not model are not lost on the way through.
func stripRawResponsesEncryptedContent(req *schemas.BifrostResponsesRequest) bool {
	body := req.RawRequestBody
	if len(body) == 0 {
		return false
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}

	items := make([]string, 0, len(input.Array()))
	changed := false
	for _, item := range input.Array() {
		if !gjson.Get(item.Raw, "encrypted_content").Exists() {
			items = append(items, item.Raw)
			continue
		}
		changed = true
		rest, err := sjson.Delete(item.Raw, "encrypted_content")
		if err != nil {
			// Leave the item untouched rather than corrupting it; the retry still
			// helps if the other items carried the unverifiable content.
			items = append(items, item.Raw)
			continue
		}
		if len(gjson.Get(rest, "summary").Array()) == 0 && !gjson.Get(rest, "content").Exists() {
			continue
		}
		items = append(items, rest)
	}
	if !changed {
		return false
	}

	updated, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(items, ",")+"]"))
	if err != nil {
		return false
	}
	req.RawRequestBody = updated
	return true
}
