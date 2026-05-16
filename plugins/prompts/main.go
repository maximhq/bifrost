// Package prompts implements the Bifrost LLM plugin that resolves stored prompt templates
// from the config store and prepends their messages to chat and Responses API requests.
// HTTP clients select a prompt via x-bf-prompt-id / x-bf-prompt-version headers; optional
// custom PromptResolver implementations can override how ID and version are chosen.
package prompts

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

const (
	// PluginName is the canonical name registered for the prompts plugin.
	PluginName = "prompts"

	// PromptIDHeader and PromptVersionHeader are request headers copied into BifrostContext
	// in HTTPTransportPreHook so PreLLMHook and custom resolvers can read them.
	PromptIDHeader      = "x-bf-prompt-id"
	PromptVersionHeader = "x-bf-prompt-version"

	// PromptIDKey and PromptVersionKey are context keys for the resolved header values.
	PromptIDKey      schemas.BifrostContextKey = PromptIDHeader
	PromptVersionKey schemas.BifrostContextKey = PromptVersionHeader
)

// InMemoryStore is the data source for prompts and all versions. Implementations typically
// wrap the framework config store; the plugin keeps an in-memory index built by loadCache.
type InMemoryStore interface {
	GetPrompts(ctx context.Context, folderID *string) ([]configstoreTables.TablePrompt, error)
	GetAllPromptVersions(ctx context.Context) ([]configstoreTables.TablePromptVersion, error)
}

