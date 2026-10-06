package antigravity

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// thoughtSignatureSentinel is Google's documented value for a functionCall whose
	// real thought signature is unknown; it bypasses signature validation.
	thoughtSignatureSentinel = "skip_thought_signature_validator"

	continueText = "(continue)"

	billingHeaderPrefix   = "x-anthropic-billing-header:"
	claudeAgentParagraph  = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	maxStopSequences      = 5
	minThoughtSignatureSz = 16
)

var (
	foreignSignaturePrefix = regexp.MustCompile(`^(?:fc|ctc|tsc|call|msg|rs|resp|reasoning|item|ws|toolu|tool|func|function)[-_]`)
	base64SignatureChars   = regexp.MustCompile(`^[A-Za-z0-9+/_=-]+$`)
)

// requestPlan is a compiled Cloud Code Assist request, independent of the account it
// will be sent with.
type requestPlan struct {
	wireModel string
	request   json.RawMessage // the whitelisted inner Gemini request
	names     *toolNameCodec
}

// wireEnvelope is the v1internal request wrapper.
type wireEnvelope struct {
	Model       string          `json:"model"`
	UserAgent   string          `json:"userAgent"`
	RequestType string          `json:"requestType"`
	Project     string          `json:"project"`
	RequestID   string          `json:"requestId"`
	Request     json.RawMessage `json:"request"`
}

// envelope wraps the plan for one attempt on project.
func (p *requestPlan) envelope(project string) ([]byte, error) {
	return marshalJSON(wireEnvelope{
		Model:       p.wireModel,
		UserAgent:   "antigravity",
		RequestType: "agent",
		Project:     project,
		RequestID:   "agent-" + uuid.NewString(),
		Request:     p.request,
	})
}

// compileOptions carries the request facts the Gemini body does not.
type compileOptions struct {
	wireModel      string
	thinkingLevel  string
	toolChoiceNone bool
}

// buildRequestPlan converts a Bifrost chat request into a Cloud Code Assist request.
func buildRequestPlan(ctx *schemas.BifrostContext, request *schemas.BifrostChatRequest) (*requestPlan, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostOperationError("chat request is not provided", nil)
	}
	wireModel, thinkingLevel := resolveWireModel(request.Model, effortFromParams(request.Params))

	// Reasoning is resolved above onto Antigravity's own model variants and levels, so
	// the Gemini converter must not also turn it into a thinking budget it would validate
	// against the wrong model's limits. The converter normalises Model in place, so it
	// gets a copy.
	converted := *request
	if request.Params != nil {
		params := *request.Params
		params.Reasoning = nil
		converted.Params = &params
	}
	geminiBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (providerUtils.RequestBodyWithExtraParams, error) {
		body, err := gemini.ToGeminiChatCompletionRequest(ctx, &converted)
		if err != nil {
			return nil, err
		}
		if body == nil {
			return nil, errors.New("chat completion request could not be converted to gemini format")
		}
		return body, nil
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if geminiBody == nil {
		return nil, providerUtils.NewBifrostOperationError("antigravity does not support large payload passthrough", nil)
	}

	inner, names, err := compileWireRequest(geminiBody, compileOptions{
		wireModel:      wireModel,
		thinkingLevel:  thinkingLevel,
		toolChoiceNone: toolChoiceIsNone(request.Params),
	})
	if err != nil {
		var badRequest *badRequestError
		if errors.As(err, &badRequest) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				StatusCode:     schemas.Ptr(http.StatusBadRequest),
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr("invalid_request_error"),
					Message: badRequest.message,
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err)
	}
	return &requestPlan{wireModel: wireModel, request: inner, names: names}, nil
}

// badRequestError is a request Cloud Code Assist cannot serve for the chosen model.
type badRequestError struct{ message string }

func (e *badRequestError) Error() string { return e.message }

