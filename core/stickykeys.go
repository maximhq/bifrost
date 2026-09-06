package bifrost

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

const stickyQuotaKeyPrefix = "sticky-quota:"

// stickyRetryObserver is deliberately narrow: retry orchestration reports only
// whether the failed attempt is eligible for sticky-key quota rotation. It
// does not carry request bodies or other per-request state through the retry
// loop.
type stickyRetryObserver interface {
	observeRetry(*schemas.BifrostError, bool)
}

// stickyKeyPlan owns one request's fixed binding and its optional quota
// migration. The plan's pool is captured once, before the first attempt, so a
// concurrent provider/key update cannot broaden a retry's eligibility.
type stickyKeyPlan struct {
	mu sync.Mutex

	ctx      *schemas.BifrostContext
	store    schemas.KVStore
	cas      schemas.ConditionalStringKVStore
	key      string
	ttl      time.Duration
	provider schemas.ModelProvider
	model    string
	pool     []schemas.Key
	selector schemas.KeySelector
	filter   schemas.KeyPoolFilter
	current  schemas.Key
	casReady bool
	rotate   bool
	// Deferred to keyProvider so the retry engine maps rejected pools to the
	// same 503/no_eligible_keys response for ordinary and streaming requests.
	selectionErr error
}

// buildStickyQuotaKey uses a dedicated namespace. Legacy session stickiness
// remains authoritative for non-chat requests and for callers without the
// opt-in, so a chat quota migration cannot move another request type's
// continuation.
func buildStickyQuotaKey(provider schemas.ModelProvider, sessionID, model string) string {
	return stickyQuotaKeyPrefix + buildSessionKey(provider, sessionID, model)
}

// stickyChatPayloadEligible is the replay-safety boundary for opt-in sticky
// quota migration. A quota retry can resend the exact typed request to another
// credential, so only payloads whose state is fully present in the neutral Chat
// schema may enter that path. Unsupported or opaque payloads fall back to the
// existing strict key selector.
func stickyChatPayloadEligible(req *schemas.BifrostChatRequest) bool {
	if req == nil || req.Input == nil || req.RawRequestBody != nil {
		return false
	}

	if params := req.Params; params != nil {
		if params.Container != nil || len(params.ContextManagement) > 0 || len(params.MCPServers) > 0 || len(params.ExtraParams) > 0 {
			return false
		}
		if params.Store != nil && *params.Store {
			return false
		}
		if params.Audio != nil || stickyHasAudioModality(params.Modalities) {
			return false
		}
		if params.IncludeServerSideToolInvocations != nil && *params.IncludeServerSideToolInvocations {
			return false
		}
		if params.WebSearchOptions != nil {
			return false
		}
		for _, tool := range params.Tools {
			if tool.Type != schemas.ChatToolTypeFunction || tool.Function == nil || strings.TrimSpace(tool.Function.Name) == "" {
				return false
			}
			if tool.Annotations != nil || tool.DeferLoading != nil || len(tool.AllowedCallers) > 0 || len(tool.InputExamples) > 0 || tool.EagerInputStreaming != nil {
				return false
			}
		}
	}

	for _, message := range req.Input {
		if !stickyChatMessageEligible(message) {
			return false
		}
	}
	return true
}

func stickyHasAudioModality(modalities []string) bool {
	for _, modality := range modalities {
		if strings.EqualFold(strings.TrimSpace(modality), "audio") {
			return true
		}
	}
	return false
}

func stickyChatMessageEligible(message schemas.ChatMessage) bool {
	switch message.Role {
	case schemas.ChatMessageRoleAssistant, schemas.ChatMessageRoleDeveloper, schemas.ChatMessageRoleSystem, schemas.ChatMessageRoleTool, schemas.ChatMessageRoleUser:
	default:
		return false
	}
	if message.ChatToolMessage != nil && message.ChatAssistantMessage != nil {
		return false
	}
	if message.ChatToolMessage != nil && message.ChatToolMessage.IsError != nil {
		return false
	}
	if message.Content != nil && !stickyChatContentEligible(message.Content) {
		return false
	}
	if message.ChatAssistantMessage != nil && !stickyChatAssistantMessageEligible(message.ChatAssistantMessage) {
		return false
	}
	return message.Content != nil || message.ChatToolMessage != nil || message.ChatAssistantMessage != nil
}

