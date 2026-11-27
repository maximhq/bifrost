package server

import (
	"context"
	"sync/atomic"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/maximhq/bifrost/framework/queue"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/handlers"
)

const (
	// evalKeysCacheTTL bounds how stale the list of eval keys may be. The runner asks
	// for it on every job it places and every job it runs, so it is not rebuilt each time.
	evalKeysCacheTTL = time.Second
	// evalsShutdownTimeout is how long shutdown waits for running evaluations. All of
	// the server's cleanup shares 30 seconds, and the client and the stores close
	// after this, so it takes a third. A job cut short stays in a durable queue.
	evalsShutdownTimeout = 10 * time.Second
)

// evalsRuntime is the part of the evals feature that runs in the background and must
// be stopped on shutdown.
type evalsRuntime struct {
	runner *evals.Runner
	queue  queue.Queue
}

// setupEvals builds the evaluator engine, its queue and runner, and returns the handler
// for the evals API. It returns nil when there is no config store to keep evaluators in.
func (s *BifrostHTTPServer) setupEvals(ctx context.Context) *handlers.EvalsHandler {
	if s.Config == nil || s.Config.ConfigStore == nil || s.Client == nil {
		return nil
	}
	// RegisterAPIRoutes has one caller today; if that changes, a second call must not
	// leave the first one's consumers running.
	s.shutdownEvals()

	// An evaluator's model call has no caller behind it: the job may run later and on
	// another instance. It goes through the plugin pipeline as a key-less request, so it
	// is logged and governed like one, and it is pinned to the eval key by the adapter.
	judgeOptions := evals.BifrostJudgeOptions{
		PrepareContext: func(_ context.Context, bifrostCtx *schemas.BifrostContext) {
			bifrostCtx.SetGrant(grant.New())
		},
	}
	judge := evals.NewBifrostJudge(s.Client.ChatCompletionRequest, s.Client.ResponsesRequest, judgeOptions)
	embedder := evals.NewBifrostEmbedder(s.Client.EmbeddingRequest, judgeOptions)

	engine := evals.NewEngine(evals.EngineOptions{LiveStore: s.Config.LiveStore})
	evals.RegisterAIEvaluator(engine, judge)
	evals.RegisterBuiltinAIEvaluators(engine, judge)
	evals.RegisterProgrammaticEvaluator(engine, evals.ProgrammaticOptions{})
	evals.RegisterAPIEvaluator(engine, evals.APIOptions{})
	evals.RegisterStatisticalEvaluators(engine, embedder)

	q, forgets := s.newEvalsQueue(ctx)
	if q == nil {
		// Evaluators can still be created and edited; running one reports the reason.
		return handlers.NewEvalsHandler(evals.NewService(s.Config.ConfigStore, engine, nil))
	}
	runner := evals.NewRunner(q, engine, s.evalKeySource(), evals.NewExecutionJobStore(s.Config.ConfigStore), evals.RunnerOptions{Volatile: forgets, Logger: logger})
	if err := runner.Start(ctx); err != nil {
		logger.Error("failed to start the evals runner, evaluators cannot be executed: %v", err)
		_ = q.Close(ctx)
		return handlers.NewEvalsHandler(evals.NewService(s.Config.ConfigStore, engine, nil))
	}
	s.evals = &evalsRuntime{runner: runner, queue: q}
	return handlers.NewEvalsHandler(evals.NewService(s.Config.ConfigStore, engine, runner))
}

