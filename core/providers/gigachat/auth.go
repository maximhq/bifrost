package gigachat

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const gigaChatOAuthRefreshLeeway = time.Minute

const (
	gigaChatAuthorizationHeader = "Authorization"
	gigaChatUserAgentHeader     = "User-Agent"
	gigaChatUserAgent           = "GigaChat-Bifrost-Provider"
)

var gigaChatContextHeaders = map[string]string{
	"x-session-id":   "X-Session-ID",
	"x-request-id":   "X-Request-ID",
	"x-service-id":   "X-Service-ID",
	"x-operation-id": "X-Operation-ID",
	"x-client-id":    "X-Client-ID",
	"x-trace-id":     "X-Trace-ID",
	"x-agent-id":     "X-Agent-ID",
}

type gigaChatCachedToken struct {
	accessToken string
	expiresAt   time.Time
}

type gigaChatTokenCacheEntry struct {
	mu    sync.Mutex
	token gigaChatCachedToken
	// Guarded by gigaChatTokenCache.mu; prevents pruning entries while callers hold a pointer.
	refCount int
}

type gigaChatTokenCache struct {
	mu      sync.Mutex
	entries map[string]*gigaChatTokenCacheEntry
	now     func() time.Time
}

func newGigaChatTokenCache(now func() time.Time) *gigaChatTokenCache {
	if now == nil {
		now = time.Now
	}
	return &gigaChatTokenCache{
		entries: make(map[string]*gigaChatTokenCacheEntry),
		now:     now,
	}
}

func (provider *GigaChatProvider) buildAuthHeaders(ctx *schemas.BifrostContext, key schemas.Key) (map[string]string, *schemas.BifrostError) {
	return provider.buildAuthHeadersWithRefresh(ctx, key, false)
}

func (provider *GigaChatProvider) refreshAuthHeaders(ctx *schemas.BifrostContext, key schemas.Key) (map[string]string, *schemas.BifrostError) {
	return provider.buildAuthHeadersWithRefresh(ctx, key, true)
}

func (provider *GigaChatProvider) buildAuthHeadersWithRefresh(ctx *schemas.BifrostContext, key schemas.Key, forceRefresh bool) (map[string]string, *schemas.BifrostError) {
	if bifrostErr := provider.rejectProviderAuthorizationExtraHeader(); bifrostErr != nil {
		return nil, bifrostErr
	}
	if bifrostErr := rejectRequestAuthorizationExtraHeader(ctx); bifrostErr != nil {
		return nil, bifrostErr
	}

	headers := map[string]string{
		gigaChatUserAgentHeader: gigaChatUserAgent,
	}

	accessToken, hasBearerAuth, bifrostErr := provider.resolveGigaChatAccessTokenWithRefresh(ctx, key, forceRefresh)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if hasBearerAuth {
		headers[gigaChatAuthorizationHeader] = "Bearer " + accessToken
	} else if key.GigaChatKeyConfig == nil || !key.GigaChatKeyConfig.HasClientCertificateMaterial() {
		return nil, newGigaChatConfigurationError("GigaChat authentication requires key.value access token, gigachat_key_config access_token, credentials, user/password auth material, or mTLS cert_file/key_file material")
	}

	applyGigaChatProviderContextHeaders(headers, provider.networkConfig.ExtraHeaders)
	applyGigaChatRequestContextHeaders(headers, ctx)
	return headers, nil
}

func (provider *GigaChatProvider) rejectProviderAuthorizationExtraHeader() *schemas.BifrostError {
	if hasGigaChatHeader(provider.networkConfig.ExtraHeaders, gigaChatAuthorizationHeader) {
		return newGigaChatConfigurationError("network_config.extra_headers cannot include Authorization for GigaChat; configure GigaChat auth material instead")
	}
	return nil
}

func rejectRequestAuthorizationExtraHeader(ctx *schemas.BifrostContext) *schemas.BifrostError {
	if _, ok := getGigaChatRequestExtraHeader(ctx, gigaChatAuthorizationHeader); ok {
		return newGigaChatConfigurationError("request extra headers cannot include Authorization for GigaChat; configure GigaChat auth material instead")
	}
	return nil
}

func hasGigaChatHeader(headers map[string]string, headerName string) bool {
	for key := range headers {
		if strings.EqualFold(strings.TrimSpace(key), headerName) {
			return true
		}
	}
	return false
}

