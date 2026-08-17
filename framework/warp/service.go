// Package warp holds the dashboard agent: its configuration, the model client
// it talks through, the read-only tools it researches with, and the loop that
// ties them together. Transports call into Service; nothing here knows about
// HTTP.
package warp

import (
	"context"
	"errors"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

var (
	// ErrUnavailable is returned when Warp has no usable configuration. It is the
	// analogue of the notification service's unavailable error, and like that
	// one it is a supported deployment state rather than a fault.
	ErrUnavailable = errors.New("warp is not configured")
	// ErrInvalidConfig wraps every validation failure from SaveConfig, so a
	// caller can map the whole family to one status without matching text.
	ErrInvalidConfig = errors.New("warp: invalid configuration")
)

// Service is the in-process face of Warp. It is built once at bootstrap, before
// plugins load, so construction must stay allocation-only: no store reads, no
// clients, nothing that assumes a logger is present.
type Service struct {
	// store is nil when the config store does not implement WarpStore. Every
	// method treats that as "not configured" rather than a fault.
	store  configstore.WarpStore
	logger schemas.Logger
	// conversations is nil when the config store does not implement history.
	// Chat still works; it just does not file anything.
	conversations configstore.WarpConversationStore
	// logs is nil on deployments with no logging plugin. Warp's tools have
	// nothing to read there, so chat is reported unavailable rather than
	// registered and always failing.
	logs LogReader
	// client owns Warp's dedicated Bifrost instance. It exists only when there is
	// something to read; tests replace chatOverride instead, so the loop can be
	// driven by a scripted model.
	client *Client
	// chatOverride, when set, replaces the real inference path. Test seam only.
	chatOverride ChatFunc
	// catalog prices Warp's own usage. Nil is supported: the panel then reports
	// tokens without a cost, rather than reporting a cost of zero.
	catalog *modelcatalog.ModelCatalog
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

// WithChatFunc replaces the real inference path. Test seam only: the agent loop
// can then be driven by a scripted model with no provider behind it.
func WithChatFunc(chat ChatFunc) Option {
	return func(s *Service) { s.chatOverride = chat }
}

// WithConversationStore sets the history store directly. Test seam, like
// WithConfigStore.
func WithConversationStore(store configstore.WarpConversationStore) Option {
	return func(s *Service) { s.conversations = store }
}

// WithConfigStore sets the configuration store directly, bypassing the
// ConfigStore narrowing NewService does. Tests use it to inject a double.
func WithConfigStore(store configstore.WarpStore) Option {
	return func(s *Service) { s.store = store }
}

// NewService builds a Service over the deployment's config store. A store that
// does not implement WarpStore is supported: the service then reports
// ErrUnavailable from every configuration call.
func NewService(store configstore.ConfigStore, opts ...Option) *Service {
	service := &Service{}
	if store != nil {
		service.store, _ = store.(configstore.WarpStore)
		service.conversations, _ = store.(configstore.WarpConversationStore)
	}
	for _, opt := range opts {
		opt(service)
	}
	if service.logs != nil {
		service.client = NewClient(service.logger)
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
	return s.logs != nil && (s.client != nil || s.chatOverride != nil)
}

// chatFuncFor resolves the inference function for a request. The conversation
// id travels upstream as a logging header, so it is settled before the first
// model call rather than after the last one.
func (s *Service) chatFuncFor(ctx context.Context, config *schemas.WarpConfig, conversationID string) ChatFunc {
	if s.chatOverride != nil {
		return s.chatOverride
	}
	if s.client == nil {
		return nil
	}
	return s.client.Chat(ctx, config, conversationID)
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
		return s.catalog.CalculateCostForUsage(usage, config.Provider, config.Model, schemas.ChatCompletionRequest, nil)
	}
}

// Shutdown releases Warp's model client. Safe to call on a service that never
// built one.
func (s *Service) Shutdown() {
	if s.client != nil {
		s.client.Shutdown()
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
