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

	configData.ProxyConfig = &GlobalProxyFileConfig{Enabled: true, Type: network.GlobalProxyTypeSOCKS5, URL: schemas.NewSecretVar("socks5://p:1080")}
	createConfigFile(t, tempDir, configData)
	stored, inMemory = bootProxy(t, tempDir, nil)
	assert.Equal(t, "http://dashboard.corp:3128", stored.URL, "an unsupported type is not applied")
	require.NotNil(t, inMemory, "and the config carries the stored proxy")
	assert.Equal(t, "http://dashboard.corp:3128", inMemory.URL)
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
