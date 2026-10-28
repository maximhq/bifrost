package schemas

import (
	"fmt"
	"net/http"
	"path"
	"strings"
)

// PassthroughPolicy limits the routes a provider's HTTP passthrough endpoints forward.
// A nil policy forwards every route; an empty AllowedRoutes forwards none.
type PassthroughPolicy struct {
	AllowedRoutes []PassthroughRouteRule `json:"allowed_routes"`
}

// PassthroughRouteRule allows the routes that match a path pattern and, when set, one of its methods.
type PassthroughRouteRule struct {
	Methods []string `json:"methods,omitempty"` // empty matches any method
	Path    string   `json:"path"`              // "*" matches within one segment; a final "/**" matches the prefix and below
}

// Validate reports the first malformed rule. It runs at config write time so a rule that
// could never match, or that relies on unsupported glob syntax, is refused instead of
// silently blocking routes.
func (p *PassthroughPolicy) Validate() error {
	if p == nil {
		return nil
	}
	for i, rule := range p.AllowedRoutes {
		if err := rule.validate(); err != nil {
			return fmt.Errorf("allowed_routes[%d]: %w", i, err)
		}
	}
	return nil
}

func (r *PassthroughRouteRule) validate() error {
	for _, method := range r.Methods {
		switch method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
		default:
			return fmt.Errorf("unsupported method %q; use GET, POST, PUT, PATCH, DELETE, HEAD or OPTIONS", method)
		}
	}
	if !strings.HasPrefix(r.Path, "/") {
		return fmt.Errorf("path must start with /")
	}
	if strings.ContainsAny(r.Path, "?#%\\[]") {
		return fmt.Errorf("path must not contain ?, #, %%, \\, [ or ]")
	}
	if r.Path == "/" {
		return nil
	}
	segments := strings.Split(r.Path[1:], "/")
	for i, segment := range segments {
		switch {
		case segment == "" || segment == "." || segment == "..":
			return fmt.Errorf("path must not contain empty, . or .. segments")
		case strings.Contains(segment, "**") && (segment != "**" || i != len(segments)-1):
			return fmt.Errorf("** is only allowed as the last path segment")
		}
	}
	return nil
}

// Matches reports whether the rule allows method and path. apiVersion is the version
// segment a route prefix consumed (GenAI's "/v1beta"); it is matched as the path's first
// segment so rules read like the URL the client called.
func (r *PassthroughRouteRule) Matches(method, apiVersion, requestPath string) bool {
	if r.Path == "" {
		return false
	}
	if len(r.Methods) > 0 {
		allowed := false
		for _, m := range r.Methods {
			if strings.EqualFold(m, method) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	pattern := r.Path
	if apiVersion != "" {
		if pattern == "/**" {
			return true
		}
		segment, rest := cutPassthroughSegment(pattern[1:])
		if !matchPassthroughSegment(segment, apiVersion) {
			return false
		}
		pattern = rest
	}
	return matchPassthroughPath(pattern, requestPath)
}

// matchPassthroughPath walks pattern and p one segment at a time without allocating.
func matchPassthroughPath(pattern, p string) bool {
	for {
		if pattern == "/**" {
			return true
		}
		if pattern == "" || p == "" {
			return pattern == p
		}
		patternSegment, patternRest := cutPassthroughSegment(pattern[1:])
		pathSegment, pathRest := cutPassthroughSegment(p[1:])
		if !matchPassthroughSegment(patternSegment, pathSegment) {
			return false
		}
		pattern, p = patternRest, pathRest
	}
}

// cutPassthroughSegment splits s at its first "/", keeping the "/" on the remainder.
func cutPassthroughSegment(s string) (string, string) {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}

// matchPassthroughSegment matches one segment; a wildcard never matches an empty segment.
func matchPassthroughSegment(pattern, segment string) bool {
	if strings.IndexByte(pattern, '*') < 0 {
		return pattern == segment
	}
	if segment == "" {
		return false
	}
	ok, _ := path.Match(pattern, segment)
	return ok
}

type BifrostPassthroughRequest struct {
	Provider    ModelProvider // provider extracted from path or body, used for key selection when non-empty
	Model       string        // model extracted from path or body, used for key selection when non-empty
	Method      string
	Path        string // stripped path, e.g. "/v1/fine-tuning/jobs"
	APIVersion  string // version segment stripped with the route prefix, e.g. "v1beta1"; empty when none
	RawQuery    string // raw query string, no "?"
	UpstreamURL string // optional base URL override for host-backed passthrough routes
	Body        []byte
	SafeHeaders map[string]string // client headers; auth stripped unless the caller's OAuth/JWT bearer is the upstream credential (Anthropic sk-ant-oat, OpenAI JWT)
}

// BifrostPassthroughUsage carries usage data extracted by the provider at stream
// completion. The pricing module converts this into cost using the existing compute
// functions — no new pricing logic is required.
type BifrostPassthroughUsage struct {
	// Text / chat / responses / embeddings
	LLMUsage     *BifrostLLMUsage
	ServiceTier  *BifrostServiceTier // Served tier, e.g. "priority", "fast", "flex", or "ultrafast".
	Speed        *string             // "fast" | "standard" — speed actually served (Anthropic fast mode); drives fast-mode billing
	InferenceGeo *string             // "us" | "global" — inference geography served (Anthropic data residency); drives the 1.1x US multiplier

	Iterations []ResponsesResponseUsage // per-pass breakdown as sent (Anthropic usage.iterations[]); advisor passes are priced from it

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

	// Cost is a provider-reported exact price for the passthrough call. When set, it wins over
	// datasheet estimation (there is often no datasheet rate for raw passthrough task types).
	// Used by providers whose responses include their own cost, e.g. Runware's per-task `cost`.
	Cost *BifrostCost
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

	ParsedType   RequestType   `json:"parsed_type,omitempty"`   // typed request kind the bodies were parsed as, e.g. "chat_completion"
	BaseProvider ModelProvider `json:"base_provider,omitempty"` // wire format of a custom provider, when it differs from the row's provider
	ModelParams  interface{}   `json:"model_params,omitempty"`  // model parameters parsed from the request body

	MultipartSummary bool `json:"multipart_summary,omitempty"` // request body logged as its form fields and file sizes, not raw
}
