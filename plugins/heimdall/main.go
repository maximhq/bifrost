// Package heimdall replays recorded agent sessions against a candidate model with every tool answered
// from a recording, compares behaviour against a noise floor, and gates a session-sticky canary rollout.
//
// A run reads sessions from the log store, replays them on the original model and on the candidate through
// a client that cannot run a tool, scores the difference, and asks the gate for a verdict. Runs and their
// results live in the config store. The plugin does not touch live requests.
package heimdall

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const PluginName = "heimdall"

// retentionInterval is how often the plugin clears old runs' content.
const retentionInterval = 24 * time.Hour

// ClientSettings are gateway settings that decide whether sessions can be replayed.
type ClientSettings struct {
	ContentLoggingDisabled     bool // payloads are not logged, so there is nothing to replay
	ObjectStorageHiddenContent bool // hidden content may still sit in object storage
}

// Deps are what the plugin needs from the gateway.
type Deps struct {
	Account     schemas.Account
	LogStore    LogReader // the gateway's log store, or anything that reads logs the same way
	ConfigStore configstore.ConfigStore
	// Catalog prices replies and lists the parameters a model accepts. It may be nil.
	Catalog *modelcatalog.ModelCatalog
	// ClientSettings reads the gateway's current settings. It may be nil.
	ClientSettings func() ClientSettings
}

// Plugin is the Heimdall built-in plugin.
type Plugin struct {
	config  *Config
	logger  schemas.Logger
	deps    Deps
	store   *store
	reader  LogReader
	recon   *Reconstructor
	pricing schemas.ModelInfoProvider
	params  paramSupport
	now     func() time.Time

	ctx    context.Context
	cancel context.CancelCauseFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	active *activeRun // the run being worked on in this process, or nil

	// testHookClient, set only by tests, is called with each run's replay client before any replay.
	testHookClient func(*bifrost.Bifrost)
}

// Init returns a new plugin. A nil config takes the defaults. It needs the log store, the config store and an
// account. Runs that a previous process left queued or running are ended as interrupted, since there is no
// resuming them.
func Init(ctx context.Context, config *Config, logger schemas.Logger, deps Deps) (*Plugin, error) {
	cfg, err := config.withDefaults()
	if err != nil {
		return nil, err
	}
	switch {
	case logger == nil:
		return nil, fmt.Errorf("heimdall: logger is required")
	case deps.LogStore == nil:
		return nil, fmt.Errorf("heimdall: the log store is required: turn logging on")
	case deps.ConfigStore == nil:
		return nil, fmt.Errorf("heimdall: the config store is required")
	case deps.Account == nil:
		return nil, fmt.Errorf("heimdall: an account is required")
	}

	p := &Plugin{
		config: cfg,
		logger: logger,
		deps:   deps,
		store:  &store{cs: deps.ConfigStore},
		reader: deps.LogStore,
		now:    time.Now,
	}
	p.recon = NewReconstructor(p.reader, ReconOptions{MaxSteps: cfg.MaxSteps, InjectedTools: p.hasInjectedTools})
	// A nil catalog pointer must not become a non-nil interface.
	if deps.Catalog != nil {
		p.pricing = deps.Catalog
		p.params = deps.Catalog
	}
	p.ctx, p.cancel = context.WithCancelCause(context.Background())

	if n, err := p.store.markStaleRuns(ctx, p.now()); err != nil {
		return nil, fmt.Errorf("heimdall: end stale runs: %w", err)
	} else if n > 0 {
		logger.Warn("heimdall: ended %d run(s) left unfinished by a previous process as interrupted", n)
	}
	p.expireOldContent(ctx)
	p.wg.Add(1)
	go p.retentionLoop()
	return p, nil
}

var errShutdown = errors.New("the plugin shut down")

// hasInjectedTools reports whether a provider has injected tools configured now.
func (p *Plugin) hasInjectedTools(provider schemas.ModelProvider) bool {
	cfg, err := p.deps.Account.GetConfigForProvider(provider)
	return err == nil && cfg != nil && len(cfg.InjectedTools) > 0
}

// retentionLoop clears old runs' content every day until the plugin stops.
func (p *Plugin) retentionLoop() {
	defer p.wg.Done()
	ticker := time.NewTicker(retentionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.expireOldContent(p.ctx)
		}
	}
}

// expireOldContent removes the prompts and model text of runs older than the retention period.
func (p *Plugin) expireOldContent(ctx context.Context) {
	cutoff := p.now().Add(-time.Duration(p.config.RetentionDays) * 24 * time.Hour)
	snapshots, replays, err := p.store.expireContent(ctx, cutoff)
	if err != nil {
		p.logger.Warn("heimdall: clearing old run content failed: %v", err)
		return
	}
	if snapshots+replays > 0 {
		p.logger.Info("heimdall: cleared the content of %d session snapshot(s) and %d replay(s) older than %d day(s)", snapshots, replays, p.config.RetentionDays)
	}
}

// GetName implements schemas.BasePlugin.
func (p *Plugin) GetName() string { return PluginName }

// Cleanup implements schemas.BasePlugin. It stops the run in progress, which ends as interrupted, and waits for it.
func (p *Plugin) Cleanup() error {
	p.cancel(errShutdown)
	p.wg.Wait()
	return nil
}

// PreRequestHook implements schemas.LLMPlugin (no-op until the rollout hook lands).
func (p *Plugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// PreLLMHook implements schemas.LLMPlugin (no-op).
func (p *Plugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook implements schemas.LLMPlugin (no-op).
func (p *Plugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}
