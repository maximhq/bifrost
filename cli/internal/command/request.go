package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/maximhq/bifrost/cli/internal/client"
	"github.com/maximhq/bifrost/cli/internal/output"
)

const maxInputBytes = 32 << 20

// repeatedValue implements flag.Value for repeatable command flags.
type repeatedValue []string

// String returns a comma-separated diagnostic form of the values.
func (v *repeatedValue) String() string {
	return strings.Join(*v, ",")
}

// Set appends one occurrence of a repeatable flag.
func (v *repeatedValue) Set(value string) error {
	*v = append(*v, value)
	return nil
}

// runRequest calls an arbitrary gateway-relative HTTP endpoint.
func (r *Runner) runRequest(ctx context.Context, env *environment, args []string) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprint(r.Out, "Usage: bifrost request <METHOD> <PATH> [--query key=value] [--header 'Name: value'] [--body VALUE|--file PATH] [--auth auto|none|management|inference|agent] [--stream]\n")
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: bifrost request <METHOD> <PATH> [flags]")
	}
	method := args[0]
	path := args[1]
	fs := flag.NewFlagSet("bifrost request", flag.ContinueOnError)
	fs.SetOutput(r.ErrOut)
	var queryFlags repeatedValue
	var headerFlags repeatedValue
	var bodyValue string
	var bodyFile string
	var authValue string
	var stream bool
	fs.Var(&queryFlags, "query", "query parameter as key=value; repeatable")
	fs.Var(&headerFlags, "header", "request header as Name: value; repeatable")
	fs.StringVar(&bodyValue, "body", "", "inline request body")
	fs.StringVar(&bodyFile, "file", "", "request body file, or - for stdin")
	fs.StringVar(&authValue, "auth", "auto", "credential mode: auto, none, management, inference, agent")
	fs.BoolVar(&stream, "stream", false, "copy the response incrementally")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected request arguments: %s", strings.Join(fs.Args(), " "))
	}
	body, err := r.readBody(bodyValue, bodyFile)
	if err != nil {
		return err
	}
	query, err := parseQuery(queryFlags)
	if err != nil {
		return err
	}
	headers, err := parseHeaders(headerFlags)
	if err != nil {
		return err
	}
	authMode, err := parseAuthMode(authValue)
	if err != nil {
		return err
	}
	request := client.Request{Method: method, Path: path, Query: query, Headers: headers, Body: body, Auth: authMode}
	if stream {
		writer := r.Out
		if env.Quiet {
			writer = io.Discard
		}
		return authErrorHint(env, request, env.Client.Stream(ctx, request, writer))
	}
	return r.executeAndPrint(ctx, env, request)
}

// readBody resolves mutually exclusive inline, file, and stdin request bodies.
func (r *Runner) readBody(inline, path string) ([]byte, error) {
	if inline != "" && path != "" {
		return nil, fmt.Errorf("use only one of --body or --file")
	}
	if inline != "" {
		return []byte(inline), nil
	}
	if path == "" {
		return nil, nil
	}
	if path == "-" {
		body, err := io.ReadAll(io.LimitReader(r.In, maxInputBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read request body from stdin: %w", err)
		}
		if len(body) > maxInputBytes {
			return nil, fmt.Errorf("request body exceeds %d bytes", maxInputBytes)
		}
		return body, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open request body file: %w", err)
	}
	defer func() { _ = file.Close() }()
	body, err := io.ReadAll(io.LimitReader(file, maxInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read request body file: %w", err)
	}
	if len(body) > maxInputBytes {
		return nil, fmt.Errorf("request body exceeds %d bytes", maxInputBytes)
	}
	return body, nil
}

// parseQuery converts repeatable key=value flags into URL query parameters.
func parseQuery(entries []string) (url.Values, error) {
	values := url.Values{}
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("invalid query parameter %q; expected key=value", entry)
		}
		values.Add(strings.TrimSpace(key), value)
	}
	return values, nil
}

// parseHeaders converts repeatable Name:value flags into HTTP headers.
func parseHeaders(entries []string) (http.Header, error) {
	headers := http.Header{}
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("invalid header %q; expected 'Name: value'", entry)
		}
		headers.Add(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	return headers, nil
}

// parseAuthMode validates a user-facing authentication mode.
func parseAuthMode(value string) (client.AuthMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return client.AuthAuto, nil
	case "none":
		return client.AuthNone, nil
	case "management", "admin":
		return client.AuthManagement, nil
	case "inference", "virtual-key":
		return client.AuthInference, nil
	case "agent", "sso":
		return client.AuthAgent, nil
	default:
		return client.AuthAuto, fmt.Errorf("invalid auth mode %q", value)
	}
}

