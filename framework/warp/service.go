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
	"github.com/maximhq/bifrost/framework/mcptools"
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
	// client owns Warp's dedicated Bifrost instance. It exists only when there is
	// something to read; tests replace chatOverride instead, so the loop can be
	// driven by a scripted model.
	client *Client
	// chatOverride, when set, replaces the real inference path. Test seam only.
	chatOverride ChatFunc
	// mu guards logs, client and chatOverride - the fields that change after
	// construction. Every reader takes it, not just the ones near SetLogReader:
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
	// mcp dispatches Warp's tool calls to Bifrost's MCP server
	// (framework/mcptools) rather than to a local closure - see MCPExecutor.
	// Set via WithMCPExecutor; the HTTP handler wires in client.ExecuteChatMCPTool.
	mcp MCPExecutor
	// mcpTools lists what that server declares, so the model is offered the
	// schemas the server actually hosts rather than a copy kept here. Set via
	// WithMCPToolLister; the HTTP handler wires in client.GetAvailableMCPTools.
	mcpTools MCPToolLister
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

// WithModelCatalog lets the service price its own spend. Warp's client is
// plugin-free, so nothing upstream computes a cost for it; its spend is
// invisible to the gateway's budgets and has to be visible in the panel instead.
func WithModelCatalog(catalog *modelcatalog.ModelCatalog) Option {
	return func(s *Service) { s.catalog = catalog }
}

