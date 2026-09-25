// Package warp holds the dashboard agent: its configuration, the model client
// it talks through, the read-only tools it researches with, and the loop that
// ties them together. Transports call into Service; nothing here knows about
// HTTP.
package warp

import (
	"context"
	"errors"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

var (
	// ErrUnavailable is returned when Warp has no usable configuration. It is the
	// analogue of the notification service's unavailable error, and like that
	// one it is a supported deployment state rather than a fault.
	ErrUnavailable = errors.New("warp is not configured")
	// ErrInvalidConfig wraps every validation failure from SaveConfig, so a
	// caller can map the whole family to one status without matching text.
	ErrInvalidConfig = errors.New("warp: invalid configuration")
	// ErrNoVectorStore means Warp's required semantic index has no backend.
	ErrNoVectorStore = errors.New("warp: vector store is not connected")
)

// Service is the in-process face of Warp. It is built once at bootstrap, before
// plugins load, so construction must stay allocation-only: no store reads, no
// clients, nothing that assumes a logger is present.
type Service struct {
	// store is nil when the config store does not implement WarpStore. Every
	// method treats that as "not configured" rather than a fault.
	store  configstore.WarpStore
	logger schemas.Logger
	// conversations is nil on a deployment with no log store. Chat still works;
	// it just does not file anything.
	conversations logstore.WarpConversationStore
	// logs is nil on deployments with no logging plugin. Warp's tools have
	// nothing to read there, so chat is reported unavailable rather than
	// registered and always failing.
	logs LogReader
	// governance is nil when the config store does not implement
	// GovernanceReader. Unlike logs, this is not fatal to CanChat - it only
	// narrows describe_virtual_key to reporting itself unavailable, the same
	// pattern semantic search already uses for its own optional dependency.
	governance GovernanceReader
	// responses is the gateway client's responses path, which Warp chats
	// through. Set once at construction; tests replace chatOverride instead, so
	// the loop can be driven by a scripted model.
	responses ResponsesExecutor
	// chatOverride, when set, replaces the real inference path. Test seam only.
	chatOverride ChatFunc
	// mu guards logs and the searcher built over it - the fields that change
	// after construction. Every reader takes it, not just the ones near SetLogReader:
	// an unguarded read elsewhere is the same race, just harder to find. A logging plugin enabled at runtime rebinds them while
	// requests are already being served.
	mu sync.RWMutex
	// closed records that Shutdown ran, so a later rebind cannot revive a
	// service whose lifecycle is over.
	closed bool
	// cleanupOnce/stopCleanup/cleanupStopOnce own the history retention loop.
	// The service is rebuilt once at route registration, so start and stop both
	// have to tolerate being called on an instance that never ran one.
	cleanupOnce     sync.Once
	cleanupStopOnce sync.Once
	stopCleanup     chan struct{}
	// catalog prices Warp's own usage. Nil is supported: the panel then reports
	// tokens without a cost, rather than reporting a cost of zero.
	catalog      *modelcatalog.ModelCatalog
	vectorStore  vectorstore.VectorStore
	embed        EmbeddingExecutor
	indexer      *LogIndexer
	semantic     *SemanticSearcher
	backfillJobs BackfillJobStore
}

// Option configures a Service.
type Option func(*Service)

// WithLogger sets the logger used for warnings the service cannot surface to a
// caller (a failed write after a successful answer, for example).
func WithLogger(logger schemas.Logger) Option {
	return func(s *Service) { s.logger = logger }
}

// WithLogReader gives the service something to research with, and with it a
// model client. Without one, Warp serves configuration only.
func WithLogReader(logs LogReader) Option {
	return func(s *Service) { s.logs = logs }
}

// WithModelCatalog lets the service price its own spend, so the panel can show
// what a turn cost as it finishes rather than only in the logs afterwards.
func WithModelCatalog(catalog *modelcatalog.ModelCatalog) Option {
	return func(s *Service) { s.catalog = catalog }
}

// WithVectorStore connects Warp to the deployment-wide vector store.
func WithVectorStore(store vectorstore.VectorStore) Option {
	return func(s *Service) { s.vectorStore = store }
}

// WithResponsesExecutor supplies the main gateway responses path, which Warp's
// chat runs on. Without one, Warp serves configuration only.
func WithResponsesExecutor(executor ResponsesExecutor) Option {
	return func(s *Service) { s.responses = executor }
}

// WithEmbeddingExecutor supplies the main gateway embedding path.
func WithEmbeddingExecutor(executor EmbeddingExecutor) Option {
	return func(s *Service) { s.embed = executor }
}

// WithBackfillJobStore overrides durable job lookup in tests.
func WithBackfillJobStore(store BackfillJobStore) Option {
	return func(s *Service) { s.backfillJobs = store }
}

// WithChatFunc replaces the real inference path. Test seam only: the agent loop
// can then be driven by a scripted model with no provider behind it.
func WithChatFunc(chat ChatFunc) Option {
	return func(s *Service) { s.chatOverride = chat }
}

// WithConversationStore gives the service somewhere to file chats.
//
// History comes from the log store, which the server only has after plugins
// load, so this is a real wiring option rather than the test seam it was while
// transcripts lived alongside configuration. A service built without one serves
// chat and drops the transcript, which is the right behaviour for a deployment
// that runs with logging off.
func WithConversationStore(store logstore.WarpConversationStore) Option {
	return func(s *Service) { s.conversations = store }
}

// WithConfigStore sets the configuration store directly, bypassing the
// ConfigStore narrowing NewService does. Tests use it to inject a double.
func WithConfigStore(store configstore.WarpStore) Option {
	return func(s *Service) { s.store = store }
}

// WithGovernanceReader sets describe_virtual_key's dependency directly, the
// same test seam as WithConfigStore - a double built to satisfy WarpStore
// alone has no reason to also implement every method GovernanceReader would
// otherwise be narrowed from.
func WithGovernanceReader(reader GovernanceReader) Option {
	return func(s *Service) { s.governance = reader }
}

// NewService builds a Service over the deployment's config store. A store that
// does not implement WarpStore is supported: the service then reports
// ErrUnavailable from every configuration call.
func NewService(store configstore.ConfigStore, opts ...Option) *Service {
	service := &Service{}
	if store != nil {
		service.store, _ = store.(configstore.WarpStore)
		service.backfillJobs, _ = store.(BackfillJobStore)
		service.governance, _ = store.(GovernanceReader)
	}
	for _, opt := range opts {
		opt(service)
	}
	if service.store != nil && service.vectorStore != nil && service.embed != nil {
		service.indexer = NewLogIndexer(service.store, service.vectorStore, service.embed, service.logger)
		if service.logs != nil {
			// Only when the reader can hydrate. A reader that cannot is still a
			// perfectly good LogReader for every other Warp tool, so semantic
			// search is the only thing that goes missing.
			if hydrator, ok := service.logs.(SemanticHydrator); ok {
				service.semantic = NewSemanticSearcher(service.store, service.vectorStore, service.embed, hydrator)
			}
		}
	}
	return service
}

// HasHistory reports whether conversations can be listed and filed.
func (s *Service) HasHistory() bool {
	return s.conversations != nil
}

// CanChat reports whether the chat endpoint can be served: there is data to
// read and a way to reach a model. Transports gate the route on it, because a
// route that is registered but always 503s is worse than absent - it tells the
// dashboard the feature is present, and the failure only shows up after a user
// has typed a question.
func (s *Service) CanChat() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.logs != nil && (s.responses != nil || s.chatOverride != nil)
}

