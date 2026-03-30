package prompts

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

const (
	PluginName                                      = "prompts"
	PromptIDHeader                                  = "bifrost-prompt-id"
	PromptVersionIDHeader                           = "bifrost-prompt-version-id"
	PromptIDKey           schemas.BifrostContextKey = PromptIDHeader
	PromptVersionIDKey    schemas.BifrostContextKey = PromptVersionIDHeader
)

type promptStore interface {
	GetPrompts(ctx context.Context, folderID *string) ([]configstoreTables.TablePrompt, error)
	GetAllPromptVersions(ctx context.Context) ([]configstoreTables.TablePromptVersion, error)
}

// PromptResolver decides which prompt and version to inject for a given request.
// Returning an empty promptID means no injection for this request.
type PromptResolver interface {
	Resolve(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (promptID string, versionID uint, versionSpecified bool, err error)
}

// headerResolver is the default OSS resolver: reads prompt ID and version from context
// keys that were populated from HTTP headers in HTTPTransportPreHook.
type headerResolver struct {
	logger schemas.Logger
}

func (r *headerResolver) Resolve(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (string, uint, bool, error) {
	promptID := promptStringFromCtx(ctx, PromptIDKey)
	if promptID == "" {
		return "", 0, false, nil
	}
	versionID, specified, err := parsePromptVersionID(ctx)
	if err != nil {
		return "", 0, false, fmt.Errorf("invalid bifrost-prompt-version-id: %w", err)
	}
	return promptID, versionID, specified, nil
}

// Plugin resolves stored prompt templates and prepends their messages to LLM requests.
type Plugin struct {
	store    promptStore
	logger   schemas.Logger
	resolver PromptResolver

	mu           sync.RWMutex
	promptsByID  map[string]*configstoreTables.TablePrompt
	versionsByID map[uint]*configstoreTables.TablePromptVersion
}

// Init wires the prompts plugin with the default header-based resolver.
func Init(ctx context.Context, store promptStore, logger schemas.Logger) (schemas.LLMPlugin, error) {
	return InitWithResolver(ctx, store, &headerResolver{logger: logger}, logger)
}

// InitWithResolver wires the prompts plugin with a custom resolver.
func InitWithResolver(ctx context.Context, store promptStore, resolver PromptResolver, logger schemas.Logger) (*Plugin, error) {
	if store == nil {
		return nil, fmt.Errorf("config store is required for prompts plugin")
	}
	if resolver == nil {
		resolver = &headerResolver{logger: logger}
	}
	p := &Plugin{
		store:        store,
		logger:       logger,
		resolver:     resolver,
		promptsByID:  make(map[string]*configstoreTables.TablePrompt),
		versionsByID: make(map[uint]*configstoreTables.TablePromptVersion),
	}
	if err := p.loadCache(ctx); err != nil {
		return nil, fmt.Errorf("failed to load prompts into memory: %w", err)
	}
	return p, nil
}

// loadCache rebuilds the in-memory maps with exactly two DB queries:
// one for all prompts (with their latest version), one for all versions.
func (p *Plugin) loadCache(ctx context.Context) error {
	prompts, err := p.store.GetPrompts(ctx, nil)
	if err != nil {
		return err
	}

	versions, err := p.store.GetAllPromptVersions(ctx)
	if err != nil {
		return fmt.Errorf("loading all prompt versions: %w", err)
	}

	newPrompts := make(map[string]*configstoreTables.TablePrompt, len(prompts))
	for i := range prompts {
		newPrompts[prompts[i].ID] = &prompts[i]
	}

	newVersions := make(map[uint]*configstoreTables.TablePromptVersion, len(versions))
	for i := range versions {
		newVersions[versions[i].ID] = &versions[i]
	}

	p.mu.Lock()
	p.promptsByID = newPrompts
	p.versionsByID = newVersions
	p.mu.Unlock()
	return nil
}

// Reload refreshes the in-memory cache from the store. Called by the HTTP handler
// after any create/update/delete operation on prompts or versions.
func (p *Plugin) Reload(ctx context.Context) error {
	return p.loadCache(ctx)
}

func (p *Plugin) GetName() string {
	return PluginName
}

func (p *Plugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	if req == nil {
		return nil, nil
	}
	if id := strings.TrimSpace(req.CaseInsensitiveHeaderLookup(PromptIDHeader)); id != "" {
		ctx.SetValue(PromptIDKey, id)
	}
	if v := strings.TrimSpace(req.CaseInsensitiveHeaderLookup(PromptVersionIDHeader)); v != "" {
		ctx.SetValue(PromptVersionIDKey, v)
	}
	p.setPromptStreamFromVersionForTransport(ctx)
	return nil, nil
}

// setPromptStreamFromVersionForTransport sets BifrostContextKeyStreamRequest when
// the resolved prompt version has stream:true in its ModelParams.
func (p *Plugin) setPromptStreamFromVersionForTransport(ctx *schemas.BifrostContext) {
	promptID := promptStringFromCtx(ctx, PromptIDKey)
	if promptID == "" {
		return
	}
	versionID, versionSpecified, err := parsePromptVersionID(ctx)
	if err != nil {
		return
	}
	_, version, ok := p.resolveVersion(promptID, versionID, versionSpecified)
	if !ok || version == nil || len(version.ModelParams) == 0 {
		return
	}
	if includesStreamInModelParams(version.ModelParams) {
		ctx.SetValue(schemas.BifrostContextKeyStreamRequest, true)
	}
}

func includesStreamInModelParams(mp configstoreTables.ModelParams) bool {
	raw, ok := mp["stream"]
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case json.Number:
		if i, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			return i != 0
		}
		b, err := strconv.ParseBool(string(v))
		return err == nil && b
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func (p *Plugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

func (p *Plugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

func (p *Plugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	promptID, versionID, versionSpecified, err := p.resolver.Resolve(ctx, req)
	if err != nil {
		p.logger.Warn("prompts plugin: failed to resolve prompt: %v", err)
		return req, nil, nil
	}
	if promptID == "" {
		return req, nil, nil
	}

	_, version, found := p.resolveVersion(promptID, versionID, versionSpecified)
	if !found {
		p.logger.Warn("prompts plugin: prompt or version not found: %s", promptID)
		return req, nil, nil
	}

	if version == nil {
		p.logger.Warn("prompts plugin: prompt %s has no versions", promptID)
		return req, nil, nil
	}

	// Apply model params from the version (version params are defaults; request params win).
	switch {
	case req.ChatRequest != nil:
		applyVersionParamsToChatRequest(version, req.ChatRequest, p.logger)
	case req.ResponsesRequest != nil:
		applyVersionParamsToResponsesRequest(version, req.ResponsesRequest, p.logger)
	}

	template, err := chatMessagesFromVersionMessages(version.Messages)
	if err != nil {
		p.logger.Warn("prompts plugin: failed to parse messages for prompt %s: %v", promptID, err)
		return req, nil, nil
	}
	if len(template) == 0 {
		return req, nil, nil
	}

	switch {
	case req.ChatRequest != nil:
		mergeChatMessages(&req.ChatRequest.Input, template)
	case req.ResponsesRequest != nil:
		mergeResponsesMessages(&req.ResponsesRequest.Input, template)
	}

	return req, nil, nil
}

func (p *Plugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// knownSyntheticChatParamKeys are flat JSON keys that ChatParameters.UnmarshalJSON
// promotes into nested structs. They should not be treated as ExtraParams even though
// they won't appear as top-level keys in a re-marshaled ChatParameters.
var knownSyntheticChatParamKeys = map[string]struct{}{
	"reasoning_effort":     {},
	"reasoning_max_tokens": {},
}

// buildMergedParamsMap builds a merged map[string]interface{} where version params
// serve as defaults and request params take priority. reqParamsJSON is the JSON of
// the request's standard params (ExtraParams excluded); reqExtraParams is its ExtraParams map.
func buildMergedParamsMap(versionParams configstoreTables.ModelParams, reqParamsJSON []byte, reqExtraParams map[string]interface{}) (map[string]interface{}, error) {
	merged := make(map[string]interface{}, len(versionParams))
	maps.Copy(merged, versionParams)
	if len(reqParamsJSON) > 0 && string(reqParamsJSON) != "null" {
		var reqMap map[string]interface{}
		if err := json.Unmarshal(reqParamsJSON, &reqMap); err != nil {
			return nil, fmt.Errorf("unmarshal request params: %w", err)
		}
		maps.Copy(merged, reqMap)
	}
	maps.Copy(merged, reqExtraParams)
	return merged, nil
}

// applyVersionParamsToChatRequest applies the prompt version's ModelParams to the
// chat request. Version params are defaults; params already set in the request win.
func applyVersionParamsToChatRequest(version *configstoreTables.TablePromptVersion, req *schemas.BifrostChatRequest, logger schemas.Logger) {
	if len(version.ModelParams) == 0 {
		return
	}

	var reqParamsJSON []byte
	var reqExtraParams map[string]interface{}
	if req.Params != nil {
		b, err := json.Marshal(req.Params)
		if err != nil {
			logger.Warn("prompts plugin: failed to marshal chat request params: %v", err)
			return
		}
		reqParamsJSON = b
		reqExtraParams = req.Params.ExtraParams
	}

	merged, err := buildMergedParamsMap(version.ModelParams, reqParamsJSON, reqExtraParams)
	if err != nil {
		logger.Warn("prompts plugin: failed to build merged chat params: %v", err)
		return
	}

	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		logger.Warn("prompts plugin: failed to marshal merged chat params: %v", err)
		return
	}

	var result schemas.ChatParameters
	if err := schemas.Unmarshal(mergedJSON, &result); err != nil {
		logger.Warn("prompts plugin: failed to unmarshal merged chat params: %v", err)
		return
	}

	// Detect keys from merged that were not recognized as standard ChatParameters fields
	// (i.e. they won't appear in the re-marshaled output) and put them in ExtraParams.
	recognizedJSON, err := json.Marshal(&result)
	if err != nil {
		logger.Warn("prompts plugin: failed to marshal result chat params: %v", err)
		return
	}
	var recognizedMap map[string]interface{}
	if err := json.Unmarshal(recognizedJSON, &recognizedMap); err != nil {
		logger.Warn("prompts plugin: failed to unmarshal recognized chat params: %v", err)
		return
	}
	for k, v := range merged {
		if _, ok := recognizedMap[k]; ok {
			continue
		}
		if _, synthetic := knownSyntheticChatParamKeys[k]; synthetic {
			continue
		}
		if result.ExtraParams == nil {
			result.ExtraParams = make(map[string]interface{})
		}
		if _, alreadySet := result.ExtraParams[k]; !alreadySet {
			result.ExtraParams[k] = v
		}
	}

	req.Params = &result
}

// applyVersionParamsToResponsesRequest applies the prompt version's ModelParams to the
// responses request. Version params are defaults; params already set in the request win.
func applyVersionParamsToResponsesRequest(version *configstoreTables.TablePromptVersion, req *schemas.BifrostResponsesRequest, logger schemas.Logger) {
	if len(version.ModelParams) == 0 {
		return
	}

	var reqParamsJSON []byte
	var reqExtraParams map[string]interface{}
	if req.Params != nil {
		b, err := json.Marshal(req.Params)
		if err != nil {
			logger.Warn("prompts plugin: failed to marshal responses request params: %v", err)
			return
		}
		reqParamsJSON = b
		reqExtraParams = req.Params.ExtraParams
	}

	merged, err := buildMergedParamsMap(version.ModelParams, reqParamsJSON, reqExtraParams)
	if err != nil {
		logger.Warn("prompts plugin: failed to build merged responses params: %v", err)
		return
	}

	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		logger.Warn("prompts plugin: failed to marshal merged responses params: %v", err)
		return
	}

	var result schemas.ResponsesParameters
	if err := schemas.Unmarshal(mergedJSON, &result); err != nil {
		logger.Warn("prompts plugin: failed to unmarshal merged responses params: %v", err)
		return
	}

	// Detect unrecognized keys and add them to ExtraParams.
	recognizedJSON, err := json.Marshal(&result)
	if err != nil {
		logger.Warn("prompts plugin: failed to marshal result responses params: %v", err)
		return
	}
	var recognizedMap map[string]interface{}
	if err := json.Unmarshal(recognizedJSON, &recognizedMap); err != nil {
		logger.Warn("prompts plugin: failed to unmarshal recognized responses params: %v", err)
		return
	}
	for k, v := range merged {
		if _, ok := recognizedMap[k]; ok {
			continue
		}
		if result.ExtraParams == nil {
			result.ExtraParams = make(map[string]interface{})
		}
		if _, alreadySet := result.ExtraParams[k]; !alreadySet {
			result.ExtraParams[k] = v
		}
	}

	req.Params = &result
}

// resolveVersion centralises the map-lookup logic shared by setPromptStreamFromVersionForTransport
// and PreLLMHook. It returns the prompt and its resolved version (either the explicitly requested
// version or the prompt's latest version), plus a bool indicating whether both were found.
func (p *Plugin) resolveVersion(promptID string, versionID uint, versionSpecified bool) (
	*configstoreTables.TablePrompt, *configstoreTables.TablePromptVersion, bool,
) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	prompt, ok := p.promptsByID[promptID]
	if !ok || prompt == nil {
		return nil, nil, false
	}
	if !versionSpecified {
		return prompt, prompt.LatestVersion, true
	}
	v, found := p.versionsByID[versionID]
	if !found || v == nil || v.PromptID != promptID {
		return nil, nil, false
	}
	return prompt, v, true
}

func (p *Plugin) Cleanup() error {
	return nil
}

func promptStringFromCtx(ctx *schemas.BifrostContext, key schemas.BifrostContextKey) string {
	if v, ok := ctx.Value(key).(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func parsePromptVersionID(ctx *schemas.BifrostContext) (id uint, specified bool, err error) {
	s, ok := ctx.Value(PromptVersionIDKey).(string)
	if !ok {
		return 0, false, nil
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, true, err
	}
	return uint(n), true, nil
}

func chatMessagePopulated(cm schemas.ChatMessage) bool {
	if strings.TrimSpace(string(cm.Role)) != "" {
		return true
	}
	if cm.Content != nil {
		return true
	}
	if cm.Name != nil && strings.TrimSpace(*cm.Name) != "" {
		return true
	}
	if cm.ChatToolMessage != nil {
		return true
	}
	if cm.ChatAssistantMessage != nil {
		return true
	}
	return false
}

// convertVersionMessagesToChatMessages unmarshals prompt-repo JSON into ChatMessage.
func convertVersionMessagesToChatMessages(data []byte) (schemas.ChatMessage, error) {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		return schemas.ChatMessage{}, fmt.Errorf("empty message")
	}
	data = []byte(s)

	var env struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := schemas.Unmarshal(data, &env); err == nil {
		ps := strings.TrimSpace(string(env.Payload))
		if ps != "" && ps != "null" {
			var message schemas.ChatMessage
			if err := schemas.Unmarshal([]byte(ps), &message); err != nil {
				return schemas.ChatMessage{}, fmt.Errorf("decoding prompt message envelope payload: %w", err)
			}
			if chatMessagePopulated(message) {
				return message, nil
			}
		}
	}

	var chatMessage schemas.ChatMessage
	if err := schemas.Unmarshal(data, &chatMessage); err != nil {
		return schemas.ChatMessage{}, err
	}
	return chatMessage, nil
}

func chatMessagesFromVersionMessages(messages []configstoreTables.TablePromptVersionMessage) ([]schemas.ChatMessage, error) {
	out := make([]schemas.ChatMessage, 0, len(messages))
	for i := range messages {
		row := &messages[i]
		data := row.Message
		if len(data) == 0 && row.MessageJSON != "" {
			data = []byte(row.MessageJSON)
		}
		cm, err := convertVersionMessagesToChatMessages(data)
		if err != nil {
			return nil, fmt.Errorf("stored prompt message is not valid chat JSON: %w", err)
		}
		out = append(out, cm)
	}
	return out, nil
}

func mergeChatMessages(dest *[]schemas.ChatMessage, prefix []schemas.ChatMessage) {
	if dest == nil || len(prefix) == 0 {
		return
	}
	cur := *dest
	merged := make([]schemas.ChatMessage, 0, len(prefix)+len(cur))
	merged = append(merged, prefix...)
	merged = append(merged, cur...)
	*dest = merged
}

func mergeResponsesMessages(dest *[]schemas.ResponsesMessage, template []schemas.ChatMessage) {
	if dest == nil || len(template) == 0 {
		return
	}
	var prefix []schemas.ResponsesMessage
	for i := range template {
		prefix = append(prefix, template[i].ToResponsesMessages()...)
	}
	cur := *dest
	merged := make([]schemas.ResponsesMessage, 0, len(prefix)+len(cur))
	merged = append(merged, prefix...)
	merged = append(merged, cur...)
	*dest = merged
}
