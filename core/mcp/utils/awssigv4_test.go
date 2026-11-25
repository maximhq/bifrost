package utils

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/maximhq/bifrost/core/providers/utils/awsauth"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAccessKey = "AKIDSIGV4TEST"
	testSecretKey = "sigv4-test-secret"
)

// withEnvCredentials points the default AWS chain at static environment keys and
// nothing else.
func withEnvCredentials(t testing.TB, sessionToken string) {
	t.Helper()
	dir := t.TempDir()
	for k, v := range map[string]string{
		"AWS_ACCESS_KEY_ID":                  testAccessKey,
		"AWS_SECRET_ACCESS_KEY":              testSecretKey,
		"AWS_SESSION_TOKEN":                  sessionToken,
		"AWS_PROFILE":                        "",
		"AWS_ROLE_ARN":                       "",
		"AWS_WEB_IDENTITY_TOKEN_FILE":        "",
		"AWS_CONFIG_FILE":                    dir + "/config",
		"AWS_SHARED_CREDENTIALS_FILE":        dir + "/credentials",
		"AWS_EC2_METADATA_DISABLED":          "true",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI": "",
		// The SDK checks the relative URI before the full one; the rest are older names it still reads.
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN":      "",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": "",
		"AWS_ACCESS_KEY":                         "",
		"AWS_SECRET_KEY":                         "",
		"AWS_DEFAULT_PROFILE":                    "",
	} {
		t.Setenv(k, v)
	}
}

// signedRequest is what the SigV4-verifying server saw.
type signedRequest struct {
	method, accessKey, region, service, signedHeaders string
	body                                              []byte
	header                                            http.Header
}

// sigV4Server verifies every request's signature with the SDK signer and the known
// secret, answering 403 for one that does not verify, and records what it saw.
func sigV4Server(t *testing.T, secret string) (*httptest.Server, func() []signedRequest) {
	t.Helper()
	var seen atomic.Pointer[[]signedRequest]
	seen.Store(&[]signedRequest{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got, err := verifySigV4(r, body, secret)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		list := append(*seen.Load(), got)
		seen.Store(&list)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server, func() []signedRequest { return *seen.Load() }
}

// verifySigV4 recomputes the request's signature from its signed headers, body and
// X-Amz-Date, and compares it with the one it carries.
func verifySigV4(r *http.Request, body []byte, secret string) (signedRequest, error) {
	auth := r.Header.Get("Authorization")
	rest, ok := strings.CutPrefix(auth, "AWS4-HMAC-SHA256 Credential=")
	if !ok {
		return signedRequest{}, errors.New("no SigV4 Authorization header")
	}
	credential, rest, _ := strings.Cut(rest, ", SignedHeaders=")
	signedHeaders, _, _ := strings.Cut(rest, ", Signature=")
	scope := strings.Split(credential, "/")
	if len(scope) != 5 {
		return signedRequest{}, errors.New("malformed credential scope")
	}
	signedAt, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return signedRequest{}, err
	}
	clone, err := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		return signedRequest{}, err
	}
	names := strings.Split(signedHeaders, ";")
	for _, name := range names {
		if name != "host" && name != "content-length" {
			clone.Header[http.CanonicalHeaderKey(name)] = r.Header.Values(name)
		}
	}
	// AWS checks only the headers SignedHeaders names; the signer adds content-length
	// from ContentLength, so it is left out when the client did not sign it.
	if !slices.Contains(names, "content-length") {
		clone.ContentLength = 0
	}
	clone.Header.Del("Authorization")
	sum := sha256.Sum256(body)
	creds := aws.Credentials{AccessKeyID: scope[0], SecretAccessKey: secret, SessionToken: r.Header.Get("X-Amz-Security-Token")}
	if err := v4.NewSigner().SignHTTP(context.Background(), creds, clone, hex.EncodeToString(sum[:]), scope[3], scope[2], signedAt); err != nil {
		return signedRequest{}, err
	}
	if clone.Header.Get("Authorization") != auth {
		return signedRequest{}, errors.New("signature does not match")
	}
	return signedRequest{method: r.Method, accessKey: scope[0], region: scope[2], service: scope[3], signedHeaders: signedHeaders, body: body, header: r.Header.Clone()}, nil
}

// testRegions numbers the regions testRegion hands out.
var testRegions atomic.Int32

