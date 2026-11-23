package awsauth

import (
	"context"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAWS is a local container credentials endpoint (what EKS Pod Identity serves) and STS.
type fakeAWS struct {
	containerFetches atomic.Int32
	containerStatus  atomic.Int32 // 0 means 200
	containerExpiry  atomic.Int64 // unix seconds; 0 means an hour from now
	stsCalls         atomic.Int32
	stsStatus        atomic.Int32 // 0 means 200
	stsExpiry        atomic.Int64 // unix seconds; 0 means an hour from now
	stsForm          atomic.Value // url.Values of the last AssumeRole request
	stsAccessKey     atomic.Value // access key ID that signed the last AssumeRole request
}

// isolate clears the provider caches and every ambient AWS credential source, then points the
// default chain and STS at a fakeAWS.
func isolate(t *testing.T) *fakeAWS {
	t.Helper()
	reset := func() {
		defaultChainProviders.Range(func(k, _ any) bool { defaultChainProviders.Delete(k); return true })
		assumeRoleProviders.Range(func(k, _ any) bool { assumeRoleProviders.Delete(k); return true })
	}
	reset()
	t.Cleanup(reset)

	f := &fakeAWS{}
	expiry := func(v int64) string {
		if v == 0 {
			return time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		}
		return time.Unix(v, 0).UTC().Format(time.RFC3339)
	}
	container := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.containerFetches.Add(1)
		if status := f.containerStatus.Load(); status != 0 {
			w.WriteHeader(int(status))
			return
		}
		_, _ = w.Write([]byte(`{"AccessKeyId":"ASIACHAIN","SecretAccessKey":"secret","Token":"token","Expiration":"` + expiry(f.containerExpiry.Load()) + `"}`))
	}))
	t.Cleanup(container.Close)
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.stsCalls.Add(1)
		_ = r.ParseForm()
		f.stsForm.Store(r.PostForm)
		credential, _, _ := strings.Cut(strings.TrimPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential="), "/")
		f.stsAccessKey.Store(credential)
		w.Header().Set("Content-Type", "text/xml")
		if status := f.stsStatus.Load(); status != 0 {
			w.WriteHeader(int(status))
			_, _ = w.Write([]byte(`<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>not authorized to perform sts:AssumeRole</Message></Error><RequestId>1</RequestId></ErrorResponse>`))
			return
		}
		if r.PostForm.Get("Action") == "AssumeRoleWithWebIdentity" {
			_, _ = w.Write([]byte(`<AssumeRoleWithWebIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>ASIAWEBIDENTITY</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>token</SessionToken><Expiration>` + expiry(f.stsExpiry.Load()) + `</Expiration></Credentials><AssumedRoleUser><Arn>arn:aws:sts::123456789012:assumed-role/irsa/s</Arn><AssumedRoleId>AROA:s</AssumedRoleId></AssumedRoleUser></AssumeRoleWithWebIdentityResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></AssumeRoleWithWebIdentityResponse>`))
			return
		}
		_, _ = w.Write([]byte(`<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>ASIAASSUMED</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>token</SessionToken><Expiration>` + expiry(f.stsExpiry.Load()) + `</Expiration></Credentials><AssumedRoleUser><Arn>arn:aws:sts::123456789012:assumed-role/r/s</Arn><AssumedRoleId>AROA:s</AssumedRoleId></AssumedRoleUser></AssumeRoleResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></AssumeRoleResponse>`))
	}))
	t.Cleanup(sts.Close)

	dir := t.TempDir()
	for k, v := range map[string]string{
		"AWS_ACCESS_KEY_ID":                      "",
		"AWS_SECRET_ACCESS_KEY":                  "",
		"AWS_ACCESS_KEY":                         "",
		"AWS_SECRET_KEY":                         "",
		"AWS_DEFAULT_PROFILE":                    "",
		"AWS_SESSION_TOKEN":                      "",
		"AWS_PROFILE":                            "",
		"AWS_ROLE_ARN":                           "",
		"AWS_WEB_IDENTITY_TOKEN_FILE":            "",
		"AWS_CONFIG_FILE":                        dir + "/config",
		"AWS_SHARED_CREDENTIALS_FILE":            dir + "/credentials",
		"AWS_EC2_METADATA_DISABLED":              "true",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI":     container.URL,
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN":      "pod-identity-token",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": "",
		"AWS_ENDPOINT_URL_STS":                   sts.URL,
	} {
		t.Setenv(k, v)
	}
	return f
}

