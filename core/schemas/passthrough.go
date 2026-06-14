package schemas

type BifrostPassthroughRequest struct {
	Provider    ModelProvider // provider extracted from path or body, used for key selection when non-empty
	Model       string        // model extracted from path or body, used for key selection when non-empty
	Method      string
	Path        string // stripped path, e.g. "/v1/fine-tuning/jobs"
	RawQuery    string // raw query string, no "?"
	UpstreamURL string // optional base URL override for host-backed passthrough routes
	Body        []byte
	SafeHeaders map[string]string // client headers, auth already stripped
}

// BifrostPassthroughUsage carries usage data extracted by the provider at stream
// completion. The pricing module converts this into cost using the existing compute
// functions — no new pricing logic is required.
type BifrostPassthroughUsage struct {
	// Text / chat / responses / embeddings
	LLMUsage     *BifrostLLMUsage
	ServiceTier  *BifrostServiceTier // "priority" | "flex" | nil (default)
	Speed        *string             // "fast" | "standard" — speed actually served (Anthropic fast mode); drives fast-mode billing
	InferenceGeo *string             // "us" | "global" — inference geography served (Anthropic data residency); drives the 1.1x US multiplier

	// Image generation / edit / variation
	ImageUsage   *ImageUsage
	ImageSize    string // e.g. "1024x1024"
	ImageQuality string // "low" | "medium" | "high" | "auto"

	// Speech TTS — character count from request body `input` field
	AudioInputChars int

	// Transcription — token details or raw seconds as duration fallback
	AudioSeconds      *int
	AudioTokenDetails *TranscriptionUsageInputTokenDetails

	// Video generation
	VideoSeconds *int

	// Container creation (code interpreter session) — synthetic pricing identifier,
	// e.g. "container-1g", or "container" when no memory limit is reported. Maps to
	// costInput.containerIdentifierString for the flat per-session fee.
	ContainerIdentifier string
}

type BifrostPassthroughResponse struct {
	StatusCode       int
	Headers          map[string]string
	Body             []byte
	ExtraFields      BifrostResponseExtraFields
	Path             string                   // stripped provider path, e.g. "/v1/chat/completions"
	PassthroughUsage *BifrostPassthroughUsage // usage extracted by the provider for billing — set on the unary response (non-streaming) or the final streaming chunk; nil when no billable usage could be extracted
}

type PassthroughLogParams struct {
	Method     string `json:"method"`
	Path       string `json:"path"`      // stripped path, e.g. "/v1/fine-tuning/jobs"
	RawQuery   string `json:"raw_query"` // raw query string, no "?"
	StatusCode int    `json:"status_code"`
	Model      string `json:"model,omitempty"` // model extracted from path or request body
}
