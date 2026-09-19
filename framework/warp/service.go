// Package warp holds the dashboard agent: its configuration, the model client
// it talks through, the read-only tools it researches with, and the loop that
// ties them together. Transports call into Service; nothing here knows about
// HTTP.
package warp

import (
	"errors"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
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
}

// Option configures a Service.
type Option func(*Service)

// WithLogger sets the logger used for warnings the service cannot surface to a
// caller (a failed write after a successful answer, for example).
func WithLogger(logger schemas.Logger) Option {
	return func(s *Service) { s.logger = logger }
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
	}
	for _, opt := range opts {
		opt(service)
	}
	return service
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
