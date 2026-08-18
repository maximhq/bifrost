package bifrost

import (
	"fmt"
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

// stripUnverifiableReasoning removes the parts of a replayed reasoning turn that only
// the identity that minted them can verify, reporting whether anything changed.
//
// Two request shapes carry replayed reasoning and both reach the provider through the
// retry loop, so both are handled here:
//
//   - Responses-shaped (/v1/responses, its token-counting twin, /v1/responses/compact),
//     where the payload rides on reasoning items.
//   - Chat-shaped (/v1/chat/completions, /v1/messages), where it rides on an assistant
//     message's reasoning_details. This is the shape a complexity or virtual-model
//     router produces: it picks a model per turn, so turn N+1 replays a thinking block
//     minted by whichever model answered turn N, and the new model refuses the
//     signature it did not issue ("messages.N.content.0: Invalid `signature` in
//     `thinking` block"). Handling only the Responses shape handed that 400 straight
//     to the client -- isEncryptedReasoningRejection already recognised the refusal,
//     but the strip it gates returned false, so no retry ever happened.
func stripUnverifiableReasoning(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) bool {
	if req == nil {
		return false
	}
	if req.ChatRequest != nil {
		return stripChatUnverifiableReasoning(ctx, req.ChatRequest)
	}
	return stripResponsesEncryptedContent(ctx, req)
}

// stripChatUnverifiableReasoning clears signatures and encrypted payloads from every
// assistant turn's reasoning_details, dropping details left with nothing to say.
//
// The visible reasoning text and summaries survive: the model loses the verifiable
// chain of thought from earlier turns but keeps the narrative, which is what a client
// that never captured a signature would have sent anyway.
//
// The caller's slice and structs are shared with plugins and the transport layer, so
// the rewrite builds new slices and clones each message it touches rather than
// mutating in place.
func stripChatUnverifiableReasoning(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) bool {
	if req == nil || len(req.Input) == 0 {
		return false
	}

	// Large-payload mode streams a body core never parsed, so it returns false rather
	// than claiming a change it cannot make: the answer must be truthful, because the
	// caller spends an upstream call on a true.
	//
	// Raw passthrough sends the client's own bytes, so the rewrite has to be written per
	// dialect. It is dispatched on the integration rather than declined outright because
	// /v1/messages is the route this failure actually arrives on -- a router replaying a
	// thinking block minted by another model, which is the case the doc comment above
	// describes. Declining here meant the typed path healed that request while the
	// drop-in route handed the client the same 400.
	if ctx != nil {
		if isLargePayload, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool); ok && isLargePayload {
			return false
		}
		if useRawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && useRawBody {
			// Only the Anthropic Messages dialect is rewritten. Gemini is the other
			// integration that enables raw chat passthrough, but its thought_signature
			// rides inside parts[*] on a generateContent body -- applying the Anthropic
			// field paths there would corrupt the request, and nothing shows it produces
			// this refusal, so it keeps the truthful false.
			if integrationType, _ := ctx.Value(schemas.BifrostContextKeyIntegrationType).(string); integrationType == "anthropic" {
				return stripRawAnthropicChatThinking(&req.RawRequestBody)
			}
			return false
		}
	}

	rewritten := make([]schemas.ChatMessage, len(req.Input))
	copy(rewritten, req.Input)

	changed := false
	for i := range rewritten {
		assistant := rewritten[i].ChatAssistantMessage
		if assistant == nil || len(assistant.ReasoningDetails) == 0 {
			continue
		}
		details, detailsChanged := stripChatReasoningDetails(assistant.ReasoningDetails)
		if !detailsChanged {
			continue
		}
		assistantCopy := *assistant
		assistantCopy.ReasoningDetails = details
		rewritten[i].ChatAssistantMessage = &assistantCopy
		changed = true
	}

	if !changed {
		return false
	}
	req.Input = rewritten
	return true
}

