// Package driver runs the Agent Gateway lifecycle scenario against a deployed
// Bifrost gateway and checks the Agent logs it produces.
package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// Gateway is a thin client for the Bifrost management API and the public
// Agent Card endpoint. AdminToken is optional and sent as the dashboard
// session cookie when set.
type Gateway struct {
	URL        string
	AdminToken string
	// BearerToken, when set, is sent as an Authorization bearer credential, for
	// example a token minted for a user by an identity provider.
	BearerToken string
	// ProjectID, when set, names the project scoping card fetches and probes.
	ProjectID string
	HTTP      *http.Client
}

// NewGateway returns a Gateway for the base URL with a bounded HTTP timeout.
func NewGateway(baseURL, adminToken string) *Gateway {
	return &Gateway{URL: strings.TrimRight(baseURL, "/"), AdminToken: adminToken, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// WithoutCredentials returns a client for the same gateway that sends no admin
// credential, so a call carries only the credentials the caller supplies.
func (g *Gateway) WithoutCredentials() *Gateway {
	return &Gateway{URL: g.URL, HTTP: g.HTTP, ProjectID: g.ProjectID}
}

// Login exchanges dashboard credentials for a session token.
func Login(ctx context.Context, baseURL, user, pass string) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/session/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == "token" && c.Value != "" {
			return c.Value, nil
		}
	}
	return "", fmt.Errorf("login: HTTP %d and no session cookie", resp.StatusCode)
}

// Do sends one management API request and returns the status and body.
func (g *Gateway) Do(ctx context.Context, method, path string, body any, header http.Header) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.URL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if g.BearerToken != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+g.BearerToken)
	}
	if g.AdminToken != "" {
		req.AddCookie(&http.Cookie{Name: "token", Value: g.AdminToken})
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, data, err
}

// RegisterAgent creates an enabled registration. With allowByDefault false,
// callers need an explicit grant (for example on a virtual key) to use it.
func (g *Gateway) RegisterAgent(ctx context.Context, name, cardURL string, allowByDefault bool) error {
	status, body, err := g.Do(ctx, http.MethodPost, "/api/agents", map[string]any{
		"name":                        name,
		"agent_card_url":              cardURL,
		"enabled":                     true,
		"allow_by_default":            allowByDefault,
		"forward_accepted_credential": false,
		"forward_accepted_credential_overrides_auth": false,
	}, nil)
	if err != nil {
		return fmt.Errorf("register Agent: %w", err)
	}
	if status != http.StatusCreated {
		return fmt.Errorf("register Agent: HTTP %d: %s", status, truncate(body))
	}
	return nil
}

// DeleteAgent removes a registration created by the caller.
func (g *Gateway) DeleteAgent(ctx context.Context, name string) error {
	status, body, err := g.Do(ctx, http.MethodDelete, "/api/agents/"+url.PathEscape(name), nil, nil)
	if err != nil {
		return fmt.Errorf("delete Agent: %w", err)
	}
	if status != http.StatusNoContent {
		return fmt.Errorf("delete Agent: HTTP %d: %s", status, truncate(body))
	}
	return nil
}

// CardURL returns the gateway-served public Agent Card URL for an Agent.
func (g *Gateway) CardURL(agent string) string {
	return g.URL + "/agents/a2a/" + agent + "/.well-known/agent-card.json"
}

// FetchCard loads the gateway-served Agent Card once.
func (g *Gateway) FetchCard(ctx context.Context, agent, virtualKey, bearer string) (*a2a.AgentCard, error) {
	header := http.Header{}
	if virtualKey != "" {
		header.Set("x-bf-vk", virtualKey)
	}
	if bearer != "" {
		header.Set("Authorization", "Bearer "+bearer)
	}
	if g.ProjectID != "" {
		header.Set("x-bf-project-id", g.ProjectID)
	}
	status, body, err := g.WithoutCredentials().Do(ctx, http.MethodGet, "/agents/a2a/"+agent+"/.well-known/agent-card.json", nil, header)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", status, truncate(body))
	}
	var card a2a.AgentCard
	if err := json.Unmarshal(body, &card); err != nil {
		return nil, err
	}
	return &card, nil
}

// WaitForCard polls until the Agent Card resolves, covering registration
// propagation.
func (g *Gateway) WaitForCard(ctx context.Context, agent, virtualKey, bearer string) (*a2a.AgentCard, error) {
	var last error
	for {
		card, err := g.FetchCard(ctx, agent, virtualKey, bearer)
		if err == nil {
			return card, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for Agent card: %w (last error: %v)", ctx.Err(), last)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// truncate shortens a response body for error messages.
func truncate(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		s = s[:400] + "..."
	}
	return s
}
