package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/scanner"
	"go/token"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// auditChanges returns the audit changes a handler recorded on ctx.
func auditChanges(t *testing.T, ctx *fasthttp.RequestCtx) []configstore.AuditChange {
	t.Helper()
	changes, _ := ctx.UserValue(configstore.AuditChangesUserValueKey).([]configstore.AuditChange)
	return changes
}

// requireSingleAuditChange asserts exactly one change with the given event name and returns it.
func requireSingleAuditChange(t *testing.T, ctx *fasthttp.RequestCtx, eventName string) configstore.AuditChange {
	t.Helper()
	changes := auditChanges(t, ctx)
	if len(changes) != 1 {
		t.Fatalf("expected 1 audit change, got %d: %+v", len(changes), changes)
	}
	if changes[0].EventName != eventName {
		t.Fatalf("event name got %q, want %q", changes[0].EventName, eventName)
	}
	return changes[0]
}

// requireNoSecret fails when any snapshot of change contains secret.
func requireNoSecret(t *testing.T, change configstore.AuditChange, secret string) {
	t.Helper()
	if secret == "" {
		t.Fatal("secret under test must not be empty")
	}
	if strings.Contains(string(change.Before), secret) {
		t.Fatalf("before snapshot leaks the secret: %s", change.Before)
	}
	if strings.Contains(string(change.After), secret) {
		t.Fatalf("after snapshot leaks the secret: %s", change.After)
	}
}

// auditTestGovernanceManager is budgetOverrideTestGovernanceManager with a no-op RemoveVirtualKey.
type auditTestGovernanceManager struct {
	*budgetOverrideTestGovernanceManager
}

// RemoveVirtualKey does nothing; the tests only read the database.
func (auditTestGovernanceManager) RemoveVirtualKey(context.Context, string) error {
	return nil
}

// newAuditVKHandler builds a governance handler on a fresh SQLite store with one customer.
func newAuditVKHandler(t *testing.T) (*GovernanceHandler, configstore.ConfigStore) {
	t.Helper()
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	if err := store.CreateCustomer(context.Background(), &configstoreTables.TableCustomer{ID: "cust-audit", Name: "Audit Customer"}); err != nil {
		t.Fatalf("create customer: %v", err)
	}
	return &GovernanceHandler{configStore: store, governanceManager: auditTestGovernanceManager{&budgetOverrideTestGovernanceManager{store: store}}}, store
}

