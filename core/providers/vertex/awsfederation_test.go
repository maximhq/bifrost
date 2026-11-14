package vertex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2/google/externalaccount"
)

// externalaccountSupplierOptions mirrors what the Google library passes to SubjectToken.
func externalaccountSupplierOptions() externalaccount.SupplierOptions {
	return externalaccount.SupplierOptions{Audience: testWIFAudience, SubjectTokenType: awsSubjectTokenType}
}

// These tests exercise the AWS→GCP federation path end to end against in-process fakes of AWS STS,
// GCP STS and the IAM Credentials API. None of them may run in parallel: they set process env vars
// and swap the package-level GCP endpoint variables.

const (
	testWIFAudience = "//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/eks-pool/providers/aws"
	testWIFSAEmail  = "vertex-caller@my-project.iam.gserviceaccount.com"
)

// isolateAWSEnvironment clears every ambient AWS credential/region source so the SDK default chain
// sees only what the test sets. Shared config files are pointed at paths that do not exist.
func isolateAWSEnvironment(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_ROLE_SESSION_NAME",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_STS",
	} {
		t.Setenv(name, "")
		os.Unsetenv(name) // t.Setenv restores the original at cleanup; an unset var must not read as "".
	}
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "missing-config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "missing-credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}

// decodeSubjectToken reverses the URL-encoded JSON envelope GCP STS receives.
func decodeSubjectToken(t *testing.T, token string) awsSignedRequest {
	t.Helper()
	raw, err := url.QueryUnescape(token)
	require.NoError(t, err)
	var signed awsSignedRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &signed))
	return signed
}

func headerValue(signed awsSignedRequest, name string) (string, bool) {
	for _, h := range signed.Headers {
		if strings.EqualFold(h.Key, name) {
			return h.Value, true
		}
	}
	return "", false
}

// fakeAWSSTS answers AssumeRole and AssumeRoleWithWebIdentity with fixed credentials and records
// the last form it saw so tests can assert on the role and token.
// The handlers run on the server's goroutines, so recorded fields are guarded by a mutex and read
// through getters, and failures are reported with assert (FailNow is only valid on the test goroutine).
type fakeAWSSTS struct {
	server    *httptest.Server
	calls     atomic.Int32
	accessKey string

	mu       sync.Mutex
	lastForm url.Values
	lastAuth string
}

func (f *fakeAWSSTS) form() url.Values { f.mu.Lock(); defer f.mu.Unlock(); return f.lastForm }
func (f *fakeAWSSTS) auth() string     { f.mu.Lock(); defer f.mu.Unlock(); return f.lastAuth }

func newFakeAWSSTS(t *testing.T, accessKey string) *fakeAWSSTS {
	t.Helper()
	f := &fakeAWSSTS{accessKey: accessKey}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.calls.Add(1)
		f.mu.Lock()
		f.lastForm = r.PostForm
		f.lastAuth = r.Header.Get("Authorization")
		f.mu.Unlock()
		action := r.PostForm.Get("Action")
		result := action + "Result"
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w, `<%[1]sResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <%[2]s>
    <Credentials>
      <AccessKeyId>%[3]s</AccessKeyId>
      <SecretAccessKey>fake-secret</SecretAccessKey>
      <SessionToken>fake-session</SessionToken>
      <Expiration>%[4]s</Expiration>
    </Credentials>
    <AssumedRoleUser>
      <Arn>arn:aws:sts::123456789012:assumed-role/BifrostVertex/session</Arn>
      <AssumedRoleId>AROAFAKE:session</AssumedRoleId>
    </AssumedRoleUser>
  </%[2]s>
  <ResponseMetadata><RequestId>fake-request</RequestId></ResponseMetadata>
</%[1]sResponse>`, action, result, accessKey, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	t.Cleanup(f.server.Close)
	t.Setenv("AWS_ENDPOINT_URL_STS", f.server.URL)
	return f
}

// fakeGCP stands in for GCP STS (/v1/token) and IAM Credentials (:generateAccessToken).
type fakeGCP struct {
	server   *httptest.Server
	stsCalls atomic.Int32
	iamCalls atomic.Int32

	mu                   sync.Mutex
	lastSubject          awsSignedRequest
	lastRawSubject       string
	lastSubjectTokenType string
	lastAudience         string
	lastIAMAuth          string
}