func applyGigaChatProviderContextHeaders(headers map[string]string, extraHeaders map[string]string) {
	for key, value := range extraHeaders {
		canonicalHeader, ok := getGigaChatContextHeaderName(key)
		if !ok || canonicalHeader == gigaChatAuthorizationHeader {
			continue
		}
		if strings.TrimSpace(value) != "" {
			headers[canonicalHeader] = value
		}
	}
}

func applyGigaChatRequestContextHeaders(headers map[string]string, ctx *schemas.BifrostContext) {
	if ctx == nil {
		return
	}
	extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	if !ok {
		return
	}
	for key, values := range extraHeaders {
		canonicalHeader, ok := getGigaChatContextHeaderName(key)
		if !ok || canonicalHeader == gigaChatAuthorizationHeader {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				headers[canonicalHeader] = value
				break
			}
		}
	}
}

func getGigaChatRequestExtraHeader(ctx *schemas.BifrostContext, headerName string) (string, bool) {
	if ctx == nil {
		return "", false
	}
	extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	if !ok {
		return "", false
	}
	for key, values := range extraHeaders {
		if !strings.EqualFold(strings.TrimSpace(key), headerName) {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return value, true
			}
		}
	}
	return "", false
}

func getGigaChatContextHeaderName(headerName string) (string, bool) {
	canonicalHeader, ok := gigaChatContextHeaders[strings.ToLower(strings.TrimSpace(headerName))]
	return canonicalHeader, ok
}

func (provider *GigaChatProvider) getOAuthAccessToken(ctx *schemas.BifrostContext, key schemas.Key) (string, *schemas.BifrostError) {
	return provider.getOAuthAccessTokenWithRefresh(ctx, key, false)
}

func (provider *GigaChatProvider) getOAuthAccessTokenWithRefresh(ctx *schemas.BifrostContext, key schemas.Key, forceRefresh bool) (string, *schemas.BifrostError) {
	authConfig, bifrostErr := resolveGigaChatOAuthConfig(key)
	if bifrostErr != nil {
		return "", bifrostErr
	}

	cacheKey := buildGigaChatOAuthCacheKey(authConfig)
	entry := provider.tokenCache.acquireEntry(cacheKey)
	entry.mu.Lock()
	defer provider.tokenCache.releaseEntry(cacheKey, entry)
	defer entry.mu.Unlock()

	if !forceRefresh && entry.token.isValid(provider.tokenCache.now().Add(gigaChatOAuthRefreshLeeway)) {
		return entry.token.accessToken, nil
	}

	token, bifrostErr := provider.requestGigaChatOAuthToken(ctx, authConfig)
	if bifrostErr != nil {
		return "", bifrostErr
	}
	entry.token = token
	return token.accessToken, nil
}

func (provider *GigaChatProvider) getPasswordAccessToken(ctx *schemas.BifrostContext, key schemas.Key) (string, *schemas.BifrostError) {
	return provider.getPasswordAccessTokenWithRefresh(ctx, key, false)
}

func (provider *GigaChatProvider) getPasswordAccessTokenWithRefresh(ctx *schemas.BifrostContext, key schemas.Key, forceRefresh bool) (string, *schemas.BifrostError) {
	authConfig, bifrostErr := provider.resolveGigaChatPasswordAuthConfig(key)
	if bifrostErr != nil {
		return "", bifrostErr
	}

	cacheKey := buildGigaChatPasswordAuthCacheKey(authConfig)
	entry := provider.tokenCache.acquireEntry(cacheKey)
	entry.mu.Lock()
	defer provider.tokenCache.releaseEntry(cacheKey, entry)
	defer entry.mu.Unlock()

	if !forceRefresh && entry.token.isValid(provider.tokenCache.now().Add(gigaChatOAuthRefreshLeeway)) {
		return entry.token.accessToken, nil
	}

	token, bifrostErr := provider.requestGigaChatPasswordToken(ctx, authConfig)
	if bifrostErr != nil {
		return "", bifrostErr
	}
	entry.token = token
	return token.accessToken, nil
}

func (provider *GigaChatProvider) getGigaChatAccessToken(ctx *schemas.BifrostContext, key schemas.Key) (string, *schemas.BifrostError) {
	return provider.getGigaChatAccessTokenWithRefresh(ctx, key, false)
}

