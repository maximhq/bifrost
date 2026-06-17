package apis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// Model represents a single model entry returned by the /v1/models API.
type Model struct {
	ID string `json:"id"`
}

type listModelsResp struct {
	Data []Model `json:"data"`
}

// VirtualKey represents a virtual key returned by the CLI handover API.
type VirtualKey struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Value string `json:"value"`
	Key   string `json:"key,omitempty"`
}

type listVirtualKeysResp struct {
	VirtualKeys []VirtualKey `json:"virtual_keys"`
	Keys        []VirtualKey `json:"keys"`
	Data        []VirtualKey `json:"data"`
}

// Client wraps HTTP calls to the Bifrost gateway API used by the CLI setup
// flow.
type Client struct {
	http    *http.Client
	openURL func(string) error
}

// NewClient creates a Bifrost API client with a default HTTP timeout.
func NewClient() *Client {
	return &Client{
		http:    &http.Client{Timeout: 20 * time.Second},
		openURL: openBrowser,
	}
}

// NormalizeBaseURL trims whitespace and trailing slashes from a base URL.
func NormalizeBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, "/")
	return raw
}

// BuildEndpoint joins a base URL with a path suffix, returning the full endpoint URL.
func BuildEndpoint(baseURL, suffix string) (string, error) {
	baseURL = NormalizeBaseURL(baseURL)
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid base url %q", baseURL)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + suffix
	return u.String(), nil
}

// ListModels fetches available model IDs from the Bifrost /v1/models endpoint,
// returning them sorted alphabetically.
func (c *Client) ListModels(ctx context.Context, baseURL, virtualKey string) ([]string, error) {
	endpoint, err := BuildEndpoint(baseURL, "/v1/models")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if strings.TrimSpace(virtualKey) != "" {
		req.Header.Set("x-bf-vk", strings.TrimSpace(virtualKey))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request /v1/models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("/v1/models status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	const maxModelsResponseBytes = 1 << 20 // 1 MiB
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read model response: %w", err)
	}

	var parsed listModelsResp
	if err := sonic.Unmarshal(b, &parsed); err != nil {
		return nil, fmt.Errorf("parse model response: %w", err)
	}

	set := map[string]struct{}{}
	for _, m := range parsed.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		set[id] = struct{}{}
	}
	models := make([]string, 0, len(set))
	for m := range set {
		models = append(models, m)
	}
	sort.Strings(models)
	return models, nil
}

// SignInWithBifrost opens the Bifrost browser handover flow, waits for a
// session token callback, then returns virtual keys assigned to that session.
func (c *Client) SignInWithBifrost(ctx context.Context, baseURL string) ([]VirtualKey, error) {
	sessionID, err := c.startCLIHandover(ctx, baseURL)
	if err != nil {
		return nil, err
	}
	return c.ListVirtualKeys(ctx, baseURL, sessionID)
}

// ListVirtualKeys fetches virtual keys assigned to the authenticated CLI
// handover session.
func (c *Client) ListVirtualKeys(ctx context.Context, baseURL, sessionID string) ([]VirtualKey, error) {
	endpoint, err := BuildEndpoint(baseURL, "/api/cli/virtual-keys")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build virtual keys request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(sessionID))
	req.Header.Set("x-bf-cli-session-id", strings.TrimSpace(sessionID))

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request CLI virtual keys: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("/api/cli/virtual-keys status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	const maxVirtualKeysResponseBytes = 1 << 20 // 1 MiB
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxVirtualKeysResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read virtual keys response: %w", err)
	}

	var parsed listVirtualKeysResp
	if err := sonic.Unmarshal(b, &parsed); err != nil {
		return nil, fmt.Errorf("parse virtual keys response: %w", err)
	}
	keys := parsed.VirtualKeys
	if len(keys) == 0 {
		keys = parsed.Keys
	}
	if len(keys) == 0 {
		keys = parsed.Data
	}

	out := make([]VirtualKey, 0, len(keys))
	for _, key := range keys {
		key.ID = strings.TrimSpace(key.ID)
		key.Name = strings.TrimSpace(key.Name)
		key.Value = strings.TrimSpace(key.Value)
		if key.Value == "" {
			key.Value = strings.TrimSpace(key.Key)
		}
		if key.Value == "" {
			continue
		}
		out = append(out, key)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].ID < out[j].ID
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (c *Client) startCLIHandover(ctx context.Context, baseURL string) (string, error) {
	state, err := randomState()
	if err != nil {
		return "", err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("start local callback listener: %w", err)
	}
	defer listener.Close()

	callbackURL := "http://" + listener.Addr().String() + "/callback"
	handoverURL, err := BuildEndpoint(baseURL, "/cli/handover")
	if err != nil {
		return "", err
	}
	u, err := url.Parse(handoverURL)
	if err != nil {
		return "", fmt.Errorf("build handover url: %w", err)
	}
	q := u.Query()
	q.Set("redirect_uri", callbackURL)
	q.Set("state", state)
	u.RawQuery = q.Encode()

	resultCh := make(chan string, 1)
	errCh := make(chan error, 1)
	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/callback" {
				http.NotFound(w, r)
				return
			}
			if got := r.URL.Query().Get("state"); got != state {
				http.Error(w, "invalid state", http.StatusBadRequest)
				select {
				case errCh <- fmt.Errorf("handover callback state mismatch"):
				default:
				}
				return
			}
			sessionID := firstNonEmpty(
				r.URL.Query().Get("session_id"),
				r.URL.Query().Get("session"),
				r.URL.Query().Get("token"),
			)
			if sessionID == "" {
				http.Error(w, "missing session_id", http.StatusBadRequest)
				select {
				case errCh <- fmt.Errorf("handover callback did not include session_id"):
				default:
				}
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<!doctype html><title>Bifrost CLI</title><p>Sign in complete. You can return to the Bifrost CLI.</p>")
			select {
			case resultCh <- sessionID:
			default:
			}
		}),
	}

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			select {
			case errCh <- err:
			default:
			}
		}
	}()
	defer server.Close()

	openURL := c.openURL
	if openURL == nil {
		openURL = openBrowser
	}
	if err := openURL(u.String()); err != nil {
		return "", fmt.Errorf("open handover URL: %w", err)
	}

	select {
	case sessionID := <-resultCh:
		return strings.TrimSpace(sessionID), nil
	case err := <-errCh:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func randomState() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate handover state: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func openBrowser(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Start()
}
