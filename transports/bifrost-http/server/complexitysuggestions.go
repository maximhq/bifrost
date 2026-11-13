package server

import (
	"context"
	"errors"
	"time"

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
	if s.Config != nil && s.Config.LogsStore != nil {
		routingPlugin.SetComplexitySuggestionStore(s.Config.LogsStore, complexitySuggestionTexts(s.Config.LogsStore))
	}
	if s.Config != nil && s.Config.ConfigStore != nil {
		// Nodes sharing the config store share the schedule, and claim each
		// due run there once between them.
		routingPlugin.SetComplexitySuggestionSchedules(s.Config.ConfigStore)
	}
	if s.SidekiqRunner != nil && s.Config != nil && s.Config.ConfigStore != nil {
		routingPlugin.SetComplexitySuggestionJobs(s.SidekiqRunner, s.Config.ConfigStore)
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
	return complexitySuggestionTexts(s.Config.LogsStore)(ctx, logIDs, historyCount)
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