// WithVectorStore connects Warp to the deployment-wide vector store.
func WithVectorStore(store vectorstore.VectorStore) Option {
	return func(s *Service) { s.vectorStore = store }
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

// WithMCPExecutor supplies the dispatch function Warp's agent calls to run a
// tool, once Bifrost's MCP server (framework/mcptools) has been reached. In
// production this wraps client.ExecuteChatMCPTool; tests wire in a scripted
// double instead.
func WithMCPExecutor(executor MCPExecutor) Option {
	return func(s *Service) { s.mcp = executor }
}

// WithMCPToolLister supplies the discovery function declaredTools uses to
// fetch the allowed tools' schemas from Bifrost's MCP server each turn. In
// production this wraps client.GetAvailableMCPTools; tests script the list.
func WithMCPToolLister(list MCPToolLister) Option {
	return func(s *Service) { s.mcpTools = list }
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
	if service.logs != nil {
		service.client = NewClient(service.logger)
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

// LogReader exposes the same log-store dependency Warp's own tools read
// from, so a caller building framework/mcptools.Deps does not need its own
// path to logging - it reuses whatever this Service was already given via
// WithLogReader. Nil is a supported deployment state (logging disabled).
func (s *Service) LogReader() LogReader {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.logs
}

// SemanticSearcher exposes Warp's semantic-search dependency for the same
// reason LogReader does. Nil when the vector store, embedding executor or
// config store were not all supplied.
func (s *Service) SemanticSearcher() *SemanticSearcher {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.semantic
}

// MCPDeps snapshots what Bifrost's MCP server (framework/mcptools) reads
// through, for mcptools.NewServer to call once per tool call. Resolved per call
// because SetLogReader rebinds the reader and rebuilds the searcher when the
// logging plugin is enabled or removed at runtime; one snapshot under one lock
// so a call never pairs the old reader with the new searcher.
func (s *Service) MCPDeps() *mcptools.Deps {
	s.mu.RLock()
	defer s.mu.RUnlock()
	deps := &mcptools.Deps{LogManager: s.logs, Governance: s.governance}
	// Assigned only when set: a nil *SemanticSearcher stored in the interface
	// is not a nil interface, and semantic_search_logs' "not configured" check
	// would pass and call Search on it.
	if s.semantic != nil {
		deps.Semantic = s.semantic
	}
	return deps
}

// GovernanceReader exposes Warp's virtual-key lookup dependency for the same
// reason LogReader does. Nil when the config store does not implement
// GovernanceReader and none was supplied via WithGovernanceReader.
func (s *Service) GovernanceReader() GovernanceReader {
	return s.governance
}

// CanChat reports whether the chat endpoint can be served: there is data to
// read and a way to reach a model. Transports gate the route on it, because a
// route that is registered but always 503s is worse than absent - it tells the
// dashboard the feature is present, and the failure only shows up after a user
// has typed a question.
func (s *Service) CanChat() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.logs != nil && (s.client != nil || s.chatOverride != nil) && s.mcp != nil && s.mcpTools != nil
}

// turnDeps returns the model client and the log reader as one consistent pair.
//
// Taken under a single RLock, because SetLogReader replaces both and reading
// them separately let a turn keep a usable chat func while the reader went nil
// underneath it - RunTurn then handed nil to NewAgent, and the first log tool
// the model reached for dereferenced it.
func (s *Service) turnDeps(ctx context.Context, config *schemas.WarpConfig, conversationID string) (ChatFunc, LogReader, *SemanticSearcher) {
	s.mu.RLock()
	override, client, logs, semantic := s.chatOverride, s.client, s.logs, s.semantic
	s.mu.RUnlock()
	return s.chatFuncFrom(ctx, config, conversationID, override, client), logs, semantic
}

// chatFuncFor resolves the inference function for a request. The conversation
// id travels upstream as a logging header, so it is settled before the first
// model call rather than after the last one.
func (s *Service) chatFuncFor(ctx context.Context, config *schemas.WarpConfig, conversationID string) ChatFunc {
	// Copied out under the read lock rather than used in place: SetLogReader
	// writes s.client while requests are in flight, so reading it here unguarded
	// is a race on a pointer another goroutine is assigning. Holding the lock
	// across the inference call itself would serialize every chat behind a
	// settings reload, which is why only the read is guarded.
	s.mu.RLock()
	override, client := s.chatOverride, s.client
	s.mu.RUnlock()
	return s.chatFuncFrom(ctx, config, conversationID, override, client)
}

// chatFuncFrom resolves an already-snapshotted override and client. Split out so
// turnDeps can take the client and the reader under one lock without reading
// s.client a second time.
func (s *Service) chatFuncFrom(ctx context.Context, config *schemas.WarpConfig, conversationID string, override ChatFunc, client *Client) ChatFunc {
	if override != nil {
		return override
	}
	if client == nil {
		return nil
	}
	return client.Chat(ctx, config, conversationID)
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

// Shutdown releases Warp's model client. Safe to call on a service that never
// built one.
func (s *Service) Shutdown() {
	s.mu.Lock()
	s.closed = true
	// Takes no lock of its own, so it is safe inside this one.
	s.stopHistoryCleanup()
	if s.indexer != nil {
		s.indexer.Close()
	}
	client := s.client
	s.mu.Unlock()
	// Outside the lock: the instance's own Shutdown drains queued requests, and
	// closed is already set, so nothing can build a replacement meanwhile.
	if client != nil {
		client.Shutdown()
	}
}

// SetLogReader rebinds what Warp researches through, after construction.
//
// Routes are registered once at startup, so a logging plugin enabled later
// cannot be picked up by rebuilding the handler - the router still holds the
// original one's closures. Rebinding inside the live service is what lets the
// chat endpoint start working without a restart. Building the model client here
// too keeps CanChat honest: a reader with no client still cannot answer.
func (s *Service) SetLogReader(logs LogReader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A service constructed without a log reader has no client, so Shutdown had
	// nothing to close and left no trace that it ran. A plugin reload landing
	// after that would build a fresh Client outside the completed shutdown, and
	// the next chat would stand up a Bifrost instance nobody owns.
	if s.closed {
		return
	}
	s.logs = logs
	if logs != nil && s.client == nil && s.chatOverride == nil {
		s.client = NewClient(s.logger)
	}
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
