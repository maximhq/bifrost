// Package prometheus provides a plugin for pushing Prometheus metrics to a Push Gateway.
// This enables accurate metrics aggregation in multi-node cluster deployments where
// traditional /metrics scraping may miss nodes behind load balancers.
package prometheus

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	PluginName = "prometheus"
)

// Config holds the configuration for the Prometheus Push Gateway plugin
type Config struct {
	// PushGatewayURL is the URL of the Prometheus Push Gateway (e.g., http://pushgateway:9091)
	PushGatewayURL string `json:"push_gateway_url"`
	// JobName is the job label for pushed metrics (default: "bifrost")
	JobName string `json:"job_name"`
	// InstanceID is the instance label for grouping metrics. If empty, hostname is used.
	InstanceID string `json:"instance_id"`
	// PushInterval is how often to push metrics in seconds (default: 15)
	PushInterval int `json:"push_interval"`
	// BasicAuth credentials for the Push Gateway
	BasicAuth *BasicAuthConfig `json:"basic_auth"`
}

// BasicAuthConfig holds basic authentication credentials
type BasicAuthConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// PrometheusPushPlugin implements the push gateway integration
type PrometheusPushPlugin struct {
	config   *Config
	logger   schemas.Logger
	registry *prometheus.Registry
	pusher   *push.Pusher

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.RWMutex
	started bool
}

// RegistryProvider is an interface for components that provide a Prometheus registry
type RegistryProvider interface {
	GetRegistry() *prometheus.Registry
}

// Init creates a new PrometheusPushPlugin
// Note: The registry must be set later via SetRegistry before calling Start
func Init(config *Config, logger schemas.Logger) (*PrometheusPushPlugin, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}

	if config.PushGatewayURL == "" {
		return nil, fmt.Errorf("push_gateway_url is required")
	}

	// Set defaults
	if config.JobName == "" {
		config.JobName = "bifrost"
	}

	if config.PushInterval <= 0 {
		config.PushInterval = 15
	}

	if config.InstanceID == "" {
		hostname, err := os.Hostname()
		if err != nil {
			config.InstanceID = "unknown"
		} else {
			config.InstanceID = hostname
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	plugin := &PrometheusPushPlugin{
		config: config,
		logger: logger,
		ctx:    ctx,
		cancel: cancel,
	}

	return plugin, nil
}

// SetRegistry sets the Prometheus registry to push from (typically from telemetry plugin)
func (p *PrometheusPushPlugin) SetRegistry(registry *prometheus.Registry) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if registry == nil {
		return fmt.Errorf("nil registry")
	}

	if p.started {
		return fmt.Errorf("cannot set registry after plugin has started")
	}

	p.registry = registry

	// Create the pusher with the registry
	pusher := push.New(p.config.PushGatewayURL, p.config.JobName).
		Gatherer(registry).
		Grouping("instance", p.config.InstanceID)

	if p.config.BasicAuth != nil && p.config.BasicAuth.Username != "" {
		pusher = pusher.BasicAuth(p.config.BasicAuth.Username, p.config.BasicAuth.Password)
	}

	p.pusher = pusher
	return nil
}

// Start begins the periodic push to the Push Gateway
func (p *PrometheusPushPlugin) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.started {
		return fmt.Errorf("plugin already started")
	}

	if p.pusher == nil {
		return fmt.Errorf("registry not set - call SetRegistry before Start")
	}

	p.started = true
	p.wg.Add(1)

	go p.pushLoop()

	p.logger.Info("prometheus push gateway plugin started, pushing to %s every %d seconds",
		p.config.PushGatewayURL, p.config.PushInterval)

	return nil
}

// pushLoop periodically pushes metrics to the Push Gateway
func (p *PrometheusPushPlugin) pushLoop() {
	defer p.wg.Done()

	ticker := time.NewTicker(time.Duration(p.config.PushInterval) * time.Second)
	defer ticker.Stop()

	// Initial push
	p.doPush()

	for {
		select {
		case <-p.ctx.Done():
			// Final push before shutdown
			p.logger.Info("prometheus plugin shutting down, performing final push")
			p.doPush()
			return
		case <-ticker.C:
			p.doPush()
		}
	}
}

// doPush performs a single push to the Push Gateway
func (p *PrometheusPushPlugin) doPush() {
	p.mu.RLock()
	pusher := p.pusher
	p.mu.RUnlock()

	if pusher == nil {
		return
	}

	if err := pusher.Push(); err != nil {
		p.logger.Error("failed to push metrics to push gateway: %v", err)
	}
}

// GetName returns the plugin name
func (p *PrometheusPushPlugin) GetName() string {
	return PluginName
}

// Cleanup stops the push loop and performs a final push
func (p *PrometheusPushPlugin) Cleanup() error {
	p.mu.Lock()
	if !p.started {
		p.mu.Unlock()
		return nil
	}
	p.started = false
	p.cancel() // Call cancel while holding the lock to prevent Start from racing
	p.mu.Unlock()

	p.wg.Wait()

	p.logger.Info("prometheus push gateway plugin stopped")
	return nil
}

// GetConfig returns the current configuration (for API/UI)
func (p *PrometheusPushPlugin) GetConfig() *Config {
	return p.config
}

// IsRunning returns whether the push loop is active
func (p *PrometheusPushPlugin) IsRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.started
}