func toolChoiceIsNone(params *schemas.ChatParameters) bool {
	if params == nil || params.ToolChoice == nil {
		return false
	}
	tc := params.ToolChoice
	if tc.ChatToolChoiceStr != nil {
		return schemas.ChatToolChoiceType(*tc.ChatToolChoiceStr) == schemas.ChatToolChoiceTypeNone
	}
	return tc.ChatToolChoiceStruct != nil && tc.ChatToolChoiceStruct.Type == schemas.ChatToolChoiceTypeNone
}

// compileWireRequest rewrites a Gemini generateContent body into the strict subset Cloud
// Code Assist accepts: {contents, systemInstruction, tools, generationConfig, toolConfig,
// sessionId}, with the per-model fixes the backend requires.
func compileWireRequest(geminiBody []byte, opts compileOptions) (json.RawMessage, *toolNameCodec, error) {
	var src map[string]any
	if err := unmarshalUseNumber(geminiBody, &src); err != nil {
		return nil, nil, err
	}
	claude := isClaudeModel(opts.wireModel)
	geminiFamily := isGeminiModel(opts.wireModel)
	names := newToolNameCodec()

	out := map[string]any{}

	tools, hasDeclarations := compileTools(firstPresent(src, "tools"), names, geminiFamily)

	contents := compileContents(firstPresent(src, "contents"), names, claude, geminiFamily)
	out["contents"] = contents

	if system := compileSystemInstruction(firstPresent(src, "systemInstruction", "system_instruction"), opts.wireModel); system != nil {
		out["systemInstruction"] = system
	}

	generationConfig, err := compileGenerationConfig(firstPresent(src, "generationConfig", "generation_config"), opts, geminiFamily)
	if err != nil {
		return nil, nil, err
	}
	if generationConfig != nil {
		out["generationConfig"] = generationConfig
	}

	toolConfig := compileToolConfig(firstPresent(src, "toolConfig", "tool_config"), names)
	if claude {
		if opts.toolChoiceNone {
			tools, hasDeclarations, toolConfig = nil, false, nil
		} else if hasDeclarations {
			fcc := map[string]any{"mode": "VALIDATED"}
			if toolConfig != nil {
				if existing, ok := toolConfig["functionCallingConfig"].(map[string]any); ok {
					if allowed, ok := existing["allowedFunctionNames"]; ok {
						fcc["allowedFunctionNames"] = allowed
					}
				}
			}
			toolConfig = map[string]any{"functionCallingConfig": fcc}
		}
	}
	if len(tools) > 0 {
		out["tools"] = tools
	}
	if toolConfig != nil && hasDeclarations {
		out["toolConfig"] = toolConfig
	}

	out["sessionId"] = sessionIDFor(contents)

	raw, err := marshalJSON(out)
	if err != nil {
		return nil, nil, err
	}
	return raw, names, nil
}

