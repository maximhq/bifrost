package sagemaker

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// Golden vectors from scripts/terraform/sagemaker/probe.py mint_token, which is byte-identical
// to sagemaker-core's generate_token on botocore 1.43.118. Fixed clock 2026-10-22T12:00:00Z.
const (
	goldenTokenWithSession = "sagemaker-api-key-c2FnZW1ha2VyLmFtYXpvbmF3cy5jb20vP0FjdGlvbj1DYWxsV2l0aEJlYXJlclRva2VuJlgtQW16LUFsZ29yaXRobT1BV1M0LUhNQUMtU0hBMjU2JlgtQW16LUNyZWRlbnRpYWw9QUtJREVYQU1QTEUlMkYyMDI2MTAyMiUyRnVzLWVhc3QtMSUyRnNhZ2VtYWtlciUyRmF3czRfcmVxdWVzdCZYLUFtei1EYXRlPTIwMjYxMDIyVDEyMDAwMFomWC1BbXotRXhwaXJlcz0zNjAwJlgtQW16LVNpZ25lZEhlYWRlcnM9aG9zdCZYLUFtei1TZWN1cml0eS1Ub2tlbj1TRVNTSU9OVE9LRU4lMkZFWEFNUExFJTJCJTNEJTNEJlgtQW16LVNpZ25hdHVyZT02OTgxMTBlMTVhZWNiNDVkMzgyYWJlOTQ2YjQyYTYyYzIzNTZhMGE4NWNhMDdkMjJkYTcwMGVkNDk2YjI3MDQxJlZlcnNpb249MQ=="
	goldenTokenNoSession   = "sagemaker-api-key-c2FnZW1ha2VyLmFtYXpvbmF3cy5jb20vP0FjdGlvbj1DYWxsV2l0aEJlYXJlclRva2VuJlgtQW16LUFsZ29yaXRobT1BV1M0LUhNQUMtU0hBMjU2JlgtQW16LUNyZWRlbnRpYWw9QUtJREVYQU1QTEUlMkYyMDI2MTAyMiUyRmV1LXdlc3QtMSUyRnNhZ2VtYWtlciUyRmF3czRfcmVxdWVzdCZYLUFtei1EYXRlPTIwMjYxMDIyVDEyMDAwMFomWC1BbXotRXhwaXJlcz05MDAmWC1BbXotU2lnbmVkSGVhZGVycz1ob3N0JlgtQW16LVNpZ25hdHVyZT1lZTFiODMzM2U4YWE1Nzg0NjExN2M2YzdlZjU5Njc1ZTE2MWI0NjhiN2ZmZGYyZWY0NzkzYTE5YmU4ZDJiYTdkJlZlcnNpb249MQ=="
	exampleSecretKey       = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
)

var goldenClock = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

type noopLogger struct{}

func (noopLogger) Debug(string, ...any)                   {}
func (noopLogger) Info(string, ...any)                    {}
func (noopLogger) Warn(string, ...any)                    {}
func (noopLogger) Error(string, ...any)                   {}
func (noopLogger) Fatal(string, ...any)                   {}
func (noopLogger) SetLevel(schemas.LogLevel)              {}
func (noopLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func TestMintBearerTokenMatchesBotocore(t *testing.T) {
	withSession := aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: exampleSecretKey, SessionToken: "SESSIONTOKEN/EXAMPLE+=="}
	require.Equal(t, goldenTokenWithSession, mintBearerToken(withSession, "us-east-1", time.Hour, goldenClock))

	noSession := aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: exampleSecretKey}
	require.Equal(t, goldenTokenNoSession, mintBearerToken(noSession, "eu-west-1", 15*time.Minute, goldenClock))
}

func TestTokenTTL(t *testing.T) {
	now := goldenClock
	require.Equal(t, time.Hour, tokenTTL(aws.Credentials{}, now), "non-expiring credentials get the default hour")

	expiring := aws.Credentials{CanExpire: true, Expires: now.Add(10 * time.Minute)}
	require.Equal(t, 8*time.Minute, tokenTTL(expiring, now), "the token must end before its temporary credentials do")

	almostGone := aws.Credentials{CanExpire: true, Expires: now.Add(30 * time.Second)}
	require.Equal(t, time.Minute, tokenTTL(almostGone, now), "X-Amz-Expires never drops below the floor")

	require.LessOrEqual(t, tokenTTL(aws.Credentials{}, now), maxTokenTTL, "SageMaker answers 500 to tokens valid for more than 12 hours")
}

func TestTokenEntryNeverOutlivesCredentials(t *testing.T) {
	now := goldenClock
	creds := aws.Credentials{AccessKeyID: "AKID", SecretAccessKey: "secret", CanExpire: true, Expires: now.Add(10 * time.Second)}
	entry := tokenEntry(creds, "us-east-1", now)
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(strings.TrimPrefix(entry.Value, "Bearer "), tokenPrefix))
	require.NoError(t, err)
	require.Contains(t, string(decoded), "X-Amz-Expires=60", "the signed lifetime keeps SageMaker's one-minute floor")
	require.False(t, entry.ExpiresAt.After(creds.Expires), "cached past the credentials' expiry: %s > %s", entry.ExpiresAt, creds.Expires)
	require.False(t, entry.RefreshAt.After(entry.ExpiresAt))

	hour := tokenEntry(aws.Credentials{AccessKeyID: "AKID", SecretAccessKey: "secret"}, "us-east-1", now)
	require.Equal(t, now.Add(time.Hour), hour.ExpiresAt, "non-expiring credentials keep the full hour")
	require.Equal(t, now.Add(55*time.Minute), hour.RefreshAt)
}