// PromptResolver decides which prompt and version to inject for a given request.
// Returning an empty promptID means no injection for this request.
type PromptResolver interface {
	Resolve(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (promptID string, versionNumber int, err error)
}

// headerResolver is the default OSS resolver: it reads prompt ID and version from context
// keys populated from HTTP headers in HTTPTransportPreHook (x-bf-prompt-id, x-bf-prompt-version).
type headerResolver struct {
	logger schemas.Logger
}

// Resolve returns the prompt ID and version number from context. An empty promptID means
// no prompt injection for this request. Version 0 means “use latest” when passed to resolveVersion.
func (r *headerResolver) Resolve(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (string, int, error) {
	promptID := bifrost.GetStringFromContext(ctx, PromptIDKey)
	if promptID == "" {
		return "", 0, nil
	}
	versionNumber, err := parseNumberFromContext(ctx, PromptVersionKey)
	if err != nil {
		return "", 0, fmt.Errorf("failed to parse version number: %w", err)
	}
	return promptID, versionNumber, nil
}

// Plugin implements schemas.LLMPlugin (and HTTP transport hooks) for server-side prompt injection.
// It loads prompts and versions into memory, resolves which version to use per request, merges
// the version’s model parameters with the client request (request wins), and prepends template
// messages before chat or Responses input.
//
// Fields:
//   - store: backing persistence for prompts and versions
//   - logger: Bifrost logger for non-fatal merge/param warnings
//   - resolver: chooses prompt ID and version; defaults to headerResolver
//   - mu: protects promptsByID and versionsByPromptAndNumber
//   - promptsByID: prompt ID → prompt row (includes LatestVersion when using “latest”)
//   - versionsByPromptAndNumber: prompt ID → version number → version row
type Plugin struct {
	store    InMemoryStore
	logger   schemas.Logger
	resolver PromptResolver

	mu                        sync.RWMutex
	promptsByID               map[string]*configstoreTables.TablePrompt
	versionsByPromptAndNumber map[string]map[int]*configstoreTables.TablePromptVersion
}

// Init constructs a Plugin using the default header-based resolver (x-bf-prompt-id / x-bf-prompt-version).
//
// Parameters:
//   - ctx: used for the initial loadCache call
//   - store: required config store backend for prompts
//   - logger: used by the default resolver and param merge paths
//
// Returns:
//   - schemas.LLMPlugin: the initialized plugin
//   - error: if the store is missing or the initial cache load fails
func Init(ctx context.Context, store InMemoryStore, logger schemas.Logger) (schemas.LLMPlugin, error) {
	return InitWithResolver(ctx, store, &headerResolver{logger: logger}, logger)
}

// InitWithResolver constructs a Plugin with an explicit PromptResolver (nil falls back to headerResolver).
//
// Parameters:
//   - ctx: used for the initial loadCache call
//   - store: required config store backend for prompts
//   - resolver: custom resolution logic; if nil, headerResolver is used
//   - logger: passed to the default resolver when it is constructed internally
//
// Returns:
//   - *Plugin: the initialized plugin (concrete type for Reload and handler integration)
//   - error: if the store is missing or the initial cache load fails
func InitWithResolver(ctx context.Context, store InMemoryStore, resolver PromptResolver, logger schemas.Logger) (*Plugin, error) {
	if store == nil {
		return nil, fmt.Errorf("config store is required for prompts plugin")
	}
	if resolver == nil {
		resolver = &headerResolver{logger: logger}
	}
	p := &Plugin{
		store:                     store,
		logger:                    logger,
		resolver:                  resolver,
		promptsByID:               make(map[string]*configstoreTables.TablePrompt),
		versionsByPromptAndNumber: make(map[string]map[int]*configstoreTables.TablePromptVersion),
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

	newVersionsByPromptAndNumber := make(map[string]map[int]*configstoreTables.TablePromptVersion)
	for i := range versions {
		v := &versions[i]
		if _, ok := newVersionsByPromptAndNumber[v.PromptID]; !ok {
			newVersionsByPromptAndNumber[v.PromptID] = make(map[int]*configstoreTables.TablePromptVersion)
		}
		newVersionsByPromptAndNumber[v.PromptID][v.VersionNumber] = v
	}

	p.mu.Lock()
	p.promptsByID = newPrompts
	p.versionsByPromptAndNumber = newVersionsByPromptAndNumber
	p.mu.Unlock()
	return nil
}

// Reload refreshes the in-memory cache from the store. Called by the HTTP handler
// after any create/update/delete operation on prompts or versions.
func (p *Plugin) Reload(ctx context.Context) error {
	return p.loadCache(ctx)
}

// GetName returns the plugin identifier ("prompts").
func (p *Plugin) GetName() string {
	return PluginName
}

// HTTPTransportPreHook copies x-bf-prompt-id and x-bf-prompt-version from the incoming HTTP request
// into BifrostContext so the default header resolver and PreLLMHook can read them.
func (p *Plugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	if req == nil {
		return nil, nil
	}
	if id := strings.TrimSpace(req.CaseInsensitiveHeaderLookup(PromptIDHeader)); id != "" {
		ctx.SetValue(PromptIDKey, id)
	}
	if v := strings.TrimSpace(req.CaseInsensitiveHeaderLookup(PromptVersionHeader)); v != "" {
		ctx.SetValue(PromptVersionKey, v)
	}
	return nil, nil
}

// HTTPTransportPostHook is a no-op; this plugin does not modify HTTP response headers.
func (p *Plugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes streaming chunks through unchanged; prompt injection
// happens in PreLLMHook before the provider call.
func (p *Plugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// PreLLMHook resolves the prompt via PromptResolver, loads the version from the in-memory
// cache, sets governance/observability context (selected prompt name and version), merges
// version ModelParams with the request (request overrides), converts stored messages to
// chat messages, and prepends them to Chat or Responses input. Non-HTTP transports rely
// on context keys set by callers instead of HTTPTransportPreHook.
//
// Parameters:
//   - ctx: may set BifrostContextKeySelectedPromptName, BifrostContextKeySelectedPromptID and BifrostContextKeySelectedPromptVersion when a prompt is applied
//   - req: chat or Responses request to mutate in place
//
// Returns:
//   - *schemas.BifrostRequest: possibly modified request
//   - *schemas.LLMPluginShortCircuit: always nil
//   - error: resolution failure or missing prompt/version; invalid or empty template returns
//     the request unchanged with a nil error
func (p *Plugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if req == nil {
		return req, nil, nil
	}

	promptID, versionNumber, err := p.resolver.Resolve(ctx, req)
	if err != nil {
		p.logger.Warn("prompts plugin: failed to resolve prompt: %v", err)
		return req, nil, nil
	}
	if promptID == "" {
		return req, nil, nil
	}

	prompt, version, found := p.resolveVersion(promptID, versionNumber)
	if !found {
		p.logger.Warn("prompts plugin: prompt or version not found: promptID=%s versionNumber=%d", promptID, versionNumber)
		return req, nil, nil
	}

	if version == nil {
		p.logger.Warn("prompts plugin: prompt has no resolved version: promptID=%s", promptID)
		return req, nil, nil
	}

	if prompt != nil && prompt.Name != "" {
		ctx.SetValue(schemas.BifrostContextKeySelectedPromptID, prompt.ID)
		ctx.SetValue(schemas.BifrostContextKeySelectedPromptName, prompt.Name)
	}
	ctx.SetValue(schemas.BifrostContextKeySelectedPromptVersion, strconv.Itoa(version.VersionNumber))

	// Apply model params from the version (version params are defaults; request params win).
	switch {
	case req.ChatRequest != nil:
		applyVersionParamsToChatRequest(version, req.ChatRequest, p.logger)
	case req.ResponsesRequest != nil:
		applyVersionParamsToResponsesRequest(version, req.ResponsesRequest, p.logger)
	}

	template, err := chatMessagesFromVersionMessages(version.Messages)
	if err != nil {
		p.logger.Warn("prompts plugin: failed to convert version messages to chat messages: %v", err)
		return req, nil, nil
	}
	if len(template) == 0 {
		p.logger.Warn("prompts plugin: no template messages found for prompt %s version %d", promptID, version.VersionNumber)
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

// PostLLMHook is a no-op; the plugin does not modify responses.
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
// serve as defaults and request params take priority. reqParamsBytes is the JSON of
// the request's standard params (ExtraParams excluded); reqExtraParams is its ExtraParams map.
func buildMergedParamsMap(versionParams configstoreTables.ModelParams, reqParamsBytes []byte, reqExtraParams map[string]interface{}) (map[string]interface{}, error) {
	merged := make(map[string]interface{}, len(versionParams))
	maps.Copy(merged, versionParams)
	if len(reqParamsBytes) > 0 && string(reqParamsBytes) != "null" {
		var reqMap map[string]interface{}
		if err := schemas.Unmarshal(reqParamsBytes, &reqMap); err != nil {
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

	var reqParamsBytes []byte
	var reqExtraParams map[string]interface{}
	if req.Params != nil {
		b, err := schemas.Marshal(req.Params)
		if err != nil {
			logger.Warn("prompts plugin: failed to marshal chat request params: %v", err)
			return
		}
		reqParamsBytes = b
		reqExtraParams = req.Params.ExtraParams
	}

	merged, err := buildMergedParamsMap(version.ModelParams, reqParamsBytes, reqExtraParams)
	if err != nil {
		logger.Warn("prompts plugin: failed to build merged chat params: %v", err)
		return
	}

	mergedJSON, err := schemas.Marshal(merged)
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
	var recognizedMap map[string]interface{}
	recognizedBytes, err := schemas.Marshal(&result)
	if err != nil {
		logger.Warn("prompts plugin: failed to marshal result chat params: %v", err)
		return
	}
	if err := schemas.Unmarshal(recognizedBytes, &recognizedMap); err != nil {
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

	var reqParamsBytes []byte
	var reqExtraParams map[string]interface{}
	if req.Params != nil {
		b, err := schemas.Marshal(req.Params)
		if err != nil {
			logger.Warn("prompts plugin: failed to marshal responses request params: %v", err)
			return
		}
		reqParamsBytes = b
		reqExtraParams = req.Params.ExtraParams
	}

	merged, err := buildMergedParamsMap(version.ModelParams, reqParamsBytes, reqExtraParams)
	if err != nil {
		logger.Warn("prompts plugin: failed to build merged responses params: %v", err)
		return
	}

	mergedJSON, err := schemas.Marshal(merged)
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
	var recognizedMap map[string]interface{}
	recognizedBytes, err := schemas.Marshal(&result)
	if err != nil {
		logger.Warn("prompts plugin: failed to marshal result responses params: %v", err)
		return
	}
	if err := schemas.Unmarshal(recognizedBytes, &recognizedMap); err != nil {
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
// and PreLLMHook. It returns the prompt and its resolved version.
//
// If versionNumber > 0, that explicit version is loaded from versionsByPromptAndNumber (from
// x-bf-prompt-version header or a custom PromptResolver such as deployment traffic routing).
// If versionNumber == 0, the prompt's latest version is used (no header / resolver chose latest).
func (p *Plugin) resolveVersion(promptID string, versionNumber int) (
	*configstoreTables.TablePrompt, *configstoreTables.TablePromptVersion, bool,
) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	prompt, ok := p.promptsByID[promptID]
	if !ok || prompt == nil {
		return nil, nil, false
	}
	if versionNumber > 0 {
		byNumber, ok := p.versionsByPromptAndNumber[promptID]
		if !ok {
			return nil, nil, false
		}
		v, found := byNumber[versionNumber]
		if !found || v == nil {
			return nil, nil, false
		}
		return prompt, v, true
	}
	return prompt, prompt.LatestVersion, true
}

// Cleanup releases plugin resources; the prompts plugin has nothing to tear down.
func (p *Plugin) Cleanup() error {
	return nil
}

// parseNumberFromContext parses a decimal integer from a string context value. Missing or
// empty values yield 0 with no error (treated as “no explicit version”).
func parseNumberFromContext(ctx *schemas.BifrostContext, key schemas.BifrostContextKey) (num int, err error) {
	s, ok := ctx.Value(key).(string)
	if !ok {
		return 0, nil
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// chatMessagePopulated reports whether a ChatMessage carries any meaningful content for injection.
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

	var msg struct {
		OriginalType string          `json:"originalType"`
		Payload      json.RawMessage `json:"payload"`
	}
	if err := schemas.Unmarshal(data, &msg); err == nil {
		ps := strings.TrimSpace(string(msg.Payload))
		if ps != "" && ps != "null" {
			if msg.OriginalType == "completion_result" {
				var result struct {
					Choices []struct {
						Message *schemas.ChatMessage `json:"message"`
					} `json:"choices"`
				}
				if err := schemas.Unmarshal([]byte(ps), &result); err == nil &&
					len(result.Choices) > 0 && result.Choices[0].Message != nil {
					if chatMessagePopulated(*result.Choices[0].Message) {
						return *result.Choices[0].Message, nil
					}
				}
			}

			// completion_request / tool_result / legacy envelope: payload is a direct ChatMessage.
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

// chatMessagesFromVersionMessages decodes each stored row into schemas.ChatMessage, preferring
// Message bytes and falling back to MessageJSON when needed.
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

// mergeChatMessages prepends prefix to the chat input slice (template first, then client messages).
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

// mergeResponsesMessages converts template chat messages to ResponsesMessage entries and
// prepends them before the client’s Responses input.
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