// compileContents applies the conversation fixes Cloud Code Assist needs: user/model
// roles only, empty parts and turns removed, tool names and call ids normalised, thought
// signatures adjusted per model family, and a user turn at both ends.
func compileContents(raw any, names *toolNameCodec, claude, geminiFamily bool) []any {
	list, _ := raw.([]any)
	compiled := make([]map[string]any, 0, len(list))
	for _, entry := range list {
		content, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		role := "user"
		if r, _ := content["role"].(string); r == "model" || r == "assistant" {
			role = "model"
		}
		parts, _ := content["parts"].([]any)
		kept := make([]any, 0, len(parts))
		firstCallSeen := false
		for _, rawPart := range parts {
			src, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			part := make(map[string]any, len(src))
			for k, v := range src {
				part[k] = v
			}
			if isEmptyTextPart(part) {
				continue
			}

			signature, _ := part["thoughtSignature"].(string)
			realSignature := isLikelyRealThoughtSignature(signature)
			_, isCall := part["functionCall"].(map[string]any)

			switch {
			case claude:
				if role != "model" || !realSignature {
					delete(part, "thoughtSignature")
				}
				if role == "model" && part["thought"] == true && !realSignature {
					continue
				}
			case role != "model":
				if signature == thoughtSignatureSentinel {
					delete(part, "thoughtSignature")
				}
			case isCall:
				if !realSignature {
					if !firstCallSeen && geminiFamily {
						part["thoughtSignature"] = thoughtSignatureSentinel
					} else {
						delete(part, "thoughtSignature")
					}
				}
				firstCallSeen = true
			default:
				if signature != "" && !realSignature {
					delete(part, "thoughtSignature")
				}
			}

			if call, ok := part["functionCall"].(map[string]any); ok {
				part["functionCall"] = compileFunctionCall(call, names, claude)
			}
			if response, ok := part["functionResponse"].(map[string]any); ok {
				part["functionResponse"] = compileFunctionResponse(response, names, claude)
			}
			if claude {
				if text, ok := part["text"].(string); ok && strings.TrimSpace(text) == "" && len(part) == 1 {
					continue
				}
			}
			kept = append(kept, part)
		}
		if len(kept) == 0 {
			continue
		}
		if claude && len(compiled) > 0 && compiled[len(compiled)-1]["role"] == role {
			prev := compiled[len(compiled)-1]
			prev["parts"] = append(prev["parts"].([]any), kept...)
			continue
		}
		compiled = append(compiled, map[string]any{"role": role, "parts": kept})
	}

	continueTurn := func() map[string]any {
		return map[string]any{"role": "user", "parts": []any{map[string]any{"text": continueText}}}
	}
	if len(compiled) == 0 || compiled[0]["role"] == "model" {
		compiled = append([]map[string]any{continueTurn()}, compiled...)
	}
	if compiled[len(compiled)-1]["role"] == "model" {
		compiled = append(compiled, continueTurn())
	}

	result := make([]any, len(compiled))
	for i, c := range compiled {
		result[i] = c
	}
	return result
}

func compileFunctionCall(call map[string]any, names *toolNameCodec, claude bool) map[string]any {
	out := make(map[string]any, len(call))
	for k, v := range call {
		out[k] = v
	}
	if name, ok := out["name"].(string); ok {
		out["name"] = names.encode(name)
	}
	if _, ok := out["args"].(map[string]any); !ok {
		out["args"] = map[string]any{}
	}
	setCallID(out, claude)
	return out
}

func compileFunctionResponse(response map[string]any, names *toolNameCodec, claude bool) map[string]any {
	out := make(map[string]any, len(response))
	for k, v := range response {
		out[k] = v
	}
	if name, ok := out["name"].(string); ok {
		out["name"] = names.encode(name)
	}
	switch v := out["response"].(type) {
	case map[string]any:
	case nil:
		out["response"] = map[string]any{"result": "(empty tool output)"}
	default:
		out["response"] = map[string]any{"result": v}
	}
	setCallID(out, claude)
	return out
}

// setCallID normalises a functionCall/functionResponse id. Embedded Bifrost thought
// signatures are stripped (they travel in thoughtSignature); Claude additionally needs a
// non-empty [A-Za-z0-9_-] id on every call and result, mapped deterministically so a call
// and its result still pair up.
func setCallID(m map[string]any, claude bool) {
	id, _ := m["id"].(string)
	id = providerUtils.StripThoughtSignature(id)
	if claude {
		if id == "" {
			if name, _ := m["name"].(string); name != "" {
				id = name
			}
		}
		m["id"] = providerUtils.SanitizeAnthropicToolUseID(id)
		return
	}
	if id == "" {
		delete(m, "id")
		return
	}
	m["id"] = id
}

// isEmptyTextPart reports a part whose only payload is an empty text.
func isEmptyTextPart(part map[string]any) bool {
	text, ok := part["text"].(string)
	if !ok || text != "" {
		return false
	}
	for k := range part {
		if k != "text" && k != "thought" {
			return false
		}
	}
	return true
}