func (f *fakeAWS) lastSTSForm() url.Values {
	form, _ := f.stsForm.Load().(url.Values)
	return form
}

func lookup(cfg Config, region string) (aws.Credentials, error) {
	provider, err := CredentialsProvider(context.Background(), cfg, region)
	if err != nil {
		return aws.Credentials{}, err
	}
	return provider.Retrieve(context.Background())
}

func retrieve(t *testing.T, cfg Config, region string) aws.Credentials {
	t.Helper()
	creds, err := lookup(cfg, region)
	require.NoError(t, err)
	return creds
}

const testRole = "arn:aws:iam::123456789012:role/gateway"

// TestCredentialsProvider_StaticKeys: static keys are used as given, with or without a session
// token, and never touch the default chain or STS. Either key alone still counts as static, as
// it always has, so a half-filled key fails at AWS rather than silently using the pod's identity.
func TestCredentialsProvider_StaticKeys(t *testing.T) {
	f := isolate(t)
	creds := retrieve(t, Config{AccessKey: "AKIASTATIC", SecretKey: "secret", SessionToken: "session"}, "us-east-1")
	assert.Equal(t, aws.Credentials{AccessKeyID: "AKIASTATIC", SecretAccessKey: "secret", SessionToken: "session", Source: creds.Source}, creds)

	creds = retrieve(t, Config{AccessKey: "AKIASTATIC", SecretKey: "secret"}, "us-east-1")
	assert.Empty(t, creds.SessionToken)

	creds = retrieve(t, Config{AccessKey: "AKIAONLY"}, "us-east-1")
	assert.Equal(t, "AKIAONLY", creds.AccessKeyID)

	assert.Zero(t, f.containerFetches.Load())
	assert.Zero(t, f.stsCalls.Load())
}

// TestCredentialsProvider_DefaultChainCachedPerRegion: the chain is fetched once per region
// and then served from the cache until it nears expiry.
func TestCredentialsProvider_DefaultChainCachedPerRegion(t *testing.T) {
	f := isolate(t)
	for range 5 {
		assert.Equal(t, "ASIACHAIN", retrieve(t, Config{}, "us-east-1").AccessKeyID)
		retrieve(t, Config{}, "us-gov-west-1")
	}
	assert.Equal(t, int32(2), f.containerFetches.Load())

	first, err := CredentialsProvider(context.Background(), Config{}, "us-east-1")
	require.NoError(t, err)
	second, err := CredentialsProvider(context.Background(), Config{}, "us-east-1")
	require.NoError(t, err)
	assert.Same(t, first, second)
}

// TestCredentialsProvider_DefaultChainLoadErrorNotCached: a chain that fails to load is
// reported and not remembered, so fixing the environment needs no restart.
func TestCredentialsProvider_DefaultChainLoadErrorNotCached(t *testing.T) {
	isolate(t)
	t.Setenv("AWS_PROFILE", "missing-profile")
	_, err := CredentialsProvider(context.Background(), Config{}, "us-east-1")
	require.Error(t, err)
	_, cached := defaultChainProviders.Load("us-east-1")
	assert.False(t, cached)

	t.Setenv("AWS_PROFILE", "")
	assert.Equal(t, "ASIACHAIN", retrieve(t, Config{}, "us-east-1").AccessKeyID)
}

// TestCredentialsProvider_DefaultChainFetchFailureRetried: a failed fetch is not cached; the
// next lookup tries the endpoint again.
func TestCredentialsProvider_DefaultChainFetchFailureRetried(t *testing.T) {
	f := isolate(t)
	f.containerStatus.Store(http.StatusInternalServerError)
	_, err := lookup(Config{}, "us-east-1")
	require.Error(t, err)

	f.containerStatus.Store(0)
	before := f.containerFetches.Load()
	assert.Equal(t, "ASIACHAIN", retrieve(t, Config{}, "us-east-1").AccessKeyID)
	assert.Equal(t, before+1, f.containerFetches.Load())
}

// TestCredentialsProvider_FailedSourceReselected: a source that fails to fetch is chosen again on the
// next lookup, so a credentials file written after startup is picked up without a restart.
func TestCredentialsProvider_FailedSourceReselected(t *testing.T) {
	f := isolate(t)
	f.containerStatus.Store(http.StatusInternalServerError)
	_, err := lookup(Config{}, "us-east-1")
	require.Error(t, err)

	body := "[default]\naws_access_key_id = AKIAWRITTENLATER\naws_secret_access_key = secret\n"
	require.NoError(t, os.WriteFile(os.Getenv("AWS_SHARED_CREDENTIALS_FILE"), []byte(body), 0o600))
	assert.Equal(t, "AKIAWRITTENLATER", retrieve(t, Config{}, "us-east-1").AccessKeyID)
}

