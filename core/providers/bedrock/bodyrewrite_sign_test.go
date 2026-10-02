package bedrock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

const signToken = "@@T0@@"

// signSplicer swaps signToken for payload, streaming.
func signSplicer(payload string) providerUtils.RequestBodyRewriter {
	return func(body []byte) (io.Reader, int64, error) {
		before, after, ok := bytes.Cut(body, []byte(signToken))
		if !ok {
			return nil, 0, nil
		}
		size := int64(len(before) + len(payload) + len(after))
		return io.MultiReader(bytes.NewReader(before), strings.NewReader(payload), bytes.NewReader(after)), size, nil
	}
}

func signTestKey() *schemas.BedrockKeyConfig {
	return &schemas.BedrockKeyConfig{
		AccessKey: *schemas.NewSecretVar("AKIAIOSFODNN7EXAMPLE"),
		SecretKey: *schemas.NewSecretVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
	}
}

// sameSecond retries sign until both signatures carry the same X-Amz-Date, so a
// second boundary between the two signings cannot make the comparison flaky.
func sameSecond(t *testing.T, sign func() (dateA, dateB string)) {
	t.Helper()
	for range 5 {
		if a, b := sign(); a == b {
			return
		}
	}
	t.Fatal("could not sign both requests within the same second")
}

// T1.5: signing the skeleton with a rewriter yields the same Authorization as
// signing the spliced bytes without one, at the same X-Amz-Date — the hash AND
// the signed content-length cover the bytes that are actually sent.
func TestSignAWSRequest_SignsRewrittenBody(t *testing.T) {
	payload := strings.Repeat("p", 4096)
	skeleton := `{"messages":[{"content":"` + signToken + `"}]}`
	spliced := `{"messages":[{"content":"` + payload + `"}]}`

	rwCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	rwCtx.SetValue(schemas.BifrostContextKeyRequestBodyRewriter, signSplicer(payload))
	plainCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	url := "https://bedrock-runtime.us-east-1.amazonaws.com/model/m/converse"

	var withHook, reference *http.Request
	sameSecond(t, func() (string, string) {
		var err error
		withHook, err = http.NewRequestWithContext(rwCtx, http.MethodPost, url, bytes.NewReader([]byte(skeleton)))
		require.NoError(t, err)
		reference, err = http.NewRequestWithContext(plainCtx, http.MethodPost, url, bytes.NewReader([]byte(spliced)))
		require.NoError(t, err)
		require.Nil(t, signAWSRequest(rwCtx, withHook, signTestKey(), "us-east-1", bedrockSigningService))
		require.Nil(t, signAWSRequest(plainCtx, reference, signTestKey(), "us-east-1", bedrockSigningService))
		return withHook.Header.Get("X-Amz-Date"), reference.Header.Get("X-Amz-Date")
	})

	assert.Contains(t, withHook.Header.Get("Authorization"), "content-length", "content-length must be signed")
	assert.Equal(t, reference.Header.Get("Authorization"), withHook.Header.Get("Authorization"))
	assert.Equal(t, reference.Header.Get("x-amz-content-sha256"), withHook.Header.Get("x-amz-content-sha256"))
	assert.Equal(t, int64(len(spliced)), withHook.ContentLength)

	// The body left on the request is still the skeleton: the transport splices it.
	got, err := io.ReadAll(withHook.Body)
	require.NoError(t, err)
	assert.Equal(t, skeleton, string(got))
}

