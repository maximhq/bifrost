package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/sidekiq"
)

// VKExpiryCleanupJobKind is the sidekiq kind of the daily job that deletes
// expired virtual keys which opted in to delete_after_expire.
const VKExpiryCleanupJobKind = "vk_expiry_cleanup"

const (
	vkExpiryCleanupStartupDelay = 30 * time.Second
	vkExpiryCleanupTickInterval = time.Hour
	vkExpiryCleanupTickJitter   = 5 * time.Minute
	// vkExpiryCleanupMaxNamesInNotification bounds the names listed in the notification body.
	vkExpiryCleanupMaxNamesInNotification = 20
	vkExpiryCleanupActionPath             = "/workspace/virtual-keys"
)

// vkExpiryCleanupJobID derives the job ID from the UTC day so every node enqueues
// the same row and the primary key admits exactly one run per day.
func vkExpiryCleanupJobID(now time.Time) string {
	return fmt.Sprintf("vk-expiry-cleanup-%d", now.UTC().Truncate(24*time.Hour).Unix())
}

// vkExpiryCleanupMeta is the job's resume cursor and final report.
type vkExpiryCleanupMeta struct {
	ScheduledFor time.Time `json:"scheduled_for"`
	// Deleted and Failed hold key names, keyed by ID in the handled set below.
	Deleted []string `json:"deleted,omitempty"`
	Failed  []string `json:"failed,omitempty"`
	// HandledIDs lets a re-claimed job skip keys it already processed.
	HandledIDs []string `json:"handled_ids,omitempty"`
}

// SetExpiryCleanupBackend wires the sidekiq runner and notification publisher and
// registers the cleanup handler. Call before the dispatcher starts: Enqueue rejects
// a kind with no handler, and any node may claim the job.
func (h *GovernanceHandler) SetExpiryCleanupBackend(runner *sidekiq.Runner, notify schemas.NotificationPublisher) {
	h.sidekiq = runner
	h.notify = notify
	if runner != nil {
		runner.Register(VKExpiryCleanupJobKind, h.runVKExpiryCleanupJob)
	}
}

// StartExpiryCleanupScheduler starts the per-node ticker that enqueues the daily
// cleanup job. It runs on every node: the deterministic job ID, not leadership,
// keeps the run to once per day.
func (h *GovernanceHandler) StartExpiryCleanupScheduler(ctx context.Context) {
	if h.sidekiq == nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	h.expiryCleanupStop = cancel
	go func() {
		timer := time.NewTimer(vkExpiryCleanupStartupDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			h.enqueueExpiryCleanup(ctx)
			timer.Reset(vkExpiryCleanupTickInterval + time.Duration(rand.Int63n(int64(vkExpiryCleanupTickJitter))))
		}
	}()
}

// StopExpiryCleanupScheduler stops the ticker started by StartExpiryCleanupScheduler.
func (h *GovernanceHandler) StopExpiryCleanupScheduler() {
	if h.expiryCleanupStop != nil {
		h.expiryCleanupStop()
		h.expiryCleanupStop = nil
	}
}

// clock returns the handler's time source, defaulting to the wall clock.
func (h *GovernanceHandler) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now().UTC()
}

// enqueueExpiryCleanup enqueues today's cleanup job. A duplicate ID means another
// node or an earlier tick already did, which is the mechanism working as intended.
func (h *GovernanceHandler) enqueueExpiryCleanup(ctx context.Context) {
	now := h.clock()
	inFlight, err := h.configStore.GetInFlightSidekiqJobByKind(ctx, VKExpiryCleanupJobKind)
	if err != nil {
		logger.Error("vk expiry cleanup: failed to check in-flight jobs: %v", err)
		return
	}
	if inFlight != nil {
		return
	}
	jobID := vkExpiryCleanupJobID(now)
	metaJSON, err := json.Marshal(vkExpiryCleanupMeta{ScheduledFor: now})
	if err != nil {
		logger.Error("vk expiry cleanup: failed to encode job metadata: %v", err)
		return
	}
	if err := h.sidekiq.Enqueue(ctx, jobID, VKExpiryCleanupJobKind, string(metaJSON), ""); err != nil {
		if sidekiq.IsDuplicateJobError(err) {
			logger.Debug("vk expiry cleanup: job %s already enqueued", jobID)
			return
		}
		logger.Error("vk expiry cleanup: failed to enqueue job %s: %v", jobID, err)
		return
	}
	logger.Info("vk expiry cleanup: enqueued job %s", jobID)
}

