package heimdall

import (
	"fmt"
	"time"
)

const (
	defaultMaxConcurrency = 8
	defaultMaxSessions    = 200
	defaultMaxSteps       = 20
	defaultCallTimeout    = 120 * time.Second
	defaultRetentionDays  = 7
)

// Config is the plugin config block. Zero values take the defaults above.
type Config struct {
	MaxConcurrency int    `json:"max_concurrency"`
	MaxSessions    int    `json:"max_sessions"`
	MaxSteps       int    `json:"max_steps"`
	CallTimeout    string `json:"call_timeout"`
	RetentionDays  int    `json:"retention_days"`

	// AllowUnauthMutations lets POST routes through when dashboard auth is off. Demo only.
	AllowUnauthMutations bool `json:"allow_unauthenticated_mutations"`

	// Defaults are the numbers a run takes when its request sets none.
	Defaults RunParams `json:"defaults"`
}

// withDefaults returns a copy of c with zero values replaced by defaults, or an error for a value that cannot be used.
func (c *Config) withDefaults() (*Config, error) {
	out := Config{}
	if c != nil {
		out = *c
	}
	if out.MaxConcurrency < 0 || out.MaxSessions < 0 || out.MaxSteps < 0 || out.RetentionDays < 0 {
		return nil, fmt.Errorf("heimdall: max_concurrency, max_sessions, max_steps and retention_days must not be negative")
	}
	if out.MaxConcurrency == 0 {
		out.MaxConcurrency = defaultMaxConcurrency
	}
	if out.MaxSessions == 0 {
		out.MaxSessions = defaultMaxSessions
	}
	if out.MaxSteps == 0 {
		out.MaxSteps = defaultMaxSteps
	}
	if out.RetentionDays == 0 {
		out.RetentionDays = defaultRetentionDays
	}
	if out.CallTimeout == "" {
		out.CallTimeout = defaultCallTimeout.String()
	}
	if _, err := out.callTimeout(); err != nil {
		return nil, err
	}
	out.Defaults = out.Defaults.withDefaults()
	if err := out.Defaults.Validate(); err != nil {
		return nil, fmt.Errorf("heimdall: defaults: %w", err)
	}
	return &out, nil
}

// callTimeout parses CallTimeout. It rejects a value that is not a positive duration.
func (c *Config) callTimeout() (time.Duration, error) {
	d, err := time.ParseDuration(c.CallTimeout)
	if err != nil {
		return 0, fmt.Errorf("heimdall: call_timeout %q is not a duration: %w", c.CallTimeout, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("heimdall: call_timeout must be positive, got %s", c.CallTimeout)
	}
	return d, nil
}