func stickyChatContentEligible(content *schemas.ChatMessageContent) bool {
	if content == nil || (content.ContentStr != nil && content.ContentBlocks != nil) {
		return false
	}
	if content.ContentStr != nil {
		return true
	}
	if content.ContentBlocks == nil || len(content.ContentBlocks) == 0 {
		return false
	}
	for _, block := range content.ContentBlocks {
		if block.Citations != nil {
			return false
		}
		switch block.Type {
		case schemas.ChatContentBlockTypeText:
			if block.Text == nil || block.Refusal != nil || block.ImageURLStruct != nil || block.InputAudio != nil || block.File != nil {
				return false
			}
		case schemas.ChatContentBlockTypeImage:
			if block.ImageURLStruct == nil || block.Text != nil || block.Refusal != nil || block.InputAudio != nil || block.File != nil {
				return false
			}
			image := block.ImageURLStruct
			if image.FileID != nil || strings.TrimSpace(image.URL) == "" || !stickyIsInlineDataURL(image.URL) {
				return false
			}
		case schemas.ChatContentBlockTypeInputAudio:
			if block.InputAudio == nil || strings.TrimSpace(block.InputAudio.Data) == "" || block.Text != nil || block.Refusal != nil || block.ImageURLStruct != nil || block.File != nil {
				return false
			}
		case schemas.ChatContentBlockTypeFile:
			if block.File == nil || block.Text != nil || block.Refusal != nil || block.ImageURLStruct != nil || block.InputAudio != nil {
				return false
			}
			file := block.File
			if file.FileID != nil || file.FileURL != nil || file.FileData == nil || strings.TrimSpace(*file.FileData) == "" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func stickyIsInlineDataURL(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "data:")
}

func stickyChatAssistantMessageEligible(message *schemas.ChatAssistantMessage) bool {
	if message.Audio != nil && strings.TrimSpace(message.Audio.ID) != "" {
		return false
	}
	if message.Refusal != nil || message.Reasoning != nil || len(message.ReasoningDetails) > 0 || len(message.Annotations) > 0 {
		return false
	}
	for _, call := range message.ToolCalls {
		if call.Type != nil && strings.TrimSpace(*call.Type) != "" && !strings.EqualFold(strings.TrimSpace(*call.Type), string(schemas.ChatToolTypeFunction)) {
			return false
		}
		if len(call.ExtraContent) > 0 {
			return false
		}
	}
	return true
}

// SupportsProcessLocalCAS is an optional discriminator implemented by the
// framework store. A delegated cluster store returns false because its local
// mutex does not establish a cluster-wide CAS guarantee.
type processLocalCASDiscriminator interface {
	SupportsProcessLocalCAS() bool
}

// buildStickyKeyPlan returns handled=false when the feature is not applicable
// or its KV store cannot prove process-local CAS support. The caller then uses
// the existing selector path unchanged.
func (bifrost *Bifrost) buildStickyKeyPlan(ctx *schemas.BifrostContext, requestType schemas.RequestType, providerKey schemas.ModelProvider, model string, baseProviderType schemas.ModelProvider) (*stickyKeyPlan, bool, error) {
	if !bifrost.stickyQuotaFailover || bifrost.kvStore == nil || ctx == nil {
		return nil, false, nil
	}
	if requestType != schemas.ChatCompletionRequest && requestType != schemas.ChatCompletionStreamRequest {
		return nil, false, nil
	}
	if _, ok := ctx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key); ok {
		return nil, false, nil
	}
	if skip, _ := ctx.Value(schemas.BifrostContextKeySkipKeySelection).(bool); skip && isKeySkippingAllowed(baseProviderType) {
		return nil, false, nil
	}
	if raw, _ := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); raw {
		return nil, false, nil
	}
	if keyID, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); strings.TrimSpace(keyID) != "" {
		return nil, false, nil
	}
	if keyName, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyName).(string); strings.TrimSpace(keyName) != "" {
		return nil, false, nil
	}
	sessionID, _ := ctx.Value(schemas.BifrostContextKeySessionID).(string)
	if sessionID == "" {
		return nil, false, nil
	}
	fallbackIndex, _ := ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int)
	if fallbackIndex != 0 {
		return nil, false, nil
	}

	cas, ok := bifrost.kvStore.(schemas.ConditionalStringKVStore)
	if !ok {
		return nil, false, nil
	}
	if discriminator, ok := cas.(processLocalCASDiscriminator); ok && !discriminator.SupportsProcessLocalCAS() {
		return nil, false, nil
	}

	keys, err := bifrost.account.GetKeysForProvider(ctx, providerKey)
	if err != nil {
		return nil, true, err
	}
	pool := eligibleStickyKeys(bifrost, keys, model, baseProviderType, providerKey)
	if len(pool) < 2 {
		return nil, false, nil
	}
	if bifrost.keyPoolFilter != nil {
		// Filter before both cached-binding lookup and initial selection. Keep
		// the captured keys canonical even if the hook edits its input slice.
		filtered, filterErr := bifrost.keyPoolFilter(ctx, providerKey, model, append([]schemas.Key(nil), pool...))
		if filterErr != nil {
			// Match normal initial selection's fail-open handling. Quota
			// migration still keeps the current key when its filter fails.
			bifrost.logger.Warn("key pool filter failed for provider %s, using unfiltered keys: %v", providerKey, filterErr)
		} else {
			canonical := make([]schemas.Key, 0, len(filtered))
			seen := make(map[string]bool, len(filtered))
			for _, key := range filtered {
				original, valid := stickyKeyByID(key.ID, pool)
				if !valid {
					return &stickyKeyPlan{selectionErr: fmt.Errorf("%w: provider %s", errAllKeysFiltered, providerKey)}, true, nil
				}
				if !seen[key.ID] {
					canonical = append(canonical, original)
					seen[key.ID] = true
				}
			}
			if len(canonical) == 0 {
				return &stickyKeyPlan{selectionErr: fmt.Errorf("%w: provider %s", errAllKeysFiltered, providerKey)}, true, nil
			}
			// Keep a one-key filtered plan handled: falling back to the legacy
			// selector would rebuild an unfiltered pool and undo the veto.
			pool = canonical
		}
	}

	ttl, _ := ctx.Value(schemas.BifrostContextKeySessionTTL).(time.Duration)
	if ttl <= 0 {
		ttl = schemas.DefaultSessionStickyTTL
	}
	plan := &stickyKeyPlan{
		ctx:      ctx,
		store:    bifrost.kvStore,
		cas:      cas,
		key:      buildStickyQuotaKey(providerKey, sessionID, model),
		ttl:      ttl,
		provider: providerKey,
		model:    model,
		pool:     pool,
		selector: bifrost.keySelector,
		filter:   bifrost.keyPoolFilter,
		casReady: true,
	}
	if err := plan.initialize(); err != nil {
		return nil, true, err
	}
	return plan, true, nil
}