func (provider *GigaChatProvider) getGigaChatAccessTokenWithRefresh(ctx *schemas.BifrostContext, key schemas.Key, forceRefresh bool) (string, *schemas.BifrostError) {
	accessToken, hasBearerAuth, bifrostErr := provider.resolveGigaChatAccessTokenWithRefresh(ctx, key, forceRefresh)
	if bifrostErr != nil || hasBearerAuth {
		return accessToken, bifrostErr
	}

	return "", newGigaChatConfigurationError("GigaChat authentication requires key.value access token or gigachat_key_config access_token, credentials, or user/password auth material")
}

func (provider *GigaChatProvider) resolveGigaChatAccessTokenWithRefresh(ctx *schemas.BifrostContext, key schemas.Key, forceRefresh bool) (string, bool, *schemas.BifrostError) {
	keyConfig := key.GigaChatKeyConfig
	if !forceRefresh {
		if accessToken, isSet, bifrostErr := resolveGigaChatExplicitAccessToken(key); isSet || bifrostErr != nil {
			return accessToken, isSet, bifrostErr
		}
	}
	if keyConfig != nil && keyConfig.Credentials.IsSet() {
		accessToken, bifrostErr := provider.getOAuthAccessTokenWithRefresh(ctx, key, forceRefresh)
		return accessToken, true, bifrostErr
	}
	if keyConfig != nil && (keyConfig.User.IsSet() || keyConfig.Password.IsSet()) {
		accessToken, bifrostErr := provider.getPasswordAccessTokenWithRefresh(ctx, key, forceRefresh)
		return accessToken, true, bifrostErr
	}
	if accessToken, isSet, bifrostErr := resolveGigaChatExplicitAccessToken(key); isSet || bifrostErr != nil {
		return accessToken, isSet, bifrostErr
	}

	return "", false, nil
}

func resolveGigaChatExplicitAccessToken(key schemas.Key) (string, bool, *schemas.BifrostError) {
	if key.GigaChatKeyConfig != nil && key.GigaChatKeyConfig.AccessToken.IsSet() {
		accessToken := strings.TrimSpace(key.GigaChatKeyConfig.AccessToken.GetValue())
		if accessToken == "" {
			return "", true, newGigaChatConfigurationError("gigachat_key_config.access_token resolved to an empty value")
		}
		return accessToken, true, nil
	}
	if key.Value.IsSet() {
		accessToken := strings.TrimSpace(key.Value.GetValue())
		if accessToken == "" {
			return "", true, newGigaChatConfigurationError("GigaChat key value resolved to an empty access token")
		}
		return accessToken, true, nil
	}
	return "", false, nil
}

func (cache *gigaChatTokenCache) acquireEntry(cacheKey string) *gigaChatTokenCacheEntry {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	cache.pruneExpiredEntriesLocked(cache.now())

	entry := cache.entries[cacheKey]
	if entry == nil {
		entry = &gigaChatTokenCacheEntry{}
		cache.entries[cacheKey] = entry
	}
	entry.refCount++
	return entry
}

func (cache *gigaChatTokenCache) releaseEntry(cacheKey string, entry *gigaChatTokenCacheEntry) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if entry.refCount > 0 {
		entry.refCount--
	}
	if entry.refCount != 0 || cache.entries[cacheKey] != entry {
		return
	}

	entry.mu.Lock()
	reusable := entry.token.isValid(cache.now())
	entry.mu.Unlock()
	if !reusable {
		delete(cache.entries, cacheKey)
	}
}

func (cache *gigaChatTokenCache) pruneExpiredEntriesLocked(now time.Time) {
	for cacheKey, entry := range cache.entries {
		if entry.refCount != 0 {
			continue
		}
		entry.mu.Lock()
		reusable := entry.token.isValid(now)
		entry.mu.Unlock()

		if !reusable {
			delete(cache.entries, cacheKey)
		}
	}
}

func (token gigaChatCachedToken) isValid(validAfter time.Time) bool {
	return token.accessToken != "" && token.expiresAt.After(validAfter)
}

func parseGigaChatExpiresAt(value int64) time.Time {
	if value > 1_000_000_000_000 {
		return time.UnixMilli(value)
	}
	return time.Unix(value, 0)
}

type gigaChatOAuthConfig struct {
	authURL     string
	credentials string
	scope       string
	keyConfig   *schemas.GigaChatKeyConfig
}

type gigaChatPasswordAuthConfig struct {
	tokenURL  string
	user      string
	password  string
	keyConfig *schemas.GigaChatKeyConfig
}

