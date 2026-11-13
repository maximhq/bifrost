package server

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/framework/evals/datasets"
	"github.com/maximhq/bifrost/framework/evals/runs"
	"github.com/maximhq/bifrost/framework/grant"
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

// setupEvals builds the evaluator engine, its queue and runner, and returns the handlers
// for the evaluators API and the test runs API. Both are nil when there is no config
// store to keep evaluators in.
func (s *BifrostHTTPServer) setupEvals(ctx context.Context) (*handlers.EvalsHandler, *handlers.EvalsRunsHandler) {
	if s.Config == nil || s.Config.ConfigStore == nil || s.Client == nil {
		return nil, nil
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

	engine := evals.NewEngine(evals.EngineOptions{})
	evals.RegisterAIEvaluator(engine, judge)
	evals.RegisterBuiltinAIEvaluators(engine, judge)
	evals.RegisterProgrammaticEvaluator(engine, evals.ProgrammaticOptions{})
	evals.RegisterAPIEvaluator(engine, evals.APIOptions{})
	evals.RegisterStatisticalEvaluators(engine, embedder)

	// A test run's work goes through the same runner as every other evaluation, so the
	// runner's job store has to know the jobs of runs. The run service is built first,
	// without a runner: it reads evaluator definitions through a service that needs none.
	var logs datasets.LogReader
	if s.Config.LogsStore != nil {
		logs = s.Config.LogsStore
	}
	runService := runs.NewService(runs.Options{
		Store:      s.Config.ConfigStore,
		Datasets:   datasets.NewService(s.Config.ConfigStore, logs),
		Evaluators: evals.NewService(s.Config.ConfigStore, engine, nil),
		Engine:     engine,
		Model:      evals.NewBifrostModelCaller(s.Client.ChatCompletionRequest, s.Client.ResponsesRequest, judgeOptions),
	})
	runsHandler := handlers.NewEvalsRunsHandler(runService)

	q, forgets := s.newEvalsQueue(ctx)
	if q == nil {
		// Evaluators and runs can still be read; starting one reports the reason.
		return handlers.NewEvalsHandler(evals.NewService(s.Config.ConfigStore, engine, nil)), runsHandler
	}
	jobs := runs.NewJobStore(evals.NewExecutionJobStore(s.Config.ConfigStore), runService)
	runner := evals.NewRunner(q, engine, s.evalKeySource(), jobs, evals.RunnerOptions{Volatile: forgets})
	// The run service has its runner before the runner starts: on a queue that forgets,
	// starting it puts unfinished work back, and that work queues more through the service.
	runService.SetRunner(runner)
	if err := runner.Start(ctx); err != nil {
		logger.Error("failed to start the evals runner, evaluators and test runs cannot be executed: %v", err)
		runService.SetRunner(nil)
		_ = q.Close(ctx)
		return handlers.NewEvalsHandler(evals.NewService(s.Config.ConfigStore, engine, nil)), runsHandler
	}
	s.evals = &evalsRuntime{runner: runner, queue: q}
	evaluators := evals.NewService(s.Config.ConfigStore, engine, runner)
	s.EvalsService = evaluators
	return handlers.NewEvalsHandler(evaluators), runsHandler
}

// newEvalsQueue returns the queue evaluations run on: backed by the log store when one
// is configured, so jobs survive a restart and are shared between instances, and
// in-memory otherwise. The second result reports that the queue loses its messages when the
// gateway stops, so the runner knows to put unfinished jobs back when it starts.
func (s *BifrostHTTPServer) newEvalsQueue(ctx context.Context) (queue.Queue, bool) {
	if s.Config.LogsStore != nil {
		q, err := queue.NewQueue(ctx, &queue.Config{Enabled: true, Type: queue.QueueTypeLogStore, Config: &queue.LogStoreQueueConfig{}}, queue.Dependencies{
			LogStore: s.Config.LogsStore,
			Locker:   configstore.NewDistributedLockManager(s.Config.ConfigStore, logger, configstore.WithDefaultTTL(30*time.Second)),
		}, logger)
		if err == nil && q != nil {
			return q, false
		}
		logger.Warn("evals: the log store cannot back the evaluation queue (%v); falling back to an in-memory queue, which is single instance; unfinished jobs are queued again on restart", err)
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
	s.Config.Mu.RLock()
	defer s.Config.Mu.RUnlock()
	var keys []evals.EvalKey
	for provider, config := range s.Config.Providers {
		for _, key := range config.Keys {
			if key.UseForEvals == nil || !*key.UseForEvals {
				continue
			}
			if key.Enabled != nil && !*key.Enabled {
				continue
			}
			concurrency := evals.DefaultEvalKeyConcurrency
			if key.EvalConcurrency != nil && *key.EvalConcurrency > 0 {
				concurrency = *key.EvalConcurrency
			}
			models, blocked := key.Models, key.BlacklistedModels
			keys = append(keys, evals.EvalKey{
				ID:          key.ID,
				Name:        key.Name,
				Provider:    string(provider),
				Concurrency: concurrency,
				Supports: func(model string) bool {
					return models.IsAllowed(model) && !blocked.IsBlocked(model)
				},
			})
		}
	}
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

// setupEvalDatasets builds the dataset API. Datasets live in the config store, so
// there is nothing to serve without one. The log store is optional: without it rows
// cannot be added from logs or sessions, and everything else works.
func (s *BifrostHTTPServer) setupEvalDatasets() *handlers.EvalsDatasetsHandler {
	if s.Config.ConfigStore == nil {
		return nil
	}
	var logs datasets.LogReader
	if s.Config.LogsStore != nil {
		logs = s.Config.LogsStore
	}
	service := datasets.NewService(s.Config.ConfigStore, logs)
	if s.Config.LogsStore != nil {
		// Rows can be curated from spans of pushed traces too.
		service.SetSpanReader(s.Config.LogsStore)
	}
	// Uploaded files go to the object store when the gateway has one. Without it
	// they are kept in the config database, with a size cap.
	if s.Config.ObjectStore != nil {
		service.SetFileStore(s.Config.ObjectStore)
	}
	return handlers.NewEvalsDatasetsHandler(service)
}