func eligibleStickyKeys(bifrost *Bifrost, keys []schemas.Key, model string, baseProviderType, providerKey schemas.ModelProvider) []schemas.Key {
	pool := make([]schemas.Key, 0, len(keys))
	for _, key := range keys {
		if key.Enabled != nil && !*key.Enabled {
			continue
		}
		if err := validateKey(baseProviderType, &key); err != nil {
			bifrost.logger.Warn("error validating key %s (%s) for provider %s: %s, skipping key", key.Name, key.ID, providerKey, err.Error())
			continue
		}
		hasValue := strings.TrimSpace(key.Value.GetValue()) != "" || CanProviderKeyValueBeEmpty(baseProviderType)
		if !hasValue || !key.Models.IsAllowed(model) || key.BlacklistedModels.IsBlocked(model) {
			continue
		}
		if baseProviderType == schemas.VLLM && key.VLLMKeyConfig != nil && key.VLLMKeyConfig.ModelName != "" && key.VLLMKeyConfig.ModelName != model {
			continue
		}
		pool = append(pool, key)
	}
	return pool
}

func (p *stickyKeyPlan) initialize() error {
	value, err := p.store.Get(p.key)
	if err == nil {
		if bound, ok := stickyKeyByID(value, p.pool); ok {
			p.current = bound
			p.refreshExisting()
			return nil
		}
		// An invalid dedicated winner is left untouched. Selecting a fixed
		// key keeps this request safe without overwriting a newer/unknown
		// binding.
		p.current = p.pool[0]
		p.casReady = false
		return nil
	}

	// Seed the dedicated namespace from the legacy session binding when it is
	// still an eligible key. This read preserves existing affinity while never
	// mutating or deleting the legacy namespace.
	legacyValue, legacyErr := p.store.Get(strings.TrimPrefix(p.key, stickyQuotaKeyPrefix))
	if legacyErr == nil {
		if bound, ok := stickyKeyByID(legacyValue, p.pool); ok {
			p.current = bound
		} else {
			p.current = p.pool[0]
			p.casReady = false
			return nil
		}
	} else {
		selected, selectErr := p.selector(p.ctx, p.pool, p.provider, p.model)
		if selectErr != nil {
			return selectErr
		}
		canonical, ok := stickyKeyByID(selected.ID, p.pool)
		if !ok {
			p.current = p.pool[0]
			p.casReady = false
			return nil
		}
		p.current = canonical
	}

	wasSet, setErr := p.store.SetNXWithTTL(p.key, p.current.ID, p.ttl)
	if setErr != nil {
		p.casReady = false
		return nil
	}
	if wasSet {
		return nil
	}
	if winner, ok := stickyKeyByIDFromStore(p.store, p.key, p.pool); ok {
		p.current = winner
		return nil
	}
	// SetNX lost to an invalid winner; keep the selected key fixed and do not
	// overwrite the store.
	p.casReady = false
	return nil
}

