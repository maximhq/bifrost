package lib

import (
	"context"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/framework/jobaccounting"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/plugins/routing"
)

const (
	// GenAIUploadKeyPrefix starts every GenAI resumable upload ID, which is also its key in
	// SharedTables.GenAIUploads.
	GenAIUploadKeyPrefix = "genai_upload_session:"
	// RealtimeKeyPrefix starts the key of every realtime client secret mapping.
	RealtimeKeyPrefix = "realtime:ephemeral-key:"
)

// SharedTables are the live-store tables that keep state one request leaves for later ones:
// session bindings, routing's warm coordination, poll leases, pending uploads and realtime client
// secrets. Every table keeps write times, and every key in one starts with the prefix its consumer
// writes under. All are created once at config load, before anything writes them, and outlive
// plugin reloads.
type SharedTables struct {
	// Sessions holds session state: core's route and key bindings and the routing plugin's
	// complexity tiers, all under bifrost.SessionStateKeyPrefix.
	Sessions *livestore.Table[string, string]
	// Warm holds the routing plugin's semantic exemplar warm claims and results.
	Warm *livestore.Table[string, string]
	// BatchPollLeases, VideoPollLeases and InteractionPollLeases hold each job's poll lease, so
	// one poller works a job at a time, one table per job kind.
	BatchPollLeases       *livestore.Table[string, jobaccounting.PollLease]
	VideoPollLeases       *livestore.Table[string, jobaccounting.PollLease]
	InteractionPollLeases *livestore.Table[string, jobaccounting.PollLease]
	// GenAIUploads holds a GenAI resumable upload between its start and its data, keyed by the
	// upload ID.
	GenAIUploads *livestore.Table[string, *gemini.GeminiResumableUploadSession]
	// RealtimeKeys maps the realtime client secrets Bifrost mints to what each stands for.
	RealtimeKeys *livestore.Table[string, RealtimeKeyMapping]
}

// RealtimeKeyMapping is what a realtime client secret Bifrost minted stands for: the key it was
// minted on, the virtual key that asked for it and the provider's own secret.
type RealtimeKeyMapping struct {
	KeyID         string `json:"key_id,omitempty"`
	VirtualKey    string `json:"virtual_key,omitempty"`
	ProviderToken string `json:"provider_token,omitempty"`
}

// The names of the shared tables this package owns. A process has one set, so the names are fixed.
const (
	sessionStateTable          = "session_state"
	genaiUploadSessionsTable   = "genai_upload_sessions"
	realtimeEphemeralKeysTable = "realtime_ephemeral_keys"
)

// UnmarshalJSON reads a mapping from JSON: the object this version writes, each field trimmed, or
// the bare key ID string earlier versions wrote.
func (m *RealtimeKeyMapping) UnmarshalJSON(raw []byte) error {
	var keyID string
	if err := sonic.Unmarshal(raw, &keyID); err == nil {
		*m = RealtimeKeyMapping{KeyID: strings.TrimSpace(keyID)}
		return nil
	}
	type fields RealtimeKeyMapping
	var f fields
	if err := sonic.Unmarshal(raw, &f); err != nil {
		return err
	}
	*m = RealtimeKeyMapping{
		KeyID:         strings.TrimSpace(f.KeyID),
		VirtualKey:    strings.TrimSpace(f.VirtualKey),
		ProviderToken: strings.TrimSpace(f.ProviderToken),
	}
	return nil
}

// newSharedTables creates every shared table on live.
func newSharedTables(ctx context.Context, live *livestore.Store) (*SharedTables, error) {
	if live == nil {
		return nil, fmt.Errorf("shared tables: %w", livestore.ErrNoLiveStore)
	}
	t := &SharedTables{}
	var err error
	if t.Sessions, err = live.NewTable[string, string](ctx, sessionStateTable, livestore.WithWriteTimes()); err != nil {
		return nil, err
	}
	if t.Warm, err = live.NewTable[string, string](ctx, routing.ComplexityWarmTable, livestore.WithWriteTimes()); err != nil {
		return nil, err
	}
	if t.BatchPollLeases, err = live.NewTable[string, jobaccounting.PollLease](ctx, jobaccounting.PollLeaseTable(jobaccounting.ProviderJobKindBatch), livestore.WithWriteTimes()); err != nil {
		return nil, err
	}
	if t.VideoPollLeases, err = live.NewTable[string, jobaccounting.PollLease](ctx, jobaccounting.PollLeaseTable(jobaccounting.ProviderJobKindVideo), livestore.WithWriteTimes()); err != nil {
		return nil, err
	}
	if t.InteractionPollLeases, err = live.NewTable[string, jobaccounting.PollLease](ctx, jobaccounting.PollLeaseTable(jobaccounting.ProviderJobKindInteraction), livestore.WithWriteTimes()); err != nil {
		return nil, err
	}
	if t.GenAIUploads, err = live.NewTable[string, *gemini.GeminiResumableUploadSession](ctx, genaiUploadSessionsTable, livestore.WithWriteTimes()); err != nil {
		return nil, err
	}
	if t.RealtimeKeys, err = live.NewTable[string, RealtimeKeyMapping](ctx, realtimeEphemeralKeysTable, livestore.WithWriteTimes()); err != nil {
		return nil, err
	}
	return t, nil
}