func (f *fakeGCP) subject() awsSignedRequest { f.mu.Lock(); defer f.mu.Unlock(); return f.lastSubject }
func (f *fakeGCP) audience() string          { f.mu.Lock(); defer f.mu.Unlock(); return f.lastAudience }
func (f *fakeGCP) iamAuth() string           { f.mu.Lock(); defer f.mu.Unlock(); return f.lastIAMAuth }
func (f *fakeGCP) rawSubject() string        { f.mu.Lock(); defer f.mu.Unlock(); return f.lastRawSubject }
func (f *fakeGCP) subjectTokenType() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastSubjectTokenType
}

func newFakeGCP(t *testing.T) *fakeGCP {
	t.Helper()
	f := &fakeGCP{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/token":
			if err := r.ParseForm(); !assert.NoError(t, err) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.stsCalls.Add(1)
			assert.Equal(t, "urn:ietf:params:oauth:grant-type:token-exchange", r.PostForm.Get("grant_type"))
			rawSubject := r.PostForm.Get("subject_token")
			var subject awsSignedRequest
			if raw, err := url.QueryUnescape(rawSubject); err == nil {
				_ = json.Unmarshal([]byte(raw), &subject) // non-AWS subject tokens are opaque and simply do not decode
			}
			f.mu.Lock()
			f.lastAudience = r.PostForm.Get("audience")
			f.lastSubjectTokenType = r.PostForm.Get("subject_token_type")
			f.lastRawSubject = rawSubject
			f.lastSubject = subject
			f.mu.Unlock()
			fmt.Fprint(w, `{"access_token":"federated-token","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`)
		case strings.HasSuffix(r.URL.Path, ":generateAccessToken"):
			f.iamCalls.Add(1)
			f.mu.Lock()
			f.lastIAMAuth = r.Header.Get("Authorization")
			f.mu.Unlock()
			assert.Equal(t, "/v1/projects/-/serviceAccounts/"+testWIFSAEmail+":generateAccessToken", r.URL.Path)
			fmt.Fprintf(w, `{"accessToken":"sa-token","expireTime":"%s"}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(f.server.Close)

	prevSTS, prevIAM := gcpSTSTokenURL, gcpIAMCredentialsBaseURL
	gcpSTSTokenURL = f.server.URL + "/v1/token"
	gcpIAMCredentialsBaseURL = f.server.URL
	t.Cleanup(func() {
		gcpSTSTokenURL, gcpIAMCredentialsBaseURL = prevSTS, prevIAM
	})
	return f
}

func wifConfig(audience string, saEmail string) *schemas.VertexAWSWorkloadIdentityConfig {
	cfg := &schemas.VertexAWSWorkloadIdentityConfig{Audience: *schemas.NewSecretVar(audience)}
	if saEmail != "" {
		cfg.ServiceAccountEmail = schemas.NewSecretVar(saEmail)
	}
	return cfg
}

func wifKey(cfg *schemas.VertexAWSWorkloadIdentityConfig, authCredentials string) schemas.Key {
	return schemas.Key{
		ID: "wif-key",
		VertexKeyConfig: &schemas.VertexKeyConfig{
			ProjectID:           *schemas.NewSecretVar("my-project"),
			Region:              *schemas.NewSecretVar("us-central1"),
			AuthCredentials:     *schemas.NewSecretVar(authCredentials),
			AWSWorkloadIdentity: cfg,
		},
	}
}

func TestAWSSTSHost(t *testing.T) {
	assert.Equal(t, "sts.us-east-1.amazonaws.com", awsSTSHost("us-east-1"))
	assert.Equal(t, "sts.us-gov-west-1.amazonaws.com", awsSTSHost("us-gov-west-1"))
	assert.Equal(t, "sts.cn-north-1.amazonaws.com.cn", awsSTSHost("cn-north-1"))
}

func TestAWSSubjectTokenSupplier_Signature(t *testing.T) {
	creds := credentials.NewStaticCredentialsProvider("AKIATEST", "secret", "session-token")
	s := newAWSSubjectTokenSupplierWithCredentials(testWIFAudience, creds, "us-gov-west-1")
	s.now = func() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }

	token, err := s.SubjectToken(context.Background(), externalaccountSupplierOptions())
	require.NoError(t, err)
	signed := decodeSubjectToken(t, token)

	assert.Equal(t, http.MethodPost, signed.Method)
	assert.Equal(t, "https://sts.us-gov-west-1.amazonaws.com?Action=GetCallerIdentity&Version=2011-06-15", signed.URL)

	host, ok := headerValue(signed, "host")
	require.True(t, ok, "host header must be replayable by GCP")
	assert.Equal(t, "sts.us-gov-west-1.amazonaws.com", host)

	auth, ok := headerValue(signed, "Authorization")
	require.True(t, ok)
	assert.Contains(t, auth, "AWS4-HMAC-SHA256 Credential=AKIATEST/20260929/us-gov-west-1/sts/aws4_request")
	assert.Contains(t, auth, "x-goog-cloud-target-resource", "target resource header must be part of the signature")

	date, ok := headerValue(signed, "X-Amz-Date")
	require.True(t, ok)
	assert.Equal(t, "20260929T120000Z", date)

	target, ok := headerValue(signed, gcpTargetResourceHeader)
	require.True(t, ok)
	assert.Equal(t, testWIFAudience, target)

	sessionToken, ok := headerValue(signed, "X-Amz-Security-Token")
	require.True(t, ok)
	assert.Equal(t, "session-token", sessionToken)

	// Without a session token the header must be absent, otherwise AWS rejects the replay.
	s = newAWSSubjectTokenSupplierWithCredentials(testWIFAudience, credentials.NewStaticCredentialsProvider("AKIATEST", "secret", ""), "us-east-1")
	token, err = s.SubjectToken(context.Background(), externalaccountSupplierOptions())
	require.NoError(t, err)
	_, ok = headerValue(decodeSubjectToken(t, token), "X-Amz-Security-Token")
	assert.False(t, ok)
}

func TestAWSSubjectTokenSupplier_RegionResolution(t *testing.T) {
	creds := credentials.NewStaticCredentialsProvider("AKIATEST", "secret", "")

	t.Run("explicit aws_region wins over the sdk region", func(t *testing.T) {
		s := newAWSSubjectTokenSupplierWithCredentials(testWIFAudience, creds, "eu-west-1")
		s.sdkRegion = "us-east-1"
		region, err := s.resolveRegion(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "eu-west-1", region)
	})

	t.Run("sdk region is used when nothing is configured", func(t *testing.T) {
		s := newAWSSubjectTokenSupplierWithCredentials(testWIFAudience, creds, "")
		s.sdkRegion = "ap-south-1"
		s.imdsRegion = func(context.Context) (string, error) { t.Fatal("imds must not be consulted"); return "", nil }
		region, err := s.resolveRegion(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "ap-south-1", region)
	})

	t.Run("instance metadata is the last resort and is cached", func(t *testing.T) {
		s := newAWSSubjectTokenSupplierWithCredentials(testWIFAudience, creds, "")
		var calls int
		s.imdsRegion = func(context.Context) (string, error) { calls++; return "us-west-2", nil }
		for i := 0; i < 2; i++ {
			region, err := s.resolveRegion(context.Background())
			require.NoError(t, err)
			assert.Equal(t, "us-west-2", region)
		}
		assert.Equal(t, 1, calls)
	})

	t.Run("unresolvable region names both escape hatches", func(t *testing.T) {
		s := newAWSSubjectTokenSupplierWithCredentials(testWIFAudience, creds, "")
		s.imdsRegion = func(context.Context) (string, error) { return "", errors.New("imds disabled") }
		_, err := s.SubjectToken(context.Background(), externalaccountSupplierOptions())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "aws_region")
		assert.Contains(t, err.Error(), "AWS_REGION")
	})
}

// TestAWSFederatedTokenSource_IRSAChain is the regression pin for the EKS gap: with only the IRSA
// environment (web identity token file + role ARN) and no static keys, the federation must still
// obtain AWS credentials, exchange them at GCP STS, and impersonate the service account.
func TestAWSFederatedTokenSource_IRSAChain(t *testing.T) {
	isolateAWSEnvironment(t)
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("fake-oidc-token"), 0o600))
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", tokenFile)
	t.Setenv("AWS_ROLE_ARN", "arn:aws:iam::123456789012:role/BifrostVertex")
	t.Setenv("AWS_REGION", "us-east-1")
	awsSTS := newFakeAWSSTS(t, "ASIAIRSA")
	gcp := newFakeGCP(t)

	ts, err := newAWSFederatedTokenSource(context.Background(), wifConfig(testWIFAudience, testWIFSAEmail))
	require.NoError(t, err)

	token, err := ts.Token()
	require.NoError(t, err)
	assert.Equal(t, "sa-token", token.AccessToken)

	assert.Equal(t, "AssumeRoleWithWebIdentity", awsSTS.form().Get("Action"))
	assert.Equal(t, "fake-oidc-token", awsSTS.form().Get("WebIdentityToken"))
	assert.Equal(t, "arn:aws:iam::123456789012:role/BifrostVertex", awsSTS.form().Get("RoleArn"))

	assert.Equal(t, testWIFAudience, gcp.audience())
	assert.Equal(t, awsSubjectTokenType, gcp.subjectTokenType())
	auth, ok := headerValue(gcp.subject(), "Authorization")
	require.True(t, ok)
	assert.Contains(t, auth, "Credential=ASIAIRSA/", "the signature must come from the IRSA-assumed role, not a static key")
	assert.Equal(t, "https://sts.us-east-1.amazonaws.com?Action=GetCallerIdentity&Version=2011-06-15", gcp.subject().URL)
	assert.Equal(t, "Bearer federated-token", gcp.iamAuth())

	// The token source caches the impersonated token; a second call must not hit any server.
	stsCalls, iamCalls, awsCalls := gcp.stsCalls.Load(), gcp.iamCalls.Load(), awsSTS.calls.Load()
	token, err = ts.Token()
	require.NoError(t, err)
	assert.Equal(t, "sa-token", token.AccessToken)
	assert.Equal(t, stsCalls, gcp.stsCalls.Load())
	assert.Equal(t, iamCalls, gcp.iamCalls.Load())
	assert.Equal(t, awsCalls, awsSTS.calls.Load())
}

// TestAWSFederatedTokenSource_DirectAccess covers the no-impersonation shape: the federated token
// itself is the access token and IAM Credentials is never called.
func TestAWSFederatedTokenSource_DirectAccess(t *testing.T) {
	isolateAWSEnvironment(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIASTATIC")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_REGION", "us-east-1")
	gcp := newFakeGCP(t)

	ts, err := newAWSFederatedTokenSource(context.Background(), wifConfig(testWIFAudience, ""))
	require.NoError(t, err)
	token, err := ts.Token()
	require.NoError(t, err)
	assert.Equal(t, "federated-token", token.AccessToken)
	assert.Equal(t, int32(0), gcp.iamCalls.Load())
}

func TestAWSSubjectTokenSupplier_AssumeRoleHop(t *testing.T) {
	isolateAWSEnvironment(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIABASE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_REGION", "us-east-1")
	awsSTS := newFakeAWSSTS(t, "ASIAHOP")

	cfg := wifConfig(testWIFAudience, "")
	cfg.AWSRoleARN = schemas.NewSecretVar("arn:aws:iam::123456789012:role/VertexHop")
	s, err := newAWSSubjectTokenSupplier(context.Background(), cfg)
	require.NoError(t, err)

	token, err := s.SubjectToken(context.Background(), externalaccountSupplierOptions())
	require.NoError(t, err)
	auth, ok := headerValue(decodeSubjectToken(t, token), "Authorization")
	require.True(t, ok)
	assert.Contains(t, auth, "Credential=ASIAHOP/", "the signature must use the assumed role's credentials")
	assert.Equal(t, "AssumeRole", awsSTS.form().Get("Action"))
	assert.Equal(t, "arn:aws:iam::123456789012:role/VertexHop", awsSTS.form().Get("RoleArn"))
	assert.Equal(t, awsFederationSessionName, awsSTS.form().Get("RoleSessionName"))
}

func TestGetAuthTokenSource_AWSWorkloadIdentityPrecedenceAndCache(t *testing.T) {
	provider := &VertexProvider{}
	isolateAWSEnvironment(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIASTATIC")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_REGION", "us-east-1")
	newFakeGCP(t)

	// auth_credentials holds JSON that would fail to parse as Google credentials; federation must win.
	key := wifKey(wifConfig(testWIFAudience, ""), `{"type":"service_account"}`)
	other := wifKey(wifConfig(testWIFAudience+"-other", ""), "")
	t.Cleanup(func() {
		provider.removeVertexClient(key)
		provider.removeVertexClient(other)
	})

	first, err := provider.getAuthTokenSource(key)
	require.NoError(t, err)
	token, err := first.Token()
	require.NoError(t, err)
	assert.Equal(t, "federated-token", token.AccessToken)

	second, err := provider.getAuthTokenSource(key)
	require.NoError(t, err)
	assert.Same(t, first, second, "identical federation config must share one cached token source")

	otherSource, err := provider.getAuthTokenSource(other)
	require.NoError(t, err)
	assert.NotSame(t, first, otherSource, "a different audience must get its own token source")

	provider.removeVertexClient(key)
	third, err := provider.getAuthTokenSource(key)
	require.NoError(t, err)
	assert.NotSame(t, first, third, "eviction must force a fresh token source")
}

func TestVertexCredentialIdentity(t *testing.T) {
	assert.Equal(t, "", vertexCredentialIdentity(schemas.Key{}), "no config means default credentials")
	assert.Equal(t, `{"type":"service_account"}`, vertexCredentialIdentity(wifKey(nil, `{"type":"service_account"}`)))

	cfg := wifConfig(testWIFAudience, testWIFSAEmail)
	cfg.AWSRegion = schemas.NewSecretVar("eu-west-1")
	identity := vertexCredentialIdentity(wifKey(cfg, `{"type":"service_account"}`))
	assert.True(t, strings.HasPrefix(identity, "aws-wif|"+testWIFAudience+"|"+testWIFSAEmail+"|eu-west-1|"), identity)

	// An audience-less config is ignored so an empty nested object cannot switch modes by accident.
	assert.Equal(t, "", vertexCredentialIdentity(wifKey(&schemas.VertexAWSWorkloadIdentityConfig{}, "")))
}

func TestVertexAWSWorkloadIdentityConfig_Redacted(t *testing.T) {
	var nilCfg *schemas.VertexAWSWorkloadIdentityConfig
	assert.Nil(t, nilCfg.Redacted())

	cfg := wifConfig(testWIFAudience, testWIFSAEmail)
	cfg.AWSRegion = schemas.NewSecretVar("us-east-1")
	cfg.AWSRoleARN = schemas.NewSecretVar("arn:aws:iam::123456789012:role/VertexHop")
	cfg.TokenLifetimeSeconds = 1800

	out := cfg.Redacted()
	assert.Equal(t, testWIFAudience, out.Audience.GetValue(), "identifiers stay readable")
	assert.Equal(t, testWIFSAEmail, out.ServiceAccountEmail.GetValue())
	assert.Equal(t, "us-east-1", out.AWSRegion.GetValue())
	assert.Equal(t, 1800, out.TokenLifetimeSeconds)
	assert.True(t, out.AWSRoleARN.IsRedacted(), "role ARN is masked like Bedrock's role_arn")
	assert.Equal(t, "arn:aws:iam::123456789012:role/VertexHop", cfg.AWSRoleARN.GetValue(), "the original is untouched")
}

// failingCredentials lets the credential-error path be asserted without a network.
type failingCredentials struct{}

func (failingCredentials) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{}, errors.New("no identity available")
}

func TestAWSSubjectTokenSupplier_CredentialError(t *testing.T) {
	s := newAWSSubjectTokenSupplierWithCredentials(testWIFAudience, failingCredentials{}, "us-east-1")
	_, err := s.SubjectToken(context.Background(), externalaccountSupplierOptions())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aws credentials")
	assert.Contains(t, err.Error(), "no identity available")
}

// awsExternalAccountJSON returns the JSON gcloud emits for `create-cred-config --aws`, pointed at
// the test fakes. The credential_source URLs target a dead IMDS endpoint so that, if the JSON were
// handed to Google's library, it would fail: the tests below prove Bifrost routes it elsewhere.
func awsExternalAccountJSON(t *testing.T, gcp *fakeGCP, saEmail string) string {
	t.Helper()
	deadIMDS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(deadIMDS.Close)
	impersonation := ""
	if saEmail != "" {
		impersonation = fmt.Sprintf(`"service_account_impersonation_url": %q, "service_account_impersonation": {"token_lifetime_seconds": 1800},`,
			gcp.server.URL+"/v1/projects/-/serviceAccounts/"+saEmail+":generateAccessToken")
	}
	return fmt.Sprintf(`{
		"type": "external_account",
		"audience": %q,
		"subject_token_type": %q,
		"token_url": %q,
		%s
		"credential_source": {
			"environment_id": "aws1",
			"region_url": %q,
			"url": %q,
			"regional_cred_verification_url": "https://sts.{region}.amazonaws.com?Action=GetCallerIdentity&Version=2011-06-15"
		}
	}`, testWIFAudience, awsSubjectTokenType, gcp.server.URL+"/v1/token", impersonation,
		deadIMDS.URL+"/latest/meta-data/placement/availability-zone", deadIMDS.URL+"/latest/meta-data/iam/security-credentials")
}

func setIRSAEnvironment(t *testing.T) *fakeAWSSTS {
	t.Helper()
	isolateAWSEnvironment(t)
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("fake-oidc-token"), 0o600))
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", tokenFile)
	t.Setenv("AWS_ROLE_ARN", "arn:aws:iam::123456789012:role/BifrostVertex")
	t.Setenv("AWS_REGION", "us-east-1")
	return newFakeAWSSTS(t, "ASIAIRSA")
}

func TestIsAWSExternalAccountJSON(t *testing.T) {
	cases := map[string]bool{
		`{"type":"service_account"}`: false,
		`{"type":"external_account","credential_source":{"file":"/var/run/token"}}`:                 false,
		`{"type":"external_account","credential_source":{"environment_id":"aws1"}}`:                 true,
		`{"type":"external_account","credential_source":{"environment_id":"aws2"}}`:                 true,
		`{"type":"external_account_authorized_user","credential_source":{"environment_id":"aws1"}}`: false,
		`not json`: false,
	}
	for input, want := range cases {
		assert.Equal(t, want, isAWSExternalAccountJSON([]byte(input)), input)
	}
}

// TestGetAuthTokenSource_AWSExternalAccountJSONUsesSDKChain pins the JSON route: the unchanged
// gcloud-generated aws1 credential config, pasted into auth_credentials, now authenticates through
// the SDK chain (IRSA here) instead of Google's env-var/IMDS lookup, which the dead IMDS in the
// JSON would have made fail.
func TestGetAuthTokenSource_AWSExternalAccountJSONUsesSDKChain(t *testing.T) {
	provider := &VertexProvider{}
	awsSTS := setIRSAEnvironment(t)
	gcp := newFakeGCP(t)
	key := wifKey(nil, awsExternalAccountJSON(t, gcp, testWIFSAEmail))
	t.Cleanup(func() { provider.removeVertexClient(key) })

	ts, err := provider.getAuthTokenSource(key)
	require.NoError(t, err)
	token, err := ts.Token()
	require.NoError(t, err)
	assert.Equal(t, "sa-token", token.AccessToken)
	assert.Equal(t, "AssumeRoleWithWebIdentity", awsSTS.form().Get("Action"))
	auth, ok := headerValue(gcp.subject(), "Authorization")
	require.True(t, ok)
	assert.Contains(t, auth, "Credential=ASIAIRSA/")
	assert.Equal(t, "Bearer federated-token", gcp.iamAuth())

	// aws2 is rejected explicitly instead of being misread as aws1.
	bad := wifKey(nil, strings.Replace(awsExternalAccountJSON(t, gcp, ""), `"aws1"`, `"aws2"`, 1))
	t.Cleanup(func() { provider.removeVertexClient(bad) })
	_, err = provider.getAuthTokenSource(bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aws2")
}

// TestGetAuthTokenSource_ADCPointsAtAWSExternalAccount covers the other way the gcloud file is
// deployed: GOOGLE_APPLICATION_CREDENTIALS naming it, with auth_credentials left empty.
func TestGetAuthTokenSource_ADCPointsAtAWSExternalAccount(t *testing.T) {
	provider := &VertexProvider{}
	setIRSAEnvironment(t)
	gcp := newFakeGCP(t)
	credFile := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(credFile, []byte(awsExternalAccountJSON(t, gcp, "")), 0o600))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credFile)

	key := wifKey(nil, "")
	t.Cleanup(func() { provider.removeVertexClient(key) })
	ts, err := provider.getAuthTokenSource(key)
	require.NoError(t, err)
	token, err := ts.Token()
	require.NoError(t, err)
	assert.Equal(t, "federated-token", token.AccessToken)
	assert.Equal(t, testWIFAudience, gcp.audience())
}

// TestGetAuthTokenSource_FileExternalAccountUnchanged is the regression guard for every other
// external_account shape: a projected-token file source still goes through Google's library.
func TestGetAuthTokenSource_FileExternalAccountUnchanged(t *testing.T) {
	provider := &VertexProvider{}
	isolateAWSEnvironment(t)
	gcp := newFakeGCP(t)
	tokenFile := filepath.Join(t.TempDir(), "gcp-token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("projected-k8s-jwt"), 0o600))
	credJSON := fmt.Sprintf(`{
		"type": "external_account",
		"audience": %q,
		"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
		"token_url": %q,
		"credential_source": {"file": %q}
	}`, testWIFAudience, gcp.server.URL+"/v1/token", tokenFile)

	key := wifKey(nil, credJSON)
	t.Cleanup(func() { provider.removeVertexClient(key) })
	ts, err := provider.getAuthTokenSource(key)
	require.NoError(t, err)
	token, err := ts.Token()
	require.NoError(t, err)
	assert.Equal(t, "federated-token", token.AccessToken)
	assert.Equal(t, "projected-k8s-jwt", gcp.rawSubject(), "the file contents are the subject token, untouched by Bifrost")
	assert.Equal(t, "urn:ietf:params:oauth:token-type:jwt", gcp.subjectTokenType())
}

// TestNewAWSFederatedTokenSourceFromJSON_LifetimeParsing pins strict parsing of
// service_account_impersonation.token_lifetime_seconds in the aws1 JSON. gjson's Int() silently
// wraps or truncates malformed numbers (18446744073709555216 becomes 3600), so a corrupt credential
// file must be rejected at configuration time instead of quietly requesting a different lifetime.
func TestNewAWSFederatedTokenSourceFromJSON_LifetimeParsing(t *testing.T) {
	isolateAWSEnvironment(t)
	t.Setenv("AWS_REGION", "us-east-1")
	build := func(lifetime string) string {
		return fmt.Sprintf(`{
			"type": "external_account",
			"audience": %q,
			"subject_token_type": %q,
			"token_url": "https://sts.googleapis.com/v1/token",
			"service_account_impersonation_url": "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/%s:generateAccessToken",
			"service_account_impersonation": {"token_lifetime_seconds": %s},
			"credential_source": {"environment_id": "aws1"}
		}`, testWIFAudience, awsSubjectTokenType, testWIFSAEmail, lifetime)
	}
	for _, lifetime := range []string{"18446744073709555216", "9223372036854779408", "1800.7", `"1800"`, "true"} {
		_, err := newAWSFederatedTokenSourceFromJSON(context.Background(), []byte(build(lifetime)))
		require.Error(t, err, "lifetime %s must be rejected", lifetime)
		assert.Contains(t, err.Error(), "token_lifetime_seconds", lifetime)
	}
	for _, lifetime := range []string{"1800", "43200", "0"} {
		_, err := newAWSFederatedTokenSourceFromJSON(context.Background(), []byte(build(lifetime)))
		require.NoError(t, err, "lifetime %s is a valid integer", lifetime)
	}
	// Absent lifetime keeps the library default.
	absent := strings.Replace(build("1800"), `"service_account_impersonation": {"token_lifetime_seconds": 1800},`, "", 1)
	_, err := newAWSFederatedTokenSourceFromJSON(context.Background(), []byte(absent))
	require.NoError(t, err)
}

// TestAWSSubjectTokenSupplier_AssumeRoleHopUsesConfiguredRegion covers a pod with aws_region and
// aws_role_arn configured but no ambient region (no AWS_REGION, IMDS blocked). The configured region
// must reach the SDK clients that perform the AssumeRole hop, not only the final SigV4 signature,
// or STS fails with "missing region" before the federation request is ever signed.
func TestAWSSubjectTokenSupplier_AssumeRoleHopUsesConfiguredRegion(t *testing.T) {
	isolateAWSEnvironment(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIABASE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	awsSTS := newFakeAWSSTS(t, "ASIAHOP")

	cfg := wifConfig(testWIFAudience, "")
	cfg.AWSRegion = schemas.NewSecretVar("eu-west-1")
	cfg.AWSRoleARN = schemas.NewSecretVar("arn:aws:iam::123456789012:role/VertexHop")
	s, err := newAWSSubjectTokenSupplier(context.Background(), cfg)
	require.NoError(t, err)

	token, err := s.SubjectToken(context.Background(), externalaccountSupplierOptions())
	require.NoError(t, err, "the configured aws_region must be used for the AssumeRole call")
	auth, ok := headerValue(decodeSubjectToken(t, token), "Authorization")
	require.True(t, ok)
	assert.Contains(t, auth, "Credential=ASIAHOP/")
	assert.Contains(t, auth, "/eu-west-1/sts/aws4_request")
	assert.Equal(t, "AssumeRole", awsSTS.form().Get("Action"))
	// The SDK signs the AssumeRole call with the client's region; an empty region here is exactly
	// what makes the real endpoint resolution fail with "missing region" outside this fake.
	assert.Contains(t, awsSTS.auth(), "/eu-west-1/sts/aws4_request", "AssumeRole must be signed for the configured region, got %q", awsSTS.auth())
}

// recordingTransport records the path of every request it carries, standing in for
// the proxy-aware authHTTPClient so a test can see which calls went through it.
type recordingTransport struct {
	mu    sync.Mutex
	paths []string
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.paths = append(r.paths, req.URL.Path)
	r.mu.Unlock()
	return http.DefaultTransport.RoundTrip(req)
}

func (r *recordingTransport) sawSuffix(suffix string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.paths {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}
	return false
}

// TestGetAuthTokenSource_AWSFederationUsesAuthHTTPClient pins that both AWS federation
// routes send the GCP STS token exchange and the service-account impersonation call
// through provider.authHTTPClient, the client that carries proxy_config. A token source
// built on context.Background() would fall back to http.DefaultClient and skip the proxy.
func TestGetAuthTokenSource_AWSFederationUsesAuthHTTPClient(t *testing.T) {
	routes := []struct {
		name string
		key  func(gcp *fakeGCP) schemas.Key
	}{
		{"aws_workload_identity config", func(*fakeGCP) schemas.Key { return wifKey(wifConfig(testWIFAudience, testWIFSAEmail), "") }},
		{"external_account json", func(gcp *fakeGCP) schemas.Key { return wifKey(nil, awsExternalAccountJSON(t, gcp, testWIFSAEmail)) }},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			setIRSAEnvironment(t)
			gcp := newFakeGCP(t)
			recorder := &recordingTransport{}
			provider := &VertexProvider{authHTTPClient: &http.Client{Transport: recorder}}
			key := route.key(gcp)
			t.Cleanup(func() { provider.removeVertexClient(key) })

			ts, err := provider.getAuthTokenSource(key)
			require.NoError(t, err)
			token, err := ts.Token()
			require.NoError(t, err)
			assert.Equal(t, "sa-token", token.AccessToken)

			assert.True(t, recorder.sawSuffix("/v1/token"), "the GCP STS exchange bypassed authHTTPClient; saw %v", recorder.paths)
			assert.True(t, recorder.sawSuffix(":generateAccessToken"), "the impersonation call bypassed authHTTPClient; saw %v", recorder.paths)
		})
	}
}