func (p *stickyKeyPlan) refreshExisting() {
	swapped, err := p.cas.CompareAndSwapStringWithTTL(p.key, p.current.ID, p.current.ID, p.ttl)
	if err != nil {
		p.casReady = false
		return
	}
	if swapped {
		return
	}
	if winner, ok := stickyKeyByIDFromStore(p.store, p.key, p.pool); ok {
		p.current = winner
		return
	}
	p.casReady = false
}

func stickyKeyByID(raw any, pool []schemas.Key) (schemas.Key, bool) {
	id := ""
	switch value := raw.(type) {
	case string:
		id = value
	case []byte:
		if err := sonic.Unmarshal(value, &id); err != nil {
			id = string(value)
		}
	}
	for _, key := range pool {
		if key.ID == id {
			return key, true
		}
	}
	return schemas.Key{}, false
}

func stickyKeyByIDFromStore(store schemas.KVStore, key string, pool []schemas.Key) (schemas.Key, bool) {
	raw, err := store.Get(key)
	if err != nil {
		return schemas.Key{}, false
	}
	return stickyKeyByID(raw, pool)
}

func (p *stickyKeyPlan) keyProvider() func(map[string]bool, map[string]bool) (schemas.Key, error) {
	return func(usedKeyIDs, deadKeyIDs map[string]bool) (schemas.Key, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.selectionErr != nil {
			return schemas.Key{}, p.selectionErr
		}
		if deadKeyIDs != nil && deadKeyIDs[p.current.ID] {
			return schemas.Key{}, errAllKeysDead
		}
		if !p.rotate || !p.casReady {
			p.rotate = false
			return p.current, nil
		}
		p.rotate = false

		alternatives := make([]schemas.Key, 0, len(p.pool)-1)
		for _, key := range p.pool {
			if key.ID == p.current.ID || (deadKeyIDs != nil && deadKeyIDs[key.ID]) || (usedKeyIDs != nil && usedKeyIDs[key.ID]) {
				continue
			}
			alternatives = append(alternatives, key)
		}
		if len(alternatives) == 0 {
			return p.current, nil
		}
		if p.filter != nil {
			filtered, err := p.filter(p.ctx, p.provider, p.model, alternatives)
			if err != nil || len(filtered) == 0 {
				return p.current, nil
			}
			filteredSet := make(map[string]schemas.Key, len(alternatives))
			for _, key := range alternatives {
				filteredSet[key.ID] = key
			}
			canonical := make([]schemas.Key, 0, len(filtered))
			seen := make(map[string]struct{}, len(filtered))
			for _, key := range filtered {
				original, ok := filteredSet[key.ID]
				if !ok {
					return p.current, nil
				}
				if _, duplicate := seen[key.ID]; duplicate {
					continue
				}
				seen[key.ID] = struct{}{}
				canonical = append(canonical, original)
			}
			if len(canonical) == 0 {
				return p.current, nil
			}
			alternatives = canonical
		}

		next, err := p.selector(p.ctx, alternatives, p.provider, p.model)
		if err != nil {
			return p.current, nil
		}
		canonicalNext, valid := stickyKeyByID(next.ID, alternatives)
		if !valid {
			return p.current, nil
		}
		won, err := p.cas.CompareAndSwapStringWithTTL(p.key, p.current.ID, canonicalNext.ID, p.ttl)
		if err != nil {
			return p.current, nil
		}
		if won {
			p.current = canonicalNext
			return canonicalNext, nil
		}
		if winner, ok := stickyKeyByIDFromStore(p.store, p.key, alternatives); ok {
			p.current = winner
			return winner, nil
		}
		return p.current, nil
	}
}