// testRegion returns a region no earlier test or run has used. awsauth caches providers per
// region for the life of the process, so a reused region would answer from a fake endpoint
// that has since closed (go test -count=2).
func testRegion(name string) string {
	return "us-sigv4-" + name + "-" + strconv.Itoa(int(testRegions.Add(1)))
}

func sigV4Client(region, service string) *http.Client {
	return NewAWSSigV4Client(&http.Client{}, &schemas.MCPAWSSigV4Config{Region: schemas.NewSecretVar(region), Service: service}, true)
}

// TestAWSSigV4Client_SignsEveryRequest: the POSTs, the GET listen stream and the
// session DELETE the MCP transport sends are each signed for the configured region
// and service, over the exact body that arrives.
func TestAWSSigV4Client_SignsEveryRequest(t *testing.T) {
	withEnvCredentials(t, "session-token")
	server, seen := sigV4Server(t, testSecretKey)
	region := testRegion("signs")
	client := sigV4Client(region, "")

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	for _, tc := range []struct {
		method string
		body   io.Reader
	}{
		{http.MethodPost, bytes.NewReader(body)},
		{http.MethodPost, io.NopCloser(bytes.NewReader(body))}, // no GetBody
		{http.MethodGet, nil},
		{http.MethodDelete, nil},
	} {
		req, err := http.NewRequest(tc.method, server.URL+"/mcp", tc.body)
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Mcp-Session-Id", "session-1")
		resp, err := client.Do(req)
		require.NoError(t, err)
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s was not signed correctly: %s", tc.method, msg)
	}

	got := seen()
	require.Len(t, got, 4)
	for i, r := range got {
		assert.Equal(t, testAccessKey, r.accessKey)
		assert.Equal(t, region, r.region)
		assert.Equal(t, schemas.DefaultAWSSigV4Service, r.service)
		assert.Contains(t, r.signedHeaders, "mcp-session-id")
		assert.Contains(t, r.signedHeaders, "x-amz-security-token")
		// Lambda function URLs refuse a POST or PUT without the body hash in this header.
		bodyHash := sha256.Sum256(r.body)
		assert.Equal(t, hex.EncodeToString(bodyHash[:]), r.header.Get("X-Amz-Content-Sha256"))
		assert.Contains(t, r.signedHeaders, "x-amz-content-sha256")
		if i < 2 {
			assert.Equal(t, body, r.body)
		} else {
			assert.Empty(t, r.body)
		}
	}
}

// TestAWSSigV4Client_RegionFromEnv: a region held in an env reference is the one the
// request is signed for.
func TestAWSSigV4Client_RegionFromEnv(t *testing.T) {
	withEnvCredentials(t, "")
	t.Setenv("SIGV4_CLIENT_REGION", "us-sigv4-env-1")
	server, seen := sigV4Server(t, testSecretKey)
	client := NewAWSSigV4Client(&http.Client{}, &schemas.MCPAWSSigV4Config{Region: schemas.NewSecretVar("env.SIGV4_CLIENT_REGION")}, true)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, seen(), 1)
	assert.Equal(t, "us-sigv4-env-1", seen()[0].region)
}

// TestAWSSigV4Client_ReplacesSignerHeaders: an Authorization or x-amz-* header already
// on the request never reaches the server; the signer's own replace them.
func TestAWSSigV4Client_ReplacesSignerHeaders(t *testing.T) {
	withEnvCredentials(t, "")
	server, seen := sigV4Server(t, testSecretKey)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer caller-token")
	req.Header.Set("X-Amz-Target", "secretsmanager.GetSecretValue")
	req.Header.Set("X-Amz-Date", "20000101T000000Z")
	req.Header.Set("X-Amz-Security-Token", "caller-session")

	resp, err := sigV4Client(testRegion("headers"), "lambda").Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	got := seen()
	require.Len(t, got, 1)
	assert.Empty(t, got[0].header.Get("X-Amz-Target"))
	assert.Empty(t, got[0].header.Get("X-Amz-Security-Token"), "no session token in the credentials, so none is sent")
	assert.NotEqual(t, "20000101T000000Z", got[0].header.Get("X-Amz-Date"))
	assert.Equal(t, "lambda", got[0].service)

	// The caller's request is left as it was.
	assert.Equal(t, "Bearer caller-token", req.Header.Get("Authorization"))
	assert.Equal(t, "secretsmanager.GetSecretValue", req.Header.Get("X-Amz-Target"))
}

