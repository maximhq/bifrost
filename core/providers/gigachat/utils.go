package gigachat

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

var (
	gigaChatAuthSchemePattern          = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[^ \t\r\n"',}]+`)
	gigaChatPrivateKeyPattern          = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
	gigaChatSensitiveAssignmentPattern = regexp.MustCompile(`(?i)(["']?)\b(authorization|access_token|credentials|username|password|cert_file|key_file|ca_bundle_file|private_key|client_key|client_secret|refresh_token)\b(["']?)(\s*[:=]\s*)("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^ \t\r\n"',}]+)`)
	gigaChatUserAssignmentPattern      = regexp.MustCompile(`(?i)(["']?)\b(user)\b(["']?)(\s*[:=]\s*)("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^ \t\r\n"',}]+)`)
	gigaChatAuthContextTextPattern     = regexp.MustCompile(`(?i)(\b(bearer|basic)\s+|\b(authorization|access_token|credentials|username|password|cert_file|key_file|ca_bundle_file|private_key|client_key|client_secret|refresh_token)\b\s*[:=])`)
)

const (
	gigaChatDefaultBaseURL = "https://gigachat.devices.sberbank.ru/api"
	gigaChatDefaultAuthURL = "https://ngw.devices.sberbank.ru:9443/api/v2/oauth"

	gigaChatAPIVersionV1 = "v1"
	gigaChatAPIVersionV2 = "v2"

	gigaChatTLSClientCacheAuth      = "auth"
	gigaChatTLSClientCacheDefault   = "default"
	gigaChatTLSClientCacheStreaming = "streaming"
)

type gigaChatTLSClientCache struct {
	mu      sync.Mutex
	clients map[string]*fasthttp.Client
}

func newGigaChatTLSClientCache() *gigaChatTLSClientCache {
	return &gigaChatTLSClientCache{clients: make(map[string]*fasthttp.Client)}
}

func resolveAuthURL(key schemas.Key) string {
	if key.GigaChatKeyConfig != nil {
		if authURL := strings.TrimSpace(key.GigaChatKeyConfig.AuthURL); authURL != "" {
			return strings.TrimRight(authURL, "/")
		}
	}
	return gigaChatDefaultAuthURL
}

func resolveBaseURL(key schemas.Key, networkConfig schemas.NetworkConfig) string {
	if key.GigaChatKeyConfig != nil {
		if baseURL := strings.TrimSpace(key.GigaChatKeyConfig.BaseURL); baseURL != "" {
			return strings.TrimRight(baseURL, "/")
		}
	}
	if baseURL := strings.TrimSpace(networkConfig.BaseURL); baseURL != "" {
		return strings.TrimRight(baseURL, "/")
	}
	return gigaChatDefaultBaseURL
}

func buildGigaChatURL(baseURL string, apiVersion string, path string) string {
	resolvedBaseURL := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if resolvedBaseURL == "" {
		resolvedBaseURL = gigaChatDefaultBaseURL
	}

	version := normalizeGigaChatAPIVersion(apiVersion)
	versionedBaseURL := buildGigaChatVersionedBaseURL(resolvedBaseURL, version)
	normalizedPath := normalizeGigaChatPath(path, version)
	if normalizedPath == "" {
		return versionedBaseURL
	}
	return versionedBaseURL + normalizedPath
}

func buildGigaChatRequestURL(ctx *schemas.BifrostContext, baseURL string, apiVersion string, defaultPath string, customProviderConfig *schemas.CustomProviderConfig, requestType schemas.RequestType) string {
	path, isCompleteURL := providerUtils.GetRequestPath(ctx, defaultPath, customProviderConfig, requestType)
	if isCompleteURL {
		return path
	}
	return buildGigaChatURL(baseURL, apiVersion, path)
}

func normalizeGigaChatAPIVersion(apiVersion string) string {
	return strings.Trim(strings.TrimSpace(apiVersion), "/")
}

func buildGigaChatVersionedBaseURL(baseURL string, apiVersion string) string {
	if apiVersion == "" {
		return baseURL
	}
	for _, version := range []string{gigaChatAPIVersionV1, gigaChatAPIVersionV2} {
		suffix := "/" + version
		if strings.HasSuffix(baseURL, suffix) {
			return strings.TrimSuffix(baseURL, suffix) + "/" + apiVersion
		}
	}
	return baseURL + "/" + apiVersion
}

func normalizeGigaChatPath(path string, apiVersion string) string {
	normalizedPath := strings.TrimSpace(path)
	if normalizedPath == "" {
		return ""
	}
	normalizedPath = "/" + strings.TrimLeft(normalizedPath, "/")

	for _, version := range []string{apiVersion, gigaChatAPIVersionV1, gigaChatAPIVersionV2} {
		if version == "" {
			continue
		}
		versionPrefix := "/" + version
		if normalizedPath == versionPrefix {
			return ""
		}
		if strings.HasPrefix(normalizedPath, versionPrefix+"/") {
			return strings.TrimPrefix(normalizedPath, versionPrefix)
		}
	}

	return normalizedPath
}

func buildGigaChatTLSClient(baseClient *fasthttp.Client, keyConfig *schemas.GigaChatKeyConfig) (*fasthttp.Client, error) {
	if keyConfig == nil || !gigaChatKeyConfigHasTLSMaterial(keyConfig) {
		return baseClient, nil
	}

	client := providerUtils.CloneFastHTTPClientConfig(baseClient)
	tlsConfig := client.TLSConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		tlsConfig = tlsConfig.Clone()
	}

	if caBundleFile := strings.TrimSpace(keyConfig.CABundleFile); caBundleFile != "" {
		caBundlePEM, err := os.ReadFile(caBundleFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read gigachat_key_config.ca_bundle_file: %w", err)
		}
		if tlsConfig.RootCAs == nil {
			rootCAs, err := x509.SystemCertPool()
			if err != nil || rootCAs == nil {
				rootCAs = x509.NewCertPool()
			}
			tlsConfig.RootCAs = rootCAs
		} else {
			tlsConfig.RootCAs = tlsConfig.RootCAs.Clone()
		}
		if !tlsConfig.RootCAs.AppendCertsFromPEM(caBundlePEM) {
			return nil, fmt.Errorf("failed to parse gigachat_key_config.ca_bundle_file")
		}
	}

	hasCertFile := strings.TrimSpace(keyConfig.CertFile) != ""
	hasKeyFile := strings.TrimSpace(keyConfig.KeyFile) != ""
	if hasCertFile != hasKeyFile {
		return nil, fmt.Errorf("gigachat_key_config.cert_file and gigachat_key_config.key_file must be set together")
	}
	if hasCertFile {
		certificate, err := tls.LoadX509KeyPair(keyConfig.CertFile, keyConfig.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load gigachat_key_config.cert_file/key_file: %w", err)
		}
		tlsConfig.Certificates = append(tlsConfig.Certificates, certificate)
	}

	client.TLSConfig = tlsConfig
	return client, nil
}