// runVKExpiryCleanupJob deletes every expired key whose effective delete_after_expire is
// true (the key's own flag, or the client default when the key has none),
// checkpointing after each one so a re-claimed job never handles a key twice, and
// posts one notification when at least one key was removed.
func (h *GovernanceHandler) runVKExpiryCleanupJob(ctx context.Context, job configstoreTables.TableSidekiqJob, progress sidekiq.ProgressFunc) (string, error) {
	var meta vkExpiryCleanupMeta
	if job.Metadata != "" {
		if err := json.Unmarshal([]byte(job.Metadata), &meta); err != nil {
			return job.Metadata, fmt.Errorf("invalid vk expiry cleanup job metadata: %w", err)
		}
	}
	handled := make(map[string]struct{}, len(meta.HandledIDs))
	for _, id := range meta.HandledIDs {
		handled[id] = struct{}{}
	}
	now := h.clock()
	// Keys without an explicit flag follow the client-wide default, read from the store
	// so every node agrees and a settings change applies to the next run.
	deleteByDefault, err := h.deleteExpiredVirtualKeysByDefault(ctx)
	if err != nil {
		return job.Metadata, fmt.Errorf("failed to load client config: %w", err)
	}
	candidates, err := h.configStore.ListExpiredVirtualKeysForDeletion(ctx, now, deleteByDefault)
	if err != nil {
		return job.Metadata, fmt.Errorf("failed to list expired virtual keys: %w", err)
	}
	checkpoint := func() {
		encoded, err := json.Marshal(meta)
		if err != nil {
			logger.Error("vk expiry cleanup: failed to encode progress: %v", err)
			return
		}
		if err := progress(string(encoded)); err != nil {
			logger.Warn("vk expiry cleanup: failed to persist progress: %v", err)
		}
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return marshalVKExpiryCleanupMeta(meta), err
		}
		if _, done := handled[candidate.ID]; done {
			continue
		}
		// Re-read the key: the user may have extended the expiry or cleared the flag
		// between the scan and this point.
		vk, err := h.configStore.GetVirtualKey(ctx, candidate.ID)
		if err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				continue
			}
			logger.Error("vk expiry cleanup: failed to load virtual key %s: %v", candidate.ID, err)
			meta.Failed = append(meta.Failed, candidate.Name)
			meta.HandledIDs = append(meta.HandledIDs, candidate.ID)
			checkpoint()
			continue
		}
		if !vk.DeletesAfterExpire(deleteByDefault) || !vk.IsExpiredAt(now) {
			continue
		}
		if err := h.configStore.DeleteVirtualKey(ctx, vk.ID); err != nil && !errors.Is(err, configstore.ErrNotFound) {
			logger.Error("vk expiry cleanup: failed to delete virtual key %s: %v", vk.ID, err)
			meta.Failed = append(meta.Failed, vk.Name)
			meta.HandledIDs = append(meta.HandledIDs, vk.ID)
			checkpoint()
			continue
		}
		// The row is gone, so the key counts as deleted even if the in-memory eviction
		// fails; the next reload from the store converges anyway.
		if err := h.governanceManager.RemoveVirtualKey(ctx, vk.ID); err != nil {
			logger.Error("vk expiry cleanup: failed to remove virtual key %s from memory: %v", vk.ID, err)
		}
		logger.Info("vk expiry cleanup: deleted expired virtual key %q (%s)", vk.Name, vk.ID)
		meta.Deleted = append(meta.Deleted, vk.Name)
		meta.HandledIDs = append(meta.HandledIDs, vk.ID)
		checkpoint()
	}
	if len(meta.Deleted) > 0 {
		h.publishExpiryCleanupNotification(ctx, meta)
	}
	return marshalVKExpiryCleanupMeta(meta), nil
}

// deleteExpiredVirtualKeysByDefault reads client.delete_expired_virtual_keys from the store.
// No client config row means the default, false.
func (h *GovernanceHandler) deleteExpiredVirtualKeysByDefault(ctx context.Context) (bool, error) {
	cfg, err := h.configStore.GetClientConfig(ctx)
	if err != nil {
		return false, err
	}
	return cfg != nil && cfg.DeleteExpiredVirtualKeys, nil
}

// marshalVKExpiryCleanupMeta encodes the metadata, falling back to "{}" so the job
// row never stores an unparseable cursor.
func marshalVKExpiryCleanupMeta(meta vkExpiryCleanupMeta) string {
	encoded, err := json.Marshal(meta)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// publishExpiryCleanupNotification posts the job's outcome to the notification bar.
// Publish errors are logged, never returned: the deletions already happened.
func (h *GovernanceHandler) publishExpiryCleanupNotification(ctx context.Context, meta vkExpiryCleanupMeta) {
	if h.notify == nil {
		return
	}
	if _, err := h.notify(ctx, vkExpiryCleanupNotification(meta)); err != nil {
		logger.Warn("vk expiry cleanup: failed to publish notification: %v", err)
	}
}

// vkExpiryCleanupNotification builds the notification for a run that deleted at least one key.
func vkExpiryCleanupNotification(meta vkExpiryCleanupMeta) schemas.NotificationInput {
	severity := schemas.NotificationSeveritySuccess
	message := fmt.Sprintf("Deleted %d expired virtual key%s: %s.", len(meta.Deleted), pluralSuffix(len(meta.Deleted)), joinNamesTruncated(meta.Deleted))
	if len(meta.Failed) > 0 {
		severity = schemas.NotificationSeverityWarning
		message += fmt.Sprintf(" %d could not be deleted: %s.", len(meta.Failed), joinNamesTruncated(meta.Failed))
	}
	return schemas.NotificationInput{
		Audience:    schemas.NotificationAudienceAll,
		Severity:    severity,
		Title:       "Expired virtual keys deleted",
		Message:     message,
		ActionLabel: "View virtual keys",
		ActionPath:  vkExpiryCleanupActionPath,
	}
}

// joinNamesTruncated lists up to vkExpiryCleanupMaxNamesInNotification names and
// summarises the rest as a count.
func joinNamesTruncated(names []string) string {
	if len(names) <= vkExpiryCleanupMaxNamesInNotification {
		return strings.Join(names, ", ")
	}
	shown := names[:vkExpiryCleanupMaxNamesInNotification]
	return fmt.Sprintf("%s and %d more", strings.Join(shown, ", "), len(names)-len(shown))
}

// pluralSuffix returns "s" for any count other than one.
func pluralSuffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
