package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/agent"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// TestAgentRegistrationWritesReachTheLiveStore checks that the registration a write leaves in the
// database is what the live store serves to governance afterwards: a new row appears, an update
// that disables it is read back disabled, and a delete drops it.
func TestAgentRegistrationWritesReachTheLiveStore(t *testing.T) {
	ctx := context.Background()
	config := libtest.Config(t, nil, nil)
	h := &AgentGatewayHandler{config: config}
	db := config.ConfigStore.DB()
	agents := config.LiveStore.Collections.Agents

	require.NoError(t, db.Create(&tables.TableAgentRegistration{Name: "alpha", AgentCardURL: "https://alpha.example/card", Enabled: true, AllowByDefault: true}).Error)
	require.NoError(t, h.refreshAgent(ctx, "alpha", false))
	got, ok := agents.Get("alpha")
	require.True(t, ok, "a written registration is served")
	require.True(t, got.Enabled)
	require.True(t, got.AllowByDefault)

	require.NoError(t, db.Model(&tables.TableAgentRegistration{}).Where("name = ?", "alpha").Update("enabled", false).Error)
	require.NoError(t, h.refreshAgent(ctx, "alpha", false))
	got, ok = agents.Get("alpha")
	require.True(t, ok, "a disabled registration keeps its record")
	require.False(t, got.Enabled)

	require.NoError(t, db.Where("name = ?", "alpha").Delete(&tables.TableAgentRegistration{}).Error)
	require.NoError(t, h.refreshAgent(ctx, "alpha", true))
	_, ok = agents.Get("alpha")
	require.False(t, ok, "a deleted registration is dropped")
}

// TestAgentRefreshFailureKeepsTheRecord checks that a refresh whose reads keep failing through its
// retries returns the error and leaves the agent's record as it was, rather than dropping it.
func TestAgentRefreshFailureKeepsTheRecord(t *testing.T) {
	ctx := context.Background()
	config := libtest.Config(t, nil, nil)
	h := &AgentGatewayHandler{config: config}
	db := config.ConfigStore.DB()
	agents := config.LiveStore.Collections.Agents

	require.NoError(t, db.Create(&tables.TableAgentRegistration{Name: "alpha", AgentCardURL: "https://alpha.example/card", Enabled: true}).Error)
	require.NoError(t, h.refreshAgent(ctx, "alpha", false))
	require.NoError(t, db.Migrator().DropTable(&tables.TableAgentRegistration{}))

	require.Error(t, h.refreshAgent(ctx, "alpha", false), "a failed read is returned")
	got, ok := agents.Get("alpha")
	require.True(t, ok, "a failed read keeps the record")
	require.True(t, got.Enabled)
}

// TestAgentRefreshRetriesAFailedRead checks that a refresh whose first read fails reads the row
// again, so a disable written just before still reaches memory instead of failing the write.
func TestAgentRefreshRetriesAFailedRead(t *testing.T) {
	ctx := context.Background()
	config := libtest.Config(t, nil, nil)
	h := &AgentGatewayHandler{config: config}
	db := config.ConfigStore.DB()
	agents := config.LiveStore.Collections.Agents

	require.NoError(t, db.Create(&tables.TableAgentRegistration{Name: "alpha", AgentCardURL: "https://alpha.example/card", Enabled: true}).Error)
	require.NoError(t, h.refreshAgent(ctx, "alpha", false))
	require.NoError(t, db.Model(&tables.TableAgentRegistration{}).Where("name = ?", "alpha").Update("enabled", false).Error)
	// Agent records are read with Scan, which runs the row callbacks rather than the query ones.
	failNextRead := &atomic.Bool{}
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register("test:fail_next_read", func(tx *gorm.DB) {
		if failNextRead.CompareAndSwap(true, false) {
			_ = tx.AddError(errors.New("sql: database is closed"))
		}
	}))
	failNextRead.Store(true)

	require.NoError(t, h.refreshAgent(ctx, "alpha", false), "the retry reads the row")
	require.False(t, failNextRead.Load(), "the first read failed")
	got, ok := agents.Get("alpha")
	require.True(t, ok)
	require.False(t, got.Enabled, "the disable is in memory")
}

// fixedAgentRegistrationManager answers every registration write with a fixed view, writing
// nothing, so a test controls which virtual keys a write reports as granted.
type fixedAgentRegistrationManager struct {
	created, updated schemas.AgentRegistrationView
}

// CreateAgentRegistration answers with the fixed created view.
func (m fixedAgentRegistrationManager) CreateAgentRegistration(context.Context, agent.CreateRequest) (schemas.AgentRegistrationView, error) {
	return m.created, nil
}

// UpdateAgentRegistration answers with the fixed updated view.
func (m fixedAgentRegistrationManager) UpdateAgentRegistration(context.Context, string, agent.UpdateRequest) (schemas.AgentRegistrationView, error) {
	return m.updated, nil
}

// DeleteAgentRegistration accepts every delete.
func (fixedAgentRegistrationManager) DeleteAgentRegistration(context.Context, string) error {
	return nil
}

