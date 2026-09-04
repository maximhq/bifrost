package handlers

import (
	"context"
	"errors"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/maximhq/bifrost/plugins/logging"
)

// warpLogReader adapts the logging plugin's manager to the read surface Warp
// declares.
//
// Most methods match exactly and pass through by embedding. The two adapters
// below cover Warp-specific return types and bounded candidate hydration.
// GetAvailableVirtualKeys needs work only because
// logging.KeyPair and warp.KeyPair are field-identical but carry different
// struct tags - aliasing them would change the JSON an existing endpoint already
// serves, so the conversion lives here instead.
//
// The adapter also has to exist somewhere: plugins/logging depends on framework,
// so framework/warp cannot import it back. handlers sits above both, which makes
// this the one place that can see both types.
type warpLogReader struct {
	logging.LogManager
}

// GetLogsByIDs preserves the vector result order while routing every read
// through LogManager.GetLog. That method applies queryscope and deliberately
// returns not-found for both absent and inaccessible rows.
func (r warpLogReader) GetLogsByIDs(ctx context.Context, ids []string) ([]logstore.Log, error) {
	const maxCandidateLogs = 100
	if len(ids) > maxCandidateLogs {
		ids = ids[:maxCandidateLogs]
	}
	logs := make([]logstore.Log, 0, len(ids))
	for _, id := range ids {
		entry, err := r.LogManager.GetLog(ctx, id)
		if errors.Is(err, logstore.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if entry != nil {
			logs = append(logs, *entry)
		}
	}
	return logs, nil
}

// GetAvailableVirtualKeys converts the manager's key pairs into Warp's.
func (r warpLogReader) GetAvailableVirtualKeys(ctx context.Context, limit int, query string) ([]warp.KeyPair, error) {
	pairs, err := r.LogManager.GetAvailableVirtualKeys(ctx, limit, query)
	if err != nil {
		return nil, err
	}
	converted := make([]warp.KeyPair, 0, len(pairs))
	for _, pair := range pairs {
		converted = append(converted, warp.KeyPair{ID: pair.ID, Name: pair.Name})
	}
	return converted, nil
}