// TestAWSSigV4Client_RefusesRedirects: a signature is bound to its host, so a redirect
// is reported rather than followed.
func TestAWSSigV4Client_RefusesRedirects(t *testing.T) {
	withEnvCredentials(t, "")
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Store(true) }))
	t.Cleanup(target.Close)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirecting.Close)

	req, err := http.NewRequest(http.MethodPost, redirecting.URL+"/mcp", strings.NewReader(`{}`))
	require.NoError(t, err)
	_, err = sigV4Client(testRegion("redirect"), "").Do(req)
	require.ErrorContains(t, err, "aws_sigv4: not following the redirect")
	assert.False(t, followed.Load())
}

// idleCloser records CloseIdleConnections, so a test can see the signer pass it on.
type idleCloser struct {
	http.RoundTripper
	closed atomic.Int32
}

func (c *idleCloser) CloseIdleConnections() { c.closed.Add(1) }

// TestAWSSigV4Client_ClosesIdleConnections: closing a signed client's idle connections
// reaches the transport it wraps, so a dropped or replaced client frees its sockets.
func TestAWSSigV4Client_ClosesIdleConnections(t *testing.T) {
	inner := &idleCloser{RoundTripper: http.DefaultTransport}
	cfg := &schemas.MCPAWSSigV4Config{Region: schemas.NewSecretVar("us-east-1")}
	NewAWSSigV4Client(&http.Client{Transport: inner}, cfg, true).CloseIdleConnections()
	assert.Equal(t, int32(1), inner.closed.Load())
}

// TestAWSSigV4Client_CredentialFailure: with no AWS identity the request is not sent,
// and the error says why.
func TestAWSSigV4Client_CredentialFailure(t *testing.T) {
	withEnvCredentials(t, "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", failing.URL)
	var reached atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	t.Cleanup(server.Close)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{}`))
	require.NoError(t, err)
	region := testRegion("failure")
	client := sigV4Client(region, "")
	_, err = client.Do(req)
	require.ErrorIs(t, err, ErrAWSSigV4Credentials)
	assert.ErrorContains(t, err, "region "+region)
	assert.False(t, reached.Load())

	// The same client picks up credentials that appear afterwards, without a restart.
	body := "[default]\naws_access_key_id = " + testAccessKey + "\naws_secret_access_key = " + testSecretKey + "\n"
	require.NoError(t, os.WriteFile(os.Getenv("AWS_SHARED_CREDENTIALS_FILE"), []byte(body), 0o600))
	req, err = http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.True(t, reached.Load())
}

// TestAWSSigV4Client_CachesAndInvalidates: the credentials are fetched once for many
// requests, and again after awsauth.Invalidate (what the credential store's
// ForceRefresh calls when the server rejects a signature).
func TestAWSSigV4Client_CachesAndInvalidates(t *testing.T) {
	withEnvCredentials(t, "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	var fetches atomic.Int32
	container := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		_, _ = w.Write([]byte(`{"AccessKeyId":"` + testAccessKey + `","SecretAccessKey":"` + testSecretKey + `","Token":"pod","Expiration":"` + expiry + `"}`))
	}))
	t.Cleanup(container.Close)
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", container.URL)
	server, seen := sigV4Server(t, testSecretKey)
	cfg := &schemas.MCPAWSSigV4Config{Region: schemas.NewSecretVar(testRegion("cache"))}
	client := NewAWSSigV4Client(&http.Client{}, cfg, true)

	send := func() {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{}`))
		require.NoError(t, err)
		resp, err := client.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}
	for range 3 {
		send()
	}
	assert.Equal(t, int32(1), fetches.Load())

	awsauth.Invalidate(AWSSigV4Credentials(cfg), cfg.Region.GetValue())
	send()
	assert.Equal(t, int32(2), fetches.Load())
	assert.Len(t, seen(), 4)
}

