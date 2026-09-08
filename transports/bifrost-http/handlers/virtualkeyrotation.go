// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file holds the reusable virtual key rotation implementation shared by the
// rotate endpoints and background producers such as enterprise scheduled rotation.
package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/governance"
)

// VirtualKeyRotator rotates a virtual key's value exactly the way
// POST /api/governance/virtual-keys/{vk_id}/rotate does, so every producer
// (manual endpoint, bulk endpoint, scheduled enterprise rotation) shares one
// implementation of the cooldown / previous-value rules.
type VirtualKeyRotator struct {
	configStore       configstore.ConfigStore
	governanceManager GovernanceManager
}

// NewVirtualKeyRotator builds a rotator over the given store and governance
// manager. The manager is what reloads the rotated key into in-memory
// enforcement (and, in enterprise, gossips it to cluster peers).
func NewVirtualKeyRotator(configStore configstore.ConfigStore, governanceManager GovernanceManager) *VirtualKeyRotator {
	return &VirtualKeyRotator{configStore: configStore, governanceManager: governanceManager}
}

// VKRotationCooldown reads the effective rotation grace period from the stored
// client config. Errors degrade to 0 (immediate flip), never block a rotation.
func (r *VirtualKeyRotator) VKRotationCooldown(ctx context.Context) time.Duration {
	clientConfig, err := r.configStore.GetClientConfig(ctx)
	if err != nil || clientConfig == nil {
		return 0
	}
	return clientConfig.VKRotationCooldown.D()
}

// RotateVirtualKey generates a fresh value for the virtual key, applies the
// cooldown rules (the retired value stays valid until now+cooldown, or is
// dropped immediately when no cooldown is configured), persists the change,
// reloads in-memory governance and hydrates VK-scoped model-config governance
// onto the returned row. Callers passing a background context run unscoped by
// DAC, which is intended for system-initiated rotation.
func (r *VirtualKeyRotator) RotateVirtualKey(ctx context.Context, vkID string) (*configstoreTables.TableVirtualKey, error) {
	vk, err := r.configStore.GetVirtualKey(ctx, vkID)
	if err != nil {
		return nil, err
	}
	oldValue := vk.Value.GetValue()
	vk.Value = *schemas.NewSecretVar(governance.GenerateVirtualKey())
	if vk.Value.GetValue() == oldValue {
		return nil, fmt.Errorf("generated virtual key matched existing value")
	}
	// RotatedAt marks this update as a rotation, making the grace-period fields
	// authoritative in UpdateVirtualKey (a plain update carries them over
	// instead). With a cooldown the retired value keeps authenticating until
	// the expiry; repeated rotation overwrites the previous value, so only one
	// grace value exists at a time and the second-oldest dies immediately.
	now := time.Now().UTC()
	vk.RotatedAt = &now
	if cooldown := r.VKRotationCooldown(ctx); cooldown > 0 {
		vk.PreviousValue = *schemas.NewSecretVar(oldValue)
		expiresAt := now.Add(cooldown)
		vk.PreviousValueExpiresAt = &expiresAt
	} else {
		vk.ClearPreviousValue()
	}
	if err := r.configStore.UpdateVirtualKey(ctx, vk); err != nil {
		return nil, err
	}
	preloadedVk, err := r.governanceManager.ReloadVirtualKey(ctx, vk.ID)
	if err != nil {
		return nil, fmt.Errorf("virtual key rotated in database but failed to reload in-memory state: %w", err)
	}
	hydrateVKGovernanceFromStore(ctx, r.configStore, preloadedVk)
	return preloadedVk, nil
}
