package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// SyncClient fetches agent configuration from the Bifrost management API.
type SyncClient struct {
	managementURL string
	agentToken    string
	httpClient    *http.Client
	lastVersion   int64
}

// NewSyncClient creates a config sync client.
func NewSyncClient(managementURL string, agentToken string) *SyncClient {
	return &SyncClient{
		managementURL: managementURL,
		agentToken:    agentToken,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// configResponse is the JSON structure returned by the management API.
type configResponse struct {
	GatewayURL   string       `json:"gateway_url"`
	VirtualKey   string       `json:"virtual_key"`
	CACertPEM    string       `json:"ca_cert_pem"`
	CAKeyPEM     string       `json:"ca_key_pem"`
	Domains      []DomainRule `json:"domains"`
	ConfigVersion int64       `json:"config_version"`
	PollInterval  int         `json:"poll_interval_seconds"`
}

// FetchConfig fetches the latest configuration from the management API.
// Returns nil if the config hasn't changed since the last fetch.
func (s *SyncClient) FetchConfig(ctx context.Context) (*AgentConfig, error) {
	url := s.managementURL + "/api/v1/agent/config"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.agentToken)
	req.Header.Set("X-Bifrost-Agent-Version", "0.1.0")

	// Send last known version for change detection
	if s.lastVersion > 0 {
		req.Header.Set("If-None-Match", fmt.Sprintf("%d", s.lastVersion))
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch config: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// New config available
	case http.StatusNotModified:
		// Config unchanged
		return nil, nil
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("authentication failed (check agent token)")
	default:
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("management API returned %d: %s", resp.StatusCode, string(body))
	}

	var cr configResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	s.lastVersion = cr.ConfigVersion

	pollInterval := 60 * time.Second
	if cr.PollInterval > 0 {
		pollInterval = time.Duration(cr.PollInterval) * time.Second
	}

	cfg := &AgentConfig{
		GatewayURL:    cr.GatewayURL,
		VirtualKey:    cr.VirtualKey,
		CACertPEM:     cr.CACertPEM,
		CAKeyPEM:      cr.CAKeyPEM,
		Domains:       cr.Domains,
		ConfigVersion: cr.ConfigVersion,
		PollInterval:  Duration{Duration: pollInterval},
		AgentToken:    s.agentToken,
		ManagementURL: s.managementURL,
	}
	return cfg, nil
}

// StartSync begins periodic config syncing. It calls onChange whenever the
// config changes. Blocks until ctx is cancelled.
func (s *SyncClient) StartSync(ctx context.Context, initialPollInterval time.Duration, onChange func(*AgentConfig)) {
	ticker := time.NewTicker(initialPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cfg, err := s.FetchConfig(ctx)
			if err != nil {
				log.Printf("config sync error: %v", err)
				continue
			}
			if cfg != nil {
				log.Printf("config updated (version %d)", cfg.ConfigVersion)
				onChange(cfg)

				// Adjust poll interval if the server requested a different one
				if cfg.PollInterval.Duration > 0 && cfg.PollInterval.Duration != initialPollInterval {
					ticker.Reset(cfg.PollInterval.Duration)
					initialPollInterval = cfg.PollInterval.Duration
				}
			}
		}
	}
}
