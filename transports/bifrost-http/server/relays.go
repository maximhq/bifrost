package server

import (
	"context"
	"errors"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/jobaccounting"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/handlers"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
)

// InitializeRelays builds the Relays manager from the config store,
// wires it to the core plugin pipeline (so governance and logging see every call) and to the global
// proxy, and builds the management handler. Without a config store there is nothing to serve.
// relayManager owns relay writes; nil uses this server, and a deployment that wraps them (cluster
// broadcast) passes its own, as for the Agent Gateway. vkReloader reloads the keys a relay delete
// touches; nil uses this server.
func (s *BifrostHTTPServer) InitializeRelays(ctx context.Context, relayManager handlers.RelayManager, vkReloader handlers.VirtualKeyReloader) {
	if s.relays != nil || s.Config == nil || s.Config.ConfigStore == nil {
		return
	}
	if relayManager == nil {
		relayManager = s
	}
	if vkReloader == nil {
		vkReloader = s
	}
	manager := relay.NewManager(s.Config.ConfigStore, logger)
	if s.Client != nil {
		manager.SetPluginPipeline(
			func() relay.PluginPipeline { return s.Client.AcquirePluginPipeline() },
			func(pipeline relay.PluginPipeline) {
				if p, ok := pipeline.(*bifrost.PluginPipeline); ok {
					s.Client.ReleasePluginPipeline(p)
				}
			},
		)
	}
	manager.SetGlobalProxy(func() (*schemas.ProxyConfig, error) { return lib.InheritedGlobalProxy(s.Config.GetGlobalProxyConfig()) })
	if err := manager.Reload(ctx); err != nil {
		logger.Warn("failed to load relays: %v", err)
	}
	s.relays = manager
	s.RelaysHandler = handlers.NewRelaysHandler(manager, relayManager, s.Config, vkReloader)
	// The server resolves redacted keys for the LLM logs filter data; Relay Logs use the same.
	s.RelaysHandler.SetRedactedKeysManager(s)
	if names := manager.Names(); len(names) > 0 {
		logger.Info("relays initialized: %d endpoint(s)", len(names))
	}
	// Bootstrap wires the other accounting sweepers before this manager exists, so this one starts
	// here; reloads restart it from WireBatchAccountingSweeper.
	s.startEndpointJobSweeper(s.endpointUsageReporter())
}