func resolveGigaChatOAuthConfig(key schemas.Key) (gigaChatOAuthConfig, *schemas.BifrostError) {
	keyConfig := key.GigaChatKeyConfig
	if keyConfig == nil || !keyConfig.Credentials.IsSet() {
		return gigaChatOAuthConfig{}, newGigaChatConfigurationError("gigachat_key_config.credentials is required for OAuth token exchange")
	}

	credentials := strings.TrimSpace(keyConfig.Credentials.GetValue())
	if credentials == "" {
		return gigaChatOAuthConfig{}, newGigaChatConfigurationError("gigachat_key_config.credentials resolved to an empty value")
	}

	scope := strings.TrimSpace(keyConfig.Scope)
	if scope == "" {
		scope = schemas.DefaultGigaChatScope
	}

	return gigaChatOAuthConfig{
		authURL:     resolveAuthURL(key),
		credentials: credentials,
		scope:       scope,
		keyConfig:   keyConfig,
	}, nil
}

func buildGigaChatOAuthCacheKey(authConfig gigaChatOAuthConfig) string {
	tlsFingerprint := gigaChatAuthTLSConfigFingerprint(authConfig.keyConfig)
	hash := sha256.New()
	hash.Write([]byte("oauth"))
	hash.Write([]byte{0})
	hash.Write([]byte(authConfig.authURL))
	hash.Write([]byte{0})
	hash.Write([]byte(authConfig.scope))
	hash.Write([]byte{0})
	hash.Write([]byte(authConfig.credentials))
	hash.Write([]byte{0})
	hash.Write([]byte(tlsFingerprint))
	return hex.EncodeToString(hash.Sum(nil))
}

func (provider *GigaChatProvider) resolveGigaChatPasswordAuthConfig(key schemas.Key) (gigaChatPasswordAuthConfig, *schemas.BifrostError) {
	keyConfig := key.GigaChatKeyConfig
	if keyConfig == nil || !keyConfig.User.IsSet() || !keyConfig.Password.IsSet() {
		return gigaChatPasswordAuthConfig{}, newGigaChatConfigurationError("gigachat_key_config.user and gigachat_key_config.password are required for password auth")
	}

	user := keyConfig.User.GetValue()
	if strings.TrimSpace(user) == "" {
		return gigaChatPasswordAuthConfig{}, newGigaChatConfigurationError("gigachat_key_config.user resolved to an empty value")
	}
	password := keyConfig.Password.GetValue()
	if strings.TrimSpace(password) == "" {
		return gigaChatPasswordAuthConfig{}, newGigaChatConfigurationError("gigachat_key_config.password resolved to an empty value")
	}

	baseURL := resolveBaseURL(key, provider.networkConfig)
	return gigaChatPasswordAuthConfig{
		tokenURL:  buildGigaChatURL(baseURL, gigaChatAPIVersionV1, "/token"),
		user:      user,
		password:  password,
		keyConfig: keyConfig,
	}, nil
}

func buildGigaChatPasswordAuthCacheKey(authConfig gigaChatPasswordAuthConfig) string {
	tlsFingerprint := gigaChatAuthTLSConfigFingerprint(authConfig.keyConfig)
	hash := sha256.New()
	hash.Write([]byte("password"))
	hash.Write([]byte{0})
	hash.Write([]byte(authConfig.tokenURL))
	hash.Write([]byte{0})
	hash.Write([]byte(authConfig.user))
	hash.Write([]byte{0})
	hash.Write([]byte(authConfig.password))
	hash.Write([]byte{0})
	hash.Write([]byte(tlsFingerprint))
	return hex.EncodeToString(hash.Sum(nil))
}