// createAuditVK creates a customer-owned VK through the handler and returns its id and plaintext value.
func createAuditVK(t *testing.T, h *GovernanceHandler, store configstore.ConfigStore) (*fasthttp.RequestCtx, string, string) {
	t.Helper()
	ctx := newTestRequestCtx(`{"name":"audit-vk","description":"first","customer_id":"cust-audit"}`)
	h.createVirtualKey(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("create status=%d body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	var resp struct {
		VirtualKey struct {
			ID string `json:"id"`
		} `json:"virtual_key"`
	}
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	vk, err := store.GetVirtualKey(context.Background(), resp.VirtualKey.ID)
	if err != nil {
		t.Fatalf("load created VK: %v", err)
	}
	return ctx, vk.ID, vk.Value.GetValue()
}

func TestAuditChange_VirtualKeyCreate(t *testing.T) {
	h, store := newAuditVKHandler(t)
	ctx, vkID, value := createAuditVK(t, h, store)

	change := requireSingleAuditChange(t, ctx, "virtual_key.created")
	if change.TargetType != "virtual_key" || change.TargetID != vkID || change.TargetName != "audit-vk" {
		t.Fatalf("unexpected target: %+v", change)
	}
	if change.Before != nil {
		t.Fatalf("create must have no before snapshot, got %s", change.Before)
	}
	if len(change.After) == 0 {
		t.Fatal("create must have an after snapshot")
	}
	requireNoSecret(t, change, value)
}

func TestAuditChange_VirtualKeyUpdate(t *testing.T) {
	h, store := newAuditVKHandler(t)
	_, vkID, value := createAuditVK(t, h, store)

	ctx := newTestRequestCtx(`{"description":"second"}`)
	ctx.SetUserValue("vk_id", vkID)
	h.updateVirtualKey(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("update status=%d body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}

	change := requireSingleAuditChange(t, ctx, "virtual_key.updated")
	if change.TargetID != vkID || change.TargetName != "audit-vk" {
		t.Fatalf("unexpected target: %+v", change)
	}
	if len(change.Before) == 0 || len(change.After) == 0 {
		t.Fatalf("update must carry both snapshots: before=%s after=%s", change.Before, change.After)
	}
	var before, after struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(change.Before, &before); err != nil {
		t.Fatalf("decode before: %v", err)
	}
	if err := json.Unmarshal(change.After, &after); err != nil {
		t.Fatalf("decode after: %v", err)
	}
	if before.Description != "first" || after.Description != "second" {
		t.Fatalf("before/after descriptions got %q/%q, want first/second", before.Description, after.Description)
	}
	requireNoSecret(t, change, value)
}

func TestAuditChange_VirtualKeyUpdateRejectedRecordsNothing(t *testing.T) {
	h, store := newAuditVKHandler(t)
	_, vkID, _ := createAuditVK(t, h, store)

	ctx := newTestRequestCtx(`{"expires_at":"not-a-time"}`)
	ctx.SetUserValue("vk_id", vkID)
	h.updateVirtualKey(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("update status=%d body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if changes := auditChanges(t, ctx); len(changes) != 0 {
		t.Fatalf("a rejected update must record nothing, got %+v", changes)
	}
}

func TestAuditChange_VirtualKeyDelete(t *testing.T) {
	h, store := newAuditVKHandler(t)
	_, vkID, value := createAuditVK(t, h, store)

	ctx := newTestRequestCtx("")
	ctx.SetUserValue("vk_id", vkID)
	h.deleteVirtualKey(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("delete status=%d body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}

	change := requireSingleAuditChange(t, ctx, "virtual_key.deleted")
	if change.TargetID != vkID || change.TargetName != "audit-vk" {
		t.Fatalf("unexpected target: %+v", change)
	}
	if len(change.Before) == 0 || change.After != nil {
		t.Fatalf("delete must carry only a before snapshot: before=%s after=%s", change.Before, change.After)
	}
	requireNoSecret(t, change, value)
}

func TestAuditChange_ProviderKeyUpdate(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	const storedValue = "sk-stored-audit-raw-0123456789abcdef"
	const newValue = "sk-updated-audit-raw-fedcba9876543210"

	h := &ProviderHandler{
		config: libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {},
		}, map[schemas.ModelProvider][]schemas.Key{
			schemas.OpenAI: {{
				ID:     "key-1",
				Name:   "openai-key",
				Value:  *schemas.NewSecretVar(storedValue),
				Weight: 1,
			}},
		}),
		modelsManager: &mockModelsManager{},
	}
	attachBifrostClient(t, h.config)

	ctx := newTestRequestCtx(`{"name":"openai-key","value":"` + newValue + `","weight":2}`)
	ctx.SetUserValue("provider", string(schemas.OpenAI))
	ctx.SetUserValue("key_id", "key-1")
	h.updateProviderKey(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("update status=%d body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if got := h.config.GetProviderKeys(schemas.OpenAI)[0].Value.GetValue(); got != newValue {
		t.Fatalf("stored value not updated, got %q", got)
	}

	change := requireSingleAuditChange(t, ctx, "provider_key.updated")
	if change.TargetType != "provider_key" || change.TargetID != "key-1" || change.TargetName != "openai-key" {
		t.Fatalf("unexpected target: %+v", change)
	}
	if len(change.Before) == 0 || len(change.After) == 0 {
		t.Fatalf("update must carry both snapshots: before=%s after=%s", change.Before, change.After)
	}
	requireNoSecret(t, change, storedValue)
	requireNoSecret(t, change, newValue)
}

func TestAuditSkillOmitsFreeFormValues(t *testing.T) {
	snap := auditSkill(&configstoreTables.TableSkill{
		Name:             "s",
		Metadata:         configstoreTables.SkillStringMap{"api_key": "plaintext-secret"},
		ExtraFrontmatter: configstoreTables.SkillJSONMap{"token": "plaintext-secret"},
	})
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "plaintext-secret") {
		t.Fatalf("skill snapshot leaks free-form values: %s", raw)
	}
	if !strings.Contains(string(raw), "api_key") || !strings.Contains(string(raw), "token") {
		t.Fatalf("skill snapshot should still list the keys: %s", raw)
	}
}

func TestAuditChange_VirtualKeyDeleteSnapshotIncludesBudgets(t *testing.T) {
	h, _ := newAuditVKHandler(t)
	createCtx := newTestRequestCtx(`{"name":"audit-vk","customer_id":"cust-audit","budgets":[{"max_limit":123.5,"reset_duration":"1d"}]}`)
	h.createVirtualKey(createCtx)
	if createCtx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("create status=%d body=%s", createCtx.Response.StatusCode(), createCtx.Response.Body())
	}
	var resp struct {
		VirtualKey struct {
			ID string `json:"id"`
		} `json:"virtual_key"`
	}
	if err := json.Unmarshal(createCtx.Response.Body(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	ctx := newTestRequestCtx("")
	ctx.SetUserValue("vk_id", resp.VirtualKey.ID)
	h.deleteVirtualKey(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("delete status=%d body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	change := requireSingleAuditChange(t, ctx, "virtual_key.deleted")
	if !strings.Contains(string(change.Before), "123.5") {
		t.Fatalf("deletion snapshot lost the key's budget: %s", change.Before)
	}
}

// auditVKUpdateStore serves the MCP update handler's VK assignment read with no rows.
// auditVKUpdateStore fails the VK assignment step's read, which runs after the
// client write has committed.
type auditVKUpdateStore struct {
	mockUpdateConfigStore
}

// GetVirtualKeyMCPConfigsByMCPClientID fails, as a store outage would.
func (s *auditVKUpdateStore) GetVirtualKeyMCPConfigsByMCPClientID(context.Context, uint) ([]configstoreTables.TableVirtualKeyMCPConfig, error) {
	return nil, errors.New("store unavailable")
}

// TestAuditChange_MCPClientUpdateRecordedWhenVKAssignmentFails pins that an
// update whose client write committed is audited even when the VK assignment
// step after it fails, and that the after snapshot keeps the fields the handler
// does not rebuild. Invalid vk_configs are refused before the client write, so
// the failure here is the store's.
func TestAuditChange_MCPClientUpdateRecordedWhenVKAssignmentFails(t *testing.T) {
	SetLogger(&mockLogger{})
	existing := &schemas.MCPClientConfig{
		ID:             "client-audit",
		Name:           "audit_mcp",
		EndpointSlug:   "audit-slug",
		ConnectionType: schemas.MCPConnectionTypeHTTP,
		AuthType:       schemas.MCPAuthTypeNone,
	}
	store := &lib.Config{
		MCPConfig:    &schemas.MCPConfig{},
		ClientConfig: &configstore.ClientConfig{},
	}
	libtest.MCPClients(t, store, existing)
	cfgStore := &auditVKUpdateStore{}
	store.ConfigStore = cfgStore
	h := &MCPHandler{store: store, mcpManager: &fakeMCPManagerVerifyOnly{}}

	disabled := true
	body, err := json.Marshal(MCPClientUpdateRequest{
		Disabled:  &disabled,
		VKConfigs: &[]MCPVKConfigRequest{{VirtualKeyID: "vk-1", ToolsToExecute: schemas.WhiteList{"*"}}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ctx := newBareRequestCtx()
	ctx.SetUserValue("id", existing.ID)
	ctx.Request.SetBody(body)
	h.updateMCPClient(ctx)

	if code := ctx.Response.StatusCode(); code != fasthttp.StatusInternalServerError {
		t.Fatalf("expected 500 from the VK assignment step, got %d: %s", code, ctx.Response.Body())
	}
	if cfgStore.updates != 1 {
		t.Fatalf("expected the client write to commit before the VK step, got %d writes", cfgStore.updates)
	}
	change := requireSingleAuditChange(t, ctx, "mcp_client.updated")
	if !strings.Contains(string(change.After), `"endpoint_slug":"audit-slug"`) {
		t.Fatalf("after snapshot dropped the unchanged endpoint slug: %s", change.After)
	}
}

// failingRemoveMCPManager fails the runtime removal after the row is deleted.
type failingRemoveMCPManager struct {
	MCPManager
}

func (failingRemoveMCPManager) RemoveMCPClient(context.Context, string) error {
	return errors.New("runtime removal failed")
}

// TestAuditChange_MCPClientDeleteRecordedWhenRuntimeRemovalFails pins that a
// delete is audited once the row is gone, even when the runtime removal fails.
func TestAuditChange_MCPClientDeleteRecordedWhenRuntimeRemovalFails(t *testing.T) {
	SetLogger(&mockLogger{})
	cfgStore := newRealOAuth2Store(t)
	if err := cfgStore.CreateMCPClientConfig(context.Background(), &schemas.MCPClientConfig{
		ID:               "mcp-audit-delete",
		Name:             "audit_delete",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"),
	}); err != nil {
		t.Fatalf("create client: %v", err)
	}
	h := &MCPHandler{store: &lib.Config{LiveStore: liveStoreForTest(t), ConfigStore: cfgStore, ClientConfig: &configstore.ClientConfig{}}, mcpManager: failingRemoveMCPManager{}}

	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodDelete)
	ctx := initCtx(&req)
	ctx.SetUserValue("id", "mcp-audit-delete")
	h.deleteMCPClient(ctx)

	if code := ctx.Response.StatusCode(); code != fasthttp.StatusInternalServerError {
		t.Fatalf("expected 500 from the runtime removal, got %d: %s", code, ctx.Response.Body())
	}
	change := requireSingleAuditChange(t, ctx, "mcp_client.deleted")
	if change.TargetID != "mcp-audit-delete" {
		t.Fatalf("unexpected target: %+v", change)
	}
}

// failingProxyReloadManager fails the proxy reload after the config is saved.
type failingProxyReloadManager struct {
	stubConfigManager
}

func (failingProxyReloadManager) ReloadProxyConfig(context.Context, *configstoreTables.GlobalProxyConfig) error {
	return errors.New("reload failed")
}

// TestAuditChange_ProxyConfigRecordedWhenReloadFails pins that a saved proxy
// config is audited even when the live reload after it fails.
func TestAuditChange_ProxyConfigRecordedWhenReloadFails(t *testing.T) {
	SetLogger(&mockLogger{})
	h := &ConfigHandler{store: &lib.Config{ConfigStore: setupPricingOverrideHandlerStore(t)}, configManager: failingProxyReloadManager{}}

	ctx := newTestRequestCtx(`{"enabled":false,"type":"http","url":"http://proxy.example:8080"}`)
	h.updateProxyConfig(ctx)

	if code := ctx.Response.StatusCode(); code != fasthttp.StatusInternalServerError {
		t.Fatalf("expected 500 from the reload, got %d: %s", code, ctx.Response.Body())
	}
	change := requireSingleAuditChange(t, ctx, "proxy_config.updated")
	if !strings.Contains(string(change.After), "proxy.example") {
		t.Fatalf("after snapshot missing the saved URL: %s", change.After)
	}
}

// snapshotReadFailingStore fails the reads the proxy and metadata handlers
// take their before snapshots from, and counts the writes that follow.
type snapshotReadFailingStore struct {
	configstore.ConfigStore
	writes int
}

func (s *snapshotReadFailingStore) GetProxyConfig(context.Context) (*configstoreTables.GlobalProxyConfig, error) {
	return nil, errors.New("read failed")
}

func (s *snapshotReadFailingStore) UpdateProxyConfig(context.Context, *configstoreTables.GlobalProxyConfig) error {
	s.writes++
	return nil
}

func (s *snapshotReadFailingStore) GetClientMetadata(context.Context) (map[string]any, error) {
	return nil, errors.New("read failed")
}

func (s *snapshotReadFailingStore) UpdateClientMetadata(context.Context, map[string]any) error {
	s.writes++
	return nil
}

// TestAuditChange_SnapshotReadFailureBlocksWrite pins that a failed before
// read fails the request instead of writing and auditing an empty before.
func TestAuditChange_SnapshotReadFailureBlocksWrite(t *testing.T) {
	SetLogger(&mockLogger{})
	for name, run := range map[string]func(*ConfigHandler, *fasthttp.RequestCtx){
		"proxy":    (*ConfigHandler).updateProxyConfig,
		"metadata": (*ConfigHandler).updateMetadata,
	} {
		t.Run(name, func(t *testing.T) {
			cfgStore := &snapshotReadFailingStore{}
			h := &ConfigHandler{store: &lib.Config{ConfigStore: cfgStore}, configManager: stubConfigManager{}}
			body := `{"theme":"dark"}`
			if name == "proxy" {
				body = `{"enabled":false,"type":"http","url":""}`
			}
			ctx := newTestRequestCtx(body)
			run(h, ctx)

			if code := ctx.Response.StatusCode(); code != fasthttp.StatusInternalServerError {
				t.Fatalf("expected 500, got %d: %s", code, ctx.Response.Body())
			}
			if cfgStore.writes != 0 {
				t.Fatalf("expected no write after a failed snapshot read, got %d", cfgStore.writes)
			}
			if changes := auditChanges(t, ctx); len(changes) != 0 {
				t.Fatalf("expected no audit change, got %+v", changes)
			}
		})
	}
}

func TestAuditDeletedIDsCapsTheList(t *testing.T) {
	ids := make([]string, auditDeletedIDsLimit+5)
	for i := range ids {
		ids[i] = "log-id"
	}
	snap := auditDeletedIDs(ids)
	if snap["count"] != len(ids) || snap["ids_truncated"] != true {
		t.Fatalf("expected full count and truncation flag, got %+v", snap)
	}
	if got := len(snap["ids"].([]string)); got != auditDeletedIDsLimit {
		t.Fatalf("expected %d ids, got %d", auditDeletedIDsLimit, got)
	}
	if small := auditDeletedIDs([]string{"a"}); small["ids_truncated"] != nil {
		t.Fatalf("a small delete must not be marked truncated: %+v", small)
	}
}

// TestAuditMCPClientMasksStdioCommandLine pins that an STDIO client's
// arguments and inline env values never reach the audit snapshot, while env
// names and the argument count stay so a change is still visible.
func TestAuditMCPClientMasksStdioCommandLine(t *testing.T) {
	cfg := &schemas.MCPClientConfig{
		ID:             "stdio-audit",
		Name:           "stdio_audit",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "npx",
			Args:    []string{"server", "--token=arg-secret"},
			Envs:    []string{"API_TOKEN=env-secret", "HOME"},
		},
	}
	snap := string(auditMCPClient(&lib.Config{}, cfg))
	for _, secret := range []string{"arg-secret", "env-secret"} {
		if strings.Contains(snap, secret) {
			t.Fatalf("snapshot leaks %q: %s", secret, snap)
		}
	}
	var decoded struct {
		StdioConfig schemas.MCPStdioConfig `json:"stdio_config"`
	}
	if err := json.Unmarshal([]byte(snap), &decoded); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	got := decoded.StdioConfig
	if got.Command != "npx" || len(got.Args) != 2 || got.Args[1] != "<redacted>" {
		t.Fatalf("expected the command kept and every argument masked, got %+v", got)
	}
	if len(got.Envs) != 2 || got.Envs[0] != "API_TOKEN=<redacted>" || got.Envs[1] != "HOME" {
		t.Fatalf("expected env names kept and inline values masked, got %+v", got.Envs)
	}
	if cfg.StdioConfig.Args[1] != "--token=arg-secret" || cfg.StdioConfig.Envs[0] != "API_TOKEN=env-secret" {
		t.Fatalf("masking mutated the live config: %+v", cfg.StdioConfig)
	}
}

// TestAuditChange_ProxyURLPasswordMaskedWhenURLDoesNotParse pins that the audit snapshot
// masks a proxy URL's password even when the URL does not parse. A disabled update is not
// validated, so the restored password can be saved inside a malformed URL.
func TestAuditChange_ProxyURLPasswordMaskedWhenURLDoesNotParse(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	if err := store.UpdateProxyConfig(context.Background(), &configstoreTables.GlobalProxyConfig{
		Enabled: true, Type: "http", URL: "http://alice:secret@proxy.example:8080",
	}); err != nil {
		t.Fatalf("seed proxy config: %v", err)
	}
	h := &ConfigHandler{store: &lib.Config{ConfigStore: store}, configManager: &proxyReloadRecorder{}}

	ctx := newTestRequestCtx(`{"enabled":false,"type":"http","url":"http://alice:<redacted>@proxy.example:bad"}`)
	h.updateProxyConfig(ctx)

	if code := ctx.Response.StatusCode(); code != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", code, ctx.Response.Body())
	}
	change := requireSingleAuditChange(t, ctx, "proxy_config.updated")
	if strings.Contains(string(change.Before), "secret") || strings.Contains(string(change.After), "secret") {
		t.Fatalf("audit snapshot carries the proxy password: before %s, after %s", change.Before, change.After)
	}
	if !strings.Contains(string(change.After), "proxy.example:bad") {
		t.Fatalf("after snapshot missing the saved URL: %s", change.After)
	}
}

// commitPointTestManager reads reloads from the real store and fails the method named in fail.
type commitPointTestManager struct {
	pricingOverrideTestGovernanceManager
	store configstore.ConfigStore
	fail  string
}

var errCommitPointInjected = errors.New("injected failure after commit")

// commitPointReloadedAt marks a row returned by a commitPointTestManager reload, so a test can tell it from the commit-time row.
var commitPointReloadedAt = time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)

func (m *commitPointTestManager) ReloadVirtualKey(ctx context.Context, id string) (*configstoreTables.TableVirtualKey, error) {
	if m.fail == "ReloadVirtualKey" {
		return nil, errCommitPointInjected
	}
	vk, err := m.store.GetVirtualKey(ctx, id)
	if err == nil && vk != nil {
		vk.UpdatedAt = commitPointReloadedAt
	}
	return vk, err
}

func (m *commitPointTestManager) RemoveVirtualKey(context.Context, string) error { return nil }

func (m *commitPointTestManager) ReloadTeam(ctx context.Context, id string) (*configstoreTables.TableTeam, error) {
	if m.fail == "ReloadTeam" {
		return nil, errCommitPointInjected
	}
	team, err := m.store.GetTeam(ctx, id)
	if err == nil && team != nil {
		team.UpdatedAt = commitPointReloadedAt
	}
	return team, err
}

func (m *commitPointTestManager) ReloadCustomer(ctx context.Context, id string) (*configstoreTables.TableCustomer, error) {
	if m.fail == "ReloadCustomer" {
		return nil, errCommitPointInjected
	}
	customer, err := m.store.GetCustomer(ctx, id)
	if err == nil && customer != nil {
		customer.UpdatedAt = commitPointReloadedAt
	}
	return customer, err
}

func (m *commitPointTestManager) ReloadModelConfig(ctx context.Context, id string) (*configstoreTables.TableModelConfig, error) {
	if m.fail == "ReloadModelConfig" {
		return nil, errCommitPointInjected
	}
	mc, err := m.store.GetModelConfigByID(ctx, id)
	if err == nil && mc != nil {
		mc.UpdatedAt = commitPointReloadedAt
	}
	return mc, err
}

func (m *commitPointTestManager) ResetBudgetUsageInMemory(context.Context, BudgetUsageResetOwner, []string) error {
	if m.fail == "ResetBudgetUsageInMemory" {
		return errCommitPointInjected
	}
	return nil
}

func (m *commitPointTestManager) UpsertPricingOverride(context.Context, *configstoreTables.TablePricingOverride) error {
	if m.fail == "UpsertPricingOverride" {
		return errCommitPointInjected
	}
	return nil
}

// liveAuditChanges returns the recorded changes a handler did not discard.
func liveAuditChanges(ctx *fasthttp.RequestCtx) []configstore.AuditChange {
	changes, _ := ctx.UserValue(configstore.AuditChangesUserValueKey).([]configstore.AuditChange)
	out := make([]configstore.AuditChange, 0, len(changes))
	for _, c := range changes {
		if !c.Discarded {
			out = append(out, c)
		}
	}
	return out
}

// requireCommitPointChange asserts exactly one live change named eventName whose after
// snapshot has field equal to want.
func requireCommitPointChange(t *testing.T, ctx *fasthttp.RequestCtx, eventName, field string, want any) configstore.AuditChange {
	t.Helper()
	changes := liveAuditChanges(ctx)
	if len(changes) != 1 || changes[0].EventName != eventName {
		t.Fatalf("want one %s change, got %+v", eventName, changes)
	}
	var after map[string]any
	if err := json.Unmarshal(changes[0].After, &after); err != nil {
		t.Fatalf("decode after %s: %v", changes[0].After, err)
	}
	if field != "" && after[field] != want {
		t.Fatalf("after.%s = %v, want %v (after=%s)", field, after[field], want, changes[0].After)
	}
	return changes[0]
}

// requireRecordedOrNone asserts exactly one change named eventName when recorded, and none otherwise.
func requireRecordedOrNone(t *testing.T, changes []configstore.AuditChange, recorded bool, eventName string) {
	t.Helper()
	if recorded && (len(changes) != 1 || changes[0].EventName != eventName) {
		t.Fatalf("want one %s change, got %+v", eventName, changes)
	}
	if !recorded && len(changes) != 0 {
		t.Fatalf("want no changes, got %+v", changes)
	}
}

func requireStatus(t *testing.T, ctx *fasthttp.RequestCtx, want int) {
	t.Helper()
	if got := ctx.Response.StatusCode(); got != want {
		t.Fatalf("status = %d, want %d: %s", got, want, ctx.Response.Body())
	}
}

func newCommitPointGovernanceHandler(t *testing.T) (*GovernanceHandler, *commitPointTestManager, configstore.ConfigStore) {
	t.Helper()
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	if err := store.CreateCustomer(context.Background(), &configstoreTables.TableCustomer{ID: "cust-audit", Name: "Audit Customer"}); err != nil {
		t.Fatalf("create customer: %v", err)
	}
	manager := &commitPointTestManager{store: store}
	return &GovernanceHandler{configStore: store, governanceManager: manager}, manager, store
}

func TestCommitPoint_VirtualKeyUpdateRecordedWhenReloadFails(t *testing.T) {
	h, manager, store := newCommitPointGovernanceHandler(t)
	_, vkID, value := createAuditVK(t, h, store)
	manager.fail = "ReloadVirtualKey"

	ctx := newTestRequestCtx(`{"description":"second"}`)
	ctx.SetUserValue("vk_id", vkID)
	h.updateVirtualKey(ctx)

	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	change := requireCommitPointChange(t, ctx, "virtual_key.updated", "description", "second")
	requireNoSecret(t, change, value)
}

func TestCommitPoint_BudgetOverrideRecordedWhenReloadFails(t *testing.T) {
	h, manager, store := newCommitPointGovernanceHandler(t)
	ctx := context.Background()
	active := true
	vk := &configstoreTables.TableVirtualKey{ID: "vk-override", Name: "override", Value: *schemas.NewSecretVar("sk-bf-override"), IsActive: &active}
	if err := store.CreateVirtualKey(ctx, vk); err != nil {
		t.Fatalf("create virtual key: %v", err)
	}
	scopeID := vk.ID
	if err := store.CreateModelConfig(ctx, &configstoreTables.TableModelConfig{
		ID: "mc-override", ModelName: configstoreTables.ModelConfigAllModels, Scope: configstoreTables.ModelConfigScopeVirtualKey, ScopeID: &scopeID,
		Budgets: []configstoreTables.TableBudget{{ID: "budget-override", MaxLimit: 100, ResetDuration: "1d"}},
	}); err != nil {
		t.Fatalf("create model config: %v", err)
	}
	manager.fail = "ReloadVirtualKey"

	req := newTestRequestCtx(`{"amount":25,"mode":"cycles","cycles":4}`)
	req.SetUserValue("vk_id", vk.ID)
	req.SetUserValue("budget_id", "budget-override")
	h.updateVirtualKeyBudgetOverride(req)

	requireStatus(t, req, fasthttp.StatusInternalServerError)
	change := requireCommitPointChange(t, req, "virtual_key.budget_override_updated", "override_mode", "cycles")
	var after struct {
		Amount    *float64 `json:"override_amount"`
		Remaining *int     `json:"override_cycles_remaining"`
		Total     *int     `json:"override_cycles_total"`
		MaxLimit  float64  `json:"max_limit"`
	}
	if err := json.Unmarshal(change.After, &after); err != nil {
		t.Fatalf("decode after: %v", err)
	}
	if after.Amount == nil || *after.Amount != 25 || after.Remaining == nil || *after.Remaining != 4 || after.Total == nil || *after.Total != 4 || after.MaxLimit != 100 {
		t.Fatalf("after = %s, want amount 25, 4 of 4 cycles and max_limit 100", change.After)
	}
}

func TestCommitPoint_TeamUpdate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fail   string
		status int
	}{
		{"success records the reloaded row", "", fasthttp.StatusOK},
		{"reload failure still records the write", "ReloadTeam", fasthttp.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, manager, store := newCommitPointGovernanceHandler(t)
			if err := store.CreateTeam(context.Background(), &configstoreTables.TableTeam{ID: "team-1", Name: "before"}); err != nil {
				t.Fatalf("create team: %v", err)
			}
			manager.fail = tc.fail
			ctx := newTestRequestCtx(`{"name":"after"}`)
			ctx.SetUserValue("team_id", "team-1")
			h.updateTeam(ctx)
			requireStatus(t, ctx, tc.status)
			requireCommitPointChange(t, ctx, "team.updated", "name", "after")
		})
	}
}

func TestCommitPoint_CustomerUpdate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fail   string
		status int
	}{
		{"success records the reloaded row", "", fasthttp.StatusOK},
		{"reload failure still records the write", "ReloadCustomer", fasthttp.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, manager, _ := newCommitPointGovernanceHandler(t)
			manager.fail = tc.fail
			ctx := newTestRequestCtx(`{"name":"Renamed Customer"}`)
			ctx.SetUserValue("customer_id", "cust-audit")
			h.updateCustomer(ctx)
			requireStatus(t, ctx, tc.status)
			requireCommitPointChange(t, ctx, "customer.updated", "name", "Renamed Customer")
		})
	}
}

