package lib

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// proxyFileConfig is a proxy declared for SCIM directory sync only.
func proxyFileConfig(url string) *GlobalProxyFileConfig {
	return &GlobalProxyFileConfig{
		Enabled:       true,
		URL:           schemas.NewSecretVar(url),
		Username:      schemas.NewSecretVar("proxy-user"),
		Password:      schemas.NewSecretVar("proxy-pass"),
		NoProxy:       "localhost,.internal",
		Timeout:       30,
		EnableForSCIM: true,
	}
}

// bootProxy boots on the file in tempDir and returns the proxy the store holds afterwards and the one
// the config carries. dashboardEdit, when set, then saves a proxy the way the settings page does,
// before the boot's config is closed.
func bootProxy(t *testing.T, tempDir string, dashboardEdit *configstoreTables.GlobalProxyConfig) (*configstoreTables.GlobalProxyConfig, *configstoreTables.GlobalProxyConfig) {
	t.Helper()
	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	require.NoError(t, err)
	defer config.Close(ctx)
	stored, err := config.ConfigStore.GetProxyConfig(ctx)
	require.NoError(t, err)
	if dashboardEdit != nil {
		require.NoError(t, config.ConfigStore.UpdateProxyConfig(ctx, dashboardEdit))
	}
	return stored, config.ProxyConfig
}

// TestLoadProxyConfig_AppliesTheDeclaration: a proxy declared in config.json is stored as the
// dashboard would store it, type defaulted to http, credentials resolved.
func TestLoadProxyConfig_AppliesTheDeclaration(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.ProxyConfig = proxyFileConfig("http://proxy.corp:8080")
	createConfigFile(t, tempDir, configData)

	stored, inMemory := bootProxy(t, tempDir, nil)
	require.NotNil(t, stored)
	assert.True(t, stored.Enabled)
	assert.Equal(t, network.GlobalProxyTypeHTTP, stored.Type)
	assert.Equal(t, "http://proxy.corp:8080", stored.URL)
	assert.Equal(t, "proxy-user", stored.Username)
	assert.Equal(t, "proxy-pass", stored.Password)
	assert.Equal(t, "localhost,.internal", stored.NoProxy)
	assert.Equal(t, 30, stored.Timeout)
	assert.True(t, stored.EnableForSCIM)
	assert.False(t, stored.EnableForInference)
	require.NotNil(t, inMemory)
	assert.Equal(t, "http://proxy.corp:8080", inMemory.URL)
}

// TestLoadProxyConfig_AppliesASOCKS5Declaration: the file can declare a SOCKS5 proxy, as the
// settings endpoint can, and it is stored with its type.
func TestLoadProxyConfig_AppliesASOCKS5Declaration(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	declared := proxyFileConfig("socks5://proxy.corp:1080")
	declared.Type = network.GlobalProxyTypeSOCKS5
	configData.ProxyConfig = declared
	createConfigFile(t, tempDir, configData)

	stored, inMemory := bootProxy(t, tempDir, nil)
	require.NotNil(t, stored, "an enabled socks5 declaration must be applied")
	assert.Equal(t, network.GlobalProxyTypeSOCKS5, stored.Type)
	assert.Equal(t, "socks5://proxy.corp:1080", stored.URL)
	require.NotNil(t, inMemory)
	assert.Equal(t, network.GlobalProxyTypeSOCKS5, inMemory.Type)
}

// TestLoadProxyConfig_SplitKeepsADashboardEditUntilTheFileChanges: the hash lives in its own row, so
// a dashboard save does not wipe it, and an unchanged file leaves the edit alone.
func TestLoadProxyConfig_SplitKeepsADashboardEditUntilTheFileChanges(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.ProxyConfig = proxyFileConfig("http://proxy.corp:8080")
	createConfigFile(t, tempDir, configData)

	bootProxy(t, tempDir, &configstoreTables.GlobalProxyConfig{
		Enabled: true, Type: network.GlobalProxyTypeHTTP, URL: "http://dashboard.corp:3128", EnableForSCIM: true, EnableForAPI: true,
	})

	stored, _ := bootProxy(t, tempDir, nil)
	assert.Equal(t, "http://dashboard.corp:3128", stored.URL, "an unchanged file keeps the dashboard's edit")
	assert.True(t, stored.EnableForAPI)

	configData.ProxyConfig = proxyFileConfig("http://proxy2.corp:8080")
	createConfigFile(t, tempDir, configData)
	stored, _ = bootProxy(t, tempDir, nil)
	assert.Equal(t, "http://proxy2.corp:8080", stored.URL, "a changed declaration wins")
	assert.False(t, stored.EnableForAPI)
}