// stripChatReasoningDetails returns the details with every signature and encrypted
// payload removed, and whether anything was removed. Details left with neither text
// nor summary are dropped rather than forwarded as empty shells.
func stripChatReasoningDetails(details []schemas.ChatReasoningDetails) ([]schemas.ChatReasoningDetails, bool) {
	changed := false
	kept := make([]schemas.ChatReasoningDetails, 0, len(details))
	// detail is the loop's copy, so clearing its fields leaves the caller's slice alone.
	for _, detail := range details {
		if detail.Signature == nil && detail.Data == nil {
			kept = append(kept, detail)
			continue
		}
		changed = true
		detail.Signature = nil
		detail.Data = nil
		if detail.Text == nil && detail.Summary == nil {
			continue
		}
		kept = append(kept, detail)
	}
	if !changed {
		return nil, false
	}
	return kept, true
}

// contentBlockReasoningCarriers name the ResponsesMessageContentBlock fields that can
// hold a payload the next upstream cannot verify. Both are cleared in one pass: the
// strip gets a single retry, so healing one field per attempt would need two upstream
// calls to reach a payload the upstream accepts.
var contentBlockReasoningCarriers = []string{"signature", "encrypted_content"}

// stripRawAnthropicChatThinking rewrites a buffered Anthropic Messages body so no
// unverifiable thinking payload survives outside the one turn Anthropic protects,
// reporting whether anything changed. Each surviving block keeps its original bytes
// (minus the edited field) so fields Bifrost's schema does not model are not lost on
// the way through.
//
// The two block types are handled differently because their payloads differ in kind:
//
//   - thinking: the signature is the unverifiable half, and the thinking text is the
//     client's own bytes. The signature is blanked rather than deleted, matching what
//     convertBifrostReasoningToAnthropicThinking already puts on the wire for reasoning
//     that carries no signature ("required by the Agent SDK ... default to empty rather
//     than omitting the field"), so the retry sends a shape this provider already accepts.
//   - redacted_thinking: `data` IS the whole block, so blanking it would forward an empty
//     shell. The block is dropped instead, which is what a client that never captured
//     redacted reasoning would have sent.
//
// A turn that removal would leave with an empty content array is left untouched: Anthropic
// rejects an empty array, and dropping the message would break user/assistant alternation.
// That turn keeps its unverifiable block and the retry will fail again -- but only when the
// assistant turn was redacted-only, and reporting a change that cannot help is worse.
func stripRawAnthropicChatThinking(rawBody *[]byte) bool {
	if rawBody == nil || len(*rawBody) == 0 {
		return false
	}
	body := *rawBody
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return false
	}

	// Anthropic verifies that the thinking and redacted_thinking blocks in the latest
	// assistant message arrive exactly as it minted them: "Within the latest assistant
	// message, the sequence of consecutive thinking blocks must match what the model
	// generated in the original request: you can't rearrange, edit, or partially drop
	// them. This includes redacted_thinking blocks." Blanking a signature there is an
	// edit and dropping a redacted_thinking block is a partial drop, so a rewritten
	// protected turn earns a second, different 400 -- "`thinking` or `redacted_thinking`
	// blocks in the latest assistant message cannot be modified" -- and the client ends
	// up with a more confusing error than the signature refusal the retry was spent on.
	// Earlier turns carry no such rule ("Allowed: outside tool use, omit prior turns'
	// thinking"), so they are still rewritten and the fail-soft still heals them.
	// https://platform.claude.com/docs/en/build-with-claude/thinking#preserving-thinking-blocks
	//
	// The protected turn is the last message whose role is assistant, not the last
	// element: a tool-result round trip ends on a user message, which is exactly the
	// shape Anthropic documents this refusal against.
	lastAssistantIndex := -1
	for messageIndex, message := range messages.Array() {
		if message.Get("role").String() == "assistant" {
			lastAssistantIndex = messageIndex
		}
	}

	changed := false
	for messageIndex, message := range messages.Array() {
		if messageIndex == lastAssistantIndex {
			continue
		}
		content := message.Get("content")
		// The shorthand string form carries no blocks, so there is nothing to rewrite.
		if !content.IsArray() {
			continue
		}

		blocks := content.Array()
		kept := make([]string, 0, len(blocks))
		messageChanged := false
		for _, block := range blocks {
			switch block.Get("type").String() {
			case "redacted_thinking":
				messageChanged = true
				continue
			case "thinking":
				if !block.Get("signature").Exists() {
					break
				}
				updated, err := sjson.Set(block.Raw, "signature", "")
				if err != nil {
					// Leave the block untouched rather than corrupting it; the retry
					// still helps if another turn carried the unverifiable payload.
					break
				}
				kept = append(kept, updated)
				messageChanged = true
				continue
			}
			kept = append(kept, block.Raw)
		}

		if !messageChanged || len(kept) == 0 {
			continue
		}
		updated, err := sjson.SetRawBytes(body, fmt.Sprintf("messages.%d.content", messageIndex), []byte("["+strings.Join(kept, ",")+"]"))
		if err != nil {
			continue
		}
		body = updated
		changed = true
	}

	if !changed {
		return false
	}
	*rawBody = body
	return true
}

