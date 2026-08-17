package handlers

import (
	"context"

	"github.com/maximhq/bifrost/framework/warp"
	"github.com/maximhq/bifrost/plugins/logging"
)

// warpLogReader adapts the logging plugin's manager to the read surface Warp
// declares.
//
// Seventeen of the eighteen methods match exactly and pass through by
// embedding. Only GetAvailableVirtualKeys needs work, and only because
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
