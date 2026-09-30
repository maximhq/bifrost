package configstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

func createPushFixtureAgent(t *testing.T, store *RDBConfigStore) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, store.CreateAgentRegistration(context.Background(), &schemas.AgentRegistration{Name: "fixture", AgentCardURL: "https://example.com", Enabled: true, CreatedAt: now, UpdatedAt: now}))
}

func TestAgentPushConfigCRUD(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	createPushFixtureAgent(t, store)
	now := time.Now().UTC()
	config := &schemas.AgentPushConfig{
		AgentName:        "fixture",
		TaskID:           "task-1",
		ConfigID:         "cfg-1",
		URL:              "https://client.example/callback",
		Token:            &schemas.SecretVar{Val: "client-token", SecretType: schemas.SecretTypePlainText},
		AuthScheme:       "Bearer",
		AuthCredentials:  &schemas.SecretVar{Val: "client-credential", SecretType: schemas.SecretTypePlainText},
		IngressTokenHash: "hash-1",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	require.NoError(t, store.SaveAgentPushConfig(ctx, config))
	// The caller's secrets must remain plaintext after the row hook encrypted its copy.
	require.Equal(t, "client-token", config.Token.GetValue())

	got, err := store.GetAgentPushConfig(ctx, "fixture", "task-1", "cfg-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "https://client.example/callback", got.URL)
	require.Equal(t, "client-token", got.Token.GetValue())
	require.Equal(t, "client-credential", got.AuthCredentials.GetValue())
	require.Equal(t, "hash-1", got.IngressTokenHash)

	byHash, err := store.GetAgentPushConfigByIngressTokenHash(ctx, "fixture", "hash-1")
	require.NoError(t, err)
	require.NotNil(t, byHash)
	require.Equal(t, "cfg-1", byHash.ConfigID)
	missing, err := store.GetAgentPushConfigByIngressTokenHash(ctx, "fixture", "other")
	require.NoError(t, err)
	require.Nil(t, missing)

	// Re-registering the same key replaces the callback (upsert semantics).
	config.URL = "https://client.example/replaced"
	config.IngressTokenHash = "hash-2"
	require.NoError(t, store.SaveAgentPushConfig(ctx, config))
	listed, err := store.ListAgentPushConfigs(ctx, "fixture", "task-1")
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, "https://client.example/replaced", listed[0].URL)
	require.Equal(t, "hash-2", listed[0].IngressTokenHash)

	page, total, err := store.ListAgentPushConfigsPaginated(ctx, schemas.AgentPushConfigQuery{AgentNames: []string{"fixture"}, TaskID: "task-", ConfigID: "cfg-", URL: "replaced", Limit: 25})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, page, 1)
	require.Equal(t, "fixture", page[0].AgentName)
	require.Equal(t, "task-1", page[0].TaskID)
	require.Equal(t, "cfg-1", page[0].ConfigID)
	names, err := store.ListAgentPushConfigAgentNames(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"fixture"}, names)

	deleted, err := store.DeleteAgentPushConfig(ctx, "fixture", "task-1", "cfg-1")
	require.NoError(t, err)
	require.True(t, deleted)
	deleted, err = store.DeleteAgentPushConfig(ctx, "fixture", "task-1", "cfg-1")
	require.NoError(t, err)
	require.False(t, deleted)
	gone, err := store.GetAgentPushConfig(ctx, "fixture", "task-1", "cfg-1")
	require.NoError(t, err)
	require.Nil(t, gone)
}

func TestPendingAgentPushConfigSettlementIsExact(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	createPushFixtureAgent(t, store)
	now := time.Now().UTC()
	for _, pending := range []struct {
		taskID string
		hash   string
	}{
		{taskID: "__bifrost_pending__:one", hash: "hash-one"},
		{taskID: "__bifrost_pending__:two", hash: "hash-two"},
	} {
		require.NoError(t, store.SaveAgentPushConfig(ctx, &schemas.AgentPushConfig{
			AgentName:        "fixture",
			TaskID:           pending.taskID,
			ConfigID:         "shared-config",
			URL:              "https://client.example/callback",
			IngressTokenHash: pending.hash,
			CreatedAt:        now,
			UpdatedAt:        now,
		}))
	}

	deleted, err := store.DeletePendingAgentPushConfig(ctx, "fixture", "hash-one", "__bifrost_pending__:one")
	require.NoError(t, err)
	require.True(t, deleted)
	remaining, err := store.GetAgentPushConfigByIngressTokenHash(ctx, "fixture", "hash-two")
	require.NoError(t, err)
	require.NotNil(t, remaining)

	require.NoError(t, store.BindAgentPushConfigTask(ctx, "fixture", "hash-two", "__bifrost_pending__:two", "task-two"))
	bound, err := store.GetAgentPushConfig(ctx, "fixture", "task-two", "shared-config")
	require.NoError(t, err)
	require.NotNil(t, bound)
	require.Equal(t, "hash-two", bound.IngressTokenHash)

	deleted, err = store.DeletePendingAgentPushConfig(ctx, "fixture", "hash-two", "__bifrost_pending__:two")
	require.NoError(t, err)
	require.False(t, deleted)
	bound, err = store.GetAgentPushConfig(ctx, "fixture", "task-two", "shared-config")
	require.NoError(t, err)
	require.NotNil(t, bound)
}