// stripContentBlockReasoningPayloads returns the message's content blocks with every
// unverifiable reasoning payload removed -- signature and encrypted_content -- and
// whether anything was removed. Anthropic thinking replayed over the Responses shape
// carries its signature here rather than on the reasoning item, so clearing the
// reasoning item's encrypted_content alone left the unverifiable half in place; the
// block models encrypted_content too, so a block-only ciphertext survived the same way.
func stripContentBlockReasoningPayloads(content *schemas.ResponsesMessageContent) ([]schemas.ResponsesMessageContentBlock, bool) {
	if content == nil || len(content.ContentBlocks) == 0 {
		return nil, false
	}
	blocks := make([]schemas.ResponsesMessageContentBlock, len(content.ContentBlocks))
	copy(blocks, content.ContentBlocks)
	changed := false
	for i := range blocks {
		if blocks[i].Signature != nil {
			blocks[i].Signature = nil
			changed = true
		}
		if blocks[i].EncryptedContent != nil {
			blocks[i].EncryptedContent = nil
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	return blocks, true
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
		messageChanged := false

		if message.ResponsesReasoning != nil && message.ResponsesReasoning.EncryptedContent != nil {
			reasoningCopy := *message.ResponsesReasoning
			reasoningCopy.EncryptedContent = nil
			message.ResponsesReasoning = &reasoningCopy
			messageChanged = true
		}

		// A thinking signature rides on the content block rather than the reasoning
		// item, so a message can need the strip with encrypted_content already absent.
		if blocks, ok := stripContentBlockReasoningPayloads(message.Content); ok {
			contentCopy := *message.Content
			contentCopy.ContentBlocks = blocks
			message.Content = &contentCopy
			messageChanged = true
		}

		if !messageChanged {
			stripped = append(stripped, message)
			continue
		}

		changed = true
		if dropItemIDs {
			// message is the loop's copy of the slice element, so this writes to the new
			// slice only, leaving the caller's items alone the way reasoningCopy does.
			message.ID = nil
		}

		// Only a reasoning item is dropped when nothing survives: an ordinary message
		// that merely carried a signature still has its own content to say.
		if message.ResponsesReasoning != nil &&
			len(message.ResponsesReasoning.Summary) == 0 &&
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
		rest := item.Raw
		itemChanged := false

		if gjson.Get(rest, "encrypted_content").Exists() {
			updated, err := sjson.Delete(rest, "encrypted_content")
			if err != nil {
				// Leave the item untouched rather than corrupting it; the retry still
				// helps if the other items carried the unverifiable content.
				items = append(items, item.Raw)
				continue
			}
			rest, itemChanged = updated, true
		}

		// The raw analogue of stripContentBlockReasoningPayloads. Keying the whole item
		// off a top-level encrypted_content forwarded an item whose only carrier was a
		// content-block signature -- replayed Anthropic thinking -- verbatim AND reported
		// no change, so the one fail-soft retry never fired on the passthrough path even
		// though the typed path heals the identical request.
		//
		// Deleting a field inside a block never reorders the content array, so the indices
		// read before the deletes stay valid. A string-valued content yields a one-element
		// array carrying neither field, and falls through untouched.
		for blockIndex, block := range gjson.Get(rest, "content").Array() {
			for _, field := range contentBlockReasoningCarriers {
				if !gjson.Get(block.Raw, field).Exists() {
					continue
				}
				updated, err := sjson.Delete(rest, fmt.Sprintf("content.%d.%s", blockIndex, field))
				if err != nil {
					continue
				}
				rest, itemChanged = updated, true
			}
		}

		if !itemChanged {
			items = append(items, item.Raw)
			continue
		}
		changed = true
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