// startEndpointJobSweeper (re)starts the sweeper that settles async jobs relay
// routes started. It runs on every reload so the reporter tracks the live governance plugin.
// It ticks every 10s, so a job's interval_seconds below 30 is honoured; each tick is one indexed
// query for due relay jobs.
func (s *BifrostHTTPServer) startEndpointJobSweeper(reporter jobaccounting.RelayUsageReporter) {
	s.endpointSweeperMu.Lock()
	defer s.endpointSweeperMu.Unlock()
	if s.endpointSweeperCancel != nil {
		s.endpointSweeperCancel()
		s.endpointSweeperCancel = nil
	}
	if s.relays == nil || s.Config == nil {
		return
	}
	store, ok := s.Config.ConfigStore.(jobaccounting.SweepStore)
	if !ok {
		return
	}
	// Only a call naming a job Bifrost started is relayed free as a status check. The lookup reads
	// the config store alone, so it stays on with logging off: a job started before logging was
	// turned off still has free status checks, though it settles only once logs are back, as batch
	// and video jobs do. A job the store does not have is no job; any other store error is a failed
	// lookup, so the status check is retried rather than charged or refused.
	s.relays.SetJobStartRouteLookup(func(ctx context.Context, relayName, routeKey, jobID string) (string, error) {
		return jobaccounting.RelayJobStartRoute(ctx, store, relayName, routeKey, jobID)
	})
	s.relays.SetJobLookup(func(ctx context.Context, relayName, routeKey, jobID string) (bool, error) {
		known, err := jobaccounting.HasRelayJob(ctx, store, relayName, routeKey, jobID)
		if errors.Is(err, configstore.ErrNotFound) {
			return false, nil
		}
		return known, err
	})
	if s.Config.LogsStore == nil {
		return
	}
	// Named per node, as the batch and video sweepers are, so claims show the relay kind and survive reloads.
	nodeID := ""
	// The logging plugin also reports each settled job to the trace-fed connectors, as it does for
	// batch and video. Assigned only when found, so a missing plugin stays a nil interface.
	var emitter jobaccounting.RelaySettlementEmitter
	if loggerPlugin, err := lib.FindPluginAs[*logging.LoggerPlugin](s.Config, logging.PluginName); err == nil && loggerPlugin != nil {
		nodeID = loggerPlugin.ClusterNodeID()
		// A caller's finished status check is confirmed with Bifrost's own poll.
		loggerPlugin.SetRelayJobPoller(s.relays)
		emitter = loggerPlugin
		// A job settled with no tracer is marked done but its cost never reaches the metrics, and the
		// first sweep runs at once. At boot the tracer comes later and Bootstrap starts the sweeper
		// then; on a reload the rebuilt plugin gets the tracer before the first sweep.
		if s.TracingMiddleware == nil {
			return
		}
		s.setSettlementTracer()
	}
	var base context.Context = context.Background()
	if s.Ctx != nil {
		base = s.Ctx
	}
	sweepCtx, cancel := context.WithCancel(base)
	s.endpointSweeperCancel = cancel
	config := jobaccounting.SweeperConfig{Interval: 10 * time.Second, Logger: logger, ClaimedBy: jobaccounting.RunnerID("relay-sweeper", nodeID)}
	// Assigned only when set: a nil *kvstore.Store in the interface would not compare as nil.
	if s.Config.KVStore != nil {
		config.KVStore = s.Config.KVStore
	}
	sweeper := jobaccounting.NewRelayJobSweeper(store, s.relays, s.Config.LogsStore, reporter, emitter, config)
	go sweeper.Run(sweepCtx)
}

// rebuildRelays recompiles every endpoint after the global proxy changes, through the
// handler so the rebuild is serialized with endpoint writes.
func (s *BifrostHTTPServer) rebuildRelays(ctx context.Context) {
	if s.RelaysHandler == nil {
		return
	}
	if err := s.RelaysHandler.ReloadFromStore(ctx); err != nil {
		logger.Warn("failed to reload relays after a proxy change: %v", err)
	}
}

// endpointUsageReporter returns the live governance plugin as the job settlement reporter, nil when
// governance is not loaded.
func (s *BifrostHTTPServer) endpointUsageReporter() jobaccounting.RelayUsageReporter {
	plugin, err := lib.FindPluginAs[governance.BaseGovernancePlugin](s.Config, s.getGovernancePluginName())
	if err != nil || plugin == nil {
		return nil
	}
	reporter, _ := plugin.(jobaccounting.RelayUsageReporter)
	return reporter
}

var errRelaysNotInitialized = errors.New("relays are not initialized")

// CreateRelay is the handlers.RelayManager this server passes to its relays handler: a plain
// manager call, which a clustered server wraps with a broadcast.
func (s *BifrostHTTPServer) CreateRelay(ctx context.Context, cfg *schemas.RelayConfig) (*schemas.RelayConfig, error) {
	if s.relays == nil {
		return nil, errRelaysNotInitialized
	}
	return s.relays.Create(ctx, cfg)
}

// UpdateRelay replaces one relay locally.
func (s *BifrostHTTPServer) UpdateRelay(ctx context.Context, name string, cfg *schemas.RelayConfig) (*schemas.RelayConfig, error) {
	if s.relays == nil {
		return nil, errRelaysNotInitialized
	}
	return s.relays.Update(ctx, name, cfg)
}

// DeleteRelay deletes one relay locally and returns the keys whose grants it took away.
func (s *BifrostHTTPServer) DeleteRelay(ctx context.Context, name string) ([]string, error) {
	if s.relays == nil {
		return nil, errRelaysNotInitialized
	}
	return s.relays.Delete(ctx, name)
}
