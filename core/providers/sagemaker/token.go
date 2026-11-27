package sagemaker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/providers/utils/tokencache"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	tokenHost            = "sagemaker.amazonaws.com"
	tokenPrefix          = "sagemaker-api-key-"
	tokenCacheKeyVersion = "sagemaker-bearer-v1"
	emptyPayloadSHA256   = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	defaultTokenTTL    = time.Hour
	maxTokenTTL        = 12 * time.Hour // SageMaker answers 500, not 4xx, to a longer X-Amz-Expires
	minTokenTTL        = time.Minute
	tokenRefreshMargin = 5 * time.Minute
	credsExpiryMargin  = 2 * time.Minute
)

// tokenCacheKey identifies the credential tuple a minted token belongs to.
func tokenCacheKey(cfg *schemas.SageMakerKeyConfig, region string) string {
	if cfg == nil {
		return tokencache.CacheKey(tokenCacheKeyVersion, region)
	}
	return tokencache.CacheKey(tokenCacheKeyVersion, region,
		cfg.AccessKey.GetValue(), cfg.SecretKey.GetValue(), secretValue(cfg.SessionToken),
		secretValue(cfg.RoleARN), secretValue(cfg.ExternalID), secretValue(cfg.RoleSessionName))
}

func secretValue(v *schemas.SecretVar) string {
	if v == nil {
		return ""
	}
	return v.GetValue()
}

// authHeader returns the Authorization header for one request and the token cache key it came
// from ("" when Key.Value carries a pre-minted token). A warm call is one cache lookup.
func (provider *SageMakerProvider) authHeader(ctx *schemas.BifrostContext, key schemas.Key, region string) (map[string]string, string, *schemas.BifrostError) {
	if v := key.Value.GetValue(); v != "" {
		return map[string]string{"Authorization": "Bearer " + v}, "", nil
	}
	cacheKey := tokenCacheKey(key.SageMakerKeyConfig, region)
	entry, bErr := provider.tokens.Get(ctx, cacheKey, func(mintCtx context.Context, _ *tokencache.Entry[string]) (*tokencache.Entry[string], *schemas.BifrostError) {
		bctx := schemas.NewBifrostContext(mintCtx, schemas.NoDeadline)
		defer bctx.Cancel()
		creds, bErr := bedrock.ResolveAWSCredentials(bctx, bedrockKeyConfig(key.SageMakerKeyConfig), region)
		if bErr != nil {
			return nil, bErr
		}
		return tokenEntry(creds, region, time.Now()), nil
	})
	if bErr != nil {
		return nil, "", bErr
	}
	return map[string]string{"Authorization": entry.Value}, cacheKey, nil
}

// tokenEntry mints a token for creds and sets when the cache refreshes and drops it. The signed
// lifetime keeps its one-minute floor, but the cache never holds the token past the credentials'
// own expiry: a token signed by expired credentials is rejected.
func tokenEntry(creds aws.Credentials, region string, now time.Time) *tokencache.Entry[string] {
	ttl := tokenTTL(creds, now)
	life := ttl
	if creds.CanExpire {
		life = min(life, max(creds.Expires.Sub(now), 0))
	}
	refreshIn := life - tokenRefreshMargin
	if refreshIn < life/2 {
		refreshIn = life / 2
	}
	return &tokencache.Entry[string]{
		Value:     "Bearer " + mintBearerToken(creds, region, ttl, now),
		ExpiresAt: now.Add(life),
		RefreshAt: now.Add(refreshIn),
	}
}

// tokenTTL is one hour, shortened to end before temporary credentials do, and clamped to
// SageMaker's 12-hour maximum.
func tokenTTL(creds aws.Credentials, now time.Time) time.Duration {
	ttl := defaultTokenTTL
	if creds.CanExpire {
		if remaining := creds.Expires.Sub(now) - credsExpiryMargin; remaining < ttl {
			ttl = remaining
		}
	}
	return min(max(ttl, minTokenTTL), maxTokenTTL)
}

// mintBearerToken ports sagemaker-core's generate_token: a SigV4 query-presigned
// "POST sagemaker.amazonaws.com/?Action=CallWithBearerToken" URL without its scheme, with
// "&Version=1" appended, standard-base64 encoded and prefixed. No network call is made.
func mintBearerToken(creds aws.Credentials, region string, ttl time.Duration, now time.Time) string {
	amzDate := now.UTC().Format("20060102T150405Z")
	scope := amzDate[:8] + "/" + region + "/sagemaker/aws4_request"
	params := [][2]string{
		{"Action", "CallWithBearerToken"},
		{"X-Amz-Algorithm", "AWS4-HMAC-SHA256"},
		{"X-Amz-Credential", creds.AccessKeyID + "/" + scope},
		{"X-Amz-Date", amzDate},
		{"X-Amz-Expires", strconv.Itoa(int(ttl / time.Second))},
		{"X-Amz-SignedHeaders", "host"},
	}
	if creds.SessionToken != "" {
		params = append(params, [2]string{"X-Amz-Security-Token", creds.SessionToken})
	}
	query := encodeQuery(params)

	sorted := append([][2]string(nil), params...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i][0] < sorted[j][0] })
	canonical := "POST\n/\n" + encodeQuery(sorted) + "\nhost:" + tokenHost + "\n\nhost\n" + emptyPayloadSHA256
	canonicalHash := sha256.Sum256([]byte(canonical))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(canonicalHash[:])

	signingKey := hmacSHA256([]byte("AWS4"+creds.SecretAccessKey), amzDate[:8])
	for _, part := range []string{region, "sagemaker", "aws4_request"} {
		signingKey = hmacSHA256(signingKey, part)
	}
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	presigned := tokenHost + "/?" + query + "&X-Amz-Signature=" + signature + "&Version=1"
	return tokenPrefix + base64.StdEncoding.EncodeToString([]byte(presigned))
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// encodeQuery joins key=value pairs in the given order with SigV4 URI encoding.
func encodeQuery(params [][2]string) string {
	var b strings.Builder
	for i, p := range params {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(uriEncode(p[0]))
		b.WriteByte('=')
		b.WriteString(uriEncode(p[1]))
	}
	return b.String()
}

// uriEncode percent-encodes everything except RFC 3986 unreserved characters, as SigV4 requires.
func uriEncode(s string) string {
	const hexUpper = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexUpper[c>>4])
		b.WriteByte(hexUpper[c&15])
	}
	return b.String()
}