func (provider *GigaChatProvider) getGigaChatTLSClient(baseClient *fasthttp.Client, cacheKind string, keyConfig *schemas.GigaChatKeyConfig) (*fasthttp.Client, error) {
	if keyConfig == nil || !gigaChatKeyConfigHasTLSMaterial(keyConfig) {
		return baseClient, nil
	}
	if provider == nil || provider.tlsClientCache == nil {
		return buildGigaChatTLSClient(baseClient, keyConfig)
	}

	fingerprint := gigaChatTLSConfigFingerprint(keyConfig)
	cacheKey := cacheKind + ":" + fingerprint
	provider.tlsClientCache.mu.Lock()
	client := provider.tlsClientCache.clients[cacheKey]
	provider.tlsClientCache.mu.Unlock()
	if client != nil {
		return client, nil
	}

	client, err := buildGigaChatTLSClient(baseClient, keyConfig)
	if err != nil {
		return nil, err
	}

	provider.tlsClientCache.mu.Lock()
	defer provider.tlsClientCache.mu.Unlock()
	if cached := provider.tlsClientCache.clients[cacheKey]; cached != nil {
		return cached, nil
	}
	provider.tlsClientCache.clients[cacheKey] = client
	return client, nil
}

// gigaChatTLSConfigFingerprint identifies the configured TLS paths without
// reading them, so cache hits never pay certificate file I/O on the request
// path and keep reusing the same connection pool. Replacing material in place
// intentionally takes effect on provider reload rather than on the next request.
func gigaChatTLSConfigFingerprint(keyConfig *schemas.GigaChatKeyConfig) string {
	if keyConfig == nil {
		return ""
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("gigachat-tls-config-v1"))
	for _, material := range []struct {
		field string
		path  string
	}{
		{field: "ca_bundle_file", path: strings.TrimSpace(keyConfig.CABundleFile)},
		{field: "cert_file", path: strings.TrimSpace(keyConfig.CertFile)},
		{field: "key_file", path: strings.TrimSpace(keyConfig.KeyFile)},
	} {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(material.field))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(material.path))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func gigaChatAuthTLSConfigFingerprint(keyConfig *schemas.GigaChatKeyConfig) string {
	return gigaChatTLSConfigFingerprint(gigaChatAuthTLSKeyConfig(keyConfig))
}