// TestCommitPoint_GovernanceCreates pins both halves of a create's audit event: a successful
// create records the reloaded row (stamped with reloadedAt by the test manager), and a create
// whose reload fails still records the committed row together with the child rows saved in
// the same transaction, such as its rate limit.
func TestCommitPoint_GovernanceCreates(t *testing.T) {
	const rateLimit = `"rate_limit":{"request_max_limit":7,"request_reset_duration":"1m"}`
	for _, tc := range []struct {
		name      string
		fail      string
		body      string
		create    func(h *GovernanceHandler) fasthttp.RequestHandler
		event     string
		field     string
		want      string
		childRows bool
	}{
		{"virtual key", "ReloadVirtualKey", `{"name":"audit-vk","customer_id":"cust-audit",` + rateLimit + `,"budgets":[{"max_limit":5,"reset_duration":"1M"}]}`, func(h *GovernanceHandler) fasthttp.RequestHandler { return h.createVirtualKey }, "virtual_key.created", "name", "audit-vk", true},
		{"team", "ReloadTeam", `{"name":"audit-team",` + rateLimit + `}`, func(h *GovernanceHandler) fasthttp.RequestHandler { return h.createTeam }, "team.created", "name", "audit-team", true},
		{"customer", "ReloadCustomer", `{"name":"audit-customer",` + rateLimit + `}`, func(h *GovernanceHandler) fasthttp.RequestHandler { return h.createCustomer }, "customer.created", "name", "audit-customer", true},
		{"model config", "ReloadModelConfig", `{"model_name":"gpt-4.1"}`, func(h *GovernanceHandler) fasthttp.RequestHandler { return h.createModelConfig }, "model_config.created", "model_name", "gpt-4.1", false},
	} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s reload fails=%v", tc.name, fail), func(t *testing.T) {
				h, manager, _ := newCommitPointGovernanceHandler(t)
				status := fasthttp.StatusOK
				if fail {
					manager.fail = tc.fail
					status = fasthttp.StatusInternalServerError
				}
				ctx := newTestRequestCtx(tc.body)
				tc.create(h)(ctx)
				requireStatus(t, ctx, status)
				change := requireCommitPointChange(t, ctx, tc.event, tc.field, tc.want)
				if strings.Contains(string(change.After), `"value"`) {
					t.Fatalf("after snapshot carries a key value: %s", change.After)
				}
				var after map[string]any
				if err := json.Unmarshal(change.After, &after); err != nil {
					t.Fatalf("decode after %s: %v", change.After, err)
				}
				reloaded := after["updated_at"] == commitPointReloadedAt.Format(time.RFC3339Nano)
				if reloaded == fail {
					t.Fatalf("after snapshot reloaded=%v, want %v (after=%s)", reloaded, !fail, change.After)
				}
				if !tc.childRows {
					return
				}
				// A legacy rate_limit carrying only request fields is saved as one request limit, and no
				// rate limit is left holding request fields.
				rules, _ := after["request_limits"].([]any)
				if len(rules) != 1 {
					t.Fatalf("after snapshot holds %d request limits, want 1: %s", len(rules), change.After)
				}
				if rule, _ := rules[0].(map[string]any); rule == nil || rule["request_max_limit"] != float64(7) || rule["request_reset_duration"] != "1m" {
					t.Fatalf("after snapshot's request limit is not 7 per 1m: %s", change.After)
				}
				if rl, _ := after["rate_limit"].(map[string]any); rl != nil && (rl["request_max_limit"] != nil || rl["request_reset_duration"] != nil) {
					t.Fatalf("after snapshot's rate limit still carries request fields: %s", change.After)
				}
				if tc.name == "virtual key" {
					if budgets, _ := after["budgets"].([]any); len(budgets) != 1 {
						t.Fatalf("after snapshot missing the saved budget: %s", change.After)
					}
				}
			})
		}
	}
}

func TestCommitPoint_ModelConfigUpdate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fail   string
		status int
	}{
		{"success records the reloaded row", "", fasthttp.StatusOK},
		{"reload failure still records the write", "ReloadModelConfig", fasthttp.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, manager, store := newCommitPointGovernanceHandler(t)
			if err := store.CreateModelConfig(context.Background(), &configstoreTables.TableModelConfig{ID: "mc-1", ModelName: "gpt-4o", Scope: configstoreTables.ModelConfigScopeGlobal}); err != nil {
				t.Fatalf("create model config: %v", err)
			}
			manager.fail = tc.fail
			ctx := newTestRequestCtx(`{"model_name":"gpt-4.1"}`)
			ctx.SetUserValue("mc_id", "mc-1")
			h.updateModelConfig(ctx)
			requireStatus(t, ctx, tc.status)
			requireCommitPointChange(t, ctx, "model_config.updated", "model_name", "gpt-4.1")
		})
	}
}

func TestCommitPoint_ProviderGovernanceRecordedWhenUsageResetFails(t *testing.T) {
	h, manager, store := newCommitPointGovernanceHandler(t)
	if err := store.AddProvider(context.Background(), schemas.OpenAI, configstore.ProviderConfig{}); err != nil {
		t.Fatalf("add provider: %v", err)
	}
	seed := newTestRequestCtx(`{"budgets":[{"max_limit":10,"reset_duration":"1d"}]}`)
	seed.SetUserValue("provider_name", "openai")
	h.updateProviderGovernance(seed)
	requireStatus(t, seed, fasthttp.StatusOK)

	manager.fail = "ResetBudgetUsageInMemory"
	ctx := newTestRequestCtx(`{"budgets":[{"max_limit":20,"reset_duration":"1d"}],"reset_budget_usage":true}`)
	ctx.SetUserValue("provider_name", "openai")
	h.updateProviderGovernance(ctx)

	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	change := requireCommitPointChange(t, ctx, "provider_governance.updated", "provider", "openai")
	var after ProviderGovernanceResponse
	if err := json.Unmarshal(change.After, &after); err != nil {
		t.Fatalf("decode after: %v", err)
	}
	if len(after.Budgets) != 1 || after.Budgets[0].MaxLimit != 20 {
		t.Fatalf("after = %s, want the new max_limit 20", change.After)
	}
}

// TestCommitPoint_ProviderGovernanceRecordedWhenReloadFails pins that a provider's saved limits whose
// re-read fails answer 500, saved but not loaded, with the change already recorded as saved.
func TestCommitPoint_ProviderGovernanceRecordedWhenReloadFails(t *testing.T) {
	h, manager, store := newCommitPointGovernanceHandler(t)
	if err := store.AddProvider(context.Background(), schemas.OpenAI, configstore.ProviderConfig{}); err != nil {
		t.Fatalf("add provider: %v", err)
	}
	manager.fail = "ReloadModelConfig"
	ctx := newTestRequestCtx(`{"request_limits":[{"max_limit":5,"reset_duration":"1m"}]}`)
	ctx.SetUserValue("provider_name", "openai")
	h.updateProviderGovernance(ctx)

	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	if body := string(ctx.Response.Body()); !strings.Contains(body, "Provider governance updated in database but failed to reload in-memory state") {
		t.Fatalf("body = %s, want the saved-but-not-loaded error", body)
	}
	change := requireCommitPointChange(t, ctx, "provider_governance.updated", "provider", "openai")
	var after ProviderGovernanceResponse
	if err := json.Unmarshal(change.After, &after); err != nil {
		t.Fatalf("decode after: %v", err)
	}
	if len(after.RequestLimits) != 1 || after.RequestLimits[0].RequestMaxLimit == nil || *after.RequestLimits[0].RequestMaxLimit != 5 {
		t.Fatalf("after = %s, want the saved request limit of 5", change.After)
	}
}

func TestCommitPoint_PricingOverrideRecordedWhenUpsertFails(t *testing.T) {
	h, manager, _ := newCommitPointGovernanceHandler(t)
	manager.fail = "UpsertPricingOverride"
	body := `{"name":"Override","scope_kind":"global","match_type":"exact","pattern":"gpt-4.1","request_types":["chat_completion"],"patch":{"input_cost_per_token":1}}`

	create := newTestRequestCtx(body)
	h.createPricingOverride(create)
	requireStatus(t, create, fasthttp.StatusInternalServerError)
	created := requireCommitPointChange(t, create, "pricing_override.created", "name", "Override")

	update := newTestRequestCtx(`{"name":"Renamed","scope_kind":"global","match_type":"exact","pattern":"gpt-4.1","request_types":["chat_completion"],"patch":{"input_cost_per_token":2}}`)
	update.SetUserValue("id", created.TargetID)
	h.updatePricingOverride(update)
	requireStatus(t, update, fasthttp.StatusInternalServerError)
	requireCommitPointChange(t, update, "pricing_override.updated", "name", "Renamed")
}

// keyReadBackFailingStore commits key writes but fails the read-back that follows them.
type keyReadBackFailingStore struct {
	configstore.ConfigStore
}