// TestCredentialsProvider_WebIdentityNearExpiryRefreshed: IRSA credentials are fetched again inside
// the expiry window, as container credentials are, not only once they have expired.
func TestCredentialsProvider_WebIdentityNearExpiryRefreshed(t *testing.T) {
	f := isolate(t)
	token := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(token, []byte("irsa-token"), 0o600))
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", token)
	t.Setenv("AWS_ROLE_ARN", "arn:aws:iam::123456789012:role/irsa")
	f.stsExpiry.Store(time.Now().Add(2 * time.Minute).Unix())
	for range 2 {
		assert.Equal(t, "ASIAWEBIDENTITY", retrieve(t, Config{}, "us-east-1").AccessKeyID)
	}
	assert.Equal(t, int32(2), f.stsCalls.Load(), "credentials two minutes from expiry are fetched again")
	assert.Zero(t, f.containerFetches.Load())
}

// TestCredentialsProvider_ExpiredCredentialsRefetched: the cache holds credentials only until
// they expire.
func TestCredentialsProvider_ExpiredCredentialsRefetched(t *testing.T) {
	f := isolate(t)
	f.containerExpiry.Store(time.Now().Add(-time.Minute).Unix())
	for range 3 {
		retrieve(t, Config{}, "us-east-1")
	}
	assert.Equal(t, int32(3), f.containerFetches.Load())
}

// TestCredentialsProvider_SharedFileKeysReloaded: keys from the shared credentials file are read
// again on each use, so a rotated file takes effect, and an assumed role is requested with the
// current keys once its credentials expire.
func TestCredentialsProvider_SharedFileKeysReloaded(t *testing.T) {
	f := isolate(t)
	writeKeys := func(accessKey string) {
		t.Helper()
		body := "[default]\naws_access_key_id = " + accessKey + "\naws_secret_access_key = secret\n"
		require.NoError(t, os.WriteFile(os.Getenv("AWS_SHARED_CREDENTIALS_FILE"), []byte(body), 0o600))
	}

	writeKeys("AKIAFILEONE")
	assert.Equal(t, "AKIAFILEONE", retrieve(t, Config{}, "us-east-1").AccessKeyID)
	writeKeys("AKIAFILETWO")
	assert.Equal(t, "AKIAFILETWO", retrieve(t, Config{}, "us-east-1").AccessKeyID)

	f.stsExpiry.Store(time.Now().Add(-time.Minute).Unix())
	retrieve(t, Config{RoleARN: testRole}, "us-east-1")
	assert.Equal(t, "AKIAFILETWO", f.stsAccessKey.Load())
	writeKeys("AKIAFILETHREE")
	retrieve(t, Config{RoleARN: testRole}, "us-east-1")
	assert.Equal(t, "AKIAFILETHREE", f.stsAccessKey.Load(), "the assumed role must be requested with the rotated keys")
	assert.Zero(t, f.containerFetches.Load(), "shared-file keys come before the container endpoint")
}

// TestCredentialsProvider_SourceProfileKeysReloaded: a profile that assumes a role from another
// profile's keys picks up rotated source keys when the role is next refreshed.
func TestCredentialsProvider_SourceProfileKeysReloaded(t *testing.T) {
	f := isolate(t)
	require.NoError(t, os.WriteFile(os.Getenv("AWS_CONFIG_FILE"), []byte("[default]\nrole_arn = "+testRole+"\nsource_profile = base\n"), 0o600))
	writeKeys := func(accessKey string) {
		t.Helper()
		body := "[base]\naws_access_key_id = " + accessKey + "\naws_secret_access_key = secret\n"
		require.NoError(t, os.WriteFile(os.Getenv("AWS_SHARED_CREDENTIALS_FILE"), []byte(body), 0o600))
	}

	writeKeys("AKIASOURCEONE")
	f.stsExpiry.Store(time.Now().Add(-time.Minute).Unix())
	assert.Equal(t, "ASIAASSUMED", retrieve(t, Config{}, "us-east-1").AccessKeyID)
	assert.Equal(t, "AKIASOURCEONE", f.stsAccessKey.Load())
	writeKeys("AKIASOURCETWO")
	retrieve(t, Config{}, "us-east-1")
	assert.Equal(t, "AKIASOURCETWO", f.stsAccessKey.Load(), "the role must be refreshed with the rotated source keys")

	f.stsExpiry.Store(0)
	calls := f.stsCalls.Load()
	for range 3 {
		retrieve(t, Config{}, "us-east-1")
	}
	assert.Equal(t, calls+1, f.stsCalls.Load(), "a role that has not expired is not assumed again")
}