// TestAWSSigV4Client_AssumesRole: with role_arn the request is signed with the assumed
// role's credentials, and the external ID and session name reach STS.
func TestAWSSigV4Client_AssumesRole(t *testing.T) {
	withEnvCredentials(t, "")
	var form atomic.Value
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form.Store(r.PostForm.Encode())
		expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>ASIAASSUMED</AccessKeyId><SecretAccessKey>assumed-secret</SecretAccessKey><SessionToken>assumed-token</SessionToken><Expiration>` + expiry + `</Expiration></Credentials><AssumedRoleUser><Arn>arn:aws:sts::123456789012:assumed-role/gateway/mcp</Arn><AssumedRoleId>AROA:mcp</AssumedRoleId></AssumedRoleUser></AssumeRoleResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></AssumeRoleResponse>`))
	}))
	t.Cleanup(sts.Close)
	t.Setenv("AWS_ENDPOINT_URL_STS", sts.URL)
	server, seen := sigV4Server(t, "assumed-secret")

	client := NewAWSSigV4Client(&http.Client{}, &schemas.MCPAWSSigV4Config{
		Region:      schemas.NewSecretVar(testRegion("role")),
		RoleARN:     schemas.NewSecretVar("arn:aws:iam::123456789012:role/gateway"),
		ExternalID:  schemas.NewSecretVar("ext-42"),
		SessionName: schemas.NewSecretVar("mcp"),
	}, true)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	got := seen()
	require.Len(t, got, 1)
	assert.Equal(t, "ASIAASSUMED", got[0].accessKey)
	assert.Equal(t, "assumed-token", got[0].header.Get("X-Amz-Security-Token"))
	sent, _ := form.Load().(string)
	assert.Contains(t, sent, "ExternalId=ext-42")
	assert.Contains(t, sent, "RoleSessionName=mcp")
}

// TestAWSSigV4Client_RefusalIsATypedError: on an MCP connection a 401 or 403 becomes
// an error carrying the server's reason (with the status text isAuthFailure reads);
// on an OpenAPI upstream it is the API's answer and passes through.
func TestAWSSigV4Client_RefusalIsATypedError(t *testing.T) {
	withEnvCredentials(t, "")
	const reason = "User: arn:aws:sts::123456789012:assumed-role/pod/x is not authorized to perform: bedrock-agentcore:InvokeGateway"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, _ := strconv.Atoi(r.URL.Query().Get("status"))
		http.Error(w, reason, status)
	}))
	t.Cleanup(server.Close)
	cfg := &schemas.MCPAWSSigV4Config{Region: schemas.NewSecretVar(testRegion("refusal"))}

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp?status="+strconv.Itoa(status), strings.NewReader(`{}`))
		require.NoError(t, err)
		_, err = NewAWSSigV4Client(&http.Client{}, cfg, true).Do(req)
		var rejected *AWSSigV4RejectedError
		require.ErrorAs(t, err, &rejected)
		assert.Equal(t, status, rejected.StatusCode)
		assert.Equal(t, reason, rejected.Message)
		assert.Contains(t, err.Error(), "HTTP "+strconv.Itoa(status))
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/pets?status=403", nil)
	require.NoError(t, err)
	resp, err := NewAWSSigV4Client(&http.Client{}, cfg, false).Do(req)
	require.NoError(t, err, "an OpenAPI upstream's 403 is the API's answer")
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	req, err = http.NewRequest(http.MethodGet, server.URL+"/mcp?status=404", nil)
	require.NoError(t, err)
	resp, err = NewAWSSigV4Client(&http.Client{}, cfg, true).Do(req)
	require.NoError(t, err, "other statuses are left to the MCP transport")
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestAWSSigV4Credentials(t *testing.T) {
	assert.Equal(t, awsauth.Config{}, AWSSigV4Credentials(nil))
	assert.Equal(t, awsauth.Config{RoleARN: "arn:aws:iam::123456789012:role/r", ExternalID: "ext", SessionName: "s"}, AWSSigV4Credentials(&schemas.MCPAWSSigV4Config{
		Region:      schemas.NewSecretVar("us-east-1"),
		RoleARN:     schemas.NewSecretVar(" arn:aws:iam::123456789012:role/r "),
		ExternalID:  schemas.NewSecretVar("ext"),
		SessionName: schemas.NewSecretVar(" s "),
	}))
}

// nopTransport answers every request without network.
type nopTransport struct{}

func (nopTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
}

// BenchmarkAWSSigV4Transport is the per-request cost an aws_sigv4 client adds: clone,
// body hash, a cached credential lookup and the signature.
func BenchmarkAWSSigV4Transport(b *testing.B) {
	withEnvCredentials(b, "")
	client := NewAWSSigV4Client(&http.Client{Transport: nopTransport{}}, &schemas.MCPAWSSigV4Config{Region: schemas.NewSecretVar("us-sigv4-bench-1")}, true)
	body := []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"web_search","arguments":{"query":"bifrost"}}}`)
	b.ReportAllocs()
	for b.Loop() {
		req, _ := http.NewRequest(http.MethodPost, "https://gw.gateway.bedrock-agentcore.us-east-1.amazonaws.com/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}