func TestAgentPushDeliveryOutbox(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	createPushFixtureAgent(t, store)
	now := time.Now().UTC()
	delivery := &schemas.AgentPushDelivery{
		ID:            "digest-1",
		AgentName:     "fixture",
		TaskID:        "task-1",
		ConfigID:      "cfg-1",
		Payload:       `{"kind":"status-update"}`,
		Status:        schemas.AgentPushDeliveryStatusPending,
		NextAttemptAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	created, err := store.CreateAgentPushDeliveryIfNotExists(ctx, delivery)
	require.NoError(t, err)
	require.True(t, created)
	// A retransmission with the same digest deduplicates.
	created, err = store.CreateAgentPushDeliveryIfNotExists(ctx, delivery)
	require.NoError(t, err)
	require.False(t, created)

	due, err := store.ListDueAgentPushDeliveries(ctx, now.Add(time.Second), 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.Equal(t, "digest-1", due[0].ID)

	for _, limit := range []int{0, -1} {
		due, err = store.ListDueAgentPushDeliveries(ctx, now.Add(time.Second), limit)
		require.EqualError(t, err, "delivery batch limit must be positive")
		require.Nil(t, due)
	}

	// A row scheduled in the future is not due.
	future := *delivery
	future.ID = "digest-2"
	future.NextAttemptAt = now.Add(time.Hour)
	_, err = store.CreateAgentPushDeliveryIfNotExists(ctx, &future)
	require.NoError(t, err)
	due, err = store.ListDueAgentPushDeliveries(ctx, now.Add(time.Second), 10)
	require.NoError(t, err)
	require.Len(t, due, 1)

	// Recording a dead outcome removes the row from the due set.
	leaseUntil := time.Now().UTC().Add(time.Minute)
	claimed, err := store.ClaimAgentPushDelivery(ctx, delivery.ID, "runner-1", leaseUntil)
	require.NoError(t, err)
	require.True(t, claimed)
	delivery.Status = schemas.AgentPushDeliveryStatusDead
	delivery.Attempts = 3
	delivery.LastError = "downstream returned 500"
	delivery.UpdatedAt = time.Now().UTC()
	require.NoError(t, store.UpdateAgentPushDeliveryOutcome(ctx, delivery, "runner-1", leaseUntil))
	due, err = store.ListDueAgentPushDeliveries(ctx, now.Add(time.Second), 10)
	require.NoError(t, err)
	require.Empty(t, due)
}

func TestConcurrentAgentPushDeliveryClaimHasOneWinner(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	sqlDB, err := store.DB().DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	createPushFixtureAgent(t, store)
	now := time.Now().UTC()
	created, err := store.CreateAgentPushDeliveryIfNotExists(ctx, &schemas.AgentPushDelivery{
		ID: "claim-race", AgentName: "fixture", TaskID: "task-1", ConfigID: "cfg-1",
		Payload: `{}`, Status: schemas.AgentPushDeliveryStatusPending, NextAttemptAt: now,
		CreatedAt: now, UpdatedAt: now,
	})
	require.NoError(t, err)
	require.True(t, created)

	const contenders = 8
	start := make(chan struct{})
	results := make(chan bool, contenders)
	errors := make(chan error, contenders)
	for i := range contenders {
		go func() {
			<-start
			won, claimErr := store.ClaimAgentPushDelivery(ctx, "claim-race", fmt.Sprintf("runner-%d", i), now.Add(time.Minute))
			results <- won
			errors <- claimErr
		}()
	}
	close(start)
	wins := 0
	for range contenders {
		require.NoError(t, <-errors)
		if <-results {
			wins++
		}
	}
	require.Equal(t, 1, wins)
}

func TestAgentPushDeliveryClaimAndFence(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	createPushFixtureAgent(t, store)
	now := time.Now().UTC()
	delivery := &schemas.AgentPushDelivery{
		ID: "claimed-delivery", AgentName: "fixture", TaskID: "task-1", ConfigID: "cfg-1",
		Payload: `{}`, Status: schemas.AgentPushDeliveryStatusPending, NextAttemptAt: now,
		CreatedAt: now, UpdatedAt: now,
	}
	created, err := store.CreateAgentPushDeliveryIfNotExists(ctx, delivery)
	require.NoError(t, err)
	require.True(t, created)

	firstLease := now.Add(time.Minute)
	claimed, err := store.ClaimAgentPushDelivery(ctx, delivery.ID, "runner-1", firstLease)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = store.ClaimAgentPushDelivery(ctx, delivery.ID, "runner-2", now.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, claimed)

	due, err := store.ListDueAgentPushDeliveries(ctx, now.Add(time.Second), 10)
	require.NoError(t, err)
	require.Empty(t, due)

	expired := now.Add(-time.Second)
	require.NoError(t, store.DB().WithContext(ctx).Model(&tables.TableAgentPushDelivery{}).
		Where("id = ?", delivery.ID).Update("claimed_until", expired).Error)
	secondLease := now.Add(2 * time.Minute)
	claimed, err = store.ClaimAgentPushDelivery(ctx, delivery.ID, "runner-2", secondLease)
	require.NoError(t, err)
	require.True(t, claimed)

	staleOutcome := *delivery
	staleOutcome.Status = schemas.AgentPushDeliveryStatusDead
	staleOutcome.Attempts = 1
	staleOutcome.UpdatedAt = now.Add(time.Second)
	err = store.UpdateAgentPushDeliveryOutcome(ctx, &staleOutcome, "runner-1", firstLease)
	require.EqualError(t, err, "agent push delivery not found or no longer owned by caller")

	var row tables.TableAgentPushDelivery
	require.NoError(t, store.DB().WithContext(ctx).First(&row, "id = ?", delivery.ID).Error)
	require.Equal(t, schemas.AgentPushDeliveryStatusPending, row.Status)
	require.Equal(t, "runner-2", row.ClaimedBy)
	require.NotNil(t, row.ClaimedUntil)

	winningOutcome := *delivery
	winningOutcome.Status = schemas.AgentPushDeliveryStatusDelivered
	winningOutcome.Attempts = 1
	winningOutcome.UpdatedAt = now.Add(2 * time.Second)
	require.NoError(t, store.UpdateAgentPushDeliveryOutcome(ctx, &winningOutcome, "runner-2", secondLease))
	row = tables.TableAgentPushDelivery{}
	require.NoError(t, store.DB().WithContext(ctx).First(&row, "id = ?", delivery.ID).Error)
	require.Equal(t, schemas.AgentPushDeliveryStatusDelivered, row.Status)
	require.Equal(t, 1, row.Attempts)
	require.Empty(t, row.ClaimedBy)
	require.Nil(t, row.ClaimedUntil)
}

func TestPruneAgentPushDeliveries(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	createPushFixtureAgent(t, store)
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Truncate(time.Second)
	for _, status := range []string{schemas.AgentPushDeliveryStatusDelivered, schemas.AgentPushDeliveryStatusDead, schemas.AgentPushDeliveryStatusPending} {
		for _, age := range []struct {
			name      string
			updatedAt time.Time
			removed   bool
		}{
			{"old", cutoff.Add(-time.Second), status != schemas.AgentPushDeliveryStatusPending},
			{"boundary", cutoff, false},
			{"recent", cutoff.Add(time.Second), false},
		} {
			t.Run(status+"/"+age.name, func(t *testing.T) {
				delivery := &schemas.AgentPushDelivery{
					ID: status + "-" + age.name, AgentName: "fixture", TaskID: "task-1", ConfigID: "cfg-1",
					Status: status, Payload: `{}`, NextAttemptAt: cutoff,
					CreatedAt: cutoff.Add(-24 * time.Hour), UpdatedAt: age.updatedAt,
				}
				created, err := store.CreateAgentPushDeliveryIfNotExists(ctx, delivery)
				require.NoError(t, err)
				require.True(t, created)
				require.NoError(t, store.PruneAgentPushDeliveries(ctx, cutoff))
				// Reinsertion proves deletion, or continued deduplication for retained rows.
				created, err = store.CreateAgentPushDeliveryIfNotExists(ctx, delivery)
				require.NoError(t, err)
				require.Equal(t, age.removed, created)
			})
		}
	}
}