// TestCredentialsProvider_EnvKeysStayCached: keys from the environment can't change while
// Bifrost runs, so they stay cached rather than reloaded like shared-file keys.
func TestCredentialsProvider_EnvKeysStayCached(t *testing.T) {
	isolate(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	provider, err := CredentialsProvider(context.Background(), Config{}, "us-east-1")
	require.NoError(t, err)
	assert.IsType(t, &aws.CredentialsCache{}, provider.(*chainSource).CredentialsProvider)
	assert.Equal(t, "AKIAENV", retrieve(t, Config{}, "us-east-1").AccessKeyID)
}

// TestCredentialsProvider_AssumeRoleSendsParameters: the role ARN, external ID and session name
// reach STS, with bifrost-session when no session name is set.
func TestCredentialsProvider_AssumeRoleSendsParameters(t *testing.T) {
	f := isolate(t)
	assert.Equal(t, "ASIAASSUMED", retrieve(t, Config{RoleARN: testRole}, "us-east-1").AccessKeyID)
	form := f.lastSTSForm()
	assert.Equal(t, "AssumeRole", form.Get("Action"))
	assert.Equal(t, testRole, form.Get("RoleArn"))
	assert.Equal(t, defaultSessionName, form.Get("RoleSessionName"))
	assert.False(t, form.Has("ExternalId"))

	retrieve(t, Config{RoleARN: testRole, ExternalID: "ext-123", SessionName: "mcp-gateway"}, "us-east-1")
	form = f.lastSTSForm()
	assert.Equal(t, "ext-123", form.Get("ExternalId"))
	assert.Equal(t, "mcp-gateway", form.Get("RoleSessionName"))
}

// TestCredentialsProvider_AssumeRoleReusedForSameParameters: an assumed role is shared only by
// configs with the same role, external ID, session name, source and region.
func TestCredentialsProvider_AssumeRoleReusedForSameParameters(t *testing.T) {
	f := isolate(t)
	base := Config{RoleARN: testRole}
	for range 3 {
		assert.Equal(t, "ASIAASSUMED", retrieve(t, base, "us-east-1").AccessKeyID)
	}
	assert.Equal(t, int32(1), f.stsCalls.Load())
	assert.Equal(t, int32(1), f.containerFetches.Load(), "the role is assumed with the cached default chain")

	withExternalID := base
	withExternalID.ExternalID = "ext"
	withSessionName := base
	withSessionName.SessionName = "mcp"
	withStaticSource := base
	withStaticSource.AccessKey, withStaticSource.SecretKey = "AKIASTATIC", "secret"
	withRotatedToken := withStaticSource
	withRotatedToken.SessionToken = "rotated"
	for _, cfg := range []Config{withExternalID, withSessionName, withStaticSource, withRotatedToken} {
		retrieve(t, cfg, "us-east-1")
		retrieve(t, cfg, "us-east-1")
	}
	retrieve(t, base, "us-west-2")
	assert.Equal(t, int32(6), f.stsCalls.Load())
}

// TestCredentialsProvider_AssumeRoleFailureNotCached: a refused AssumeRole is reported, and the
// next lookup asks STS again rather than replaying the failure.
func TestCredentialsProvider_AssumeRoleFailureNotCached(t *testing.T) {
	f := isolate(t)
	f.stsStatus.Store(http.StatusForbidden)
	_, err := lookup(Config{RoleARN: testRole}, "us-east-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AccessDenied")

	f.stsStatus.Store(0)
	assert.Equal(t, "ASIAASSUMED", retrieve(t, Config{RoleARN: testRole}, "us-east-1").AccessKeyID)
	assert.Equal(t, int32(2), f.stsCalls.Load())
}

// TestAssumeRoleKey_HoldsNoSecret: the cache key names the source by access key and a short
// token hash, so neither the secret key nor the session token is kept in it.
// TestCredentialsProvider_AssumeRoleNearExpiryRefreshed: assumed credentials within the expiry
// window are fetched again rather than served until they lapse.
func TestCredentialsProvider_AssumeRoleNearExpiryRefreshed(t *testing.T) {
	f := isolate(t)
	f.stsExpiry.Store(time.Now().Add(expiryWindow / 2).Unix())
	retrieve(t, Config{RoleARN: testRole}, "us-east-1")
	retrieve(t, Config{RoleARN: testRole}, "us-east-1")
	assert.Equal(t, int32(2), f.stsCalls.Load(), "credentials inside the expiry window must be refreshed")

	f.stsExpiry.Store(time.Now().Add(time.Hour).Unix())
	other := Config{RoleARN: testRole, SessionName: "fresh"}
	retrieve(t, other, "us-east-1")
	retrieve(t, other, "us-east-1")
	assert.Equal(t, int32(3), f.stsCalls.Load(), "credentials well within their life are reused")
}

// TestCredentialsProvider_CorrectedSecretGetsItsOwnRole: a role assumed with a static key pair is
// cached per secret too, so correcting a wrong secret under the same access key reaches STS
// instead of reusing the provider built with the wrong one.
func TestCredentialsProvider_CorrectedSecretGetsItsOwnRole(t *testing.T) {
	isolate(t)
	wrong := Config{AccessKey: "AKIASTATIC", SecretKey: "wrong", RoleARN: testRole}
	corrected := wrong
	corrected.SecretKey = "right"
	first, err := CredentialsProvider(context.Background(), wrong, "us-east-1")
	require.NoError(t, err)
	second, err := CredentialsProvider(context.Background(), corrected, "us-east-1")
	require.NoError(t, err)
	assert.NotSame(t, first, second)
	again, err := CredentialsProvider(context.Background(), corrected, "us-east-1")
	require.NoError(t, err)
	assert.Same(t, second, again, "the same pair still shares one provider")
}

func TestAssumeRoleKey_HoldsNoSecret(t *testing.T) {
	cfg := Config{AccessKey: "AKIASTATIC", SecretKey: "very-secret-key", SessionToken: "very-secret-token", RoleARN: testRole}
	key := assumeRoleKey(cfg, "us-east-1")
	assert.NotContains(t, key, "very-secret-key")
	assert.NotContains(t, key, "very-secret-token")
	assert.True(t, strings.HasPrefix(key, "us-east-1|"+testRole+"||"+defaultSessionName+"|AKIASTATIC|"), key)

	assert.Equal(t, "us-east-1|"+testRole+"|ext|s|default_chain", assumeRoleKey(Config{RoleARN: testRole, ExternalID: "ext", SessionName: "s"}, "us-east-1"))
}

func TestInvalidate_RefetchesOnNextLookup(t *testing.T) {
	f := isolate(t)
	role := Config{RoleARN: testRole}
	retrieve(t, Config{}, "us-east-1")
	retrieve(t, role, "us-east-1")
	require.Equal(t, int32(1), f.containerFetches.Load())
	require.Equal(t, int32(1), f.stsCalls.Load())

	Invalidate(Config{}, "us-east-1")
	retrieve(t, Config{}, "us-east-1")
	assert.Equal(t, int32(2), f.containerFetches.Load())
	assert.Equal(t, int32(1), f.stsCalls.Load(), "invalidating the chain leaves an assumed role alone")

	// A role over the default chain refreshes both: the source may be what AWS rejected.
	Invalidate(role, "us-east-1")
	retrieve(t, role, "us-east-1")
	assert.Equal(t, int32(2), f.stsCalls.Load())
	assert.Equal(t, int32(3), f.containerFetches.Load())

	// A failed fetch replaces the region's source; the role signs through the new one, so
	// invalidating the role still refetches the source it uses.
	f.containerStatus.Store(http.StatusInternalServerError)
	_, err := lookup(role, "us-west-2")
	require.Error(t, err)
	f.containerStatus.Store(0)
	retrieve(t, role, "us-west-2")
	fetches := f.containerFetches.Load()
	Invalidate(role, "us-west-2")
	retrieve(t, role, "us-west-2")
	assert.Equal(t, fetches+1, f.containerFetches.Load(), "the role's refresh fetches its current source again")

	// Nothing cached yet, or nothing to cache: both are no-ops.
	Invalidate(Config{RoleARN: "arn:aws:iam::123456789012:role/never-used"}, "eu-west-1")
	Invalidate(Config{AccessKey: "AKIASTATIC", SecretKey: "secret"}, "us-east-1")
}

func TestCredentialsProvider_ConcurrentFirstUseFetchesOnce(t *testing.T) {
	f := isolate(t)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			retrieve(t, Config{RoleARN: testRole}, "eu-west-1")
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), f.containerFetches.Load())
	assert.Equal(t, int32(1), f.stsCalls.Load())
}