// isLikelyRealThoughtSignature tells a Gemini thought signature from a sentinel or a
// foreign id smuggled into the field, which Cloud Code Assist rejects with a base64
// decoding error.
func isLikelyRealThoughtSignature(sig string) bool {
	return len(sig) >= minThoughtSignatureSz &&
		sig != thoughtSignatureSentinel &&
		!foreignSignaturePrefix.MatchString(sig) &&
		base64SignatureChars.MatchString(sig)
}

func compileSystemInstruction(raw any, wireModel string) map[string]any {
	var texts []string
	switch v := raw.(type) {
	case map[string]any:
		parts, _ := v["parts"].([]any)
		for _, p := range parts {
			if pm, ok := p.(map[string]any); ok {
				if text, ok := pm["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
	case string:
		texts = append(texts, v)
	}
	if len(texts) == 0 {
		return nil
	}

	// A leading billing-header line makes Cloud Code Assist answer 429 RESOURCE_EXHAUSTED.
	first := strings.TrimLeft(texts[0], " \t\r\n")
	if strings.HasPrefix(first, billingHeaderPrefix) {
		if idx := strings.IndexByte(first, '\n'); idx >= 0 {
			texts[0] = first[idx+1:]
		} else {
			texts[0] = ""
		}
	}

	// The newest Flash models refuse the Claude Agent SDK identity paragraph the same way.
	stripAgentParagraph := strings.HasPrefix(wireModel, "gemini-3.7-flash") || strings.HasPrefix(wireModel, "gemini-3.8-flash")

	parts := make([]any, 0, len(texts))
	for _, text := range texts {
		if stripAgentParagraph {
			text = strings.ReplaceAll(text, claudeAgentParagraph, "")
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		parts = append(parts, map[string]any{"text": text})
	}
	if len(parts) == 0 {
		return nil
	}
	return map[string]any{"parts": parts}
}

// compileTools emits function declarations with sanitized `parameters` schemas and
// encoded names. Built-in Google tools (search, code execution, URL context) are kept
// for Gemini models only.
func compileTools(raw any, names *toolNameCodec, geminiFamily bool) ([]any, bool) {
	list, _ := raw.([]any)
	var declarations []any
	var builtins []any
	for _, entry := range list {
		tool, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		for key, value := range tool {
			switch key {
			case "functionDeclarations", "function_declarations":
				decls, _ := value.([]any)
				for _, d := range decls {
					decl, ok := d.(map[string]any)
					if !ok {
						continue
					}
					name, _ := decl["name"].(string)
					if name == "" {
						continue
					}
					compiled := map[string]any{"name": names.encode(name)}
					if desc, ok := decl["description"].(string); ok && desc != "" {
						compiled["description"] = desc
					}
					compiled["parameters"] = sanitizeToolParameters(firstPresent(decl, "parametersJsonSchema", "parameters_json_schema", "parameters"))
					declarations = append(declarations, compiled)
				}
			case "googleSearch", "google_search", "codeExecution", "code_execution", "urlContext", "url_context":
				if geminiFamily {
					builtins = append(builtins, map[string]any{key: value})
				}
			}
		}
	}
	var tools []any
	if len(declarations) > 0 {
		tools = append(tools, map[string]any{"functionDeclarations": declarations})
	}
	tools = append(tools, builtins...)
	return tools, len(declarations) > 0
}

func compileToolConfig(raw any, names *toolNameCodec) map[string]any {
	src, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	fcc, ok := firstPresent(src, "functionCallingConfig", "function_calling_config").(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	if mode, ok := fcc["mode"].(string); ok && mode != "" {
		out["mode"] = strings.ToUpper(mode)
	}
	if allowed, ok := firstPresent(fcc, "allowedFunctionNames", "allowed_function_names").([]any); ok && len(allowed) > 0 {
		encoded := make([]any, 0, len(allowed))
		for _, a := range allowed {
			if name, ok := a.(string); ok && name != "" {
				encoded = append(encoded, names.encode(name))
			}
		}
		if len(encoded) > 0 {
			out["allowedFunctionNames"] = encoded
		}
	}
	if len(out) == 0 {
		return nil
	}
	return map[string]any{"functionCallingConfig": out}
}

// compileGenerationConfig keeps the generationConfig fields Cloud Code Assist accepts,
// clamped to its limits, and sets thinkingConfig from the resolved thinking level.
func compileGenerationConfig(raw any, opts compileOptions, geminiFamily bool) (map[string]any, error) {
	src, _ := raw.(map[string]any)
	out := map[string]any{}

	if v, ok := toFloat(firstPresent(src, "maxOutputTokens", "max_output_tokens")); ok {
		if tokens := int(math.Floor(v)); tokens > 0 {
			out["maxOutputTokens"] = min(tokens, maxOutputTokenCap(opts.wireModel))
		}
	}
	if v, ok := toFloat(src["temperature"]); ok {
		out["temperature"] = math.Min(math.Max(v, 0), 2)
	}
	if v, ok := toFloat(firstPresent(src, "topP", "top_p")); ok {
		out["topP"] = math.Min(math.Max(v, 0), 1)
	}
	if list, ok := firstPresent(src, "stopSequences", "stop_sequences").([]any); ok {
		var stops []any
		seen := map[string]bool{}
		for _, s := range list {
			str, ok := s.(string)
			if !ok || str == "" || seen[str] {
				continue
			}
			seen[str] = true
			stops = append(stops, str)
			if len(stops) == maxStopSequences {
				break
			}
		}
		if len(stops) > 0 {
			out["stopSequences"] = stops
		}
	}
	if list, ok := firstPresent(src, "responseModalities", "response_modalities").([]any); ok {
		var modalities []any
		for _, m := range list {
			if str, ok := m.(string); ok {
				switch upper := strings.ToUpper(str); upper {
				case "TEXT", "IMAGE", "AUDIO":
					modalities = append(modalities, upper)
				}
			}
		}
		if len(modalities) > 0 {
			out["responseModalities"] = modalities
		}
	}
	if mime, ok := firstPresent(src, "responseMimeType", "response_mime_type").(string); ok && mime != "" {
		out["responseMimeType"] = mime
	}
	if schema := firstPresent(src, "responseJsonSchema", "response_json_schema"); schema != nil {
		out["responseJsonSchema"] = schema
	}
	if !geminiFamily && (out["responseJsonSchema"] != nil || out["responseMimeType"] == "application/json") {
		return nil, &badRequestError{message: "antigravity: structured output is only supported on Gemini models, not " + opts.wireModel}
	}

	if opts.wireModel == imageModelID {
		out["responseModalities"] = []any{"TEXT", "IMAGE"}
	} else {
		thinking := map[string]any{}
		if opts.thinkingLevel != "" {
			thinking["thinkingLevel"] = opts.thinkingLevel
		}
		if geminiFamily {
			thinking["includeThoughts"] = true
		}
		if len(thinking) > 0 {
			out["thinkingConfig"] = thinking
		}
	}

	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// sessionIDFor derives a session id that stays stable across the turns of one
// conversation, from its first user text; Cloud Code Assist keys caching on it.
func sessionIDFor(contents []any) string {
	for _, entry := range contents {
		content, _ := entry.(map[string]any)
		if content == nil || content["role"] != "user" {
			continue
		}
		parts, _ := content["parts"].([]any)
		for _, p := range parts {
			part, _ := p.(map[string]any)
			text, _ := part["text"].(string)
			if text == "" || text == continueText {
				continue
			}
			sum := sha256.Sum256([]byte(text))
			return "-" + strconv.FormatUint(binary.BigEndian.Uint64(sum[:8])&math.MaxInt64, 10)
		}
	}
	random := uuid.New()
	return "-" + strconv.FormatUint(binary.BigEndian.Uint64(random[:8])&math.MaxInt64, 10)
}

func firstPresent(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func unmarshalUseNumber(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}

// marshalJSON encodes without HTML escaping, so prompts reach upstream byte-for-byte.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
