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

// encryptedReasoningFieldMarkers name the wire fields Bifrost's egress converters
// write a replayed reasoning item's encrypted_content into. One Responses payload
// reaches each provider as a different field, so each provider refuses it in its own
// vocabulary -- an OpenAI-only detector heals the OpenAI route and hands every other
// provider the raw 400:
//
//   - encrypted_content: OpenAI and Azure, forwarded verbatim.
//   - redacted_thinking: Anthropic on every platform it is served from (direct,
//     Vertex, Bedrock), via convertBifrostReasoningToAnthropicThinking, which maps
//     encrypted_content onto a redacted_thinking block's `data` field.
//   - thought_signature: Gemini and Vertex Gemini, via thoughtSignatureFromEncryptedContent.
//   - reasoningContent: Bedrock Converse, via reasoningSignatureForBedrock, which
//     carries the payload as the reasoning block's signature.
//
// Cohere is absent deliberately: it has no encrypted-reasoning field, so the payload
// travels as a marked thinking block that the upstream never validates. There is no
// refusal to catch.
//
// "encrypted content" (spaced) is the same field named in prose rather than by key.
// Bedrock Mantle's OpenAI-compatible /v1/responses surface refuses a foreign payload
// that way -- it mints its own `rsn_`/`smry_`-prefixed tokens and checks the prefix
// before it ever attempts to decrypt, so its 400 quotes neither the JSON key nor an
// item id. That is the exact refusal a fallback from Azure to Bedrock Mantle earns on
// the next turn, both hosting the same OpenAI-family model.
//
// The match is on the field name rather than on the sentence around it because the
// field names are Bifrost's own output -- they change only when a converter changes,
// and a converter change breaks these same tests. Upstream prose can be reworded at
// any time, and only OpenAI and Anthropic document theirs at all.
var encryptedReasoningFieldMarkers = []string{
	"encrypted_content",
	"encrypted content",
	"redacted_thinking",
	"thought_signature",
	"thoughtsignature",
	"reasoningcontent",
}

// unverifiablePayloadMarkers are the ways an upstream says the payload itself is
// unusable, as opposed to absent, too large, or in the wrong place.
//
// The prefix entries cover upstreams that reject on shape before attempting to
// decrypt. Bedrock Mantle stamps its own tokens (`rsn_` for the reasoning body,
// `smry_` for the summary) and refuses anything else with "missing recognized
// prefix", never reaching the vocabulary of decryption or verification. The verdict
// is the same as an outright decrypt failure -- this identity did not mint the
// payload -- so it earns the same fail-soft strip.
var unverifiablePayloadMarkers = []string{
	"invalid",
	"could not be verified",
	"cannot be verified",
	"could not be decrypted",
	"failed to verify",
	"malformed",
	"missing recognized prefix",
	"unrecognized prefix",
}

// namesEncryptedReasoningField reports whether a lowercased error message points at
// the field this request's encrypted reasoning was written into.
func namesEncryptedReasoningField(message string) bool {
	for _, marker := range encryptedReasoningFieldMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	// Bedrock Converse names the field in prose rather than by key, and AWS does not
	// document the sentence, so a bare "signature" is accepted only alongside a
	// reasoning word. On its own it is far more likely to be a request-signing
	// failure -- SigV4 mismatch reads "the request signature we calculated does not
	// match" -- which stripping reasoning cannot fix.
	if !strings.Contains(message, "signature") {
		return false
	}
	return strings.Contains(message, "reasoning") ||
		strings.Contains(message, "thinking") ||
		strings.Contains(message, "thought")
}

func containsAnyMarker(message string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

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
// fallback. Providers other than OpenAI return no code for this at all and describe
// the refusal in terms of the field they received the payload in, so the text check
// also covers those (see encryptedReasoningFieldMarkers). Every check is gated on
// 400: a 5xx carrying similar text is a transient upstream fault the normal retry
// path already covers.
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
	if strings.Contains(message, encryptedContentErrorCode) ||
		(strings.Contains(message, "encrypted content") && strings.Contains(message, "could not be verified")) {
		return true
	}

	// Anthropic's neighbouring 400 -- "`thinking` or `redacted_thinking` blocks in the
	// latest assistant message cannot be modified" -- names the same block and is the
	// exact opposite complaint: the payload was dropped or rewritten, not unverifiable.
	// The strip drops the redacted block outright, so treating this as a fail-soft
	// trigger would spend an upstream call to arrive at the same error, worse.
	if strings.Contains(message, "cannot be modified") {
		return false
	}

	return namesEncryptedReasoningField(message) && containsAnyMarker(message, unverifiablePayloadMarkers)
}

