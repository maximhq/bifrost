package gigachat

import (
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"strings"
	"sync"
	"time"
	"weak"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	gigaChatAttachmentCacheTTL           = 15 * time.Minute
	gigaChatAttachmentCacheSweepInterval = time.Minute
)

type gigaChatAttachmentCacheContextKey struct{}

var gigaChatAttachmentCacheKey = gigaChatAttachmentCacheContextKey{}

// GigaChat uploads inline attachments before inference. The context only keeps
// a small cache ID; uploaded file metadata lives in this provider-owned manager.
type gigaChatAttachmentCacheManager struct {
	mu         sync.Mutex
	entries    map[string]*gigaChatAttachmentCacheEntry
	sweepTimer *time.Timer
}

type gigaChatAttachmentCacheEntry struct {
	cache     gigaChatAttachmentCache
	expiresAt time.Time
	writers   int
}

type gigaChatAttachmentCache struct {
	mu        sync.Mutex
	chat      map[gigaChatChatAttachmentCacheKey]gigaChatCachedAttachment[schemas.ChatContentBlock]
	responses map[gigaChatResponsesAttachmentCacheKey]gigaChatCachedAttachment[schemas.ResponsesMessageContentBlock]
}

type gigaChatCachedAttachment[T any] struct {
	replacement T
	expiresAt   time.Time
}

type gigaChatChatAttachmentCacheKey struct {
	request      weak.Pointer[schemas.BifrostChatRequest]
	keyHash      string
	messageIndex int
	blockIndex   int
}

type gigaChatResponsesAttachmentCacheKey struct {
	request      weak.Pointer[schemas.BifrostResponsesRequest]
	keyHash      string
	messageIndex int
	blockIndex   int
}

func newGigaChatAttachmentCacheManager() *gigaChatAttachmentCacheManager {
	return &gigaChatAttachmentCacheManager{
		entries: make(map[string]*gigaChatAttachmentCacheEntry),
	}
}

func (manager *gigaChatAttachmentCacheManager) lookupCache(ctx *schemas.BifrostContext) *gigaChatAttachmentCache {
	cache, _ := manager.cacheFor(ctx, false)
	return cache
}

func (manager *gigaChatAttachmentCacheManager) cacheForWrite(ctx *schemas.BifrostContext) (*gigaChatAttachmentCache, *gigaChatAttachmentCacheEntry) {
	return manager.cacheFor(ctx, true)
}

func (manager *gigaChatAttachmentCacheManager) cacheFor(ctx *schemas.BifrostContext, create bool) (*gigaChatAttachmentCache, *gigaChatAttachmentCacheEntry) {
	if manager == nil || ctx == nil {
		return nil, nil
	}

	ctx = ctx.Root()
	if ctx.Err() != nil {
		return nil, nil
	}
	now := time.Now()
	manager.mu.Lock()
	cacheID, _ := ctx.Value(gigaChatAttachmentCacheKey).(string)
	if cacheID == "" {
		if !create {
			manager.mu.Unlock()
			return nil, nil
		}
		cacheID = uuid.NewString()
		ctx.SetValue(gigaChatAttachmentCacheKey, cacheID)
	}

	entry := manager.entries[cacheID]
	if entry != nil && entry.writers == 0 && !entry.expiresAt.After(now) {
		delete(manager.entries, cacheID)
		entry = nil
	}
	if entry == nil {
		if !create {
			manager.mu.Unlock()
			return nil, nil
		}
		entry = &gigaChatAttachmentCacheEntry{}
		manager.entries[cacheID] = entry
	}
	entry.expiresAt = now.Add(gigaChatAttachmentCacheTTL)
	if create {
		entry.writers++
	}
	manager.scheduleSweepLocked()
	manager.mu.Unlock()

	return &entry.cache, entry
}

func (manager *gigaChatAttachmentCacheManager) finishWrite(entry *gigaChatAttachmentCacheEntry) {
	if manager == nil || entry == nil {
		return
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if entry.writers > 0 {
		entry.writers--
	}
}

func (manager *gigaChatAttachmentCacheManager) scheduleSweepLocked() {
	if manager.sweepTimer != nil || len(manager.entries) == 0 {
		return
	}
	manager.sweepTimer = time.AfterFunc(gigaChatAttachmentCacheSweepInterval, manager.sweep)
}

func (manager *gigaChatAttachmentCacheManager) sweep() {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.sweepTimer = nil
	manager.pruneEntriesLocked(time.Now())
	manager.scheduleSweepLocked()
}

func (manager *gigaChatAttachmentCacheManager) pruneEntriesLocked(now time.Time) {
	for cacheID, entry := range manager.entries {
		if entry.writers > 0 {
			continue
		}
		if !entry.expiresAt.After(now) {
			delete(manager.entries, cacheID)
			continue
		}
		if entry.cache.prune(now) {
			delete(manager.entries, cacheID)
		}
	}
}

func (cache *gigaChatAttachmentCache) prune(now time.Time) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for key, attachment := range cache.chat {
		if key.request.Value() == nil || !attachment.expiresAt.After(now) {
			delete(cache.chat, key)
		}
	}
	for key, attachment := range cache.responses {
		if key.request.Value() == nil || !attachment.expiresAt.After(now) {
			delete(cache.responses, key)
		}
	}
	return len(cache.chat) == 0 && len(cache.responses) == 0
}