func (s *keyReadBackFailingStore) GetProviderKey(context.Context, schemas.ModelProvider, string) (*configstoreTables.TableKey, error) {
	return nil, errCommitPointInjected
}

func newKeyReadBackFailingHandler(t *testing.T, keys ...schemas.Key) (*ProviderHandler, configstore.ConfigStore) {
	t.Helper()
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	cfg := libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{schemas.OpenAI: {}}, map[schemas.ModelProvider][]schemas.Key{schemas.OpenAI: keys})
	attachBifrostClient(t, cfg)
	store := cfg.ConfigStore
	h := &ProviderHandler{
		configStore:   &keyReadBackFailingStore{ConfigStore: store},
		config:        cfg,
		modelsManager: &mockModelsManager{},
	}
	return h, store
}

func TestCommitPoint_ProviderKeyCreateRecordedWhenReadBackFails(t *testing.T) {
	h, store := newKeyReadBackFailingHandler(t)
	const secret = "sk-commit-point-create-0123456789abcdef"

	ctx := newTestRequestCtx(`{"name":"commit-key","value":"` + secret + `","weight":1}`)
	ctx.SetUserValue("provider", string(schemas.OpenAI))
	h.createProviderKey(ctx)

	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	keys, err := store.GetProviderKeys(context.Background(), schemas.OpenAI)
	if err != nil || len(keys) != 1 {
		t.Fatalf("expected the key to be stored, got %+v %v", keys, err)
	}
	change := requireCommitPointChange(t, ctx, "provider_key.created", "name", "commit-key")
	requireNoSecret(t, change, secret)
}

func TestCommitPoint_ProviderKeyUpdateRecordedWhenReadBackFails(t *testing.T) {
	const oldSecret = "sk-commit-point-old-0123456789abcdef"
	const newSecret = "sk-commit-point-new-fedcba9876543210"
	key := schemas.Key{ID: "key-1", Name: "commit-key", Value: *schemas.NewSecretVar(oldSecret), Weight: 1}
	h, _ := newKeyReadBackFailingHandler(t, key)

	ctx := newTestRequestCtx(`{"name":"renamed-key","value":"` + newSecret + `","weight":2}`)
	ctx.SetUserValue("provider", string(schemas.OpenAI))
	ctx.SetUserValue("key_id", "key-1")
	h.updateProviderKey(ctx)

	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	change := requireCommitPointChange(t, ctx, "provider_key.updated", "name", "renamed-key")
	requireNoSecret(t, change, oldSecret)
	requireNoSecret(t, change, newSecret)
}

func TestCommitPoint_DeleteProvider(t *testing.T) {
	for _, tc := range []struct {
		name      string
		removeErr error
		status    int
		recorded  bool
	}{
		{"success", nil, fasthttp.StatusOK, true},
		{"already deleted", lib.ErrNotFound, fasthttp.StatusOK, false},
		{"nothing deleted", errors.New("client refused the removal"), fasthttp.StatusInternalServerError, false},
		{"deleted, cleanup failed", &lib.SavedNotAppliedError{Err: errors.New("pricing manager not found")}, fasthttp.StatusInternalServerError, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			h := &ProviderHandler{
				config:        libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{schemas.OpenAI: {}}, nil),
				modelsManager: &mockModelsManager{remove: func(context.Context, schemas.ModelProvider) error { return tc.removeErr }},
			}
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetMethod(fasthttp.MethodDelete)
			ctx.SetUserValue("provider", string(schemas.OpenAI))
			h.deleteProvider(ctx)

			requireStatus(t, ctx, tc.status)
			changes := liveAuditChanges(ctx)
			requireRecordedOrNone(t, changes, tc.recorded, "provider.deleted")
		})
	}
}

// failingRuntimeUpdateMCPManager fails the in-memory update after the row is written.
type failingRuntimeUpdateMCPManager struct {
	fakeMCPManagerVerifyOnly
}

func (*failingRuntimeUpdateMCPManager) UpdateMCPClient(context.Context, string, *schemas.MCPClientConfig) error {
	return errors.New("runtime update failed")
}

// rollbackStore commits the first client write and fails the rollback write when failRollback is set.
type rollbackStore struct {
	mockUpdateConfigStore
	failRollback bool
}

func (s *rollbackStore) UpdateMCPClientConfig(ctx context.Context, id string, row *configstoreTables.TableMCPClient) error {
	s.updates++
	if s.updates > 1 && s.failRollback {
		return errors.New("rollback write failed")
	}
	return nil
}

func TestCommitPoint_MCPClientUpdateRuntimeFailure(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failRollback bool
		recorded     bool
	}{
		{"rollback restores the row", false, false},
		{"rollback fails so the write stands", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			existing := &schemas.MCPClientConfig{ID: "client-rb", Name: "rb_mcp", EndpointSlug: "rb-slug", ConnectionType: schemas.MCPConnectionTypeHTTP, AuthType: schemas.MCPAuthTypeNone}
			store := libtest.MCPClients(t, &lib.Config{
				MCPConfig:    &schemas.MCPConfig{},
				ClientConfig: &configstore.ClientConfig{},
			}, existing)
			cfgStore := &rollbackStore{failRollback: tc.failRollback}
			store.ConfigStore = cfgStore
			h := &MCPHandler{store: store, mcpManager: &failingRuntimeUpdateMCPManager{}}

			disabled := true
			body, err := json.Marshal(MCPClientUpdateRequest{Disabled: &disabled})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			ctx := &fasthttp.RequestCtx{}
			ctx.SetUserValue("id", existing.ID)
			ctx.Request.SetBody(body)
			h.updateMCPClient(ctx)

			requireStatus(t, ctx, fasthttp.StatusInternalServerError)
			if cfgStore.updates != 2 {
				t.Fatalf("expected the write and its rollback, got %d writes", cfgStore.updates)
			}
			changes := liveAuditChanges(ctx)
			requireRecordedOrNone(t, changes, tc.recorded, "mcp_client.updated")
		})
	}
}

// storedDiscoveryRollbackStore holds per_user_discovery on in the stored row and fails the rollback write.
type storedDiscoveryRollbackStore struct {
	rollbackStore
}

func (s *storedDiscoveryRollbackStore) GetMCPClientByID(_ context.Context, id string) (*configstoreTables.TableMCPClient, error) {
	on := true
	return &configstoreTables.TableMCPClient{ClientID: id, PerUserDiscovery: &on}, nil
}

func TestCommitPoint_MCPClientUpdateRuntimeFailureKeepsStoredDiscovery(t *testing.T) {
	SetLogger(&mockLogger{})
	off := false
	existing := &schemas.MCPClientConfig{ID: "client-pud", Name: "pud_mcp", EndpointSlug: "pud-slug", ConnectionType: schemas.MCPConnectionTypeHTTP, AuthType: schemas.MCPAuthTypeNone, PerUserDiscovery: &off}
	store := libtest.MCPClients(t, &lib.Config{
		MCPConfig:    &schemas.MCPConfig{},
		ClientConfig: &configstore.ClientConfig{},
	}, existing)
	store.ConfigStore = &storedDiscoveryRollbackStore{rollbackStore{failRollback: true}}
	h := &MCPHandler{store: store, mcpManager: &failingRuntimeUpdateMCPManager{}, mcpCredentialCacheManager: &evictionLog{}}

	disabled := true
	body, err := json.Marshal(MCPClientUpdateRequest{Disabled: &disabled})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("id", existing.ID)
	ctx.Request.SetBody(body)
	h.updateMCPClient(ctx)

	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	change := requireCommitPointChange(t, ctx, "mcp_client.updated", "per_user_discovery", true)
	live, ok := store.LiveStore.Collections.MCPClients.Get(existing.ID)
	if !ok || live.Config.PerUserDiscovery == nil || *live.Config.PerUserDiscovery {
		t.Fatalf("live config must keep its own value, live = %+v, after = %s", live, change.After)
	}
}

// readBackFailingMCPStore commits the client update and fails the read-back after it.
type readBackFailingMCPStore struct {
	configstore.ConfigStore
	updated bool
}

func (s *readBackFailingMCPStore) UpdateMCPClientConfig(ctx context.Context, id string, row *configstoreTables.TableMCPClient) error {
	s.updated = true
	return s.ConfigStore.UpdateMCPClientConfig(ctx, id, row)
}

func (s *readBackFailingMCPStore) GetMCPClientConfigByID(ctx context.Context, id string) (*schemas.MCPClientConfig, error) {
	if s.updated {
		return nil, errCommitPointInjected
	}
	return s.ConfigStore.GetMCPClientConfigByID(ctx, id)
}

func TestCommitPoint_MCPClientDiscoveryFlipReadBackFailure(t *testing.T) {
	h, _ := newOpenAPITestHandler(t)
	h.mcpCredentialCacheManager = &evictionLog{}
	client := headersNotesClient()
	createSignInClient(t, h.store.ConfigStore, client, false)
	loadMCPClient(t, h, client.ID)
	h.store.ConfigStore = &readBackFailingMCPStore{ConfigStore: h.store.ConfigStore}

	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodPut)
	req.Header.SetContentType("application/json")
	req.SetBodyString(`{"per_user_discovery":true,"disabled":true}`)
	ctx := initCtx(&req)
	ctx.SetUserValue("id", client.ID)
	h.updateMCPClient(ctx)

	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	change := requireCommitPointChange(t, ctx, "mcp_client.updated", "per_user_discovery", true)
	var after map[string]any
	if err := json.Unmarshal(change.After, &after); err != nil {
		t.Fatalf("decode after: %v", err)
	}
	if after["disabled"] != true {
		t.Fatalf("after.disabled = %v, want the committed true (after=%s)", after["disabled"], change.After)
	}
}

// providerDeleteFailStore commits provider writes and fails the delete a rollback starts with, or reports deleteErr after it.
type providerDeleteFailStore struct {
	configstore.ConfigStore
	failDelete bool
	deleteErr  error
}

func (s *providerDeleteFailStore) DeleteProvider(ctx context.Context, provider schemas.ModelProvider, tx ...*gorm.DB) error {
	if s.failDelete {
		return errCommitPointInjected
	}
	if err := s.ConfigStore.DeleteProvider(ctx, provider, tx...); err != nil {
		return err
	}
	return s.deleteErr
}

const commitPointProxySecret = "proxy-secret-commit-point"

func commitPointProviderBody(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"provider":               "audit-openai",
		"custom_provider_config": map[string]any{"base_provider_type": "openai", "is_key_less": true},
		"proxy_config":           map[string]any{"type": "http", "url": "http://proxy.internal:3128", "username": "proxy-user", "password": commitPointProxySecret},
	})
	if err != nil {
		t.Fatalf("marshal provider body: %v", err)
	}
	return body
}

func TestCommitPoint_ProviderCreateRollback(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failDelete   bool
		deleteErr    error
		wantRecorded bool
	}{
		{"rollback removes the provider", false, nil, false},
		{"rollback fails so the provider stays", true, nil, true},
		{"rollback removes the provider, then fails", false, &lib.SavedNotAppliedError{Err: errCommitPointInjected}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			lib.SetLogger(&mockLogger{})
			cfg := libtest.WithProviders(t, &lib.Config{ClientConfig: &configstore.ClientConfig{}}, nil, nil)
			store := &providerDeleteFailStore{ConfigStore: cfg.ConfigStore, failDelete: tc.failDelete, deleteErr: tc.deleteErr}
			cfg.ConfigStore = store
			h := &ProviderHandler{
				config:        cfg,
				modelsManager: &mockModelsManager{reloadErr: errCommitPointInjected},
			}
			ctx := newTestRequestCtx(string(commitPointProviderBody(t)))
			ctx.Request.Header.SetMethod(fasthttp.MethodPost)
			h.addProvider(ctx)

			requireStatus(t, ctx, fasthttp.StatusInternalServerError)
			stored, err := store.ConfigStore.GetProvidersConfig(context.Background())
			if err != nil {
				t.Fatalf("read providers: %v", err)
			}
			if _, kept := stored["audit-openai"]; kept != tc.failDelete {
				t.Fatalf("stored provider kept=%v, want %v", kept, tc.failDelete)
			}
			changes := liveAuditChanges(ctx)
			requireRecordedOrNone(t, changes, tc.wantRecorded, "provider.created")
			if tc.wantRecorded {
				requireNoSecret(t, changes[0], commitPointProxySecret)
			}
		})
	}
}

// newProviderRefreshFailingConfig saves providers to sqlite while its live store has no database to re-read them from.
func newProviderRefreshFailingConfig(t *testing.T) *lib.Config {
	t.Helper()
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	store := libtest.WithProviders(t, &lib.Config{}, nil, nil).ConfigStore
	cfg := libtest.FileOnly(t, &lib.Config{ClientConfig: &configstore.ClientConfig{}}, nil, nil)
	cfg.ConfigStore = store
	return cfg
}