func TestUriEncode(t *testing.T) {
	require.Equal(t, "AKID%2F20261022%2Fus-east-1", uriEncode("AKID/20261022/us-east-1"))
	require.Equal(t, "a%2Bb%3D%3D~-_.", uriEncode("a+b==~-_."))
	require.Equal(t, "%20", uriEncode(" "))
}

func newTestProvider(t testing.TB) *SageMakerProvider {
	t.Helper()
	config := &schemas.ProviderConfig{}
	config.CheckAndSetDefaults()
	provider, err := NewSageMakerProvider(config, noopLogger{})
	require.NoError(t, err)
	return provider
}

func explicitCredsKey() schemas.Key {
	return schemas.Key{SageMakerKeyConfig: &schemas.SageMakerKeyConfig{
		AccessKey: *schemas.NewSecretVar("AKIDEXAMPLE"),
		SecretKey: *schemas.NewSecretVar(exampleSecretKey),
		Region:    schemas.NewSecretVar("us-east-1"),
	}}
}

func TestAuthHeaderCachesMintedToken(t *testing.T) {
	provider := newTestProvider(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	key := explicitCredsKey()

	first, cacheKey, bErr := provider.authHeader(ctx, key, "us-east-1")
	require.Nil(t, bErr)
	require.NotEmpty(t, cacheKey)
	require.True(t, strings.HasPrefix(first["Authorization"], "Bearer "+tokenPrefix))

	second, _, bErr := provider.authHeader(ctx, key, "us-east-1")
	require.Nil(t, bErr)
	require.Equal(t, first, second, "a warm call must reuse the cached token")

	otherRegion, otherKey, bErr := provider.authHeader(ctx, key, "eu-west-1")
	require.Nil(t, bErr)
	require.NotEqual(t, cacheKey, otherKey, "tokens are region-scoped")
	require.NotEqual(t, first, otherRegion)

	provider.tokens.Invalidate(cacheKey)
	require.Nil(t, provider.tokens.Peek(cacheKey), "invalidation drops the token so the next call mints again")
}

func TestAuthHeaderUsesKeyValueVerbatim(t *testing.T) {
	provider := newTestProvider(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	header, cacheKey, bErr := provider.authHeader(ctx, schemas.Key{Value: *schemas.NewSecretVar("sagemaker-api-key-premint")}, "us-east-1")
	require.Nil(t, bErr)
	require.Equal(t, "Bearer sagemaker-api-key-premint", header["Authorization"])
	require.Empty(t, cacheKey, "a pre-minted token is never cached or invalidated")
}

func TestMintedTokenCarriesNoSecret(t *testing.T) {
	token := mintBearerToken(aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: exampleSecretKey}, "us-east-1", time.Hour, goldenClock)
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(token, tokenPrefix))
	require.NoError(t, err)
	require.NotContains(t, string(decoded), exampleSecretKey)
	require.NotContains(t, string(decoded), uriEncode(exampleSecretKey))
	require.Contains(t, string(decoded), "X-Amz-Signature=", "the token carries a signature, never the secret")
}

func TestExpiredTokenResponseDropsCachedToken(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"[REDACTED] has expired","type":"permission_error","code":"access_denied"}}`))
	}))
	defer ts.Close()

	config := &schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 5, InsecureSkipVerify: true, AllowPrivateNetwork: true}}
	config.CheckAndSetDefaults()
	provider, err := NewSageMakerProvider(config, noopLogger{})
	require.NoError(t, err)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	key := explicitCredsKey()
	key.SageMakerKeyConfig.Endpoints = &schemas.SageMakerEndpoints{Runtime: schemas.NewSecretVar(strings.TrimPrefix(ts.URL, "https://"))}
	cacheKey := tokenCacheKey(key.SageMakerKeyConfig, "us-east-1")

	_, bErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{
		Provider: schemas.SageMaker,
		Model:    "qwen3-ep",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
	})
	require.NotNil(t, bErr)
	require.Equal(t, http.StatusForbidden, *bErr.StatusCode)
	require.Nil(t, provider.tokens.Peek(cacheKey), "a token SageMaker rejected as expired must not be served again")
}

func BenchmarkBearerTokenWarm(b *testing.B) {
	provider := newTestProvider(b)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	key := explicitCredsKey()
	if _, _, bErr := provider.authHeader(ctx, key, "us-east-1"); bErr != nil {
		b.Fatal(bErr.Error.Message)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, _, bErr := provider.authHeader(ctx, key, "us-east-1"); bErr != nil {
			b.Fatal(bErr.Error.Message)
		}
	}
}
