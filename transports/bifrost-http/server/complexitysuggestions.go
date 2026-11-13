package server

import (
	"context"
	"errors"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/sidekiq"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
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