func TestSignAWSRequestFastHTTP_SignsRewrittenBody(t *testing.T) {
	payload := strings.Repeat("q", 4096)
	skeleton := []byte(`{"c":"` + signToken + `"}`)
	spliced := []byte(`{"c":"` + payload + `"}`)
	rwCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	rwCtx.SetValue(schemas.BifrostContextKeyRequestBodyRewriter, signSplicer(payload))

	build := func(body []byte) *fasthttp.Request {
		req := fasthttp.AcquireRequest()
		req.SetRequestURI("https://bedrock-runtime.us-east-1.amazonaws.com/model/m/invoke")
		req.Header.SetMethod(http.MethodPost)
		req.SetBody(body)
		req.Header.SetContentLength(len(body))
		return req
	}
	var withHook, reference *fasthttp.Request
	t.Cleanup(func() {
		fasthttp.ReleaseRequest(withHook)
		fasthttp.ReleaseRequest(reference)
	})
	sameSecond(t, func() (string, string) {
		if withHook != nil {
			fasthttp.ReleaseRequest(withHook)
			fasthttp.ReleaseRequest(reference)
		}
		withHook, reference = build(skeleton), build(spliced)
		require.Nil(t, signAWSRequestFastHTTP(rwCtx, withHook, skeleton, "AKIAIOSFODNN7EXAMPLE", "secret", nil, "us-east-1", bedrockSigningService))
		require.Nil(t, signAWSRequestFastHTTP(context.Background(), reference, spliced, "AKIAIOSFODNN7EXAMPLE", "secret", nil, "us-east-1", bedrockSigningService))
		return string(withHook.Header.Peek(amzDateKey)), string(reference.Header.Peek(amzDateKey))
	})

	auth := string(withHook.Header.Peek("Authorization"))
	assert.Contains(t, auth, "content-length")
	assert.Equal(t, string(reference.Header.Peek("Authorization")), auth)
}

// A rewriter abort while signing is a non-retried Bifrost operation error that
// still carries ErrRequestBodyRewrite.
func TestSignAWSRequest_RewriterAbort(t *testing.T) {
	errMutated := errors.New("token mutated")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestBodyRewriter, providerUtils.RequestBodyRewriter(
		func([]byte) (io.Reader, int64, error) { return nil, 0, errMutated }))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/model/m/converse", strings.NewReader(`{"c":"x"}`))
	require.NoError(t, err)

	berr := signAWSRequest(ctx, req, signTestKey(), "us-east-1", bedrockSigningService)
	require.NotNil(t, berr)
	assert.True(t, berr.IsBifrostError)
	assert.ErrorIs(t, berr.Error.Error, providerUtils.ErrRequestBodyRewrite)
	assert.ErrorIs(t, berr.Error.Error, errMutated)
}

// Without the rewriter context key, signAWSRequest produces exactly the
// pre-existing signature: sha256 of the body, ContentLength left as the caller
// set it, signed by the SDK at the same X-Amz-Date. The reference is computed
// here with the SDK directly, the way the unpatched signer did it.
func TestSignAWSRequest_NoRewriterIsUnchanged(t *testing.T) {
	body := `{"messages":[{"content":"` + signToken + `"}]}`
	url := "https://bedrock-runtime.us-east-1.amazonaws.com/model/m/converse"
	key := signTestKey()
	cases := map[string]func(*http.Request){
		"content length from NewRequest": func(*http.Request) {},
		"caller-set content length 0":    func(r *http.Request) { r.ContentLength = 0 },
	}
	for name, adjust := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
			require.NoError(t, err)
			adjust(req)
			wantCL := req.ContentLength
			require.Nil(t, signAWSRequest(ctx, req, key, "us-east-1", bedrockSigningService))

			signedAt, err := time.Parse("20060102T150405Z", req.Header.Get("X-Amz-Date"))
			require.NoError(t, err)
			ref, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
			require.NoError(t, err)
			adjust(ref)
			sum := sha256.Sum256([]byte(body))
			hash := hex.EncodeToString(sum[:])
			ref.Header.Set("Content-Type", "application/json")
			ref.Header.Set("Accept", "application/json")
			ref.Header.Set("x-amz-content-sha256", hash)
			creds := aws.Credentials{AccessKeyID: key.AccessKey.GetValue(), SecretAccessKey: key.SecretKey.GetValue()}
			require.NoError(t, v4.NewSigner().SignHTTP(context.Background(), creds, ref, hash, bedrockSigningService, "us-east-1", signedAt))

			assert.Equal(t, wantCL, req.ContentLength, "ContentLength must be left as the caller set it")
			assert.Equal(t, hash, req.Header.Get("x-amz-content-sha256"))
			assert.Equal(t, ref.Header.Get("Authorization"), req.Header.Get("Authorization"))
			got, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			assert.Equal(t, body, string(got))
		})
	}
}
