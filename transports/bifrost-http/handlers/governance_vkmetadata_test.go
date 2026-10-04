package handlers

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// TestVirtualKeyMetadataRoundTrip drives the handlers end to end: create stores metadata, an
// update that omits it leaves it alone, an object replaces it, {} clears it, and invalid metadata
// is refused with a 400 that names the problem on both create and update.
func TestVirtualKeyMetadataRoundTrip(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	handler := &GovernanceHandler{configStore: store, governanceManager: &budgetOverrideTestGovernanceManager{store: store}}
	ctx := context.Background()

	createCtx := newTestRequestCtx(`{"name":"vk-metadata","metadata":{"cost_center":"cc-42","owner":"a@example.com"}}`)
	handler.createVirtualKey(createCtx)
	require.Equal(t, fasthttp.StatusOK, createCtx.Response.StatusCode(), "create resp=%s", createCtx.Response.Body())
	var created struct {
		VirtualKey struct {
			ID       string            `json:"id"`
			Metadata map[string]string `json:"metadata"`
		} `json:"virtual_key"`
	}
	require.NoError(t, json.Unmarshal(createCtx.Response.Body(), &created))
	assert.Equal(t, map[string]string{"cost_center": "cc-42", "owner": "a@example.com"}, created.VirtualKey.Metadata, "create response must echo the metadata")

	putVK := func(t *testing.T, body string, wantStatus int) []byte {
		t.Helper()
		putCtx := newTestRequestCtx(body)
		putCtx.SetUserValue("vk_id", created.VirtualKey.ID)
		handler.updateVirtualKey(putCtx)
		require.Equal(t, wantStatus, putCtx.Response.StatusCode(), "PUT body=%s resp=%s", body, putCtx.Response.Body())
		return putCtx.Response.Body()
	}
	metadataOf := func(t *testing.T) map[string]string {
		t.Helper()
		stored, err := store.GetVirtualKey(ctx, created.VirtualKey.ID)
		require.NoError(t, err)
		return stored.Metadata
	}

	putVK(t, `{"description":"touched"}`, fasthttp.StatusOK)
	assert.Equal(t, map[string]string{"cost_center": "cc-42", "owner": "a@example.com"}, metadataOf(t), "an update that omits metadata must not change it")

	putVK(t, `{"metadata":{"cost_center":"cc-7"}}`, fasthttp.StatusOK)
	assert.Equal(t, map[string]string{"cost_center": "cc-7"}, metadataOf(t), "metadata replaces as a whole")

	body := putVK(t, `{"metadata":{"bad key":"x"}}`, fasthttp.StatusBadRequest)
	assert.Contains(t, string(body), "invalid metadata key")
	assert.Equal(t, map[string]string{"cost_center": "cc-7"}, metadataOf(t), "a refused update must not change metadata")

	putVK(t, `{"metadata":{}}`, fasthttp.StatusOK)
	assert.Empty(t, metadataOf(t), "{} clears metadata")

	for _, bad := range []string{
		`{"name":"vk-bad-1","metadata":{"bifrost_alb_provider":"x"}}`,
		`{"name":"vk-bad-2","metadata":{"a/b":"x"}}`,
	} {
		badCtx := newTestRequestCtx(bad)
		handler.createVirtualKey(badCtx)
		assert.Equal(t, fasthttp.StatusBadRequest, badCtx.Response.StatusCode(), "create body=%s resp=%s", bad, badCtx.Response.Body())
		assert.Contains(t, string(badCtx.Response.Body()), "metadata key")
	}
}

// TestGetVirtualKeysMetadataFilter pins the list contract: metadata_<key>=<value> query params
// route to the filtered store path and AND together, and an invalid key is a 400 rather than an
// ignored filter that would return every key.
func TestGetVirtualKeysMetadataFilter(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	handler := &GovernanceHandler{configStore: store, governanceManager: &budgetOverrideTestGovernanceManager{store: store}}

	for _, body := range []string{
		`{"name":"vk-a","metadata":{"cost_center":"cc-42","env":"prod"}}`,
		`{"name":"vk-b","metadata":{"cost_center":"cc-42","env":"dev"}}`,
		`{"name":"vk-c"}`,
	} {
		createCtx := newTestRequestCtx(body)
		handler.createVirtualKey(createCtx)
		require.Equal(t, fasthttp.StatusOK, createCtx.Response.StatusCode(), "create resp=%s", createCtx.Response.Body())
	}

	list := func(t *testing.T, query string) (int, []string) {
		t.Helper()
		listCtx := newTestRequestCtx("")
		listCtx.Request.SetRequestURI("/api/governance/virtual-keys?" + query)
		handler.getVirtualKeys(listCtx)
		if listCtx.Response.StatusCode() != fasthttp.StatusOK {
			return listCtx.Response.StatusCode(), nil
		}
		var resp struct {
			VirtualKeys []struct {
				Name string `json:"name"`
			} `json:"virtual_keys"`
		}
		require.NoError(t, json.Unmarshal(listCtx.Response.Body(), &resp))
		names := make([]string, 0, len(resp.VirtualKeys))
		for _, vk := range resp.VirtualKeys {
			names = append(names, vk.Name)
		}
		sort.Strings(names)
		return fasthttp.StatusOK, names
	}

	status, names := list(t, "metadata_cost_center=cc-42")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, []string{"vk-a", "vk-b"}, names)

	status, names = list(t, "metadata_cost_center=cc-42&metadata_env=prod")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, []string{"vk-a"}, names)

	status, names = list(t, "search=cc-42&limit=10")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, []string{"vk-a", "vk-b"}, names, "search matches metadata values")

	status, _ = list(t, "metadata_bad%20key=x")
	assert.Equal(t, fasthttp.StatusBadRequest, status)
}