func (p *stickyKeyPlan) observeRetry(err *schemas.BifrostError, eligible bool) {
	p.mu.Lock()
	p.rotate = eligible && isStickyQuotaRetry(err)
	p.mu.Unlock()
}

// isStickyQuotaRetry deliberately excludes permanent credential failures,
// transport/server failures, and misleading quota text on those failures.
func isStickyQuotaRetry(err *schemas.BifrostError) bool {
	if err == nil || err.IsBifrostError {
		return false
	}
	// A recognized transport or credential classification wins over incidental
	// quota text (and even a contradictory 429 supplied by an adapter).
	if err.Error != nil {
		if err.Error.Message == schemas.ErrProviderDoRequest || err.Error.Message == schemas.ErrProviderNetworkError {
			return false
		}
		for _, classification := range []*string{err.Error.Type, err.Error.Code} {
			if classification == nil {
				continue
			}
			value := strings.TrimSpace(*classification)
			if strings.EqualFold(value, schemas.ErrProviderDoRequest) || strings.EqualFold(value, schemas.ErrProviderNetworkError) {
				return false
			}
			switch strings.ToLower(value) {
			case "authentication_error", "invalid_api_key", "unauthorized", "forbidden", "permission_error", "permission_denied", "insufficient_permissions", "access_denied", "billing_error", "billing_not_active", "network_error", "connection_error", "timeout_error":
				return false
			}
		}
	}
	if err.StatusCode != nil {
		switch *err.StatusCode {
		case 401, 402, 403:
			return false
		case 429:
			return true
		}
		if *err.StatusCode >= 500 && *err.StatusCode <= 599 {
			return false
		}
	}
	if err.Error == nil {
		return false
	}
	return IsRateLimitErrorMessage(err.Error.Message) ||
		(err.Error.Type != nil && IsRateLimitErrorMessage(*err.Error.Type)) ||
		(err.Error.Code != nil && IsRateLimitErrorMessage(*err.Error.Code))
}

var _ stickyRetryObserver = (*stickyKeyPlan)(nil)
