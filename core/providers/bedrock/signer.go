package bedrock

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// percentEncodeRFC3986 encodes a string per RFC 3986
// Keep unreserved characters (A-Z, a-z, 0-9, -, _, ., ~) as-is
// Percent-encode everything else as %HH using uppercase hex
func percentEncodeRFC3986(s string) string {
	var result strings.Builder
	result.Grow(len(s))

	for i := 0; i < len(s); i++ {
		b := s[i]
		// RFC 3986 unreserved characters
		if (b >= 'A' && b <= 'Z') ||
			(b >= 'a' && b <= 'z') ||
			(b >= '0' && b <= '9') ||
			b == '-' || b == '_' || b == '.' || b == '~' {
			result.WriteByte(b)
		} else {
			// Percent-encode with uppercase hex
			result.WriteByte('%')
			result.WriteByte(uppercaseHex(b >> 4))
			result.WriteByte(uppercaseHex(b & 0x0F))
		}
	}

	return result.String()
}

// uppercaseHex returns the uppercase hex character for a nibble (0-15)
func uppercaseHex(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'A' + (b - 10)
}

// percentDecode decodes percent-encoded sequences in a string without treating + as space
// This differs from url.QueryUnescape which uses form encoding (+ becomes space)
func percentDecode(s string) string {
	// Quick check if there are any percent signs
	if !strings.Contains(s, "%") {
		return s
	}

	var result strings.Builder
	result.Grow(len(s))

	for i := 0; i < len(s); {
		if s[i] == '%' && i+2 < len(s) {
			// Try to decode the hex sequence
			if h1 := unhex(s[i+1]); h1 >= 0 {
				if h2 := unhex(s[i+2]); h2 >= 0 {
					result.WriteByte(byte(h1<<4 | h2))
					i += 3
					continue
				}
			}
		}
		result.WriteByte(s[i])
		i++
	}

	return result.String()
}

// unhex converts a hex character to its value, or -1 if not a hex char
func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c - 'a' + 10)
	case 'A' <= c && c <= 'F':
		return int(c - 'A' + 10)
	}
	return -1
}

// queryPair represents a query parameter name-value pair
type queryPair struct {
	encodedName  string
	encodedValue string
}

// buildCanonicalQueryString builds a canonical query string per AWS SigV4 spec
// using proper RFC 3986 percent-encoding
func buildCanonicalQueryString(queryString string) string {
	if queryString == "" {
		return ""
	}

	// Split the raw query string on '&' into pairs
	rawPairs := strings.Split(queryString, "&")
	pairs := make([]queryPair, 0, len(rawPairs))

	for _, rawPair := range rawPairs {
		if rawPair == "" {
			continue
		}

		// Split on the first '=' to get name and value
		var name, value string
		if idx := strings.IndexByte(rawPair, '='); idx >= 0 {
			name = rawPair[:idx]
			value = rawPair[idx+1:]
		} else {
			// No '=' means name only, empty value
			name = rawPair
			value = ""
		}

		// Decode percent-encoded sequences first to normalize (handles already-encoded values)
		// then encode per RFC 3986 to ensure consistent encoding
		// Note: We use percentDecode instead of url.QueryUnescape because the latter
		// treats + as space (form encoding), but we need + to encode as %2B
		decodedName := percentDecode(name)
		decodedValue := percentDecode(value)

		// Percent-encode name and value per RFC 3986
		encodedName := percentEncodeRFC3986(decodedName)
		encodedValue := percentEncodeRFC3986(decodedValue)

		pairs = append(pairs, queryPair{
			encodedName:  encodedName,
			encodedValue: encodedValue,
		})
	}

	// Sort pairs lexicographically by encoded name, then by encoded value
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].encodedName != pairs[j].encodedName {
			return pairs[i].encodedName < pairs[j].encodedName
		}
		return pairs[i].encodedValue < pairs[j].encodedValue
	})

	// Join encoded pairs with '&'
	var result strings.Builder
	for i, pair := range pairs {
		if i > 0 {
			result.WriteByte('&')
		}
		result.WriteString(pair.encodedName)
		result.WriteByte('=')
		result.WriteString(pair.encodedValue)
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
	if cl := req.Header.ContentLength(); cl >= 0 {
		headerNames = append(headerNames, "content-length")
		headerMap["content-length"] = []string{strconv.Itoa(cl)}
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

	// Build canonical query string using RFC 3986 encoding
	canonicalQueryString := buildCanonicalQueryString(queryString)

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