// TestLoadProxyConfig_SourceOfTruthAppliesTheFileEveryBoot: under config.json source of truth a
// dashboard edit is reverted on the next boot even though the file did not change.
func TestLoadProxyConfig_SourceOfTruthAppliesTheFileEveryBoot(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.SourceOfTruth = SourceOfTruthConfigJSON
	configData.ProxyConfig = proxyFileConfig("http://proxy.corp:8080")
	createConfigFile(t, tempDir, configData)

	bootProxy(t, tempDir, &configstoreTables.GlobalProxyConfig{Enabled: false, Type: network.GlobalProxyTypeHTTP})

	stored, _ := bootProxy(t, tempDir, nil)
	assert.True(t, stored.Enabled)
	assert.Equal(t, "http://proxy.corp:8080", stored.URL)
}

// TestLoadProxyConfig_AbsentOrInvalidLeavesTheStoredProxy: no proxy_config, or one the settings page
// would refuse, leaves what the dashboard stored.
func TestLoadProxyConfig_AbsentOrInvalidLeavesTheStoredProxy(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	createConfigFile(t, tempDir, configData)

	stored, _ := bootProxy(t, tempDir, &configstoreTables.GlobalProxyConfig{Enabled: true, Type: network.GlobalProxyTypeHTTP, URL: "http://dashboard.corp:3128"})
	assert.Nil(t, stored)

	stored, inMemory := bootProxy(t, tempDir, nil)
	assert.Equal(t, "http://dashboard.corp:3128", stored.URL, "an absent section leaves the stored proxy")
	require.NotNil(t, inMemory, "and the config carries the stored proxy")
	assert.Equal(t, "http://dashboard.corp:3128", inMemory.URL)

	configData.ProxyConfig = &GlobalProxyFileConfig{Enabled: true, EnableForSCIM: true}
	createConfigFile(t, tempDir, configData)
	stored, _ = bootProxy(t, tempDir, nil)
	assert.Equal(t, "http://dashboard.corp:3128", stored.URL, "an enabled proxy without a url is not applied")

	configData.ProxyConfig = &GlobalProxyFileConfig{Enabled: true, Type: network.GlobalProxyTypeTCP, URL: schemas.NewSecretVar("tcp://p:9000")}
	createConfigFile(t, tempDir, configData)
	stored, inMemory = bootProxy(t, tempDir, nil)
	assert.Equal(t, "http://dashboard.corp:3128", stored.URL, "an unsupported type is not applied")
	require.NotNil(t, inMemory, "and the config carries the stored proxy")
	assert.Equal(t, "http://dashboard.corp:3128", inMemory.URL)
}

// TestLoadProxyConfig_MalformedURLLeavesTheStoredProxy: the file is held to the settings page's URL
// rules. The client factory reads a URL it cannot parse as no proxy, so applying one would send
// traffic direct while the proxy reads as enabled.
func TestLoadProxyConfig_MalformedURLLeavesTheStoredProxy(t *testing.T) {
	cases := []struct {
		name      string
		proxyType network.GlobalProxyType
		url       string
	}{
		{"socks5 with a non-numeric port", network.GlobalProxyTypeSOCKS5, "socks5://proxy.corp:bad"},
		{"socks5 named by an http url", network.GlobalProxyTypeSOCKS5, "http://proxy.corp:1080"},
		{"http named by a socks5 url", network.GlobalProxyTypeHTTP, "socks5://proxy.corp:1080"},
		{"http with an out-of-range port", network.GlobalProxyTypeHTTP, "http://proxy.corp:70000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initTestLogger()
			tempDir := createTempDir(t)
			configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
			createConfigFile(t, tempDir, configData)
			bootProxy(t, tempDir, &configstoreTables.GlobalProxyConfig{Enabled: true, Type: network.GlobalProxyTypeHTTP, URL: "http://dashboard.corp:3128"})

			configData.ProxyConfig = &GlobalProxyFileConfig{Enabled: true, Type: tc.proxyType, URL: schemas.NewSecretVar(tc.url)}
			createConfigFile(t, tempDir, configData)
			stored, inMemory := bootProxy(t, tempDir, nil)
			assert.Equal(t, "http://dashboard.corp:3128", stored.URL, "a malformed url is not applied")
			require.NotNil(t, inMemory, "and the config carries the stored proxy")
			assert.Equal(t, "http://dashboard.corp:3128", inMemory.URL)
		})
	}
}

// TestLoadProxyConfig_ResolvesEnvReferences: credentials can come from the environment, and the
// resolved value is what is stored.
func TestLoadProxyConfig_ResolvesEnvReferences(t *testing.T) {
	initTestLogger()
	t.Setenv("BIFROST_TEST_PROXY_PASSWORD", "from-env")
	tempDir := createTempDir(t)
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.ProxyConfig = proxyFileConfig("http://proxy.corp:8080")
	configData.ProxyConfig.Password = schemas.NewSecretVar("env.BIFROST_TEST_PROXY_PASSWORD")
	createConfigFile(t, tempDir, configData)

	stored, _ := bootProxy(t, tempDir, nil)
	assert.Equal(t, "from-env", stored.Password)
}