func requireCreatedProviderRecorded(t *testing.T, ctx *fasthttp.RequestCtx, cfg *lib.Config) {
	t.Helper()
	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	stored, err := cfg.ConfigStore.GetProvidersConfig(context.Background())
	if err != nil {
		t.Fatalf("read providers: %v", err)
	}
	if _, ok := stored["audit-openai"]; !ok {
		t.Fatal("the provider must be stored although the request failed")
	}
	changes := liveAuditChanges(ctx)
	requireRecordedOrNone(t, changes, true, "provider.created")
	requireNoSecret(t, changes[0], commitPointProxySecret)
}

func TestCommitPoint_ProviderCreateRefreshFailure(t *testing.T) {
	cfg := newProviderRefreshFailingConfig(t)
	h := &ProviderHandler{config: cfg, modelsManager: &mockModelsManager{}}
	ctx := newTestRequestCtx(string(commitPointProviderBody(t)))
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	h.addProvider(ctx)
	requireCreatedProviderRecorded(t, ctx, cfg)
}

func TestCommitPoint_ProviderUpsertRefreshFailure(t *testing.T) {
	cfg := newProviderRefreshFailingConfig(t)
	h := &ProviderHandler{config: cfg, modelsManager: &mockModelsManager{}}
	body := strings.Replace(string(commitPointProviderBody(t)), "{", `{"concurrency_and_buffer_size":{"concurrency":1,"buffer_size":10},`, 1)
	ctx := newTestRequestCtx(body)
	ctx.Request.Header.SetMethod(fasthttp.MethodPut)
	ctx.SetUserValue("provider", "audit-openai")
	h.updateProvider(ctx)
	requireCreatedProviderRecorded(t, ctx, cfg)
}

func TestCommitPoint_ProviderUpsertUpdateRejectedAfterAdd(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	cfg := libtest.WithProviders(t, &lib.Config{ClientConfig: &configstore.ClientConfig{}}, nil, nil)
	cfg.ConfigStore = providerUpdateRejectingStore{ConfigStore: cfg.ConfigStore}
	h := &ProviderHandler{config: cfg, modelsManager: &mockModelsManager{}}
	body := strings.Replace(string(commitPointProviderBody(t)), "{", `{"concurrency_and_buffer_size":{"concurrency":1,"buffer_size":10},`, 1)
	ctx := newTestRequestCtx(body)
	ctx.Request.Header.SetMethod(fasthttp.MethodPut)
	ctx.SetUserValue("provider", "audit-openai")
	h.updateProvider(ctx)
	requireCreatedProviderRecorded(t, ctx, cfg)
}

// providerUpdateRejectingStore refuses the provider update write.
type providerUpdateRejectingStore struct {
	configstore.ConfigStore
}

func (providerUpdateRejectingStore) UpdateProvider(context.Context, schemas.ModelProvider, configstore.ProviderConfig, ...*gorm.DB) error {
	return errCommitPointInjected
}

func TestCommitPoint_ProviderUpdateRejectedWriteRecordsNothing(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	cfg := libtest.WithProviders(t, &lib.Config{ClientConfig: &configstore.ClientConfig{}}, nil, nil)
	store := cfg.ConfigStore
	h := &ProviderHandler{
		config:        cfg,
		modelsManager: &mockModelsManager{},
	}
	addCtx := newTestRequestCtx(string(commitPointProviderBody(t)))
	addCtx.Request.Header.SetMethod(fasthttp.MethodPost)
	h.addProvider(addCtx)
	requireStatus(t, addCtx, fasthttp.StatusOK)
	before, err := store.GetProvidersConfig(context.Background())
	if err != nil {
		t.Fatalf("read providers: %v", err)
	}
	cfg.ConfigStore = providerUpdateRejectingStore{ConfigStore: store}

	ctx := newTestRequestCtx(`{"concurrency_and_buffer_size":{"concurrency":3,"buffer_size":6},"custom_provider_config":{"base_provider_type":"openai","is_key_less":true}}`)
	ctx.Request.Header.SetMethod(fasthttp.MethodPut)
	ctx.SetUserValue("provider", "audit-openai")
	h.updateProvider(ctx)

	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	after, err := store.GetProvidersConfig(context.Background())
	if err != nil {
		t.Fatalf("read providers: %v", err)
	}
	if !reflect.DeepEqual(before["audit-openai"].ConcurrencyAndBufferSize, after["audit-openai"].ConcurrencyAndBufferSize) {
		t.Fatalf("stored concurrency changed from %+v to %+v after a rejected write", before["audit-openai"].ConcurrencyAndBufferSize, after["audit-openai"].ConcurrencyAndBufferSize)
	}
	requireRecordedOrNone(t, liveAuditChanges(ctx), false, "provider.updated")
}

func TestCommitPoint_ProviderUpdateRecordedWhenRuntimeRebuildFails(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	cfg := libtest.WithProviders(t, &lib.Config{ClientConfig: &configstore.ClientConfig{}}, nil, nil)
	store := cfg.ConfigStore
	h := &ProviderHandler{
		config:        cfg,
		modelsManager: &mockModelsManager{},
	}
	addCtx := newTestRequestCtx(string(commitPointProviderBody(t)))
	addCtx.Request.Header.SetMethod(fasthttp.MethodPost)
	h.addProvider(addCtx)
	requireStatus(t, addCtx, fasthttp.StatusOK)

	// A client that is shutting down refuses UpdateProvider, which runs after the store write.
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: lib.NewBaseAccount(cfg),
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("init client: %v", err)
	}
	client.Shutdown()
	cfg.SetBifrostClient(client)

	ctx := newTestRequestCtx(`{"concurrency_and_buffer_size":{"concurrency":3,"buffer_size":6},"custom_provider_config":{"base_provider_type":"openai","is_key_less":true},"proxy_config":{"type":"http","url":"http://proxy.internal:3128","username":"proxy-user","password":"` + commitPointProxySecret + `"}}`)
	ctx.Request.Header.SetMethod(fasthttp.MethodPut)
	ctx.SetUserValue("provider", "audit-openai")
	h.updateProvider(ctx)

	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	stored, err := store.GetProvidersConfig(context.Background())
	if err != nil {
		t.Fatalf("read providers: %v", err)
	}
	if got := stored["audit-openai"].ConcurrencyAndBufferSize; got == nil || got.Concurrency != 3 {
		t.Fatalf("stored concurrency = %+v, want the committed update", got)
	}
	changes := liveAuditChanges(ctx)
	requireRecordedOrNone(t, changes, true, "provider.updated")
	requireNoSecret(t, changes[0], commitPointProxySecret)
}

// runWithParams runs handler on a fresh request and returns its context.
func runWithParams(handler fasthttp.RequestHandler, body string, params map[string]string) *fasthttp.RequestCtx {
	ctx := newTestRequestCtx(body)
	for key, value := range params {
		ctx.SetUserValue(key, value)
	}
	handler(ctx)
	return ctx
}

func TestCommitPoint_ModelRouterWritesRecordedWhenServingFails(t *testing.T) {
	h, manager, _ := modelRouterHandler(t)
	modelRouterOK(t, h.createModelRouter, routerBody("support", "Support"), nil)
	manager.serveErr = errors.New("plugin is restarting")
	support := modelRouterPath("support")

	for _, step := range []struct {
		event   string
		handler fasthttp.RequestHandler
		body    string
		params  map[string]string
	}{
		{"model_router.created", h.createModelRouter, routerBody("late", "Late"), nil},
		{"model_router.updated", h.updateModelRouter, `{"name":"Renamed"}`, support},
		{"model_router.targets_updated", h.setModelRouterTargets, `{"targets":[{"tier":"SIMPLE","provider":"openai","model":"gpt-4o-mini","weight":1}]}`, support},
		{"model_router.routing_updated", h.setModelRouterRouting, `{"enabled":true,"priority":5}`, support},
		{"model_router.reset", h.resetModelRouter, "", support},
		{"model_router.deleted", h.deleteModelRouter, "", support},
	} {
		t.Run(step.event, func(t *testing.T) {
			ctx := runWithParams(step.handler, step.body, step.params)
			requireStatus(t, ctx, fasthttp.StatusInternalServerError)
			changes := liveAuditChanges(ctx)
			if len(changes) != 1 || changes[0].EventName != step.event {
				t.Fatalf("want one %s change, got %+v", step.event, changes)
			}
		})
	}
}

func TestCommitPoint_ModelRouterUpdateFallbackKeepsStoredTargets(t *testing.T) {
	h, manager, _ := modelRouterHandler(t)
	modelRouterOK(t, h.createModelRouter, routerBody("support", "Support"), nil)
	support := modelRouterPath("support")
	modelRouterOK(t, h.setModelRouterTargets, `{"targets":[{"tier":"SIMPLE","provider":"openai","model":"gpt-4o-mini","weight":1}]}`, support)
	manager.serveErr = errors.New("plugin is restarting")

	ctx := runWithParams(h.updateModelRouter, `{"name":"Renamed"}`, support)
	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	change := requireCommitPointChange(t, ctx, "model_router.updated", "name", "Renamed")
	if !strings.Contains(string(change.After), "gpt-4o-mini") {
		t.Fatalf("after = %s, want the stored targets kept", change.After)
	}
}

func TestCommitPoint_ModelRouterCreateFallbackHasDefaultRouting(t *testing.T) {
	h, manager, store := modelRouterHandler(t)
	manager.serveErr = errors.New("plugin is restarting")

	ctx := runWithParams(h.createModelRouter, routerBody("support", "Support"), nil)
	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	change := requireCommitPointChange(t, ctx, "model_router.created", "name", "Support")
	saved, err := store.GetModelRouter(context.Background(), "support")
	if err != nil || saved.Routing == nil {
		t.Fatalf("stored router = %+v, %v", saved, err)
	}
	var after configstore.ModelRouter
	if err := json.Unmarshal(change.After, &after); err != nil {
		t.Fatalf("decode after: %v", err)
	}
	if !reflect.DeepEqual(after.Routing, saved.Routing) {
		t.Fatalf("after = %s, want the stored routing %+v", change.After, *saved.Routing)
	}
}

func TestCommitPoint_ModelRouterResetFallbackKeepsTargetsAndRouting(t *testing.T) {
	h, manager, _ := modelRouterHandler(t)
	modelRouterOK(t, h.createModelRouter, routerBody("support", "Support"), nil)
	support := modelRouterPath("support")
	modelRouterOK(t, h.setModelRouterTargets, `{"targets":[{"tier":"SIMPLE","provider":"openai","model":"gpt-4o-mini","weight":1}]}`, support)
	modelRouterOK(t, h.setModelRouterRouting, `{"enabled":true,"priority":5}`, support)
	manager.serveErr = errors.New("plugin is restarting")

	ctx := runWithParams(h.resetModelRouter, "", support)
	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	change := requireCommitPointChange(t, ctx, "model_router.reset", "", nil)
	if !strings.Contains(string(change.After), "gpt-4o-mini") || !strings.Contains(string(change.After), `"priority":5`) {
		t.Fatalf("after = %s, want the stored targets and routing kept", change.After)
	}
}

// routingRaceStore changes the router's routing right after the handler's first read of it.
type routingRaceStore struct {
	configstore.ConfigStore
	raced bool
}

func (s *routingRaceStore) GetModelRouter(ctx context.Context, id string) (*configstore.ModelRouter, error) {
	router, err := s.ConfigStore.GetModelRouter(ctx, id)
	if err != nil || s.raced {
		return router, err
	}
	s.raced = true
	routing := *router.Routing
	routing.Priority = 9
	return router, s.ConfigStore.SetModelRouterRouting(ctx, id, routing)
}

func TestCommitPoint_ModelRouterResetFallbackHasTheRoutingCommitted(t *testing.T) {
	h, manager, store := modelRouterHandler(t)
	modelRouterOK(t, h.createModelRouter, routerBody("support", "Support"), nil)
	support := modelRouterPath("support")
	modelRouterOK(t, h.setModelRouterRouting, `{"enabled":true,"priority":5}`, support)
	h.configStore = &routingRaceStore{ConfigStore: store}
	manager.serveErr = errors.New("plugin is restarting")

	ctx := runWithParams(h.resetModelRouter, "", support)
	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	change := requireCommitPointChange(t, ctx, "model_router.reset", "", nil)
	if !strings.Contains(string(change.After), `"priority":9`) {
		t.Fatalf("after = %s, want the routing stored when the reset committed", change.After)
	}
}

func TestCommitPoint_ModelRouterUpdateSuccessRecordsReadBack(t *testing.T) {
	h, _, _ := modelRouterHandler(t)
	modelRouterOK(t, h.createModelRouter, routerBody("support", "Support"), nil)
	ctx := runWithParams(h.updateModelRouter, `{"name":"Renamed"}`, modelRouterPath("support"))
	requireStatus(t, ctx, fasthttp.StatusOK)
	requireCommitPointChange(t, ctx, "model_router.updated", "name", "Renamed")
}