// turnDeps returns the chat func, the log reader and the searcher for a turn.
//
// The reader and searcher are taken under a single RLock, because SetLogReader
// replaces both and a turn that read them separately could search one backend
// and hydrate from another.
func (s *Service) turnDeps(_ context.Context, config *schemas.WarpConfig, conversationID string) (ChatFunc, LogReader, *SemanticSearcher) {
	s.mu.RLock()
	logs, semantic := s.logs, s.semantic
	s.mu.RUnlock()
	return s.chatFuncFrom(config, conversationID), logs, semantic
}

// chatFuncFor resolves the inference function for a request. The conversation
// id travels with every model call as a logging label, so it is settled before
// the first call rather than after the last one.
func (s *Service) chatFuncFor(_ context.Context, config *schemas.WarpConfig, conversationID string) ChatFunc {
	return s.chatFuncFrom(config, conversationID)
}

// chatFuncFrom prefers the test override, then the gateway client. Neither
// changes after construction, so no lock is needed to read them.
func (s *Service) chatFuncFrom(config *schemas.WarpConfig, conversationID string) ChatFunc {
	if s.chatOverride != nil {
		return s.chatOverride
	}
	if s.responses == nil {
		return nil
	}
	return NewChat(s.responses, config, conversationID)
}

// costFuncFor prices usage against the model Warp is configured to run on.
//
// Priced against the configured model, not the qualified provider/model form
// sent upstream: the catalog keys on the bare name, and a "openai/gpt-5.5"
// lookup misses and silently prices the turn at zero.
func (s *Service) costFuncFor(config *schemas.WarpConfig) CostFunc {
	if s.catalog == nil {
		return nil
	}
	return func(usage *schemas.BifrostLLMUsage) float64 {
		// ResponsesRequest, because that is what the agent calls. The catalog
		// keeps separate chat and responses rates and only falls back when the
		// requested mode is absent, so naming the wrong one silently prices the
		// turn off the other rate card.
		// costProviderFor, not config.Provider: a provider-qualified model routes
		// by its own prefix, and pricing must follow routing or the turn is
		// priced off the wrong rate card.
		return s.catalog.CalculateCostForUsage(usage, costProviderFor(config), catalogModel(config.Model), schemas.ResponsesRequest, nil)
	}
}

