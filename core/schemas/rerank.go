package schemas

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"
)

// RerankDocument represents a document to be reranked.
type RerankDocument struct {
	Text string                 `json:"text"`
	ID   *string                `json:"id,omitempty"`
	Meta map[string]interface{} `json:"meta,omitempty"`
}

// RerankParameters contains optional parameters for a rerank request.
type RerankParameters struct {
	TopN            *int                   `json:"top_n,omitempty"`
	MaxTokensPerDoc *int                   `json:"max_tokens_per_doc,omitempty"`
	Priority        *int                   `json:"priority,omitempty"`
	ReturnDocuments *bool                  `json:"return_documents,omitempty"`
	ExtraParams     map[string]interface{} `json:"-"`
}

// BifrostRerankRequest represents a request to rerank documents by relevance to a query.
type BifrostRerankRequest struct {
	Provider       ModelProvider     `json:"provider"`
	Model          string            `json:"model"`
	Query          string            `json:"query"`
	Documents      []RerankDocument  `json:"documents"`
	Params         *RerankParameters `json:"params,omitempty"`
	Fallbacks      []Fallback        `json:"fallbacks,omitempty"`
	RawRequestBody []byte            `json:"-"`
}

// UnmarshalJSON accepts both the Cohere-spec string-array shape
// (`"documents": ["a", "b"]`) and the legacy object-array shape
// (`"documents": [{"text":"a"}, ...]`) and normalizes both into the
// canonical []RerankDocument. Putting this on the schema type keeps
// the wire-format concern with the type that owns the field, so any
// direct SDK consumer of BifrostRerankRequest benefits — not just the
// HTTP transport.
//
// Cohere's published /v1/rerank API and vLLM both accept the string-array
// form natively. Many OpenAI-compatible clients (e.g. Open WebUI's
// ExternalReranker.predict) send strings.
//
// IMPORTANT: do NOT implement UnmarshalJSON on RerankDocument (the
// element type) — bytedance/sonic's decoder rejects type mismatches at
// the array-element level BEFORE invoking any element-level
// UnmarshalJSON, surfacing "Mismatch type schemas.RerankDocument with
// value string". The fix has to live on the parent struct (here) or
// at the field-type level (json.RawMessage in the HTTP transport's
// RerankRequest, which uses NormalizeRerankDocuments below).
func (r *BifrostRerankRequest) UnmarshalJSON(data []byte) error {
	// Alias-shadow trick: decode everything-except-Documents into the
	// real struct via an alias (which strips the parent UnmarshalJSON
	// and avoids infinite recursion), then capture Documents as raw
	// bytes so we can pick its shape ourselves.
	type alias BifrostRerankRequest
	aux := &struct {
		Documents json.RawMessage `json:"documents"`
		*alias
	}{alias: (*alias)(r)}
	if err := sonic.Unmarshal(data, aux); err != nil {
		return err
	}
	// Treat missing/empty/`null` Documents as nil; callers can apply
	// their own "documents required" check with their preferred
	// phrasing. Only a malformed shape errors at decode time.
	// bytes.Equal beats string-conversion + comparison for the null
	// fast-path.
	trimmed := bytes.TrimSpace(aux.Documents)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		r.Documents = nil
		return nil
	}
	docs, err := NormalizeRerankDocuments(aux.Documents)
	if err != nil {
		return err
	}
	r.Documents = docs
	return nil
}

// NormalizeRerankDocuments accepts both Cohere-spec string-array
// ("documents": ["foo", "bar"]) and the existing object-array form
// ("documents": [{"text":"foo"}, ...]) and returns the canonical
// []RerankDocument slice. Exported so the HTTP transport (which
// decodes into its own intermediate RerankRequest type with a
// json.RawMessage Documents field) can reuse the same normalization.
//
// Empty/nil/`null` inputs return an error rather than a zero-length
// slice, so the missing-field and null-field paths surface the same
// "documents is required" message.
func NormalizeRerankDocuments(raw json.RawMessage) ([]RerankDocument, error) {
	// Reject empty + null up front so the error message is consistent with
	// the missing-field case. Without the null check, sonic unmarshals
	// `null` into `[]string` as a nil slice (no error), and the caller's
	// `len(docs) == 0` guard then fires with a different message.
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, fmt.Errorf("documents is required")
	}

	// Sniff the first non-whitespace element of the array to pick the right
	// shape, so the common case (whichever it is in practice) doesn't pay
	// for a failed-decode attempt at the other shape. Documents can be the
	// largest field in a rerank body — doubling parse work matters on hot
	// paths.
	//   `["foo"]`     → first element starts with `"` → string array
	//   `[{"text":…}]`→ first element starts with `{` → object array
	//   anything else → still try object-array (matches legacy behaviour
	//                  for invalid shapes, and the error message is the
	//                  same regardless of which branch hits it)
	if len(trimmed) >= 2 && trimmed[0] == '[' {
		inner := bytes.TrimSpace(trimmed[1:])
		if len(inner) > 0 && inner[0] == '"' {
			var asStrings []string
			if err := sonic.Unmarshal(raw, &asStrings); err != nil {
				return nil, fmt.Errorf("documents must be []string or [{text:string}]: %w", err)
			}
			out := make([]RerankDocument, len(asStrings))
			for i, s := range asStrings {
				out[i] = RerankDocument{Text: s}
			}
			return out, nil
		}
	}

	// Object array (the legacy bifrost shape) — also catches `[]` and any
	// other non-string-array input. Sonic surfaces a clear type-mismatch
	// error if the actual shape is neither.
	var asObjects []RerankDocument
	if err := sonic.Unmarshal(raw, &asObjects); err != nil {
		return nil, fmt.Errorf("documents must be []string or [{text:string}]: %w", err)
	}
	return asObjects, nil
}

// GetRawRequestBody returns the raw request body for the rerank request.
func (r *BifrostRerankRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

// RerankResult represents a single reranked document with its relevance score.
type RerankResult struct {
	Index          int             `json:"index"`
	RelevanceScore float64         `json:"relevance_score"`
	Document       *RerankDocument `json:"document,omitempty"`
}

// BifrostRerankResponse represents the response from a rerank request.
type BifrostRerankResponse struct {
	ID          string                     `json:"id,omitempty"`
	Results     []RerankResult             `json:"results"`
	Model       string                     `json:"model"`
	Usage       *BifrostLLMUsage           `json:"usage,omitempty"`
	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}