func TestCommitPoint_ComplexityResetRecordedWhenReloadFails(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	h := &RoutingHandler{configStore: store, routingManager: &mockRoutingManager{reloadErr: errors.New("plugin is not wired")}}
	custom := complexity.DefaultAnalyzerConfig()
	custom.TierBoundaries.MediumComplex = 0.55
	if err := store.UpdateComplexityAnalyzerConfig(context.Background(), &custom); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	ctx := newTestRequestCtx("")
	h.resetComplexityAnalyzerConfig(ctx)

	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	changes := liveAuditChanges(ctx)
	if len(changes) != 1 || changes[0].EventName != "complexity_analyzer_config.reset" {
		t.Fatalf("want one reset change, got %+v", changes)
	}
}

// failingReloadPluginsLoader refuses every plugin load.
type failingReloadPluginsLoader struct {
	noopPluginsLoader
}

func (failingReloadPluginsLoader) ReloadPlugin(context.Context, string, *string, any, *schemas.PluginPlacement, *int) error {
	return errors.New("plugin failed to load")
}

// restoreFailingPluginStore fails the delete that restoring the previous row starts with.
type restoreFailingPluginStore struct {
	configstore.ConfigStore
}

func (s *restoreFailingPluginStore) DeletePlugin(context.Context, string, ...*gorm.DB) error {
	return errors.New("restore write failed")
}

func TestCommitPoint_PluginUpdateLoadFailure(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failRestore  bool
		wantRecorded bool
	}{
		{"restored row records nothing", false, false},
		{"failed restore keeps the event", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			store := setupPricingOverrideHandlerStore(t)
			if err := store.CreatePlugin(context.Background(), &configstoreTables.TablePlugin{Name: "otel", Enabled: true, Config: map[string]any{}}); err != nil {
				t.Fatalf("seed plugin: %v", err)
			}
			var cfgStore configstore.ConfigStore = store
			if tc.failRestore {
				cfgStore = &restoreFailingPluginStore{ConfigStore: store}
			}
			h := &PluginsHandler{pluginsLoader: failingReloadPluginsLoader{}, configStore: cfgStore}
			ctx := newTestRequestCtx(`{"enabled":true,"config":{}}`)
			ctx.Request.Header.SetMethod("PUT")
			ctx.SetUserValue("name", "otel")
			h.updatePlugin(ctx)

			if ctx.Response.StatusCode() == fasthttp.StatusOK {
				t.Fatalf("expected the load failure to fail the request: %s", ctx.Response.Body())
			}
			requireRecordedOrNone(t, liveAuditChanges(ctx), tc.wantRecorded, "plugin.updated")
		})
	}
}

// marshallerOnLoadPluginsLoader can redact a plugin's config only after loading it.
type marshallerOnLoadPluginsLoader struct {
	noopPluginsLoader
	loaded bool
}

func (l *marshallerOnLoadPluginsLoader) ReloadPlugin(context.Context, string, *string, any, *schemas.PluginPlacement, *int) error {
	l.loaded = true
	return nil
}

func (l *marshallerOnLoadPluginsLoader) ExpandPluginConfigForAPI(string, map[string]any) (map[string]any, error) {
	if !l.loaded {
		return nil, nil
	}
	return map[string]any{"token": "redacted"}, nil
}

func TestCommitPoint_PluginEnableRecordsConfigRedactedByTheLoadedPlugin(t *testing.T) {
	SetLogger(&mockLogger{})
	const secret = "plugin-secret-commit-point"
	store := setupPricingOverrideHandlerStore(t)
	if err := store.CreatePlugin(context.Background(), &configstoreTables.TablePlugin{Name: "otel", Config: map[string]any{"token": secret}}); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	h := &PluginsHandler{pluginsLoader: &marshallerOnLoadPluginsLoader{}, configStore: store}
	ctx := newTestRequestCtx(`{"enabled":true,"config":{"token":"` + secret + `"}}`)
	ctx.Request.Header.SetMethod("PUT")
	ctx.SetUserValue("name", "otel")
	h.updatePlugin(ctx)

	requireStatus(t, ctx, fasthttp.StatusOK)
	change := requireCommitPointChange(t, ctx, "plugin.updated", "enabled", true)
	requireNoSecret(t, change, secret)
	if !strings.Contains(string(change.After), `"token":"redacted"`) {
		t.Fatalf("after = %s, want the redacted config", change.After)
	}
}

// failingRuleReloadManager fails the in-memory reload after a routing rule write commits.
type failingRuleReloadManager struct {
	RoutingManager
}

func (failingRuleReloadManager) ReloadRoutingRule(context.Context, string) error {
	return errCommitPointInjected
}

func TestCommitPoint_RoutingRuleWritesRecordedWhenReloadFails(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	ok := &RoutingHandler{configStore: store, routingManager: &reloadRecordingRoutingManager{}}
	failing := &RoutingHandler{configStore: store, routingManager: failingRuleReloadManager{}}

	createCtx := newTestRequestCtx(`{"name":"audit-rule","cel_expression":"true","targets":[{"provider":"openai","weight":1}]}`)
	failing.createRoutingRule(createCtx)
	requireStatus(t, createCtx, fasthttp.StatusInternalServerError)
	created := requireCommitPointChange(t, createCtx, "routing_rule.created", "name", "audit-rule")

	updateCtx := newTestRequestCtx(`{"name":"audit-rule-renamed","cel_expression":"true","targets":[{"provider":"openai","weight":1}]}`)
	updateCtx.SetUserValue("rule_id", created.TargetID)
	failing.updateRoutingRule(updateCtx)
	requireStatus(t, updateCtx, fasthttp.StatusInternalServerError)
	requireCommitPointChange(t, updateCtx, "routing_rule.updated", "name", "audit-rule-renamed")

	successCtx := newTestRequestCtx(`{"name":"audit-rule-final","cel_expression":"true","targets":[{"provider":"openai","weight":1}]}`)
	successCtx.SetUserValue("rule_id", created.TargetID)
	ok.updateRoutingRule(successCtx)
	requireStatus(t, successCtx, fasthttp.StatusOK)
	requireCommitPointChange(t, successCtx, "routing_rule.updated", "name", "audit-rule-final")
}

func TestCommitPoint_ComplexityUpdateRecordedWhenReloadFails(t *testing.T) {
	for _, tc := range []struct {
		name       string
		reloadErr  error
		wantStatus int
	}{
		{"reload fails", errCommitPointInjected, fasthttp.StatusInternalServerError},
		{"reload succeeds", nil, fasthttp.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			store := setupPricingOverrideHandlerStore(t)
			h := &RoutingHandler{configStore: store, routingManager: &mockRoutingManager{reloadErr: tc.reloadErr}}
			custom := complexity.DefaultAnalyzerConfig()
			custom.TierBoundaries.MediumComplex = 0.55

			ctx := newTestRequestCtx(testComplexityAnalyzerPayload(t, custom))
			h.updateComplexityAnalyzerConfig(ctx)

			requireStatus(t, ctx, tc.wantStatus)
			change := requireCommitPointChange(t, ctx, "complexity_analyzer_config.updated", "", nil)
			if !strings.Contains(string(change.After), "0.55") {
				t.Fatalf("after = %s, want the saved boundary", change.After)
			}
		})
	}
}

// skillReadBackFailStore commits skill writes and fails every read-back after the write.
type skillReadBackFailStore struct {
	configstore.ConfigStore
	skill    *configstoreTables.TableSkill
	getCalls int
	failRead bool
}

func (s *skillReadBackFailStore) GetSkillLean(context.Context, string) (*configstoreTables.TableSkill, error) {
	copied := *s.skill
	return &copied, nil
}

func (s *skillReadBackFailStore) GetSkill(context.Context, string) (*configstoreTables.TableSkill, error) {
	s.getCalls++
	if s.failRead && s.getCalls > 1 {
		return nil, errCommitPointInjected
	}
	copied := *s.skill
	return &copied, nil
}

func (s *skillReadBackFailStore) ShiftSkillVersion(_ context.Context, _ string, version string, _ objectstore.ObjectStore) error {
	s.getCalls++ // the shift has no pre-read GetSkill, so the next GetSkill is the read-back
	return nil
}

func (s *skillReadBackFailStore) UpdateSkill(_ context.Context, skill *configstoreTables.TableSkill, _ string, _ bool, _ objectstore.ObjectStore) error {
	s.skill.Description = skill.Description
	return nil
}

func TestCommitPoint_SkillWritesRecordedWhenReadBackFails(t *testing.T) {
	SetLogger(&mockLogger{})
	newStore := func(failRead bool) *skillReadBackFailStore {
		return &skillReadBackFailStore{skill: &configstoreTables.TableSkill{ID: "sk-1", Name: "audit-skill", Description: "old", LatestVersion: "1.0.0"}, failRead: failRead}
	}
	params := map[string]string{"id": "sk-1"}
	updateBody := `{"description":"new","skill_md_body":"# body","version":"1.0.1"}`

	t.Run("version shift", func(t *testing.T) {
		h := &SkillsHandler{store: newStore(true)}
		ctx := runWithParams(h.shiftVersion, `{"version":"1.0.1"}`, params)
		requireStatus(t, ctx, fasthttp.StatusInternalServerError)
		change := requireCommitPointChange(t, ctx, "skill.version_shifted", "latest_version", "1.0.1")
		var after map[string]any
		if err := json.Unmarshal(change.After, &after); err != nil {
			t.Fatalf("decode after: %v", err)
		}
		if len(after) != 3 || after["id"] != "sk-1" || after["name"] != "audit-skill" {
			t.Fatalf("after = %s, want only id, name and latest_version", change.After)
		}
		var prev map[string]any
		if err := json.Unmarshal(change.Before, &prev); err != nil {
			t.Fatalf("decode before: %v", err)
		}
		if len(prev) != 3 || prev["latest_version"] != "1.0.0" {
			t.Fatalf("before = %s, want the same three fields as after", change.Before)
		}
	})
	t.Run("version shift success records the read-back", func(t *testing.T) {
		h := &SkillsHandler{store: newStore(false)}
		ctx := runWithParams(h.shiftVersion, `{"version":"1.0.1"}`, params)
		requireStatus(t, ctx, fasthttp.StatusOK)
		change := requireCommitPointChange(t, ctx, "skill.version_shifted", "description", "old")
		if !strings.Contains(string(change.Before), `"description":"old"`) {
			t.Fatalf("before = %s, want the full snapshot", change.Before)
		}
	})
	t.Run("update", func(t *testing.T) {
		h := &SkillsHandler{store: newStore(true)}
		ctx := runWithParams(h.updateSkill, updateBody, params)
		requireStatus(t, ctx, fasthttp.StatusInternalServerError)
		requireCommitPointChange(t, ctx, "skill.updated", "description", "new")
	})
	t.Run("update success records the read-back", func(t *testing.T) {
		h := &SkillsHandler{store: newStore(false)}
		ctx := runWithParams(h.updateSkill, updateBody, params)
		requireStatus(t, ctx, fasthttp.StatusOK)
		requireCommitPointChange(t, ctx, "skill.updated", "description", "new")
	})
}

// virtualMCPKeysFailStore commits the virtual MCP update and fails the key assignment read.
type virtualMCPKeysFailStore struct {
	configstore.ConfigStore
}

func (virtualMCPKeysFailStore) GetVirtualMCPByID(context.Context, uint) (*configstoreTables.TableVirtualMCP, error) {
	return &configstoreTables.TableVirtualMCP{ID: 7, Name: "old-name", Enabled: true}, nil
}

func (virtualMCPKeysFailStore) UpdateVirtualMCP(context.Context, *configstoreTables.TableVirtualMCP) error {
	return nil
}

func (virtualMCPKeysFailStore) GetVirtualKeyIDsForVirtualMCP(context.Context, uint) ([]string, error) {
	return nil, errCommitPointInjected
}

func TestCommitPoint_VirtualMCPUpdateRecordedWhenKeyReadFails(t *testing.T) {
	SetLogger(&mockLogger{})
	h := &VirtualMCPHandler{store: &lib.Config{ConfigStore: virtualMCPKeysFailStore{}}, logger: &mockLogger{}}
	ctx := runWithParams(h.updateVirtualMCP, `{"name":"new-name"}`, map[string]string{"id": "7"})
	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	requireCommitPointChange(t, ctx, "virtual_mcp.updated", "name", "new-name")
}

// pluginCreateFailStore fails the rollback delete or the read-back after createPlugin commits.
type pluginCreateFailStore struct {
	configstore.ConfigStore
	failDelete   bool
	failReadBack bool
	getCalls     int
}

func (s *pluginCreateFailStore) DeletePlugin(ctx context.Context, name string, tx ...*gorm.DB) error {
	if s.failDelete {
		return errCommitPointInjected
	}
	return s.ConfigStore.DeletePlugin(ctx, name, tx...)
}

func (s *pluginCreateFailStore) GetPlugin(ctx context.Context, name string) (*configstoreTables.TablePlugin, error) {
	s.getCalls++
	if s.failReadBack && s.getCalls > 1 {
		return nil, errCommitPointInjected
	}
	return s.ConfigStore.GetPlugin(ctx, name)
}

