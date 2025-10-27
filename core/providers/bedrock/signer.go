package bedrock

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/smithy-go/encoding/httpbinding"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	signingAlgorithm = "AWS4-HMAC-SHA256"
	amzDateKey       = "X-Amz-Date"
	amzSecurityToken = "X-Amz-Security-Token"
	timeFormat       = "20060102T150405Z"
	shortTimeFormat  = "20060102"
)

// Headers to ignore during signing
var ignoredHeaders = map[string]struct{}{
	"authorization":     {},
	"user-agent":        {},
	"x-amzn-trace-id":   {},
	"expect":            {},
	"transfer-encoding": {},
}

// signingKeyCache caches derived signing keys to avoid recomputation
type signingKeyCache struct {
	cache map[string]cachedKey
	mu    sync.RWMutex
}

type cachedKey struct {
	key       []byte
	date      string // YYYYMMDD format
	accessKey string
}

var keyCache = &signingKeyCache{
	cache: make(map[string]cachedKey),
}

// hmacSHA256 computes HMAC-SHA256
func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// deriveSigningKey derives the AWS signing key
func deriveSigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return kSigning
}

// getSigningKey retrieves or computes the signing key with caching
func getSigningKey(accessKey, secretKey, dateStamp, region, service string) []byte {
	cacheKey := fmt.Sprintf("%s/%s/%s/%s", accessKey, dateStamp, region, service)

	keyCache.mu.RLock()
	if cached, ok := keyCache.cache[cacheKey]; ok && cached.accessKey == accessKey && cached.date == dateStamp {
		keyCache.mu.RUnlock()
		return cached.key
	}
	keyCache.mu.RUnlock()

	keyCache.mu.Lock()
	defer keyCache.mu.Unlock()

	// Double-check after acquiring write lock
	if cached, ok := keyCache.cache[cacheKey]; ok && cached.accessKey == accessKey && cached.date == dateStamp {
		return cached.key
	}

	key := deriveSigningKey(secretKey, dateStamp, region, service)
	keyCache.cache[cacheKey] = cachedKey{
		key:       key,
		date:      dateStamp,
		accessKey: accessKey,
	}

	return key
}

// stripExcessSpaces removes excess spaces from a string
func stripExcessSpaces(str string) string {
	str = strings.TrimSpace(str)
	if !strings.Contains(str, "  ") {
		return str
	}

	var result strings.Builder
	result.Grow(len(str))
	prevWasSpace := false

	for _, ch := range str {
		if ch == ' ' {
			if !prevWasSpace {
				result.WriteRune(ch)
			}
			prevWasSpace = true
		} else {
			result.WriteRune(ch)
			prevWasSpace = false
		}
	}

	return result.String()
}

// signAWSRequestFastHTTP signs a fasthttp request using AWS Signature Version 4
// This is a native implementation that avoids allocating http.Request
func signAWSRequestFastHTTP(
	ctx context.Context,
	req *fasthttp.Request,
	body []byte,
	accessKey, secretKey string,
	sessionToken *string,
	region, service string,
	providerName schemas.ModelProvider,
) *schemas.BifrostError {
	// Get AWS credentials if not provided
	if accessKey == "" && secretKey == "" {
		cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
		if err != nil {
			return providerUtils.NewBifrostOperationError("failed to load aws config", err, providerName)
		}
		creds, err := cfg.Credentials.Retrieve(ctx)
		if err != nil {
			return providerUtils.NewBifrostOperationError("failed to retrieve aws credentials", err, providerName)
		}
		accessKey = creds.AccessKeyID
		secretKey = creds.SecretAccessKey
		if creds.SessionToken != "" {
			st := creds.SessionToken
			sessionToken = &st
		}
	}

	// Get current time
	now := time.Now().UTC()
	amzDate := now.Format(timeFormat)
	dateStamp := now.Format(shortTimeFormat)

	// Parse URI
	uri := req.URI()
	host := string(uri.Host())
	path := string(uri.Path())
	if path == "" {
		path = "/"
	}
	queryString := string(uri.QueryString())

	// Escape path for canonical URI (Bedrock doesn't disable escaping)
	canonicalURI := httpbinding.EscapePath(path, false)

	// Calculate payload hash
	hash := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(hash[:])

	// Set required headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(amzDateKey, amzDate)
	if sessionToken != nil && *sessionToken != "" {
		req.Header.Set(amzSecurityToken, *sessionToken)
	}

	// Build canonical headers
	var headerNames []string
	headerMap := make(map[string][]string)

	// Always include host
	headerNames = append(headerNames, "host")
	headerMap["host"] = []string{host}

	// Include content-length if body is present
	if len(body) > 0 {
		headerNames = append(headerNames, "content-length")
		headerMap["content-length"] = []string{strconv.Itoa(len(body))}
	}

	// Collect other headers
	for key, value := range req.Header.All() {
		keyStr := strings.ToLower(string(key))

		// Skip ignored headers
		if _, ignore := ignoredHeaders[keyStr]; ignore {
			continue
		}

		// Skip if already handled
		if keyStr == "host" || keyStr == "content-length" {
			continue
		}

		if _, exists := headerMap[keyStr]; !exists {
			headerNames = append(headerNames, keyStr)
		}
		headerMap[keyStr] = append(headerMap[keyStr], string(value))
	}

	// Sort header names
	sort.Strings(headerNames)

	// Build canonical headers string
	var canonicalHeaders strings.Builder
	for _, name := range headerNames {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteRune(':')

		values := headerMap[name]
		for i, v := range values {
			cleanedValue := stripExcessSpaces(v)
			canonicalHeaders.WriteString(cleanedValue)
			if i < len(values)-1 {
				canonicalHeaders.WriteRune(',')
			}
		}
		canonicalHeaders.WriteRune('\n')
	}

	signedHeaders := strings.Join(headerNames, ";")

	// Parse and normalize query string
	var canonicalQueryString string
	if queryString != "" {
		values, _ := url.ParseQuery(queryString)
		// Sort keys
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		// Sort values for each key
		for _, k := range keys {
			sort.Strings(values[k])
		}

		canonicalQueryString = values.Encode()
		canonicalQueryString = strings.ReplaceAll(canonicalQueryString, "+", "%20")
	}

	// Build canonical request
	canonicalRequest := strings.Join([]string{
		string(req.Header.Method()),
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	// Build credential scope
	credentialScope := strings.Join([]string{
		dateStamp,
		region,
		service,
		"aws4_request",
	}, "/")

	// Build string to sign
	canonicalRequestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		signingAlgorithm,
		amzDate,
		credentialScope,
		hex.EncodeToString(canonicalRequestHash[:]),
	}, "\n")

	// Calculate signature
	signingKey := getSigningKey(accessKey, secretKey, dateStamp, region, service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	// Build authorization header
	authHeader := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		signingAlgorithm,
		accessKey,
		credentialScope,
		signedHeaders,
		signature,
	)

	req.Header.Set("Authorization", authHeader)

	return nil
}