func gigaChatAuthTLSKeyConfig(keyConfig *schemas.GigaChatKeyConfig) *schemas.GigaChatKeyConfig {
	if keyConfig == nil {
		return nil
	}
	authKeyConfig := &schemas.GigaChatKeyConfig{
		CABundleFile: strings.TrimSpace(keyConfig.CABundleFile),
	}
	if !gigaChatKeyConfigHasTLSMaterial(authKeyConfig) {
		return nil
	}
	return authKeyConfig
}

func gigaChatKeyConfigHasTLSMaterial(keyConfig *schemas.GigaChatKeyConfig) bool {
	return strings.TrimSpace(keyConfig.CABundleFile) != "" ||
		strings.TrimSpace(keyConfig.CertFile) != "" ||
		strings.TrimSpace(keyConfig.KeyFile) != ""
}

func enrichGigaChatError(ctx *schemas.BifrostContext, bifrostErr *schemas.BifrostError, requestBody []byte, responseBody []byte, sendBackRawRequest bool, sendBackRawResponse bool) *schemas.BifrostError {
	enriched := providerUtils.EnrichError(ctx, bifrostErr, redactGigaChatRawPayload(requestBody), redactGigaChatRawPayload(responseBody), sendBackRawRequest, sendBackRawResponse)
	if enriched == nil {
		return nil
	}
	enriched.ExtraFields.RawRequest = redactGigaChatRawValue(enriched.ExtraFields.RawRequest)
	enriched.ExtraFields.RawResponse = redactGigaChatRawValue(enriched.ExtraFields.RawResponse)
	if enriched.Error != nil {
		enriched.Error.Message = redactGigaChatSensitiveText(enriched.Error.Message)
	}
	return enriched
}

func redactGigaChatRawPayload(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}
	var value interface{}
	if err := sonic.Unmarshal(payload, &value); err != nil {
		return []byte(redactGigaChatSensitiveText(string(payload)))
	}
	if stringValue, ok := value.(string); ok {
		redacted := redactGigaChatSensitiveText(stringValue)
		if redacted == stringValue {
			return payload
		}
		redactedPayload, err := sonic.Marshal(redacted)
		if err != nil {
			return []byte(redacted)
		}
		return redactedPayload
	}
	if !redactGigaChatJSONValue(value) {
		return payload
	}
	redacted, err := sonic.Marshal(value)
	if err != nil {
		return []byte(redactGigaChatSensitiveText(string(payload)))
	}
	return redacted
}