func TestCommitPoint_PluginCreate(t *testing.T) {
	path := "/opt/bifrost/plugins/trusted.so"
	for _, tc := range []struct {
		name         string
		loader       PluginsLoader
		enabled      bool
		failDelete   bool
		failReadBack bool
		wantRecorded bool
	}{
		{"load fails, rollback succeeds", failingReloadPluginsLoader{}, true, false, false, false},
		{"load fails, rollback fails", failingReloadPluginsLoader{}, true, true, false, true},
		{"read-back fails", noopPluginsLoader{}, false, false, true, true},
		{"success", noopPluginsLoader{}, false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			store := &pluginCreateFailStore{ConfigStore: setupPricingOverrideHandlerStore(t), failDelete: tc.failDelete, failReadBack: tc.failReadBack}
			h := &PluginsHandler{pluginsLoader: tc.loader, configStore: store}
			body, err := json.Marshal(map[string]any{"name": "audit-plugin", "enabled": tc.enabled, "path": path})
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			ctx := newTestRequestCtx(string(body))
			ctx.Request.Header.SetMethod("POST")
			h.createPlugin(ctx)

			requireRecordedOrNone(t, liveAuditChanges(ctx), tc.wantRecorded, "plugin.created")
		})
	}
}

// droppingReloadWebhookManager reports a clean reload but leaves the endpoint out of memory.
type droppingReloadWebhookManager struct{ configWebhookManager }

func (m droppingReloadWebhookManager) ReloadWebhookEndpoint(ctx context.Context, id string) error {
	return m.RemoveWebhookEndpoint(ctx, id)
}

func TestCommitPoint_WebhookUpdate(t *testing.T) {
	handler, config := newWebhookTestHandler(t)
	id, _ := createTestWebhookEndpoint(t, handler, "audit-hook")
	body := func(name string) string {
		return `{"name":"` + name + `","url":"https://93.184.216.34/hook","events":["async_job.completed"]}`
	}

	okCtx := newWebhookRequestCtx(body("audit-hook-renamed"), map[string]string{"id": id})
	handler.updateWebhookEndpoint(okCtx)
	requireStatus(t, okCtx, fasthttp.StatusOK)
	requireCommitPointChange(t, okCtx, "webhook.updated", "name", "audit-hook-renamed")

	dropping := NewWebhookHandler(droppingReloadWebhookManager{configWebhookManager{config}}, config, nil)
	missingCtx := newWebhookRequestCtx(body("audit-hook-missing"), map[string]string{"id": id})
	dropping.updateWebhookEndpoint(missingCtx)
	requireStatus(t, missingCtx, fasthttp.StatusInternalServerError)
	requireCommitPointChange(t, missingCtx, "webhook.updated", "name", "audit-hook-missing")
}

func TestCommitPoint_ConfigCompatReloadFailure(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failRestore  bool
		wantStatus   int
		wantRecorded bool
	}{
		{"restore succeeds records nothing", false, fasthttp.StatusBadRequest, false},
		{"failed restore keeps the event", true, fasthttp.StatusInternalServerError, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			store := newRealOAuth2Store(t)
			var cfgStore configstore.ConfigStore = store
			writes := 0
			if tc.failRestore {
				cfgStore = failingRestoreStore{store, &writes}
			}
			cfg := newTestOAuth2Config(t, cfgStore, configstoreTables.MCPServerAuthModeHeaders, false)
			cfg.ClientConfig.Compat = configstore.CompatConfig{ConvertTextToChat: true}
			if err := store.UpdateClientConfig(context.Background(), cfg.ClientConfig); err != nil {
				t.Fatalf("seed config: %v", err)
			}
			h := &ConfigHandler{store: cfg, configManager: &recordingCompatConfigManager{failReload: true}}

			ctx := putConfigCtx(compatAllOffPayload)
			h.updateConfig(ctx)

			requireStatus(t, ctx, tc.wantStatus)
			changes := liveAuditChanges(ctx)
			requireRecordedOrNone(t, changes, tc.wantRecorded, "config.updated")
			if !tc.wantRecorded {
				return
			}
			saved, err := store.GetClientConfig(context.Background())
			if err != nil {
				t.Fatalf("read saved config: %v", err)
			}
			var after struct {
				Compat configstore.CompatConfig `json:"compat"`
			}
			if err := json.Unmarshal(changes[0].After, &after); err != nil {
				t.Fatalf("decode after %s: %v", changes[0].After, err)
			}
			if saved.Compat.ConvertTextToChat || after.Compat != saved.Compat {
				t.Fatalf("after compat = %+v, saved compat = %+v, want the saved all-off config", after.Compat, saved.Compat)
			}
		})
	}
}

// flushFailingStore saves auth config but fails to flush sessions after it.
type flushFailingStore struct {
	configstore.ConfigStore
}

func (flushFailingStore) FlushSessions(context.Context) error {
	return errCommitPointInjected
}

// authWriteFailingManager fails every auth config write.
type authWriteFailingManager struct {
	stubConfigManager
}

func (authWriteFailingManager) UpdateAuthConfig(context.Context, *configstore.AuthConfig) error {
	return errCommitPointInjected
}

// changeNamed returns the one live change named eventName.
func changeNamed(t *testing.T, ctx *fasthttp.RequestCtx, eventName string) configstore.AuditChange {
	t.Helper()
	var found []configstore.AuditChange
	for _, c := range liveAuditChanges(ctx) {
		if c.EventName == eventName {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one %s change, got %+v", eventName, liveAuditChanges(ctx))
	}
	return found[0]
}

func seedStoredAdmin(t *testing.T, store configstore.ConfigStore, password string) string {
	t.Helper()
	hash, err := encrypt.Hash(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := store.UpdateAuthConfig(context.Background(), &configstore.AuthConfig{AdminUserName: schemas.NewSecretVar("admin"), AdminPassword: schemas.NewSecretVar(hash), IsEnabled: false}); err != nil {
		t.Fatalf("seed auth config: %v", err)
	}
	return hash
}

func TestCommitPoint_AuthConfigRecordedWhenSessionFlushFails(t *testing.T) {
	SetLogger(&mockLogger{})
	const newPass = "Replacement-Pass-5678!"
	store := newRealOAuth2Store(t)
	storedHash := seedStoredAdmin(t, store, "Current-Pass-1234!")
	cfg := newTestOAuth2Config(t, flushFailingStore{store}, configstoreTables.MCPServerAuthModeHeaders, false)
	h := &ConfigHandler{store: cfg, configManager: authConfigTestManager{store: store}}

	ctx := putConfigCtx(`{"client_config":{"log_retention_days":7},"auth_config":{"is_enabled":true,"admin_username":"admin","admin_password":"` + newPass + `"}}`)
	h.updateConfig(ctx)

	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	stored, err := store.GetAuthConfig(context.Background())
	if err != nil || stored == nil || !stored.IsEnabled {
		t.Fatalf("auth config should be saved before the flush fails: %+v %v", stored, err)
	}
	change := changeNamed(t, ctx, "auth_config.updated")
	if string(change.Before) != `{"is_enabled":false}` || string(change.After) != `{"admin_password_changed":true,"is_enabled":true}` {
		t.Fatalf("before=%s after=%s", change.Before, change.After)
	}
	for _, secret := range []string{newPass, storedHash, stored.AdminPassword.GetValue(), `"admin"`} {
		if strings.Contains(string(change.Before)+string(change.After), secret) {
			t.Fatalf("snapshot carries a credential %q", secret)
		}
	}
}

func TestCommitPoint_FrameworkConfigRecordedWhenAuthWriteFails(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	seedStoredAdmin(t, store, "Current-Pass-1234!")
	cfg := newTestOAuth2Config(t, store, configstoreTables.MCPServerAuthModeHeaders, false)
	h := &ConfigHandler{store: cfg, configManager: authWriteFailingManager{}}

	ctx := putConfigCtx(`{"client_config":{"log_retention_days":7},"framework_config":{"pricing_sync_interval":7200},"auth_config":{"is_enabled":true,"admin_username":"admin","admin_password":"Replacement-Pass-5678!"}}`)
	h.updateConfig(ctx)

	requireStatus(t, ctx, fasthttp.StatusInternalServerError)
	change := changeNamed(t, ctx, "framework_config.updated")
	var after map[string]any
	if err := json.Unmarshal(change.After, &after); err != nil {
		t.Fatalf("decode after: %v", err)
	}
	if after["pricing_sync_interval"] != float64(7200) {
		t.Fatalf("after = %s, want the saved interval", change.After)
	}
	for _, c := range liveAuditChanges(ctx) {
		if c.EventName == "auth_config.updated" {
			t.Fatalf("a failed auth write must not be recorded: %+v", c)
		}
	}
}

// TestAuditFrameworkConfigUnparseableURLRedacted pins that a stored URL whose credentials the
// redactor cannot locate (one url.Parse rejects, or an opaque one it accepts) never reaches the
// framework_config.updated snapshots verbatim, even on a save that changes only an interval.
func TestAuditFrameworkConfigUnparseableURLRedacted(t *testing.T) {
	for name, stored := range map[string]string{
		"unparseable": "https://operator:p%zzsecret@catalog.example/pricing.json",
		"opaque":      "https:operator:secret@catalog.example/pricing.json",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := url.Parse(stored); (err != nil) != (name == "unparseable") {
				t.Fatalf("fixture %q: url.Parse error = %v", stored, err)
			}
			SetLogger(&mockLogger{})
			store := newRealOAuth2Store(t)
			seedStoredAdmin(t, store, "Current-Pass-1234!")
			pricingURL, interval := stored, int64(3600)
			if err := store.UpdateFrameworkConfig(context.Background(), &configstoreTables.TableFrameworkConfig{PricingURL: &pricingURL, PricingSyncInterval: &interval}); err != nil {
				t.Fatalf("seed framework config: %v", err)
			}
			cfg := newTestOAuth2Config(t, store, configstoreTables.MCPServerAuthModeHeaders, false)
			h := &ConfigHandler{store: cfg, configManager: authConfigTestManager{store: store}}

			ctx := putConfigCtx(`{"client_config":{"log_retention_days":7},"framework_config":{"pricing_sync_interval":7200}}`)
			h.updateConfig(ctx)

			requireStatus(t, ctx, fasthttp.StatusOK)
			change := changeNamed(t, ctx, "framework_config.updated")
			for _, snap := range []json.RawMessage{change.Before, change.After} {
				if strings.Contains(string(snap), "secret") || strings.Contains(string(snap), "operator") {
					t.Fatalf("snapshot carries the stored URL's credentials: before=%s after=%s", change.Before, change.After)
				}
			}
			var after map[string]any
			if err := json.Unmarshal(change.After, &after); err != nil {
				t.Fatalf("decode after: %v", err)
			}
			if after["pricing_sync_interval"] != float64(7200) || after["pricing_url"] == nil {
				t.Fatalf("after = %s, want the saved interval and a placeholder pricing_url", change.After)
			}
		})
	}
}

// TestRecordAuthConfigChangeComparesReferences pins that swapping the admin username to another
// reference records a change even when both resolve to the same value, as the handler saves it.
func TestRecordAuthConfigChangeComparesReferences(t *testing.T) {
	t.Setenv("BF_AUDIT_ADMIN_A", "admin")
	t.Setenv("BF_AUDIT_ADMIN_B", "admin")
	pass := schemas.NewSecretVar("Current-Pass-1234!")
	before := &configstore.AuthConfig{IsEnabled: true, AdminUserName: schemas.NewSecretVar("env.BF_AUDIT_ADMIN_A"), AdminPassword: pass}

	ctx := newTestRequestCtx("")
	recordAuthConfigChange(ctx, before, &configstore.AuthConfig{IsEnabled: true, AdminUserName: schemas.NewSecretVar("env.BF_AUDIT_ADMIN_B"), AdminPassword: pass})
	change := requireSingleAuditChange(t, ctx, "auth_config.updated")
	if !strings.Contains(string(change.After), `"admin_username_changed":true`) || strings.Contains(string(change.After), "BF_AUDIT_ADMIN") {
		t.Fatalf("after = %s, want admin_username_changed and no reference", change.After)
	}

	ctx = newTestRequestCtx("")
	recordAuthConfigChange(ctx, before, &configstore.AuthConfig{IsEnabled: true, AdminUserName: schemas.NewSecretVar("env.BF_AUDIT_ADMIN_A"), AdminPassword: pass})
	if changes := liveAuditChanges(ctx); len(changes) != 0 {
		t.Fatalf("resubmitting the same reference must record nothing, got %+v", changes)
	}
}

func TestCommitPoint_AuthConfigUnchangedSaveRecordsNothing(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	seedStoredAdmin(t, store, "Current-Pass-1234!")
	cfg := newTestOAuth2Config(t, store, configstoreTables.MCPServerAuthModeHeaders, false)
	h := &ConfigHandler{store: cfg, configManager: authConfigTestManager{store: store}}

	ctx := putConfigCtx(`{"client_config":{"log_retention_days":7},"auth_config":{"is_enabled":false}}`)
	h.updateConfig(ctx)

	requireStatus(t, ctx, fasthttp.StatusOK)
	for _, c := range liveAuditChanges(ctx) {
		if c.EventName == "auth_config.updated" {
			t.Fatalf("a save that resubmits the stored auth config must not record: %+v", c)
		}
	}
}