func (provider *GigaChatProvider) getCachedGigaChatChatAttachment(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostChatRequest,
	messageIndex int,
	blockIndex int,
) (schemas.ChatContentBlock, bool) {
	if request == nil {
		return schemas.ChatContentBlock{}, false
	}
	cache := provider.attachmentCache.lookupCache(ctx)
	if cache == nil {
		return schemas.ChatContentBlock{}, false
	}

	cacheKey := gigaChatChatAttachmentCacheKey{
		request:      weak.Make(request),
		keyHash:      gigaChatAttachmentKeyHash(key),
		messageIndex: messageIndex,
		blockIndex:   blockIndex,
	}
	cache.mu.Lock()
	attachment, ok := cache.chat[cacheKey]
	now := time.Now()
	if ok && !attachment.expiresAt.After(now) {
		delete(cache.chat, cacheKey)
		attachment = gigaChatCachedAttachment[schemas.ChatContentBlock]{}
		ok = false
	} else if ok {
		attachment.expiresAt = now.Add(gigaChatAttachmentCacheTTL)
		cache.chat[cacheKey] = attachment
	}
	cache.mu.Unlock()
	runtime.KeepAlive(request)
	return attachment.replacement, ok
}

func (provider *GigaChatProvider) setCachedGigaChatChatAttachment(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostChatRequest,
	messageIndex int,
	blockIndex int,
	replacement schemas.ChatContentBlock,
) {
	if request == nil {
		return
	}
	cache, entry := provider.attachmentCache.cacheForWrite(ctx)
	if cache == nil {
		return
	}
	defer provider.attachmentCache.finishWrite(entry)

	cacheKey := gigaChatChatAttachmentCacheKey{
		request:      weak.Make(request),
		keyHash:      gigaChatAttachmentKeyHash(key),
		messageIndex: messageIndex,
		blockIndex:   blockIndex,
	}
	cache.mu.Lock()
	if cache.chat == nil {
		cache.chat = make(map[gigaChatChatAttachmentCacheKey]gigaChatCachedAttachment[schemas.ChatContentBlock])
	}
	cache.chat[cacheKey] = gigaChatCachedAttachment[schemas.ChatContentBlock]{
		replacement: replacement,
		expiresAt:   time.Now().Add(gigaChatAttachmentCacheTTL),
	}
	cache.mu.Unlock()
	runtime.KeepAlive(request)
}

func (provider *GigaChatProvider) getCachedGigaChatResponsesAttachment(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
	messageIndex int,
	blockIndex int,
) (schemas.ResponsesMessageContentBlock, bool) {
	if request == nil {
		return schemas.ResponsesMessageContentBlock{}, false
	}
	cache := provider.attachmentCache.lookupCache(ctx)
	if cache == nil {
		return schemas.ResponsesMessageContentBlock{}, false
	}

	cacheKey := gigaChatResponsesAttachmentCacheKey{
		request:      weak.Make(request),
		keyHash:      gigaChatAttachmentKeyHash(key),
		messageIndex: messageIndex,
		blockIndex:   blockIndex,
	}
	cache.mu.Lock()
	attachment, ok := cache.responses[cacheKey]
	now := time.Now()
	if ok && !attachment.expiresAt.After(now) {
		delete(cache.responses, cacheKey)
		attachment = gigaChatCachedAttachment[schemas.ResponsesMessageContentBlock]{}
		ok = false
	} else if ok {
		attachment.expiresAt = now.Add(gigaChatAttachmentCacheTTL)
		cache.responses[cacheKey] = attachment
	}
	cache.mu.Unlock()
	runtime.KeepAlive(request)
	return attachment.replacement, ok
}

func (provider *GigaChatProvider) setCachedGigaChatResponsesAttachment(
	ctx *schemas.BifrostContext,
	key schemas.Key,
	request *schemas.BifrostResponsesRequest,
	messageIndex int,
	blockIndex int,
	replacement schemas.ResponsesMessageContentBlock,
) {
	if request == nil {
		return
	}
	cache, entry := provider.attachmentCache.cacheForWrite(ctx)
	if cache == nil {
		return
	}
	defer provider.attachmentCache.finishWrite(entry)

	cacheKey := gigaChatResponsesAttachmentCacheKey{
		request:      weak.Make(request),
		keyHash:      gigaChatAttachmentKeyHash(key),
		messageIndex: messageIndex,
		blockIndex:   blockIndex,
	}
	cache.mu.Lock()
	if cache.responses == nil {
		cache.responses = make(map[gigaChatResponsesAttachmentCacheKey]gigaChatCachedAttachment[schemas.ResponsesMessageContentBlock])
	}
	cache.responses[cacheKey] = gigaChatCachedAttachment[schemas.ResponsesMessageContentBlock]{
		replacement: replacement,
		expiresAt:   time.Now().Add(gigaChatAttachmentCacheTTL),
	}
	cache.mu.Unlock()
	runtime.KeepAlive(request)
}

func gigaChatAttachmentKeyHash(key schemas.Key) string {
	hash := sha256.New()
	writePart := func(label string, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		hash.Write([]byte(label))
		hash.Write([]byte{0})
		hash.Write([]byte(value))
		hash.Write([]byte{0})
	}

	writePart("id", key.ID)
	writePart("name", key.Name)
	writePart("value", key.Value.GetValue())

	if key.GigaChatKeyConfig != nil {
		config := key.GigaChatKeyConfig
		writePart("credentials", config.Credentials.GetValue())
		writePart("scope", config.Scope)
		writePart("user", config.User.GetValue())
		writePart("password", config.Password.GetValue())
		writePart("access_token", config.AccessToken.GetValue())
		writePart("auth_url", config.AuthURL)
		writePart("base_url", config.BaseURL)
		writePart("cert_file", config.CertFile)
		writePart("key_file", config.KeyFile)
		writePart("ca_bundle_file", config.CABundleFile)
	}

	return hex.EncodeToString(hash.Sum(nil))
}
