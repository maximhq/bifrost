package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/handlers"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/sidekiq"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggestrun"
)

// maxComplexitySuggestionTexts bounds the logs one text read opens: a
// cluster shows the model at most a dozen requests.
const maxComplexitySuggestionTexts = 64

// complexitySuggestionTexts reads back the text the semantic router embedded
// for captured requests, from their logs. A log that is gone, or that hides
// its content, gives no text, so its request never reaches the suggestion
// model. Reads go through FindByID, which also fetches a payload offloaded to
// object storage, and never back for a hidden one.
func complexitySuggestionTexts(store logstore.LogStore) routing.ComplexitySuggestionTexts {
	return func(ctx context.Context, logIDs []string, messageHistoryCount int) (map[string]string, error) {
		if len(logIDs) > maxComplexitySuggestionTexts {
			logIDs = logIDs[:maxComplexitySuggestionTexts]
		}
		texts := make(map[string]string, len(logIDs))
		for _, id := range logIDs {
			entry, err := store.FindByID(ctx, id)
			if errors.Is(err, logstore.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if entry == nil || entry.ContentHidden {
				continue
			}
			userAgent := ""
			if entry.UserAgent != nil {
				userAgent = *entry.UserAgent
			}
			if text := complexity.LoggedInputText(entry.InputHistoryParsed, entry.ResponsesInputHistoryParsed, userAgent, messageHistoryCount); text != "" {
				texts[id] = text
			}
		}
		return texts, nil
	}
}

// wireRoutingSuggestions gives a routing plugin instance what phrase
// suggestions need from the server: the logs' retention, which bounds
// captured vectors; the log store, for failed vector deletes, suggestions,
// runs and request text; the config store, for the schedule's state; and the
// job runner runs are queued on. Called for
// the instance Bootstrap starts and again for every reloaded one, which
// otherwise would come up without any of it.
func (s *BifrostHTTPServer) wireRoutingSuggestions(routingPlugin *routing.RoutingPlugin) {
	if routingPlugin == nil {
		return
	}
	s.applyRoutingLogRetention(routingPlugin)
	s.applyRoutingCaptureDeleteRecorder(routingPlugin)
	// The job runner first: a run is started only once the store is set, so
	// one due at start-up is queued as a job rather than run inline on this
	// node.
	if s.SidekiqRunner != nil && s.Config != nil && s.Config.ConfigStore != nil {
		routingPlugin.SetComplexitySuggestionJobs(s.SidekiqRunner, s.Config.ConfigStore)
	}
	if s.Config != nil && s.Config.ConfigStore != nil {
		// Nodes sharing the config store share the schedule, and claim each
		// due run there once between them.
		routingPlugin.SetComplexitySuggestionSchedules(s.Config.ConfigStore)
	}
	if s.Config != nil && s.Config.LogsStore != nil {
		routingPlugin.SetComplexitySuggestionStore(s.Config.LogsStore, complexitySuggestionTexts(s.Config.LogsStore))
	}
}

// registerComplexitySuggestionsJob registers the suggestion-run job kind. The
// handler looks the routing plugin up per job, so a run queued before a
// plugin reload is run by the instance that replaced it.
func (s *BifrostHTTPServer) registerComplexitySuggestionsJob(runner *sidekiq.Runner) {
	if runner == nil {
		return
	}
	runner.Register(routing.ComplexitySuggestionsJobKind, func(ctx context.Context, job tables.TableSidekiqJob, progress sidekiq.ProgressFunc) (string, error) {
		routingPlugin, err := s.getRoutingPlugin()
		if err != nil {
			// No routing plugin, so no suggestions to run: not a failure. The
			// run it was recorded as when queued is closed as skipped, so it
			// never reads as waiting for a run that will not come.
			var store logstore.LogStore
			if s.Config != nil {
				store = s.Config.LogsStore
			}
			meta, recordErr := routing.SkipComplexitySuggestionsJob(ctx, store, job, "the routing plugin was not loaded when the run started")
			if recordErr != nil {
				logger.Warn("failed to record phrase suggestion run as skipped: %v", recordErr)
			}
			return meta, nil
		}
		return routingPlugin.RunComplexitySuggestionsJob(ctx, job, progress)
	})
	runner.RegisterSummarizer(routing.ComplexitySuggestionsJobKind, routing.SummarizeComplexitySuggestionsJob)
}

// ComplexitySuggestionStore is where phrase suggestions and their runs are
// kept, or nil when the deployment has no log store.
func (s *BifrostHTTPServer) ComplexitySuggestionStore() logstore.ComplexitySuggestionStore {
	if s.Config == nil || s.Config.LogsStore == nil {
		return nil
	}
	return s.Config.LogsStore
}

// ComplexitySuggestionTexts reads the request text of example logs within the
// caller's access scope, as the router embedded them.
func (s *BifrostHTTPServer) ComplexitySuggestionTexts(ctx context.Context, routerID string, logIDs []string) (map[string]string, error) {
	if s.Config == nil || s.Config.LogsStore == nil {
		return map[string]string{}, nil
	}
	historyCount := 1
	if routingPlugin, err := s.getRoutingPlugin(); err == nil {
		historyCount = routingPlugin.ComplexitySuggestionMessageHistoryCount(routerID)
	}
	// Each log is read once, in batches of what one read opens, so a caller
	// showing more examples than that (an impact report's changes and its
	// flagged phrases) gets every readable one.
	seen := make(map[string]struct{}, len(logIDs))
	unique := make([]string, 0, len(logIDs))
	for _, id := range logIDs {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			unique = append(unique, id)
		}
	}
	read := complexitySuggestionTexts(s.Config.LogsStore)
	texts := make(map[string]string, len(unique))
	for start := 0; start < len(unique); start += maxComplexitySuggestionTexts {
		batch, err := read(ctx, unique[start:min(start+maxComplexitySuggestionTexts, len(unique))], historyCount)
		if err != nil {
			return nil, err
		}
		for id, text := range batch {
			texts[id] = text
		}
	}
	return texts, nil
}