// savedThenFailedMessage matches the messages handlers send when a write is saved but a later step fails.
var savedThenFailedMessage = regexp.MustCompile(`(in database but|in the database but|saved but|committed but|stored but)`)

// funcDeclName captures a function or method name from its declaration line.
var funcDeclName = regexp.MustCompile(`^func (?:\([^)]*\) )?([A-Za-z0-9_]+)\(`)

// commitPointFunc is one top-level function of the handlers package.
type commitPointFunc struct {
	file, name string
	start      int
	body       []string
}

// handlerSignature marks a route handler, which is never treated as a helper.
const handlerSignature = "(ctx *fasthttp.RequestCtx) {"

// sendsSavedThenFailed reports whether body[i] starts a call that answers a saved-then-failed 500
// inline. The call is read through to its closing parenthesis, so a message on a later line counts.
func sendsSavedThenFailed(body []string, i int) bool {
	line := body[i]
	if isCommentLine(line) {
		return false
	}
	if !strings.Contains(line, "SendError(") && !strings.Contains(line, "sendAgentGatewayError(") {
		return false
	}
	call := line
	for depth, j := strings.Count(line, "(")-strings.Count(line, ")"), i+1; depth > 0 && j < len(body); j++ {
		call += " " + strings.TrimSpace(body[j])
		depth += strings.Count(body[j], "(") - strings.Count(body[j], ")")
	}
	compact := strings.Join(strings.Fields(call), "")
	is500 := strings.Contains(compact, "StatusInternalServerError") || strings.Contains(compact, "(ctx,500,")
	return is500 && savedThenFailedMessage.MatchString(call)
}

// stripGoComments blanks every // and /* */ comment in src, keeping newlines so line numbers
// hold, so text inside a comment never reads as a call. String literals are left as they are.
func stripGoComments(src string) string {
	out := []byte(src)
	fset := token.NewFileSet()
	var sc scanner.Scanner
	sc.Init(fset.AddFile("", fset.Base(), len(out)), out, nil, scanner.ScanComments)
	for {
		pos, tok, lit := sc.Scan()
		if tok == token.EOF {
			return string(out)
		}
		if tok != token.COMMENT {
			continue
		}
		// The scanner drops \r from a comment's text, so the span is measured in the source.
		start := fset.Position(pos).Offset
		end := len(out)
		if strings.HasPrefix(lit, "//") {
			if nl := bytes.IndexByte(out[start:], '\n'); nl >= 0 {
				end = start + nl
			}
		} else if close := bytes.Index(out[start+2:], []byte("*/")); close >= 0 {
			end = start + 2 + close + 2
		}
		for i := start; i < end; i++ {
			if out[i] != '\n' && out[i] != '\r' {
				out[i] = ' '
			}
		}
	}
}

// isCommentLine reports whether line is a // comment, which calls nothing.
func isCommentLine(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "//") }

// recordsAudit reports whether line records an audit change, directly or through a wrapper in recorders.
func recordsAudit(line string, recorders map[string]bool) bool {
	return !isCommentLine(line) && (strings.Contains(line, "RecordAuditChange(") || callsAny(line, recorders) != "")
}

// callsAny returns the first helper in names that line calls, or "".
func callsAny(line string, names map[string]bool) string {
	if isCommentLine(line) {
		return ""
	}
	for name := range names {
		if strings.Contains(line, "."+name+"(") || strings.Contains(line, " "+name+"(") || strings.Contains(line, "\t"+name+"(") || strings.Contains(line, "!"+name+"(") || strings.Contains(line, "("+name+"(") {
			return name
		}
	}
	return ""
}

// TestCommitPointRule fails when a handler answers a saved-then-failed 500 before its first audit
// record, which means the change it saved reaches the audit log only as a failed request. A 500
// sent through a helper (refreshPrompts, sendMCPClientReloadError) counts, and so does a record
// made through a wrapper (recordMCPClientAction).
func TestCommitPointRule(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read handlers dir: %v", err)
	}
	files := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		files[name] = string(data)
	}
	for _, v := range commitPointViolations(files) {
		t.Error(v)
	}
}

// TestCommitPointRuleFixtures runs the rule over small sources, so a guard that stops finding
// helpers, wrappers, or calls split across lines fails here even while every real handler is right.
func TestCommitPointRuleFixtures(t *testing.T) {
	const helpers = `func refreshThing(ctx *fasthttp.RequestCtx) bool {
	SendError(ctx, fasthttp.StatusInternalServerError, "Thing saved but reload failed")
	return false
}

func recordThing(ctx *fasthttp.RequestCtx, id string) {
	lib.RecordAuditChange(ctx, "thing.updated", "thing", "id", "name", nil, nil)
}
`
	for _, tc := range []struct {
		name, src string
		flagged   bool
	}{
		{"error helper before a record wrapper", helpers + `
func (h *H) save(ctx *fasthttp.RequestCtx) {
	if !refreshThing(ctx) {
		return
	}
	recordThing(ctx, "id")
}
`, true},
		{"record wrapper before an error helper", helpers + `
func (h *H) save(ctx *fasthttp.RequestCtx) {
	recordThing(ctx, "id")
	if !refreshThing(ctx) {
		return
	}
}
`, false},
		{"helper that sends before it records is checked itself", `func saveAndRecord(ctx *fasthttp.RequestCtx, err error) {
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Thing saved but reload failed")
		return
	}
	lib.RecordAuditChange(ctx, "thing.updated", "thing", "id", "name", nil, nil)
}
`, true},
		{"multiline saved-then-failed error before the record", `func (h *H) save(ctx *fasthttp.RequestCtx) {
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError,
			"Thing saved but reload failed")
		return
	}
	lib.RecordAuditChange(ctx, "thing.updated", "thing", "id", "name", nil, nil)
}
`, true},
		{"multiline saved-then-failed error after the record", `func (h *H) save(ctx *fasthttp.RequestCtx) {
	lib.RecordAuditChange(ctx, "thing.updated", "thing", "id", "name", nil, nil)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError,
			"Thing saved but reload failed")
		return
	}
}
`, false},
		{"multiline numeric 500 before the record", `func (h *H) save(ctx *fasthttp.RequestCtx) {
	if err != nil {
		SendError(
			ctx,
			500,
			"Thing saved but reload failed",
		)
		return
	}
	lib.RecordAuditChange(ctx, "thing.updated", "thing", "id", "name", nil, nil)
}
`, true},
		{"multiline numeric 503 before the record", `func (h *H) save(ctx *fasthttp.RequestCtx) {
	if err != nil {
		SendError(
			ctx,
			503,
			"Thing saved but reload failed",
		)
		return
	}
	lib.RecordAuditChange(ctx, "thing.updated", "thing", "id", "name", nil, nil)
}
`, false},
		{"free sender with a ctx-only signature before the record", `func sendReloadError(ctx *fasthttp.RequestCtx) {
	SendError(ctx, fasthttp.StatusInternalServerError, "Thing saved but reload failed")
}

func (h *H) save(ctx *fasthttp.RequestCtx) {
	if err != nil {
		sendReloadError(ctx)
		return
	}
	lib.RecordAuditChange(ctx, "thing.updated", "thing", "id", "name", nil, nil)
}
`, true},
		{"handler that answers a saved-then-failed error and never records", `func (h *H) save(ctx *fasthttp.RequestCtx) {
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Thing saved but reload failed")
		return
	}
	SendJSON(ctx, nil)
}
`, true},
		{"commented-out error helper before the record", helpers + `
func (h *H) save(ctx *fasthttp.RequestCtx) {
	// refreshThing(ctx)
	recordThing(ctx, "id")
}
`, false},
		{"commented-out record before an error helper", helpers + `
func (h *H) save(ctx *fasthttp.RequestCtx) {
	// recordThing(ctx, "id")
	if !refreshThing(ctx) {
		return
	}
	recordThing(ctx, "id")
}
`, true},
		{"error helper inside the record's own arguments", helpers + `
func (h *H) save(ctx *fasthttp.RequestCtx) {
	recordThing(ctx, fmt.Sprint(refreshThing(ctx)))
}
`, true},
		{"record in a trailing comment before an error helper", helpers + `
func (h *H) save(ctx *fasthttp.RequestCtx) {
	doWork() // recordThing(ctx, "id")
	if !refreshThing(ctx) {
		return
	}
	recordThing(ctx, "id")
}
`, true},
		{"error helper in a block comment before the record", helpers + `
func (h *H) save(ctx *fasthttp.RequestCtx) {
	/* refreshThing(ctx)
	   refreshThing(ctx) */
	recordThing(ctx, "id")
}
`, false},
		{"error helper in a long CRLF block comment before the record", strings.ReplaceAll(helpers+`
func (h *H) save(ctx *fasthttp.RequestCtx) {
	/* A long comment: the scanner drops each \r from its text, so blanking by
	   that text's length would stop one byte short per line and leave the
	   call on the last line visible.`+strings.Repeat("\n\t   filler", 30)+`
	   refreshThing(ctx) */
	recordThing(ctx, "id")
}
`, "\n", "\r\n"), false},
		{"error helper in a later argument line of the record", helpers + `
func (h *H) save(ctx *fasthttp.RequestCtx) {
	recordThing(
		ctx,
		fmt.Sprint(refreshThing(ctx)),
	)
}
`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := commitPointViolations(map[string]string{"fixture.go": tc.src})
			if tc.flagged && len(got) == 0 {
				t.Fatalf("want a violation, got none")
			}
			if !tc.flagged && len(got) != 0 {
				t.Fatalf("want none, got %v", got)
			}
		})
	}
}

// commitPointViolations runs the commit-point rule over Go sources keyed by file name and
// returns one message per saved-then-failed 500 answered before the function's first record.
func commitPointViolations(files map[string]string) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var funcs []commitPointFunc
	for _, name := range names {
		lines := strings.Split(stripGoComments(files[name]), "\n")
		start := -1
		for i := 0; i <= len(lines); i++ {
			if i < len(lines) && !strings.HasPrefix(lines[i], "func ") {
				continue
			}
			if start >= 0 {
				if m := funcDeclName.FindStringSubmatch(lines[start]); m != nil {
					funcs = append(funcs, commitPointFunc{file: name, name: m[1], start: start, body: lines[start:i]})
				}
			}
			start = i
		}
	}
	// Helpers are non-handler functions. Recorders are settled first, so a helper that both
	// records and sends (completePerUserOAuthAdminRepair) is a recorder and is checked itself.
	// A route handler is a method; a free function with the same ctx-only signature is a helper.
	isHandler := func(f commitPointFunc) bool {
		return strings.HasPrefix(f.body[0], "func (") && strings.HasSuffix(strings.TrimSpace(f.body[0]), handlerSignature)
	}
	recorders, senders := map[string]bool{}, map[string]bool{}
	grow := func(set map[string]bool, joins func(f commitPointFunc) bool) {
		for changed := true; changed; {
			changed = false
			for _, f := range funcs {
				if !isHandler(f) && !set[f.name] && joins(f) {
					set[f.name], changed = true, true
				}
			}
		}
	}
	grow(recorders, func(f commitPointFunc) bool {
		for _, line := range f.body[1:] {
			if recordsAudit(line, recorders) {
				return true
			}
		}
		return false
	})
	grow(senders, func(f commitPointFunc) bool {
		if recorders[f.name] {
			return false
		}
		for i, line := range f.body[1:] {
			if sendsSavedThenFailed(f.body, i+1) || callsAny(line, senders) != "" {
				return true
			}
		}
		return false
	})

	var violations []string
	for _, f := range funcs {
		if senders[f.name] {
			continue
		}
		firstRecord := -1
		for i, line := range f.body[1:] {
			i++
			if recordsAudit(line, recorders) {
				firstRecord = i
				break
			}
		}
		if firstRecord < 0 {
			if !isHandler(f) {
				continue
			}
			// A handler that answers a saved-then-failed error and never records loses the change.
			for i, line := range f.body[1:] {
				if via := callsAny(line, senders); sendsSavedThenFailed(f.body, i+1) || via != "" {
					violations = append(violations, fmt.Sprintf("%s:%d: %s answers a saved-then-failed error but never records the change; record right after the write commits (AGENTS.md)", f.file, f.start+i+2, strings.TrimSpace(f.body[0])))
					break
				}
			}
			continue
		}
		// The record call is scanned through its closing parenthesis: a sender in its arguments
		// runs before the record does.
		recordEnd := firstRecord
		for depth := strings.Count(f.body[firstRecord], "(") - strings.Count(f.body[firstRecord], ")"); depth > 0 && recordEnd+1 < len(f.body); {
			recordEnd++
			depth += strings.Count(f.body[recordEnd], "(") - strings.Count(f.body[recordEnd], ")")
		}
		for i, line := range f.body[:recordEnd+1] {
			via := callsAny(line, senders)
			if sendsSavedThenFailed(f.body, i) || via != "" {
				if via != "" {
					via = " (through " + via + ")"
				}
				violations = append(violations, fmt.Sprintf("%s:%d: %s answers a saved-then-failed error%s before recording the change; record right after the write commits (AGENTS.md)", f.file, f.start+i+1, strings.TrimSpace(f.body[0]), via))
			}
		}
	}
	return violations
}
