package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// auditChanges returns the audit changes a handler recorded on ctx.
func auditChanges(t *testing.T, ctx *fasthttp.RequestCtx) []schemas.AuditChange {
	t.Helper()
	changes, _ := ctx.UserValue(schemas.AuditChangesUserValueKey).([]schemas.AuditChange)
	return changes
}

// requireSingleAuditChange asserts exactly one change with the given event name and returns it.
func requireSingleAuditChange(t *testing.T, ctx *fasthttp.RequestCtx, eventName string) schemas.AuditChange {
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
func requireNoSecret(t *testing.T, change schemas.AuditChange, secret string) {
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
		inMemoryStore: &lib.Config{
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				schemas.OpenAI: {Keys: []schemas.Key{{
					ID:     "key-1",
					Name:   "openai-key",
					Value:  *schemas.NewSecretVar(storedValue),
					Weight: 1,
				}}},
			},
		},
		modelsManager: &mockModelsManager{},
	}
	attachBifrostClient(t, h.inMemoryStore)

	ctx := newTestRequestCtx(`{"name":"openai-key","value":"` + newValue + `","weight":2}`)
	ctx.SetUserValue("provider", string(schemas.OpenAI))
	ctx.SetUserValue("key_id", "key-1")
	h.updateProviderKey(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("update status=%d body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if got := h.inMemoryStore.Providers[schemas.OpenAI].Keys[0].Value.GetValue(); got != newValue {
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