// RoutingPluginLoaded reports whether the routing plugin is loaded.
func (s *BifrostHTTPServer) RoutingPluginLoaded() bool {
	_, err := s.getRoutingPlugin()
	return err == nil
}

// ComplexitySuggestionRouter reports whether a router exists, and the phrase
// bank version it serves.
func (s *BifrostHTTPServer) ComplexitySuggestionRouter(routerID string) (string, bool) {
	routingPlugin, err := s.getRoutingPlugin()
	if err != nil || !routingPlugin.ComplexitySuggestionRouterExists(routerID) {
		return "", false
	}
	return routingPlugin.ComplexitySuggestionRouterVersion(routerID), true
}

// StartComplexitySuggestionRun starts one router's suggestion run outside the
// schedule. Without a routing plugin it fails with
// handlers.ErrRoutingPluginUnavailable: the server is missing a part, which is
// not the same as suggestions being off.
func (s *BifrostHTTPServer) StartComplexitySuggestionRun(ctx context.Context, routerID string, window time.Duration) (string, error) {
	routingPlugin, err := s.getRoutingPlugin()
	if err != nil {
		return "", err
	}
	return routingPlugin.StartComplexitySuggestionRunNow(ctx, routerID, window)
}

// EstimateComplexitySuggestionRun sizes one router's run over window.
func (s *BifrostHTTPServer) EstimateComplexitySuggestionRun(ctx context.Context, routerID string, window time.Duration) (suggestrun.Estimate, error) {
	routingPlugin, err := s.getRoutingPlugin()
	if err != nil {
		return suggestrun.Estimate{}, err
	}
	return routingPlugin.EstimateComplexitySuggestionRun(ctx, routerID, window)
}

// TestComplexitySuggestionPhrases routes examples with and without phrases
// against one router's phrase bank.
func (s *BifrostHTTPServer) TestComplexitySuggestionPhrases(ctx context.Context, routerID string, phrases []complexity.SuggestionPhrase, examples []suggestrun.TestExample) (suggestrun.PhraseTest, error) {
	routingPlugin, err := s.getRoutingPlugin()
	if err != nil {
		return suggestrun.PhraseTest{}, complexity.ErrNoServingGeneration
	}
	return routingPlugin.TestComplexitySuggestionPhrases(ctx, routerID, phrases, examples)
}

// The server backs the phrase-suggestion endpoints; a missing method would
// make every one of them answer 503, so it is checked here.
var _ handlers.ComplexitySuggestionManager = (*BifrostHTTPServer)(nil)

// CancelComplexitySuggestionRun stops one router's queued or running run.
func (s *BifrostHTTPServer) CancelComplexitySuggestionRun(ctx context.Context, routerID, runID string) (logstore.ComplexitySuggestionRun, error) {
	routingPlugin, err := s.getRoutingPlugin()
	if err != nil {
		return logstore.ComplexitySuggestionRun{}, routing.ErrComplexitySuggestionsOff
	}
	return routingPlugin.CancelComplexitySuggestionRun(ctx, routerID, runID)
}