func redactGigaChatRawValue(raw interface{}) interface{} {
	switch typed := raw.(type) {
	case nil:
		return nil
	case json.RawMessage:
		return json.RawMessage(redactGigaChatRawPayload([]byte(typed)))
	case []byte:
		redacted := redactGigaChatRawPayload(typed)
		if json.Valid(redacted) {
			return json.RawMessage(redacted)
		}
		return string(redacted)
	case string:
		return string(redactGigaChatRawPayload([]byte(typed)))
	default:
		payload, err := sonic.Marshal(raw)
		if err != nil {
			return raw
		}
		redactedPayload := redactGigaChatRawPayload(payload)
		if bytes.Equal(payload, redactedPayload) {
			return raw
		}
		var redacted interface{}
		if err := sonic.Unmarshal(redactedPayload, &redacted); err != nil {
			return string(redactedPayload)
		}
		return redacted
	}
}

func redactGigaChatJSONValue(value interface{}) bool {
	return redactGigaChatJSONValueInContext(value, false)
}

func redactGigaChatJSONValueInContext(value interface{}, inGigaChatKeyConfig bool) bool {
	changed := false
	switch typed := value.(type) {
	case map[string]interface{}:
		authFieldContext := inGigaChatKeyConfig || hasGigaChatAuthSensitiveField(typed)
		for key, child := range typed {
			childInGigaChatKeyConfig := inGigaChatKeyConfig || strings.EqualFold(strings.TrimSpace(key), "gigachat_key_config")
			if isGigaChatSensitiveField(key, authFieldContext) {
				typed[key] = "<redacted>"
				changed = true
				continue
			}
			if redactedValue, ok := child.(string); ok {
				redacted := redactGigaChatSensitiveText(redactedValue)
				if redacted != redactedValue {
					typed[key] = redacted
					changed = true
				}
				continue
			}
			if redactGigaChatJSONValueInContext(child, childInGigaChatKeyConfig) {
				changed = true
			}
		}
	case []interface{}:
		for index, child := range typed {
			if redactedValue, ok := child.(string); ok {
				redacted := redactGigaChatSensitiveText(redactedValue)
				if redacted != redactedValue {
					typed[index] = redacted
					changed = true
				}
				continue
			}
			if redactGigaChatJSONValueInContext(child, inGigaChatKeyConfig) {
				changed = true
			}
		}
	}
	return changed
}

func hasGigaChatAuthSensitiveField(fields map[string]interface{}) bool {
	for fieldName := range fields {
		if isGigaChatSensitiveField(fieldName, false) {
			return true
		}
	}
	return false
}

func isGigaChatSensitiveField(fieldName string, inGigaChatKeyConfig bool) bool {
	switch strings.ToLower(strings.TrimSpace(fieldName)) {
	case "authorization", "access_token", "credentials", "username", "password", "cert_file", "key_file", "ca_bundle_file", "private_key", "client_key", "client_secret", "refresh_token":
		return true
	case "user":
		return inGigaChatKeyConfig
	default:
		return false
	}
}

func redactGigaChatSensitiveText(text string) string {
	redacted := text
	redacted = gigaChatPrivateKeyPattern.ReplaceAllString(redacted, "<redacted-private-key>")
	redacted = gigaChatAuthSchemePattern.ReplaceAllString(redacted, "$1 <redacted>")
	redacted = redactGigaChatSensitiveAssignments(redacted)
	return redacted
}

func redactGigaChatSensitiveAssignments(text string) string {
	redacted := redactGigaChatAssignmentsWithPattern(text, gigaChatSensitiveAssignmentPattern)
	if gigaChatAuthContextTextPattern.MatchString(text) {
		redacted = redactGigaChatAssignmentsWithPattern(redacted, gigaChatUserAssignmentPattern)
	}
	return redacted
}

func redactGigaChatAssignmentsWithPattern(text string, pattern *regexp.Regexp) string {
	return pattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := pattern.FindStringSubmatch(match)
		if len(parts) != 6 {
			return "<redacted>"
		}
		if parts[1] != parts[3] {
			return match
		}

		value := "<redacted>"
		if quote := firstGigaChatQuote(parts[5]); quote != "" {
			value = quote + value + quote
		}
		return parts[1] + parts[2] + parts[3] + parts[4] + value
	})
}

func firstGigaChatQuote(value string) string {
	if value == "" {
		return ""
	}
	switch value[0] {
	case '"':
		return `"`
	case '\'':
		return `'`
	default:
		return ""
	}
}