// TestAgentRefreshFailureStillReloadsGrantedKeys checks that an agent create, update or delete whose
// own refresh fails still reloads the virtual keys that gained or lost the agent's grant before
// answering 500. Otherwise a key the write revoked keeps the grant, and a retry cannot repair it:
// the retry reads the registration already written, which no longer names that key.
func TestAgentRefreshFailureStillReloadsGrantedKeys(t *testing.T) {
	ctx := context.Background()
	store := &agentAuthStore{registrations: map[string]schemas.AgentRegistration{
		"alpha": {Name: "alpha", AgentCardURL: "https://alpha.example/card", VirtualKeyIDs: []string{"vk-v"}},
	}}
	manager, err := agent.NewManager(ctx, store, nil, "", http.DefaultClient, agent.ManagerConfig{})
	require.NoError(t, err)
	defer manager.Close()
	// A live store that never loaded fails every refresh.
	live, err := livestore.New()
	require.NoError(t, err)
	t.Cleanup(live.Close)
	registrations := fixedAgentRegistrationManager{
		created: schemas.AgentRegistrationView{Name: "beta", VirtualKeyIDs: []string{"vk-w"}},
		updated: schemas.AgentRegistrationView{Name: "alpha"},
	}
	reloader := &reloadCountingGovernanceManager{}
	h := NewAgentGatewayHandler(manager, registrations, &lib.Config{LiveStore: live}, reloader)

	createCtx := &fasthttp.RequestCtx{}
	createCtx.Request.SetBodyString(`{}`)
	h.create(createCtx)
	require.Equal(t, fasthttp.StatusInternalServerError, createCtx.Response.StatusCode(), string(createCtx.Response.Body()))
	require.Contains(t, string(createCtx.Response.Body()), "Agent created in database but failed to reload in-memory state")
	require.Equal(t, [][]string{{"vk-w"}}, reloader.reloadedBatches, "the key the new agent granted is reloaded")

	reloader.reloadedBatches = nil
	updateCtx := &fasthttp.RequestCtx{}
	updateCtx.SetUserValue("name", "alpha")
	updateCtx.Request.SetBodyString(`{}`)
	h.update(updateCtx)
	require.Equal(t, fasthttp.StatusInternalServerError, updateCtx.Response.StatusCode(), string(updateCtx.Response.Body()))
	require.Contains(t, string(updateCtx.Response.Body()), "Agent updated in database but failed to reload in-memory state")
	require.Equal(t, [][]string{{"vk-v"}}, reloader.reloadedBatches, "the key the update dropped is reloaded")

	reloader.reloadedBatches = nil
	deleteCtx := &fasthttp.RequestCtx{}
	deleteCtx.SetUserValue("name", "alpha")
	h.delete(deleteCtx)
	require.Equal(t, fasthttp.StatusInternalServerError, deleteCtx.Response.StatusCode(), string(deleteCtx.Response.Body()))
	require.Contains(t, string(deleteCtx.Response.Body()), "Agent deleted in database but failed to reload in-memory state")
	require.Equal(t, [][]string{{"vk-v"}}, reloader.reloadedBatches, "the key the deleted agent granted is reloaded")
}

// deleteResultRegistrations answers every delete with err, as a deployment's registration manager
// does when the delete is saved but a follow-up of its own fails, or when the delete itself fails.
type deleteResultRegistrations struct {
	err error
}

// CreateAgentRegistration is not reached by the delete route.
func (deleteResultRegistrations) CreateAgentRegistration(context.Context, agent.CreateRequest) (schemas.AgentRegistrationView, error) {
	return schemas.AgentRegistrationView{}, nil
}

// UpdateAgentRegistration is not reached by the delete route.
func (deleteResultRegistrations) UpdateAgentRegistration(context.Context, string, agent.UpdateRequest) (schemas.AgentRegistrationView, error) {
	return schemas.AgentRegistrationView{}, nil
}

// DeleteAgentRegistration answers with the configured result.
func (m deleteResultRegistrations) DeleteAgentRegistration(context.Context, string) error {
	return m.err
}

// TestAgentDeleteAnswersASavedButStaleDelete checks the delete route against a registration manager
// whose delete is saved but whose own in-memory follow-up failed: the route still drops the agent's
// record and answers the 500 that says the delete is saved, while a delete that failed outright
// keeps the record and is answered as a failed delete.
func TestAgentDeleteAnswersASavedButStaleDelete(t *testing.T) {
	ctx := context.Background()
	config := libtest.Config(t, nil, nil)
	db := config.ConfigStore.DB()
	agents := config.LiveStore.Collections.Agents
	store := &agentAuthStore{registrations: map[string]schemas.AgentRegistration{"alpha": {Name: "alpha", AgentCardURL: "https://alpha.example/card", Enabled: true}}}
	manager, err := agent.NewManager(ctx, store, nil, "", http.DefaultClient, agent.ManagerConfig{})
	require.NoError(t, err)
	defer manager.Close()
	require.NoError(t, db.Create(&tables.TableAgentRegistration{Name: "alpha", AgentCardURL: "https://alpha.example/card", Enabled: true}).Error)

	failed := NewAgentGatewayHandler(manager, deleteResultRegistrations{err: errors.New("delete failed")}, config, nil)
	require.NoError(t, failed.refreshAgent(ctx, "alpha", false))
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.SetUserValue("name", "alpha")
	failed.delete(reqCtx)
	require.Equal(t, fasthttp.StatusInternalServerError, reqCtx.Response.StatusCode())
	require.NotContains(t, string(reqCtx.Response.Body()), "deleted in database", "a failed delete is not answered as saved")
	_, ok := agents.Get("alpha")
	require.True(t, ok, "a failed delete keeps the record")

	saved := NewAgentGatewayHandler(manager, deleteResultRegistrations{err: fmt.Errorf("%w: holders", ErrSavedButNotReloaded)}, config, nil)
	reqCtx = &fasthttp.RequestCtx{}
	reqCtx.SetUserValue("name", "alpha")
	saved.delete(reqCtx)
	require.Equal(t, fasthttp.StatusInternalServerError, reqCtx.Response.StatusCode())
	require.Contains(t, string(reqCtx.Response.Body()), "Agent deleted in database but failed to reload in-memory state")
	_, ok = agents.Get("alpha")
	require.False(t, ok, "a saved delete still drops the record")
}
