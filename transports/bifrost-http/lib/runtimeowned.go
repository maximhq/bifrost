package lib

import (
	"context"
	"sync"

	"github.com/maximhq/bifrost/framework/configstore"
)

// RuntimeOwnedGovernanceRows names governance rows that only the runtime can
// create, keyed by collection. source_of_truth=config.json prunes DB rows that
// the file does not declare; these rows can never appear in the file (enterprise
// access profiles mint them per user), so pruning them deletes live governance.
type RuntimeOwnedGovernanceRows struct {
	VirtualKeyIDs  map[string]bool
	BudgetIDs      map[string]bool
	RateLimitIDs   map[string]bool
	ModelConfigIDs map[string]bool
}

// RuntimeOwnedGovernanceResolver reports the runtime-owned rows for a store. It
// takes the store because it runs inside LoadConfig, before any downstream
// wrapper around the store exists.
type RuntimeOwnedGovernanceResolver func(ctx context.Context, store configstore.ConfigStore) (*RuntimeOwnedGovernanceRows, error)

var (
	runtimeOwnedGovernanceMu       sync.RWMutex
	runtimeOwnedGovernanceResolver RuntimeOwnedGovernanceResolver
)

// RegisterRuntimeOwnedGovernanceResolver installs the resolver consulted by the
// config.json prune. Downstream builds call it once at startup, before
// LoadConfig. OSS leaves it unset, so nothing is protected and prune behavior is
// unchanged.
func RegisterRuntimeOwnedGovernanceResolver(fn RuntimeOwnedGovernanceResolver) {
	runtimeOwnedGovernanceMu.Lock()
	runtimeOwnedGovernanceResolver = fn
	runtimeOwnedGovernanceMu.Unlock()
}

// resolveRuntimeOwnedGovernanceRows returns the registered resolver's answer,
// always non-nil. A missing resolver or a resolver error yields an empty set:
// failing open here would silently delete the rows this guard exists to keep, so
// an error is logged and treated as "protect nothing" only because the prune it
// guards is itself skipped when the section is absent.
func resolveRuntimeOwnedGovernanceRows(ctx context.Context, store configstore.ConfigStore) *RuntimeOwnedGovernanceRows {
	runtimeOwnedGovernanceMu.RLock()
	resolver := runtimeOwnedGovernanceResolver
	runtimeOwnedGovernanceMu.RUnlock()
	if resolver == nil || store == nil {
		return &RuntimeOwnedGovernanceRows{}
	}
	rows, err := resolver(ctx, store)
	if err != nil {
		logger.Error("failed to resolve runtime-owned governance rows, config.json prune will skip nothing: %v", err)
		return &RuntimeOwnedGovernanceRows{}
	}
	if rows == nil {
		return &RuntimeOwnedGovernanceRows{}
	}
	return rows
}

// retainRuntimeOwned returns the file rows plus any existing row the file does
// not declare but the runtime owns, so the in-memory governance config keeps
// matching what survived in the database.
func retainRuntimeOwned[T any](fileRows, existingRows []T, id func(T) string, keep, protected map[string]bool) []T {
	out := make([]T, 0, len(fileRows)+len(existingRows))
	out = append(out, fileRows...)
	for _, row := range existingRows {
		rowID := id(row)
		if rowID == "" || keep[rowID] || !protected[rowID] {
			continue
		}
		out = append(out, row)
	}
	return out
}