// ComplexitySuggestionImpact replays one router's recent captured requests
// against a draft phrase bank and prices what changes: each changed request's
// logged tokens at the model it was served by and at the router's model for
// its new tier. Logs and example text are read within the caller's scope.
func (s *BifrostHTTPServer) ComplexitySuggestionImpact(ctx context.Context, routerID string, window time.Duration, draft []complexity.SuggestionPhrase, draftMinSimilarity *float64) (handlers.ComplexitySuggestionImpactReport, error) {
	routingPlugin, err := s.getRoutingPlugin()
	if err != nil {
		return handlers.ComplexitySuggestionImpactReport{}, routing.ErrComplexitySuggestionsOff
	}
	impact, window, err := routingPlugin.ComplexitySuggestionImpact(ctx, routerID, window, draft, draftMinSimilarity)
	if err != nil {
		return handlers.ComplexitySuggestionImpactReport{}, err
	}
	var usage []logstore.ComplexityReplayUsage
	if store := s.ComplexitySuggestionStore(); store != nil && len(impact.Changed) > 0 {
		ids := make([]string, len(impact.Changed))
		for i, c := range impact.Changed {
			ids[i] = c.LogID
		}
		if usage, err = store.ListComplexityReplayUsage(ctx, ids); err != nil {
			return handlers.ComplexitySuggestionImpactReport{}, err
		}
	}
	// Text only for what the report lists: the first changes and each
	// flagged phrase's example.
	var shown []string
	for i, c := range impact.Changed {
		if i == 25 {
			break
		}
		shown = append(shown, c.LogID)
	}
	for _, a := range impact.Attention {
		shown = append(shown, a.LogID)
	}
	texts, err := s.ComplexitySuggestionTexts(ctx, routerID, shown)
	if err != nil {
		return handlers.ComplexitySuggestionImpactReport{}, err
	}
	models, err := complexityImpactModels(ctx, s.complexityImpactRouters(), routerID)
	if err != nil {
		return handlers.ComplexitySuggestionImpactReport{}, err
	}
	return handlers.PriceComplexitySuggestionImpact(impact, window, usage, models,
		handlers.MemoizeComplexityImpactPrice(s.complexityImpactPrice), texts), nil
}

// complexityImpactRouterStore reads the router whose targets price a replay.
type complexityImpactRouterStore interface {
	GetModelRouter(ctx context.Context, id string) (*configstore.ModelRouter, error)
}

// complexityImpactRouters is where routers are read from, or nil without a
// config store.
func (s *BifrostHTTPServer) complexityImpactRouters() complexityImpactRouterStore {
	if s.Config == nil || s.Config.ConfigStore == nil {
		return nil
	}
	return s.Config.ConfigStore
}

// complexityImpactModels names the model each of a router's tiers is priced
// at: its highest-weighted target that names both a provider and a model
// (targets are read heaviest first). A tier without one is not priced, and a
// router the store does not hold (the Complexity Router page's) has none. A
// store that cannot be read fails the report rather than price nothing.
func complexityImpactModels(ctx context.Context, store complexityImpactRouterStore, routerID string) (map[string]handlers.ComplexityImpactModel, error) {
	models := map[string]handlers.ComplexityImpactModel{}
	if store == nil {
		return models, nil
	}
	router, err := store.GetModelRouter(ctx, routerID)
	if errors.Is(err, configstore.ErrNotFound) {
		return models, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the router's targets: %w", err)
	}
	if router == nil {
		return models, nil
	}
	for _, target := range router.Targets {
		if _, seen := models[target.Tier]; seen || target.Provider == nil || target.Model == nil || *target.Provider == "" || *target.Model == "" {
			continue
		}
		models[target.Tier] = handlers.ComplexityImpactModel{Provider: *target.Provider, Model: *target.Model}
	}
	return models, nil
}

// complexityImpactPrice prices token counts at list price, before caching,
// as a chat completion. A model the catalog has no price for is not priced.
func (s *BifrostHTTPServer) complexityImpactPrice(provider, model string, promptTokens, completionTokens int64) (float64, bool) {
	if s.Config == nil || s.Config.ModelCatalog == nil {
		return 0, false
	}
	catalog := s.Config.ModelCatalog
	if catalog.GetPricingEntryForModel(model, schemas.ModelProvider(provider)) == nil {
		return 0, false
	}
	usage := &schemas.BifrostLLMUsage{PromptTokens: int(promptTokens), CompletionTokens: int(completionTokens), TotalTokens: int(promptTokens + completionTokens)}
	return catalog.CalculateCostForUsage(usage, schemas.ModelProvider(provider), model, schemas.ChatCompletionRequest, nil), true
}