// Shutdown stops Warp's background work. The model client is the gateway's and
// is not Warp's to close.
func (s *Service) Shutdown() {
	s.mu.Lock()
	s.closed = true
	// Takes no lock of its own, so it is safe inside this one.
	s.stopHistoryCleanup()
	if s.indexer != nil {
		s.indexer.Close()
	}
	s.mu.Unlock()
}

// SetLogReader rebinds what Warp researches through, after construction.
//
// Routes are registered once at startup, so a logging plugin enabled later
// cannot be picked up by rebuilding the handler - the router still holds the
// original one's closures. Rebinding inside the live service is what lets the
// chat endpoint start working without a restart.
func (s *Service) SetLogReader(logs LogReader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A plugin reload landing after Shutdown must not revive a service whose
	// lifecycle is over.
	if s.closed {
		return
	}
	s.logs = logs
	// The searcher holds its own reference to the reader, so rebinding without
	// rebuilding it left semantic search hydrating through the reader this
	// service no longer uses - or absent entirely on a deployment that enabled
	// logging after startup, which is exactly the case SetLogReader exists for.
	s.semantic = s.buildSemanticSearcher()
}

// buildSemanticSearcher returns a searcher for the current dependencies, or nil
// when any of them is missing. Callers must hold s.mu.
func (s *Service) buildSemanticSearcher() *SemanticSearcher {
	if s.store == nil || s.vectorStore == nil || s.embed == nil {
		return nil
	}
	hydrator, ok := s.logs.(SemanticHydrator)
	if !ok {
		return nil
	}
	return NewSemanticSearcher(s.store, s.vectorStore, s.embed, hydrator)
}

// researchDeps returns the reader and the searcher as one consistent pair.
//
// Taken together under a single RLock, because SetLogReader replaces both and a
// turn that read them under two locks could get the old reader with the newly
// rebuilt searcher - the agent's tools would then search one backend and hydrate
// the details from another, and the inconsistency would look like a data bug
// rather than a race.
func (s *Service) researchDeps() (LogReader, *SemanticSearcher) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.logs, s.semantic
}

// logReader returns the current reader under the read lock.
func (s *Service) logReader() LogReader {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.logs
}

// ChatUnavailableReason says why CanChat is false, so the transport can report
// the cause the dashboard branches on rather than guessing.
func (s *Service) ChatUnavailableReason() schemas.WarpUnavailableReason {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.logs == nil {
		return schemas.WarpUnavailableNoLogStore
	}
	return schemas.WarpUnavailableNotConfigured
}

// IndexLog accepts a post-persistence logging notification. It copies and
// queues bounded data; provider and vector-store I/O happen in worker goroutines.
func (s *Service) IndexLog(ctx context.Context, entry *logstore.Log) {
	if s.indexer != nil {
		s.indexer.Enqueue(ctx, entry)
	}
}

// HasConfigStore reports whether configuration can be read and written at all.
// Transports use it to decide whether to register the settings routes.
func (s *Service) HasConfigStore() bool {
	return s.store != nil
}

// warnf logs when a logger is present. The service is built before plugins
// load, so it must not assume one.
func (s *Service) warnf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Warn(format, args...)
	}
}

// infof logs when a logger is present. See warnf.
func (s *Service) infof(format string, args ...any) {
	if s.logger != nil {
		s.logger.Info(format, args...)
	}
}