func (provider *GigaChatProvider) requestGigaChatOAuthToken(ctx *schemas.BifrostContext, authConfig gigaChatOAuthConfig) (gigaChatCachedToken, *schemas.BifrostError) {
	if ctx == nil {
		ctx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	form := url.Values{}
	form.Set("scope", authConfig.scope)

	req.SetRequestURI(authConfig.authURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("RqUID", uuid.NewString())
	req.Header.Set(gigaChatUserAgentHeader, gigaChatUserAgent)
	req.Header.Set("Authorization", "Basic "+authConfig.credentials)
	req.SetBodyString(form.Encode())

	client, err := provider.getGigaChatTLSClient(provider.client, gigaChatTLSClientCacheAuth, gigaChatAuthTLSKeyConfig(authConfig.keyConfig))
	if err != nil {
		return gigaChatCachedToken{}, newGigaChatConfigurationError(err.Error())
	}

	_, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	defer wait()
	if bifrostErr != nil {
		bifrostErr.ExtraFields.Provider = provider.GetProviderKey()
		return gigaChatCachedToken{}, bifrostErr
	}

	if resp.StatusCode() < http.StatusOK || resp.StatusCode() >= http.StatusMultipleChoices {
		return gigaChatCachedToken{}, ParseGigaChatError(resp, provider.GetProviderKey())
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return gigaChatCachedToken{}, newGigaChatProviderResponseError("failed to decode GigaChat token response", err)
	}

	var tokenResponse GigaChatTokenResponse
	if err := sonic.Unmarshal(body, &tokenResponse); err != nil {
		return gigaChatCachedToken{}, newGigaChatProviderResponseError("failed to parse GigaChat token response", err)
	}
	if strings.TrimSpace(tokenResponse.AccessToken) == "" {
		return gigaChatCachedToken{}, newGigaChatProviderResponseError("GigaChat token response missing access_token", nil)
	}
	if tokenResponse.ExpiresAt <= 0 {
		return gigaChatCachedToken{}, newGigaChatProviderResponseError("GigaChat token response missing expires_at", nil)
	}

	expiresAt := parseGigaChatExpiresAt(tokenResponse.ExpiresAt)
	if !expiresAt.After(provider.tokenCache.now()) {
		return gigaChatCachedToken{}, newGigaChatProviderResponseError("GigaChat token response is already expired", nil)
	}

	return gigaChatCachedToken{
		accessToken: tokenResponse.AccessToken,
		expiresAt:   expiresAt,
	}, nil
}

func (provider *GigaChatProvider) requestGigaChatPasswordToken(ctx *schemas.BifrostContext, authConfig gigaChatPasswordAuthConfig) (gigaChatCachedToken, *schemas.BifrostError) {
	if ctx == nil {
		ctx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(authConfig.tokenURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(gigaChatUserAgentHeader, gigaChatUserAgent)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(authConfig.user+":"+authConfig.password)))

	client, err := provider.getGigaChatTLSClient(provider.client, gigaChatTLSClientCacheAuth, gigaChatAuthTLSKeyConfig(authConfig.keyConfig))
	if err != nil {
		return gigaChatCachedToken{}, newGigaChatConfigurationError(err.Error())
	}

	_, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	defer wait()
	if bifrostErr != nil {
		bifrostErr.ExtraFields.Provider = provider.GetProviderKey()
		return gigaChatCachedToken{}, bifrostErr
	}

	if resp.StatusCode() < http.StatusOK || resp.StatusCode() >= http.StatusMultipleChoices {
		return gigaChatCachedToken{}, ParseGigaChatError(resp, provider.GetProviderKey())
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return gigaChatCachedToken{}, newGigaChatProviderResponseError("failed to decode GigaChat password token response", err)
	}

	var tokenResponse GigaChatPasswordTokenResponse
	if err := sonic.Unmarshal(body, &tokenResponse); err != nil {
		return gigaChatCachedToken{}, newGigaChatProviderResponseError("failed to parse GigaChat password token response", err)
	}
	if strings.TrimSpace(tokenResponse.Token) == "" {
		return gigaChatCachedToken{}, newGigaChatProviderResponseError("GigaChat password token response missing tok", nil)
	}
	if tokenResponse.ExpiresAt <= 0 {
		return gigaChatCachedToken{}, newGigaChatProviderResponseError("GigaChat password token response missing exp", nil)
	}

	expiresAt := parseGigaChatExpiresAt(tokenResponse.ExpiresAt)
	if !expiresAt.After(provider.tokenCache.now()) {
		return gigaChatCachedToken{}, newGigaChatProviderResponseError("GigaChat password token response is already expired", nil)
	}

	return gigaChatCachedToken{
		accessToken: tokenResponse.Token,
		expiresAt:   expiresAt,
	}, nil
}

func newGigaChatConfigurationError(message string) *schemas.BifrostError {
	bifrostErr := providerUtils.NewConfigurationError(message)
	bifrostErr.ExtraFields.Provider = schemas.GigaChat
	return bifrostErr
}

func newGigaChatProviderResponseError(message string, err error) *schemas.BifrostError {
	statusCode := http.StatusBadGateway
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Message: message,
			Error:   err,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider: schemas.GigaChat,
		},
	}
	if err != nil {
		bifrostErr.Error.Message = fmt.Sprintf("%s: %v", message, err)
	}
	return bifrostErr
}
