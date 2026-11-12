package heimdall

import (
	"context"
	"fmt"
	"slices"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// unsupportedCandidates are providers a replay refuses. Their models can act outside the recording, so a
// replay would not be hermetic in spirit even though no tool of ours runs.
var unsupportedCandidates = map[schemas.ModelProvider]string{
	schemas.Perplexity: "its chat API searches the web on its own and reports the queries",
}

// UnsupportedCandidateError says why a route cannot be replayed.
type UnsupportedCandidateError struct {
	Provider schemas.ModelProvider
	Reason   string
}

func (e *UnsupportedCandidateError) Error() string {
	return fmt.Sprintf("unsupported_candidate: provider %q cannot be replayed: %s", e.Provider, e.Reason)
}

// CheckCandidate refuses a route on the denylist.
func CheckCandidate(r Route) error {
	if reason, denied := unsupportedCandidates[r.Provider]; denied {
		return &UnsupportedCandidateError{Provider: r.Provider, Reason: reason}
	}
	return nil
}

// replayInitialPoolSize keeps the replay client's sync pools small; a run makes few concurrent calls.
const replayInitialPoolSize = 16

// replayAccount exposes only the providers a run needs, with the settings that could let a tool run
// or inflate a provider pool stripped. Keys come from the base account.
type replayAccount struct {
	base        schemas.Account
	providers   []schemas.ModelProvider
	concurrency int
}

func newReplayAccount(base schemas.Account, routes []Route, concurrency int) *replayAccount {
	providers := make([]schemas.ModelProvider, 0, len(routes))
	for _, r := range routes {
		if !slices.Contains(providers, r.Provider) {
			providers = append(providers, r.Provider)
		}
	}
	return &replayAccount{base: base, providers: providers, concurrency: concurrency}
}

// GetConfiguredProviders returns only the providers of the run's routes.
func (a *replayAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return slices.Clone(a.providers), nil
}

// GetKeysForProvider returns the base account's keys for a provider of the run.
func (a *replayAccount) GetKeysForProvider(ctx context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	if !slices.Contains(a.providers, p) {
		return nil, fmt.Errorf("heimdall: provider %q is not part of this replay", p)
	}
	return a.base.GetKeysForProvider(ctx, p)
}

// GetConfigForProvider returns a copy of the base config with injected tools and raw capture removed
// and concurrency capped. The base account's config is never modified.
func (a *replayAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if !slices.Contains(a.providers, p) {
		return nil, fmt.Errorf("heimdall: provider %q is not part of this replay", p)
	}
	base, err := a.base.GetConfigForProvider(p)
	if err != nil {
		return nil, err
	}
	if base == nil {
		return nil, fmt.Errorf("heimdall: no config for provider %q", p)
	}
	cfg := *base
	cfg.InjectedTools = nil
	cfg.SendBackRawRequest = false
	cfg.SendBackRawResponse = false
	cfg.CheckAndSetDefaults()
	if a.concurrency > 0 && cfg.ConcurrencyAndBufferSize.Concurrency > a.concurrency {
		cfg.ConcurrencyAndBufferSize.Concurrency = a.concurrency
	}
	return &cfg, nil
}

// newReplayClient makes a second Bifrost client with no MCP config, plugins, key store or session
// affinity. The caller owns it and must call Shutdown. Pass a nil catalog interface, not a nil pointer
// inside one, when there is no catalog.
func newReplayClient(ctx context.Context, base schemas.Account, routes []Route, concurrency int, catalog schemas.ModelInfoProvider, logger schemas.Logger) (*bifrost.Bifrost, error) {
	if base == nil {
		return nil, fmt.Errorf("heimdall: base account is required")
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("heimdall: at least one route is required")
	}
	for _, r := range routes {
		if err := CheckCandidate(r); err != nil {
			return nil, err
		}
	}
	client, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account:         newReplayAccount(base, routes, concurrency),
		Logger:          logger,
		ModelCatalog:    catalog,
		InitialPoolSize: replayInitialPoolSize,
	})
	if err != nil {
		return nil, fmt.Errorf("heimdall: replay client: %w", err)
	}
	if err := assertHermetic(client); err != nil {
		client.Shutdown()
		return nil, err
	}
	return client, nil
}

// assertHermetic fails when the client could run a tool. The replay engine calls it before every turn.
func assertHermetic(c *bifrost.Bifrost) error {
	if c == nil {
		return fmt.Errorf("not_hermetic: replay client is nil")
	}
	if c.MCPManager != nil {
		return fmt.Errorf("not_hermetic: replay client has an MCP manager")
	}
	return nil
}