// newEvalsQueue returns the queue evaluations run on: backed by the log store when one
// is configured, so jobs survive a restart and are shared between instances, and
// in-memory otherwise. The second result reports that the queue loses its messages when the
// gateway stops, so the runner knows to put unfinished jobs back when it starts.
//
// With a log store there is no in-memory fallback. Other instances may share the
// execution store, and a runner on a queue that forgets takes over every running job on
// start, theirs included. When the log store cannot back the queue, evaluations are
// unavailable and the reason is logged.
func (s *BifrostHTTPServer) newEvalsQueue(ctx context.Context) (queue.Queue, bool) {
	if s.Config.LogsStore != nil {
		q, err := queue.NewQueue(ctx, &queue.Config{Enabled: true, Type: queue.QueueTypeLogStore, Config: &queue.LogStoreQueueConfig{}}, queue.Dependencies{
			LogStore: s.Config.LogsStore,
			Locker:   configstore.NewDistributedLockManager(s.Config.ConfigStore, logger, configstore.WithDefaultTTL(30*time.Second)),
		}, logger)
		if err == nil && q != nil {
			return q, false
		}
		logger.Error("evals: the log store cannot back the evaluation queue, evaluators cannot be executed: %v", err)
		return nil, false
	}
	q, err := queue.NewQueue(ctx, &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, logger)
	if err != nil {
		logger.Error("evals: failed to create the evaluation queue, evaluators cannot be executed: %v", err)
		return nil, false
	}
	return q, true
}

// evalKeysSnapshot is one reading of the eval keys.
type evalKeysSnapshot struct {
	keys []evals.EvalKey
	at   time.Time
}

// evalKeySource lists the provider keys marked "use for evaluations". Only enabled keys
// count. The result is cached briefly: it is read far more often than keys change.
func (s *BifrostHTTPServer) evalKeySource() evals.KeySource {
	var cached atomic.Pointer[evalKeysSnapshot]
	return func(context.Context) ([]evals.EvalKey, error) {
		if snapshot := cached.Load(); snapshot != nil && time.Since(snapshot.at) < evalKeysCacheTTL {
			return snapshot.keys, nil
		}
		keys := s.listEvalKeys()
		cached.Store(&evalKeysSnapshot{keys: keys, at: time.Now()})
		return keys, nil
	}
}

func (s *BifrostHTTPServer) listEvalKeys() []evals.EvalKey {
	var keys []evals.EvalKey
	s.Config.LiveStore.Collections.Providers.Range(func(p *registry.Provider) bool {
		provider, config := p.Name, p.Config
		// Key selection checks a key against the provider's base type (a vLLM key serves
		// only its instance's model), so placement checks it the same way.
		baseProviderType := provider
		if config.CustomProviderConfig != nil && config.CustomProviderConfig.BaseProviderType != "" {
			baseProviderType = config.CustomProviderConfig.BaseProviderType
		}
		for _, key := range s.Config.GetProviderKeys(provider) {
			if key.UseForEvals == nil || !*key.UseForEvals {
				continue
			}
			if key.Enabled != nil && !*key.Enabled {
				continue
			}
			concurrency := evals.DefaultEvalKeyConcurrency
			if key.EvalConcurrency != nil && *key.EvalConcurrency > 0 {
				// A value saved before the maximum existed must not start that many workers.
				concurrency = min(*key.EvalConcurrency, evals.MaxEvalKeyConcurrency)
			}
			served := key // GetProviderKeys returns a copy, so Supports can keep it
			keys = append(keys, evals.EvalKey{
				ID:          key.ID,
				Name:        key.Name,
				Provider:    string(provider),
				Concurrency: concurrency,
				Supports: func(model string) bool {
					return bifrost.KeySupportsModelOn(baseProviderType, &served, model)
				},
			})
		}
		return true
	})
	return keys
}

// shutdownEvals stops consuming evaluation jobs and waits for the ones in flight. Jobs
// still queued stay in the queue for the next start, when the queue is durable.
func (s *BifrostHTTPServer) shutdownEvals() {
	if s.evals == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), evalsShutdownTimeout)
	defer cancel()
	if err := s.evals.runner.Close(ctx); err != nil {
		logger.Warn("evals: runner did not shut down cleanly: %v", err)
	}
	if err := s.evals.queue.Close(ctx); err != nil {
		logger.Warn("evals: queue did not shut down cleanly: %v", err)
	}
	s.evals = nil
}