// executeAndPrint performs a request and renders its successful response.
func (r *Runner) executeAndPrint(ctx context.Context, env *environment, request client.Request) error {
	response, err := env.Client.Do(ctx, request)
	if err != nil {
		return authErrorHint(env, request, err)
	}
	if env.Quiet {
		return nil
	}
	return output.Print(r.Out, response.Body, env.Output)
}

// authErrorHint explains the separation between administrator, virtual-key,
// and Enterprise SSO credentials after an authorization failure.
func authErrorHint(env *environment, request client.Request, err error) error {
	if err == nil || env == nil || env.Client == nil {
		return err
	}
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || (apiErr.StatusCode != http.StatusUnauthorized && apiErr.StatusCode != http.StatusForbidden) {
		return err
	}
	mode := client.ResolveAuthMode(request.Auth, request.Path)
	credentials := env.Client.CredentialsSnapshot()
	switch mode {
	case client.AuthManagement:
		if strings.HasPrefix("/"+strings.TrimPrefix(request.Path, "/"), "/api/agent/") {
			return fmt.Errorf("%w; this agent endpoint requires '--auth agent'", err)
		}
		if strings.TrimSpace(credentials.ManagementKey) == "" && strings.TrimSpace(credentials.SessionToken) == "" {
			return fmt.Errorf("%w; Enterprise browser SSO is user-scoped and does not grant management access—configure a bfst-* key with 'bifrost auth set-management-key'", err)
		}
	case client.AuthInference:
		if strings.TrimSpace(credentials.VirtualKey) == "" && strings.TrimSpace(credentials.AgentToken) == "" {
			return fmt.Errorf("%w; sign in with 'bifrost auth login' or configure a virtual key", err)
		}
	case client.AuthAgent:
		if strings.TrimSpace(credentials.AgentToken) == "" {
			return fmt.Errorf("%w; sign in with 'bifrost auth login'", err)
		}
	}
	return err
}

// runStatus prints gateway health, version, and authentication status.
func (r *Runner) runStatus(ctx context.Context, env *environment) error {
	result := map[string]any{
		"base_url": env.Client.BaseURL,
		"context":  env.ProfileID,
	}
	checks := []struct {
		name string
		path string
		auth client.AuthMode
	}{
		{name: "health", path: "/health", auth: client.AuthNone},
		{name: "version", path: "/api/version", auth: client.AuthNone},
		{name: "authentication", path: "/api/session/is-auth-enabled", auth: client.AuthManagement},
	}
	for _, check := range checks {
		response, err := env.Client.Do(ctx, client.Request{Path: check.path, Auth: check.auth})
		if err != nil {
			result[check.name] = map[string]any{"ok": false, "error": err.Error()}
			continue
		}
		var value any
		if json.Unmarshal(response.Body, &value) != nil {
			value = strings.TrimSpace(string(response.Body))
		}
		result[check.name] = value
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if env.Quiet {
		return nil
	}
	return output.Print(r.Out, body, env.Output)
}

// runDoctor verifies gateway reachability, management auth, and inference discovery.
func (r *Runner) runDoctor(ctx context.Context, env *environment) error {
	type checkResult struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	}
	checks := []struct {
		name string
		path string
		auth client.AuthMode
	}{
		{name: "gateway health", path: "/health", auth: client.AuthNone},
		{name: "management authentication", path: "/api/session/is-auth-enabled", auth: client.AuthManagement},
		{name: "model discovery", path: "/v1/models", auth: client.AuthInference},
	}
	results := make([]checkResult, 0, len(checks))
	failed := false
	for _, check := range checks {
		_, err := env.Client.Do(ctx, client.Request{Path: check.path, Auth: check.auth})
		if err != nil {
			failed = true
			results = append(results, checkResult{Name: check.name, Status: "failed", Detail: err.Error()})
			continue
		}
		results = append(results, checkResult{Name: check.name, Status: "ok", Detail: check.path})
	}
	body, err := json.Marshal(map[string]any{"data": results})
	if err != nil {
		return err
	}
	if !env.Quiet {
		if err := output.Print(r.Out, body, env.Output); err != nil {
			return err
		}
	}
	if failed {
		return fmt.Errorf("one or more diagnostic checks failed")
	}
	return nil
}
