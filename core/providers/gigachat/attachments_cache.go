package gigachat

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
)

type gigaChatAttachmentCacheContextKey struct{}

// GigaChat uploads inline attachments before inference; keep successful uploads
// request-scoped so retries and token refreshes reuse file IDs instead of re-uploading.
type gigaChatAttachmentCache struct {
	mu        sync.Mutex
	chat      map[gigaChatChatAttachmentCacheKey]*schemas.BifrostChatRequest
	responses map[gigaChatResponsesAttachmentCacheKey]*schemas.BifrostResponsesRequest
}

type gigaChatChatAttachmentCacheKey struct {
	request *schemas.BifrostChatRequest
	keyHash string
}

type gigaChatResponsesAttachmentCacheKey struct {
	request *schemas.BifrostResponsesRequest
	keyHash string
}

var gigaChatAttachmentCacheKey = gigaChatAttachmentCacheContextKey{}

func getGigaChatAttachmentCache(ctx *schemas.BifrostContext) *gigaChatAttachmentCache {
	if ctx == nil {
		return nil
	}
	if cache, ok := ctx.Value(gigaChatAttachmentCacheKey).(*gigaChatAttachmentCache); ok && cache != nil {
		return cache
	}
	cache := &gigaChatAttachmentCache{}
	ctx.SetValue(gigaChatAttachmentCacheKey, cache)
	return cache
}

func getCachedGigaChatChatAttachmentRequest(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatRequest, bool) {
	cache := getGigaChatAttachmentCache(ctx)
	if cache == nil || request == nil {
		return nil, false
	}

	cacheKey := gigaChatChatAttachmentCacheKey{request: request, keyHash: gigaChatAttachmentKeyHash(key)}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.chat == nil {
		return nil, false
	}
	prepared, ok := cache.chat[cacheKey]
	return prepared, ok
}

func setCachedGigaChatChatAttachmentRequest(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest, prepared *schemas.BifrostChatRequest) {
	cache := getGigaChatAttachmentCache(ctx)
	if cache == nil || request == nil || prepared == nil {
		return
	}

	cacheKey := gigaChatChatAttachmentCacheKey{request: request, keyHash: gigaChatAttachmentKeyHash(key)}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.chat == nil {
		cache.chat = make(map[gigaChatChatAttachmentCacheKey]*schemas.BifrostChatRequest)
	}
	cache.chat[cacheKey] = prepared
}

func getCachedGigaChatResponsesAttachmentRequest(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesRequest, bool) {
	cache := getGigaChatAttachmentCache(ctx)
	if cache == nil || request == nil {
		return nil, false
	}

	cacheKey := gigaChatResponsesAttachmentCacheKey{request: request, keyHash: gigaChatAttachmentKeyHash(key)}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.responses == nil {
		return nil, false
	}
	prepared, ok := cache.responses[cacheKey]
	return prepared, ok
}

func setCachedGigaChatResponsesAttachmentRequest(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest, prepared *schemas.BifrostResponsesRequest) {
	cache := getGigaChatAttachmentCache(ctx)
	if cache == nil || request == nil || prepared == nil {
		return
	}

	cacheKey := gigaChatResponsesAttachmentCacheKey{request: request, keyHash: gigaChatAttachmentKeyHash(key)}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.responses == nil {
		cache.responses = make(map[gigaChatResponsesAttachmentCacheKey]*schemas.BifrostResponsesRequest)
	}
	cache.responses[cacheKey] = prepared
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
