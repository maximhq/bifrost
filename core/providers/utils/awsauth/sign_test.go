package awsauth

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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testCreds = aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret", SessionToken: "token"}

func TestHashBody(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","method":"tools/list","id":1}`)
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	empty := sha256.Sum256(nil)
	require.Equal(t, hex.EncodeToString(empty[:]), emptyBodyHash)

	for name, tc := range map[string]struct {
		body io.Reader
		want string
	}{
		"rewindable body": {bytes.NewReader(body), want},
		"one-shot body":   {io.NopCloser(bytes.NewReader(body)), want},
		"empty body":      {bytes.NewReader(nil), emptyBodyHash},
		"no body":         {nil, emptyBodyHash},
		"http.NoBody":     {http.NoBody, emptyBodyHash},
	} {
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, "https://example.amazonaws.com/mcp", tc.body)
			require.NoError(t, err)
			got, err := HashBody(req)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			if tc.want == want {
				rest, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				assert.Equal(t, body, rest, "the body must be readable from the start after hashing")
			}
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestHashBody_Errors(t *testing.T) {
	t.Run("one-shot body fails to read", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, "https://example.amazonaws.com/mcp", io.NopCloser(failingReader{}))
		require.NoError(t, err)
		_, err = HashBody(req)
		assert.ErrorContains(t, err, "reading request body: connection reset")
	})
	t.Run("rewindable body fails to read", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, "https://example.amazonaws.com/mcp", bytes.NewReader([]byte("x")))
		require.NoError(t, err)
		req.Body = io.NopCloser(failingReader{})
		_, err = HashBody(req)
		assert.ErrorContains(t, err, "reading request body: connection reset")
	})
	t.Run("body fails to rewind", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, "https://example.amazonaws.com/mcp", bytes.NewReader([]byte("x")))
		require.NoError(t, err)
		req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("gone") }
		_, err = HashBody(req)
		assert.ErrorContains(t, err, "rewinding request body: gone")
	})
}

func TestStripHeadersForSigning(t *testing.T) {
	t.Run("nothing volatile leaves the request untouched", func(t *testing.T) {
		header := http.Header{"Content-Type": {"application/json"}, "Mcp-Session-Id": {"abc"}}
		assert.Nil(t, stripHeadersForSigning(header))
		assert.Equal(t, http.Header{"Content-Type": {"application/json"}, "Mcp-Session-Id": {"abc"}}, header)
	})
	t.Run("volatile headers are lifted out and internal ones dropped", func(t *testing.T) {
		header := http.Header{}
		for _, name := range []string{"Connection", "Keep-Alive", "Te", "Trailer", "Upgrade", "Proxy-Authorization", "Proxy-Authenticate", "X-Real-Ip", "X-Request-Id", "X-Forwarded-For", "X-Forwarded-Host", "X-Bf-Vk", "X-Bf-Api-Key", "Accept"} {
			header.Set(name, "v")
		}
		restore := stripHeadersForSigning(header)
		assert.Equal(t, http.Header{"Accept": {"v"}}, header)
		var restored []string
		for _, h := range restore {
			restored = append(restored, h.name)
		}
		assert.ElementsMatch(t, []string{"Connection", "Keep-Alive", "Te", "Trailer", "Upgrade", "Proxy-Authorization", "Proxy-Authenticate", "X-Real-Ip", "X-Request-Id", "X-Forwarded-For", "X-Forwarded-Host"}, restored)
	})
}

// TestSign_DropsInternalAndRestoresVolatileHeaders: x-bf-* never reaches AWS, and volatile
// headers stay on the wire but out of SignedHeaders.
func TestSign_DropsInternalAndRestoresVolatileHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://gw.gateway.bedrock-agentcore.us-east-1.amazonaws.com/mcp", nil)
	require.NoError(t, err)
	req.Header.Set("x-bf-vk", "sk-bf-secret")
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	req.Header.Set("Mcp-Session-Id", "abc")

	require.NoError(t, Sign(context.Background(), req, testCreds, emptyBodyHash, "bedrock-agentcore", "us-east-1"))

	auth := req.Header.Get("Authorization")
	require.True(t, strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/"), auth)
	assert.Contains(t, auth, "/us-east-1/bedrock-agentcore/aws4_request")
	assert.Equal(t, "host;mcp-session-id;x-amz-date;x-amz-security-token", signedHeaders(t, auth))
	assert.Equal(t, "token", req.Header.Get("X-Amz-Security-Token"))
	assert.Empty(t, req.Header.Get("x-bf-vk"))
	assert.Equal(t, "10.0.0.1", req.Header.Get("X-Forwarded-For"))
}

// TestSign_MatchesSDKSigner: Sign's signature is the one the SDK signer computes for the same
// request, payload hash and time, and only s3 skips the second path escaping.
func TestSign_MatchesSDKSigner(t *testing.T) {
	const rawURL = "https://bucket.s3.us-east-1.amazonaws.com/path%20with%20space/key?b=2&a=1"
	body := []byte(`{"a":1}`)
	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])

	sign := func(service string) *http.Request {
		req, err := http.NewRequest(http.MethodPut, rawURL, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		require.NoError(t, Sign(context.Background(), req, testCreds, payloadHash, service, "us-east-1"))
		return req
	}
	expect := func(req *http.Request, service string, disableEscaping bool) string {
		signedAt, err := time.Parse("20060102T150405Z", req.Header.Get("X-Amz-Date"))
		require.NoError(t, err)
		clone, err := http.NewRequest(http.MethodPut, rawURL, bytes.NewReader(body))
		require.NoError(t, err)
		clone.Header.Set("Content-Type", "application/json")
		signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = disableEscaping })
		require.NoError(t, signer.SignHTTP(context.Background(), testCreds, clone, payloadHash, service, "us-east-1", signedAt))
		return clone.Header.Get("Authorization")
	}

	s3Req := sign("s3")
	assert.Equal(t, expect(s3Req, "s3", true), s3Req.Header.Get("Authorization"))
	apiReq := sign("execute-api")
	assert.Equal(t, expect(apiReq, "execute-api", false), apiReq.Header.Get("Authorization"))
	assert.NotEqual(t, expect(s3Req, "s3", false), s3Req.Header.Get("Authorization"), "s3 must not escape the path a second time")
}

func signedHeaders(t *testing.T, auth string) string {
	t.Helper()
	_, signed, found := strings.Cut(auth, "SignedHeaders=")
	require.True(t, found, auth)
	signed, _, _ = strings.Cut(signed, ",")
	return signed
}