// encryptedReasoningCarriers returns the input array and raw body of the request
// shape that replays reasoning items, or nils when this request carries none.
//
// Three endpoints replay the same []ResponsesMessage and all three reach the
// provider through the retry loop that calls the strip: /v1/responses, its
// token-counting twin, and /v1/responses/compact. The last one is a separate
// top-level request rather than a flag on the first, and a coding CLI resuming a
// session hits it with the entire prior transcript -- the exact payload most likely
// to be refused. Keying the strip off one shape would hand that 400 straight to
// the client, so the shapes are resolved here and rewritten by one code path.
//
// Pointers are returned rather than values because both branches write back.
func encryptedReasoningCarriers(req *schemas.BifrostRequest) (input *[]schemas.ResponsesMessage, rawBody *[]byte) {
	if req == nil {
		return nil, nil
	}
	switch {
	case req.ResponsesRequest != nil:
		return &req.ResponsesRequest.Input, &req.ResponsesRequest.RawRequestBody
	case req.CountTokensRequest != nil:
		return &req.CountTokensRequest.Input, &req.CountTokensRequest.RawRequestBody
	case req.CompactionRequest != nil:
		return &req.CompactionRequest.Input, &req.CompactionRequest.RawRequestBody
	default:
		return nil, nil
	}
}

// stripResponsesEncryptedContent removes encrypted_content from every reasoning item
// in a Responses-shaped request, reporting whether anything changed. Reasoning items
// left with nothing to say -- no summary and no content blocks -- are dropped entirely
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
	inputRef, rawBodyRef := encryptedReasoningCarriers(req)
	if inputRef == nil {
		return false
	}

	// A reasoning item's id was issued by the identity that minted the encrypted_content
	// this upstream just refused, so on a shape that resolves ids server-side it is no
	// more replayable than the ciphertext was. /v1/responses and its token-counting twin
	// accept an inline reasoning item whose id they never issued, and the id is worth
	// keeping there because it is what links the item to the turn that produced it.
	// /v1/responses/compact instead looks the id up and answers 404 "Items are not
	// persisted when `store` is set to false", so a stripped retry that carries the id
	// spends an upstream call only to earn a different error. Summary and content survive
	// on both shapes -- those are the client's own bytes, not the upstream's handle.
	dropItemIDs := req.CompactionRequest != nil

	if ctx != nil {
		if isLargePayload, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool); ok && isLargePayload {
			return false
		}
		if useRawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && useRawBody {
			return stripRawResponsesEncryptedContent(rawBodyRef, dropItemIDs)
		}
	}

	if len(*inputRef) == 0 {
		return false
	}

	input := *inputRef
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
		if dropItemIDs {
			// message is the loop's copy of the slice element, so this writes to the new
			// slice only, leaving the caller's items alone the way reasoningCopy does.
			message.ID = nil
		}

		if len(reasoningCopy.Summary) == 0 &&
			(message.Content == nil || (len(message.Content.ContentBlocks) == 0 && message.Content.ContentStr == nil)) {
			continue
		}
		stripped = append(stripped, message)
	}

	if !changed {
		return false
	}

	*inputRef = stripped
	return true
}

// stripRawResponsesEncryptedContent applies the same rewrite to a buffered raw request
// body, which is what reaches the provider when the caller opted into passthrough.
// Each surviving item keeps its original bytes (minus the deleted fields) so fields
// Bifrost's schema does not model are not lost on the way through.
//
// dropItemIDs carries the caller's shape decision, described at its assignment.
func stripRawResponsesEncryptedContent(rawBody *[]byte, dropItemIDs bool) bool {
	if rawBody == nil {
		return false
	}
	body := *rawBody
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
		if dropItemIDs {
			if withoutID, err := sjson.Delete(rest, "id"); err == nil {
				rest = withoutID
			}
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
	*rawBody = updated
	return true
}