// TestLoadProxyConfig_ClearedHashReappliesTheFile: a write interrupted between the proxy and its hash
// leaves the hash cleared, never the previous declaration's, so the next boot re-applies the file
// rather than keeping a stored proxy no declaration describes.
func TestLoadProxyConfig_ClearedHashReappliesTheFile(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	configData := makeConfigDataWithProvidersAndDir(nil, tempDir)
	configData.ProxyConfig = proxyFileConfig("http://proxy.corp:8080")
	createConfigFile(t, tempDir, configData)
	ctx := context.Background()

	bootProxy(t, tempDir, nil)
	config, err := LoadConfig(ctx, tempDir)
	require.NoError(t, err)
	// What a boot that cleared the hash and then failed would leave: some other proxy, no hash.
	require.NoError(t, config.ConfigStore.UpdateProxyConfig(ctx, &configstoreTables.GlobalProxyConfig{
		Enabled: true, Type: network.GlobalProxyTypeHTTP, URL: "http://half-written.corp:1",
	}))
	require.NoError(t, config.ConfigStore.UpdateConfig(ctx, &configstoreTables.TableGovernanceConfig{Key: configstoreTables.ConfigProxyHashKey}))
	config.Close(ctx)

	stored, _ := bootProxy(t, tempDir, nil)
	assert.Equal(t, "http://proxy.corp:8080", stored.URL)
}

// TestProxyURLPasswordRedaction pins which part of a proxy URL GET masks and how a save of
// the masked URL gets the stored password back. Only the userinfo password is a secret; an
// "@" past the authority is not userinfo.
func TestProxyURLPasswordRedaction(t *testing.T) {
	for _, tc := range []struct{ raw, redacted string }{
		{"socks5://svc:s3cret@10.0.0.5:1080", "socks5://svc:<redacted>@10.0.0.5:1080"},
		{"svc:s3cret@10.0.0.5:1080", "svc:<redacted>@10.0.0.5:1080"},
		{"http://svc:p@ss@10.0.0.5:3128", "http://svc:<redacted>@10.0.0.5:3128"},
		{"http://svc@10.0.0.5:3128", "http://svc@10.0.0.5:3128"},
		{"http://svc:@10.0.0.5:3128", "http://svc:@10.0.0.5:3128"},
		{"http://10.0.0.5:3128/a@b", "http://10.0.0.5:3128/a@b"},
		{"10.0.0.5:1080", "10.0.0.5:1080"},
	} {
		assert.Equal(t, tc.redacted, RedactProxyURLPassword(tc.raw), tc.raw)
		assert.Equal(t, tc.raw, RestoreProxyURLPassword(RedactProxyURLPassword(tc.raw), tc.raw), "round trip of %s", tc.raw)
	}
	assert.Equal(t, "socks5://svc@10.0.0.5:1080", RestoreProxyURLPassword("socks5://svc:<redacted>@10.0.0.5:1080", "socks5://10.0.0.5:1080"),
		"with no stored password the placeholder is dropped")
	assert.Equal(t, "socks5://svc:typed@10.0.0.5:1080", RestoreProxyURLPassword("socks5://svc:typed@10.0.0.5:1080", "socks5://svc:s3cret@10.0.0.5:1080"),
		"a password the admin typed is kept")
}

// TestValidateGlobalProxyURL_ErrorsDoNotQuotePassword pins that a refused proxy URL's error
// never carries its userinfo password: the API returns it in a 400 and the config.json load
// logs it.
func TestValidateGlobalProxyURL_ErrorsDoNotQuotePassword(t *testing.T) {
	for _, raw := range []string{
		"socks5://svc:s3cret@10.0.0.5:bad",
		"socks5://svc:s3cret@10.0.0.5:0",
		"socks5://svc:s3cret@:1080",
		"socks5://svc:s3cret@[::1:1080",
		"svc:s3cret@10.0.0.5:bad",
	} {
		_, err := ValidateGlobalProxyURL(network.GlobalProxyTypeSOCKS5, raw)
		require.Error(t, err, raw)
		assert.NotContains(t, err.Error(), "s3cret", raw)
	}
	err := validateGlobalProxyConfig(configstoreTables.GlobalProxyConfig{Enabled: true, Type: network.GlobalProxyTypeSOCKS5, URL: "socks5://svc:s3cret@10.0.0.5:bad"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "s3cret", "the config.json load logs this error")
}
