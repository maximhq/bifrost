package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/cli/internal/client"
	"github.com/maximhq/bifrost/cli/internal/config"
	"github.com/maximhq/bifrost/cli/internal/operations"
	"github.com/maximhq/bifrost/cli/internal/secrets"
)

// testRoundTripFunc adapts a function into an in-memory HTTP transport.
type testRoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip executes one in-memory gateway exchange.
func (function testRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

// memorySecretStore stores test credentials without using an operating-system keyring.
type memorySecretStore map[string]string

// Get retrieves one in-memory credential.
func (store memorySecretStore) Get(profileID string, kind secrets.Kind) (string, error) {
	return store[profileID+":"+string(kind)], nil
}

// Set stores one in-memory credential.
func (store memorySecretStore) Set(profileID string, kind secrets.Kind, value string) error {
	store[profileID+":"+string(kind)] = value
	return nil
}

// Delete removes one in-memory credential.
func (store memorySecretStore) Delete(profileID string, kind secrets.Kind) error {
	delete(store, profileID+":"+string(kind))
	return nil
}

type recordingSecretStore struct {
	values map[string]string
	gets   []secrets.Kind
	sets   []secrets.Kind
	getErr error
}

func (store *recordingSecretStore) Get(profileID string, kind secrets.Kind) (string, error) {
	store.gets = append(store.gets, kind)
	if store.getErr != nil {
		return "", store.getErr
	}
	return store.values[profileID+":"+string(kind)], nil
}

func (store *recordingSecretStore) Set(profileID string, kind secrets.Kind, value string) error {
	store.sets = append(store.sets, kind)
	if store.values == nil {
		store.values = map[string]string{}
	}
	store.values[profileID+":"+string(kind)] = value
	return nil
}

func (store *recordingSecretStore) Delete(profileID string, kind secrets.Kind) error {
	delete(store.values, profileID+":"+string(kind))
	return nil
}

// newTestRunner creates an isolated runner with one configured context.
func newTestRunner(t *testing.T, transport http.RoundTripper) (*Runner, *bytes.Buffer, memorySecretStore) {
	t.Helper()
	statePath := t.TempDir() + "/state.json"
	state := &config.State{
		Profiles:      []config.Profile{{ID: "test", Name: "Test", BaseURL: "https://gateway.example"}},
		LastProfileID: "test", Selections: map[string]config.Selection{},
	}
	if err := config.SaveState(statePath, state); err != nil {
		t.Fatal(err)
	}
	outputBuffer := &bytes.Buffer{}
	secretStore := memorySecretStore{}
	runner := &Runner{
		In: strings.NewReader(""), Out: outputBuffer, ErrOut: &bytes.Buffer{},
		Build: BuildInfo{Version: "test", Commit: "abc"}, Secrets: secretStore,
		HTTPClient: &http.Client{Transport: transport}, StatePath: statePath,
	}
	return runner, outputBuffer, secretStore
}

// jsonResponse constructs a successful JSON response for test transports.
func jsonResponse(request *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(body)), Request: request,
	}
}

// TestResourceUsesInferenceCredential verifies model discovery receives only the virtual key.
func TestResourceUsesInferenceCredential(t *testing.T) {
	var received *http.Request
	runner, outputBuffer, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		received = request
		return jsonResponse(request, `{"data":[{"id":"openai/gpt-test"}]}`), nil
	}))
	secretStore["test:virtual-key"] = "sk-bf-test"
	secretStore["test:management-key"] = "bfst-admin"

	if err := runner.Run(context.Background(), []string{"models", "list", "--limit", "5", "--search", "gpt", "--output", "raw"}); err != nil {
		t.Fatal(err)
	}
	if received.URL.Path != "/v1/models" || received.Header.Get("x-bf-vk") != "sk-bf-test" {
		t.Fatalf("unexpected request path=%q virtual-key=%q", received.URL.Path, received.Header.Get("x-bf-vk"))
	}
	if received.URL.Query().Get("limit") != "5" || received.URL.Query().Get("search") != "gpt" {
		t.Fatalf("list query = %q", received.URL.RawQuery)
	}
	if received.Header.Get("Authorization") != "" {
		t.Fatal("management authorization leaked into inference request")
	}
	if !strings.Contains(outputBuffer.String(), "openai/gpt-test") {
		t.Fatalf("unexpected output %q", outputBuffer.String())
	}
}

func TestLocalConfigDoesNotReadKeyring(t *testing.T) {
	runner, outputBuffer, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("config unexpectedly called %s", request.URL)
		return nil, nil
	}))
	store := &recordingSecretStore{getErr: errors.New("keyring unavailable")}
	runner.Secrets = store
	if err := runner.Run(context.Background(), []string{"config", "get", "base-url"}); err != nil {
		t.Fatal(err)
	}
	if len(store.gets) != 0 {
		t.Fatalf("config read keyring kinds: %#v", store.gets)
	}
	if !strings.Contains(outputBuffer.String(), "https://gateway.example") {
		t.Fatalf("config output = %q", outputBuffer.String())
	}
}

func TestInferenceLoadsOnlyInferenceCredentials(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(request, `{"data":[]}`), nil
	}))
	store := &recordingSecretStore{values: map[string]string{"test:virtual-key": "sk-bf-test"}}
	runner.Secrets = store
	if err := runner.Run(context.Background(), []string{"models", "list", "--output", "raw"}); err != nil {
		t.Fatal(err)
	}
	want := []secrets.Kind{secrets.VirtualKey, secrets.AgentToken, secrets.AgentVirtualKeyID}
	if !slices.Equal(store.gets, want) {
		t.Fatalf("keyring reads = %#v, want %#v", store.gets, want)
	}
}

func TestPersistentAuthRequiresNamedContext(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	store := &recordingSecretStore{values: map[string]string{}}
	runner := &Runner{
		In: strings.NewReader("sk-bf-test\n"), Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{},
		Build: BuildInfo{Version: "test"}, Secrets: store, StatePath: statePath,
	}
	err := runner.Run(context.Background(), []string{"--base-url", "https://gateway.example", "auth", "set-virtual-key", "--stdin"})
	if err == nil || !strings.Contains(err.Error(), "named context") {
		t.Fatalf("error = %v, want named-context guidance", err)
	}
	if len(store.sets) != 0 {
		t.Fatalf("stored orphaned credentials: %#v", store.sets)
	}
}

func TestAdHocContextDoesNotInstallKeyringBackedRefresh(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	store := &recordingSecretStore{values: map[string]string{}}
	runner := &Runner{
		In: strings.NewReader(""), Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{},
		Build: BuildInfo{Version: "test"}, Secrets: store, StatePath: statePath,
	}
	t.Setenv("BIFROST_AGENT_TOKEN", "ck-bf-agent-test")

	env, err := runner.resolveEnvironment(globalOptions{BaseURL: "https://gateway.example", Output: "json"}, credentialAgentToken)
	if err != nil {
		t.Fatal(err)
	}
	if env.Client.RefreshAgentToken != nil {
		t.Fatal("ad-hoc context installed a keyring-backed token refresher")
	}
	if len(store.gets) != 0 {
		t.Fatalf("ad-hoc context read keyring kinds: %#v", store.gets)
	}
}

func TestRequestMissingOperandsReturnsError(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("incomplete request unexpectedly called %s", request.URL)
		return nil, nil
	}))
	if err := runner.Run(context.Background(), []string{"request"}); err == nil {
		t.Fatal("expected incomplete request to fail")
	}
}

func TestNonListActionRejectsListFlags(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("invalid flags unexpectedly called %s", request.URL)
		return nil, nil
	}))
	err := runner.Run(context.Background(), []string{"providers", "get", "openai", "--limit", "1"})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("error = %v, want undefined-list-flag error", err)
	}
}

// TestVirtualKeyListUsesAgentSelfService verifies browser SSO lists only keys
// assigned to the signed-in user when no administrative credential is present.
func TestVirtualKeyListUsesAgentSelfService(t *testing.T) {
	var received *http.Request
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		received = request
		return jsonResponse(request, `{"virtual_keys":[]}`), nil
	}))
	secretStore["test:agent-token"] = "ck-bf-agent-test"

	if err := runner.Run(context.Background(), []string{"virtual-keys", "list", "--output", "raw"}); err != nil {
		t.Fatal(err)
	}
	if received.URL.Path != "/api/agent/virtual-keys" {
		t.Fatalf("virtual-key list path = %q", received.URL.Path)
	}
	if got := received.Header.Get("Authorization"); got != "Bearer ck-bf-agent-test" {
		t.Fatalf("authorization = %q", got)
	}
}

// TestVirtualKeyListPrefersManagementScope verifies administrators retain the
// complete governance list even when they also have a browser SSO session.
func TestVirtualKeyListPrefersManagementScope(t *testing.T) {
	var received *http.Request
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		received = request
		return jsonResponse(request, `{"data":[]}`), nil
	}))
	secretStore["test:agent-token"] = "ck-bf-agent-test"
	secretStore["test:management-key"] = "bfst-admin"

	if err := runner.Run(context.Background(), []string{"virtual-keys", "list", "--output", "raw"}); err != nil {
		t.Fatal(err)
	}
	if received.URL.Path != "/api/governance/virtual-keys" {
		t.Fatalf("virtual-key list path = %q", received.URL.Path)
	}
	if got := received.Header.Get("Authorization"); got != "Bearer bfst-admin" {
		t.Fatalf("authorization = %q", got)
	}
}

// TestVirtualKeyAssignedListAlwaysUsesAgentScope verifies administrators can
// explicitly inspect the signed-in user's tray-visible keys.
func TestVirtualKeyAssignedListAlwaysUsesAgentScope(t *testing.T) {
	var received *http.Request
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		received = request
		return jsonResponse(request, `{"virtual_keys":[]}`), nil
	}))
	secretStore["test:agent-token"] = "ck-bf-agent-test"
	secretStore["test:management-key"] = "bfst-admin"

	if err := runner.Run(context.Background(), []string{"virtual-keys", "assigned", "--output", "raw"}); err != nil {
		t.Fatal(err)
	}
	if received.URL.Path != "/api/agent/virtual-keys" {
		t.Fatalf("assigned virtual-key path = %q", received.URL.Path)
	}
	if got := received.Header.Get("Authorization"); got != "Bearer ck-bf-agent-test" {
		t.Fatalf("authorization = %q", got)
	}
}

// TestAgentVirtualKeySelectionValidatesAndApplies verifies only assigned keys
// can become the per-request SSO routing override.
func TestAgentVirtualKeySelectionValidatesAndApplies(t *testing.T) {
	var inferenceRequest *http.Request
	runner, outputBuffer, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/agent/virtual-keys":
			return jsonResponse(request, `{"virtual_keys":[{"id":"vk-1","name":"Engineering","is_active":true}]}`), nil
		case "/v1/models":
			inferenceRequest = request
			return jsonResponse(request, `{"data":[]}`), nil
		default:
			t.Fatalf("unexpected path %q", request.URL.Path)
			return nil, nil
		}
	}))
	secretStore["test:agent-token"] = "ck-bf-agent-test"

	if err := runner.Run(context.Background(), []string{"auth", "select-virtual-key", "vk-1"}); err != nil {
		t.Fatal(err)
	}
	if got := secretStore["test:agent-virtual-key-id"]; got != "vk-1" {
		t.Fatalf("stored selection = %q", got)
	}
	if !strings.Contains(outputBuffer.String(), "Engineering (vk-1)") {
		t.Fatalf("selection output = %q", outputBuffer.String())
	}
	if err := runner.Run(context.Background(), []string{"models", "list", "--output", "raw"}); err != nil {
		t.Fatal(err)
	}
	if got := inferenceRequest.Header.Get("x-bf-agent-vk-id"); got != "vk-1" {
		t.Fatalf("inference selection header = %q", got)
	}
	if got := inferenceRequest.Header.Get("X-Bifrost-Agent"); got != "bifrost-cli/test" {
		t.Fatalf("agent identity header = %q", got)
	}
}

// TestUsageSummaryRendersUserLimits verifies the CLI presents the tray's user-scoped data.
func TestUsageSummaryRendersUserLimits(t *testing.T) {
	runner, outputBuffer, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/agent/usage-summary" || request.Header.Get("Authorization") != "Bearer ck-bf-agent-test" {
			t.Fatalf("unexpected usage request path=%q auth=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		return jsonResponse(request, `{
			"budgets":[{"id":"budget-1","access_profile_name":"Engineering","scope":"profile","reset_duration":"1M","used":5,"limit":20,"available":15}],
			"rate_limits":[{"id":"rate-1","provider":"openai","scope":"provider","tokens":{"used":100,"limit":1000,"available":900,"reset_duration":"1h"}}],
			"top_models":[{"label":"openai/gpt-5","provider":"openai","total_requests":3,"total_tokens":120,"total_cost":0.25}],
			"top_apps":[{"label":"bifrost-cli","total_requests":2,"total_tokens":80,"total_cost":0.15}]
		}`), nil
	}))
	secretStore["test:agent-token"] = "ck-bf-agent-test"

	if err := runner.Run(context.Background(), []string{"usage"}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Budgets", "Engineering / overall", "$5.00", "Rate limits", "openai/gpt-5", "bifrost-cli"} {
		if !strings.Contains(outputBuffer.String(), expected) {
			t.Fatalf("usage output %q does not contain %q", outputBuffer.String(), expected)
		}
	}
}

// TestUsageRejectsUnknownViewBeforeNetworkCall verifies an unknown usage view
// is rejected locally instead of surfacing a gateway error for what is
// actually a typo in the view name.
func TestUsageRejectsUnknownViewBeforeNetworkCall(t *testing.T) {
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("usage unexpectedly called %s", request.URL)
		return nil, nil
	}))
	secretStore["test:agent-token"] = "ck-bf-agent-test"

	if err := runner.Run(context.Background(), []string{"usage", "bogus-view"}); err == nil {
		t.Fatal("expected an unknown usage view to be rejected")
	}
}

// TestUsageQuietRejectsUnknownView verifies --quiet does not bypass view
// validation and return a false success for an unknown view.
func TestUsageQuietRejectsUnknownView(t *testing.T) {
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("usage unexpectedly called %s", request.URL)
		return nil, nil
	}))
	secretStore["test:agent-token"] = "ck-bf-agent-test"

	if err := runner.Run(context.Background(), []string{"--quiet", "usage", "bogus-view"}); err == nil {
		t.Fatal("expected an unknown usage view to be rejected even with --quiet")
	}
}

func TestSubcommandHelpReturnsSuccess(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("help unexpectedly called %s", request.URL)
		return nil, nil
	}))

	for _, args := range [][]string{
		{"request", "GET", "/api/providers", "--help"},
		{"providers", "get", "openai", "--help"},
	} {
		if err := runner.Run(context.Background(), args); err != nil {
			t.Fatalf("Run(%q) returned %v after printing help", args, err)
		}
	}
}

// TestAgentDeviceIDIsInstallationScoped verifies contexts share a stable,
// opaque identity without deriving it from machine identifiers.
func TestAgentDeviceIDIsInstallationScoped(t *testing.T) {
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("device ID generation unexpectedly called %s", request.URL)
		return nil, nil
	}))
	first, err := runner.ensureAgentDeviceID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.ensureAgentDeviceID()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("device IDs differ across contexts: %q != %q", first, second)
	}
	if !strings.HasPrefix(first, "cli-") || len(first) < 40 {
		t.Fatalf("device ID is not an opaque CLI identifier: %q", first)
	}
	if got := secretStore["installation:agent-device-id"]; got != first {
		t.Fatalf("installation device ID = %q, want %q", got, first)
	}
}

// TestResourceDryRunRequiresNoNetwork verifies mutations can be inspected offline.
func TestResourceDryRunRequiresNoNetwork(t *testing.T) {
	runner, outputBuffer, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("dry-run unexpectedly called %s", request.URL)
		return nil, nil
	}))
	err := runner.Run(context.Background(), []string{
		"--output", "json", "providers", "create", "--body", `{"name":"openai"}`, "--dry-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outputBuffer.String(), `"dry_run": true`) || !strings.Contains(outputBuffer.String(), "/api/providers") {
		t.Fatalf("unexpected dry-run output %q", outputBuffer.String())
	}
}

// TestAuthLoginStoresCookieToken verifies password login persists only the returned session token.
func TestAuthLoginStoresCookieToken(t *testing.T) {
	runner, outputBuffer, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/session/login" || request.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected login request path=%q auth=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Set-Cookie": {"token=session-test; Path=/; HttpOnly"}},
			Body:       io.NopCloser(strings.NewReader(`{"message":"Login successful"}`)), Request: request,
		}, nil
	}))
	runner.In = strings.NewReader("password\n")
	if err := runner.Run(context.Background(), []string{"auth", "login", "--username", "admin", "--password-stdin"}); err != nil {
		t.Fatal(err)
	}
	if got := secretStore["test:session-token"]; got != "session-test" {
		t.Fatalf("stored session = %q", got)
	}
	if !strings.Contains(outputBuffer.String(), "Logged in") {
		t.Fatalf("unexpected output %q", outputBuffer.String())
	}
}

// TestAuthPrintTokenRefreshesBeforeOutput verifies coding-agent helpers receive
// only the current agent credential on stdout.
func TestAuthPrintTokenRefreshesBeforeOutput(t *testing.T) {
	modelRequests := 0
	runner, outputBuffer, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/models":
			modelRequests++
			if got := request.Header.Get("Authorization"); got == "Bearer ck-bf-agent-old" {
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Header:     http.Header{"Content-Type": {"application/json"}, "x-bf-agent-auth": {"rejected"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":"expired"}`)), Request: request,
				}, nil
			}
			if got := request.Header.Get("Authorization"); got != "Bearer ck-bf-agent-new" {
				t.Fatalf("retried authorization = %q", got)
			}
			return jsonResponse(request, `{"data":[]}`), nil
		case "/api/agent/auth/refresh":
			return jsonResponse(request, `{"agent_access_token":"ck-bf-agent-new","refresh_token":"refresh-new"}`), nil
		default:
			t.Fatalf("unexpected path %q", request.URL.Path)
			return nil, nil
		}
	}))
	secretStore["test:agent-token"] = "ck-bf-agent-old"
	secretStore["test:agent-refresh-token"] = "refresh-old"

	if err := runner.Run(context.Background(), []string{"auth", "print-token"}); err != nil {
		t.Fatal(err)
	}
	if got := outputBuffer.String(); got != "ck-bf-agent-new\n" {
		t.Fatalf("stdout = %q", got)
	}
	if modelRequests != 2 {
		t.Fatalf("model requests = %d", modelRequests)
	}
	if secretStore["test:agent-token"] != "ck-bf-agent-new" || secretStore["test:agent-refresh-token"] != "refresh-new" {
		t.Fatalf("stored rotated credentials = %#v", secretStore)
	}
}

// TestAPICallResolvesPathParameters verifies operation calls safely substitute identifiers.
func TestAPICallResolvesPathParameters(t *testing.T) {
	var received *http.Request
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		received = request
		return jsonResponse(request, `{"ok":true}`), nil
	}))
	if err := runner.Run(context.Background(), []string{
		"--output", "raw", "api", "call", "getProvider", "--param", "provider=openai",
	}); err != nil {
		t.Fatal(err)
	}
	if received.URL.Path != "/api/providers/openai" {
		t.Fatalf("operation path = %q", received.URL.Path)
	}
}

// TestConfigureAndUnconfigureRestoresAgentFiles verifies persistent setup is reversible.
func TestConfigureAndUnconfigureRestoresAgentFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"theme":"dark"}`)
	if err := os.WriteFile(settingsPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("configure unexpectedly called %s", request.URL)
		return nil, nil
	}))
	secretStore["test:virtual-key"] = "sk-bf-test"
	if err := runner.Run(context.Background(), []string{"configure", "claude", "--model", "gpt-test"}); err != nil {
		t.Fatal(err)
	}
	configured, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configured), "ANTHROPIC_BASE_URL") || bytes.Equal(configured, original) {
		t.Fatalf("settings were not configured: %s", configured)
	}
	if err := runner.Run(context.Background(), []string{"unconfigure", "claude"}); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("restored settings = %q, want %q", restored, original)
	}
}

// TestConfigureRejectsUnexpectedTrailingArguments verifies a typo'd extra
// argument is rejected instead of silently ignored.
func TestConfigureRejectsUnexpectedTrailingArguments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("configure unexpectedly called %s", request.URL)
		return nil, nil
	}))
	secretStore["test:virtual-key"] = "sk-bf-test"
	if err := runner.Run(context.Background(), []string{"configure", "claude", "unexpected"}); err == nil {
		t.Fatal("expected an unexpected trailing argument to be rejected")
	}
}

// TestUnconfigureRejectsUnexpectedTrailingArguments mirrors the configure
// case. A valid receipt is set up first so the trailing argument is the only
// possible reason for rejection.
func TestUnconfigureRejectsUnexpectedTrailingArguments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("unconfigure unexpectedly called %s", request.URL)
		return nil, nil
	}))
	secretStore["test:virtual-key"] = "sk-bf-test"
	if err := runner.Run(context.Background(), []string{"configure", "claude", "--model", "gpt-test"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), []string{"unconfigure", "claude", "unexpected"}); err == nil {
		t.Fatal("expected an unexpected trailing argument to be rejected")
	}
}

// TestReadSecretPreservesIntentionalWhitespaceFromStdin verifies a credential
// with intentional leading/trailing whitespace on stdin is preserved exactly
// (minus the line terminator), instead of being corrupted by a blanket trim
// before it reaches the gateway or a stored provider credential.
func TestReadSecretPreservesIntentionalWhitespaceFromStdin(t *testing.T) {
	runner := &Runner{In: strings.NewReader("  secret-with-spaces  \n"), ErrOut: &bytes.Buffer{}}
	value, err := runner.readSecret("Password: ", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := "  secret-with-spaces  "; value != want {
		t.Fatalf("readSecret() = %q, want %q", value, want)
	}
}

// TestReadSecretStdinStripsOnlyLineTerminator verifies a CRLF terminator is
// stripped without touching intentional whitespace before it.
func TestReadSecretStdinStripsOnlyLineTerminator(t *testing.T) {
	runner := &Runner{In: strings.NewReader("secret \r\n"), ErrOut: &bytes.Buffer{}}
	value, err := runner.readSecret("Password: ", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := "secret "; value != want {
		t.Fatalf("readSecret() = %q, want %q", value, want)
	}
}

// TestContextListRespectsGlobalOutputFormat verifies "--output json context
// list" produces JSON instead of always rendering a table.
func TestContextListRespectsGlobalOutputFormat(t *testing.T) {
	runner, outputBuffer, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("context list unexpectedly called %s", request.URL)
		return nil, nil
	}))
	if err := runner.Run(context.Background(), []string{"--output", "json", "context", "list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outputBuffer.String(), `"id": "test"`) {
		t.Fatalf("context list output = %q, want JSON", outputBuffer.String())
	}
}

// TestContextShowRespectsGlobalOutputFormat mirrors the list case.
func TestContextShowRespectsGlobalOutputFormat(t *testing.T) {
	runner, outputBuffer, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("context show unexpectedly called %s", request.URL)
		return nil, nil
	}))
	if err := runner.Run(context.Background(), []string{"--output", "json", "context", "show", "test"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outputBuffer.String(), `"id": "test"`) {
		t.Fatalf("context show output = %q, want JSON", outputBuffer.String())
	}
}

// TestContextAddPreservesLocalBaseURLFlag verifies the context command keeps
// its own --base-url flag instead of having the global parser consume it.
func TestContextAddPreservesLocalBaseURLFlag(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "separate value", args: []string{"context", "add", "internal", "--base-url", "https://internal.getbifrost.dev", "--use"}},
		{name: "inline value", args: []string{"context", "add", "internal", "--base-url=https://internal.getbifrost.dev", "--use"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), "state.json")
			runner := &Runner{Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, StatePath: statePath}
			if err := runner.Run(context.Background(), test.args); err != nil {
				t.Fatalf("context add: %v", err)
			}

			state, err := config.LoadState(statePath)
			if err != nil {
				t.Fatal(err)
			}
			profile := state.ProfileByName("internal")
			if profile == nil {
				t.Fatal("context was not persisted")
			}
			if want := "https://internal.getbifrost.dev"; profile.BaseURL != want {
				t.Fatalf("context base URL = %q, want %q", profile.BaseURL, want)
			}
			if state.LastProfileID != profile.ID {
				t.Fatalf("active context = %q, want %q", state.LastProfileID, profile.ID)
			}
		})
	}
}

// TestConfigureValidatesEndpointBeforeSavingPendingReceipt verifies a bad
// Base URL fails fast without ever writing a pending receipt to disk, since
// nothing was actually changed and a leftover pending receipt would
// otherwise force a later configure/unconfigure to require --force for no
// reason.
func TestConfigureValidatesEndpointBeforeSavingPendingReceipt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	statePath := t.TempDir() + "/state.json"
	state := &config.State{
		Profiles:      []config.Profile{{ID: "test", Name: "Test", BaseURL: "not-a-valid-url"}},
		LastProfileID: "test", Selections: map[string]config.Selection{},
	}
	if err := config.SaveState(statePath, state); err != nil {
		t.Fatal(err)
	}
	outputBuffer := &bytes.Buffer{}
	secretStore := memorySecretStore{"test:virtual-key": "sk-bf-test"}
	runner := &Runner{
		In: strings.NewReader(""), Out: outputBuffer, ErrOut: &bytes.Buffer{},
		Build: BuildInfo{Version: "test", Commit: "abc"}, Secrets: secretStore,
		HTTPClient: &http.Client{Transport: testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			t.Fatalf("configure unexpectedly called %s", request.URL)
			return nil, nil
		})}, StatePath: statePath,
	}

	if err := runner.Run(context.Background(), []string{"configure", "claude", "--model", "gpt-test"}); err == nil {
		t.Fatal("expected an invalid base URL to be rejected")
	}
	receiptPath := filepath.Join(filepath.Dir(statePath), "receipts", "test-claude.json")
	if _, statErr := os.Stat(receiptPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected no receipt to be written for a failed endpoint, stat err = %v", statErr)
	}
}

// TestConfigureRejectsReceiptWithStaleFilePaths verifies an existing receipt
// whose captured file paths no longer match the current manifest (e.g. a
// changed $HOME) is rejected instead of silently reconfiguring against a
// receipt that can't correctly restore what's about to be written. The stale
// path is made to exist so verifyConfiguredFiles/recordConfiguredHashes
// would otherwise succeed against it, leaving a receipt with no relation to
// the file actually configured.
func TestConfigureRejectsReceiptWithStaleFilePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("configure unexpectedly called %s", request.URL)
		return nil, nil
	}))
	secretStore["test:virtual-key"] = "sk-bf-test"

	staleDir := t.TempDir()
	stalePath := filepath.Join(staleDir, "settings.json")
	staleContent := []byte(`{"stale":true}`)
	if err := os.WriteFile(stalePath, staleContent, 0o600); err != nil {
		t.Fatal(err)
	}
	staleReceiptPath := filepath.Join(filepath.Dir(runner.StatePath), "receipts", "test-claude.json")
	staleReceipt := &configReceipt{
		Version: 1, ContextID: "test", Agent: "claude",
		Files: []configSnapshot{{Path: stalePath, Existed: true, Mode: 0o600, Original: staleContent}},
	}
	if err := saveConfigReceipt(staleReceiptPath, staleReceipt); err != nil {
		t.Fatal(err)
	}

	if err := runner.Run(context.Background(), []string{"configure", "claude", "--model", "gpt-test"}); err == nil {
		t.Fatal("expected a receipt with stale file paths to be rejected")
	}
	realSettingsPath := filepath.Join(home, ".claude", "settings.json")
	if _, statErr := os.Stat(realSettingsPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected configure to have made no changes to the real settings path, stat err = %v", statErr)
	}
	unchanged, err := os.ReadFile(stalePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unchanged, staleContent) {
		t.Fatalf("stale path was modified: %s", unchanged)
	}
}

// TestUnconfigureForceDiscardsStaleManifestWithoutRestoring verifies the
// recovery command recommended by configure cannot overwrite paths captured
// under an old home directory. Force may discard the unusable receipt, but it
// must not restore any stale path from it.
func TestUnconfigureForceDiscardsStaleManifestWithoutRestoring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runner, output, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("unconfigure unexpectedly called %s", request.URL)
		return nil, nil
	}))

	stalePath := filepath.Join(t.TempDir(), "settings.json")
	staleContent := []byte(`{"stale":"leave-me-alone"}`)
	if err := os.WriteFile(stalePath, staleContent, 0o600); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(filepath.Dir(runner.StatePath), "receipts", "test-claude.json")
	receipt := &configReceipt{
		Version: 1, ContextID: "test", Agent: "claude",
		Files: []configSnapshot{{Path: stalePath, Existed: true, Mode: 0o600, Original: []byte(`{"restored":true}`)}},
	}
	if err := saveConfigReceipt(receiptPath, receipt); err != nil {
		t.Fatal(err)
	}

	if err := runner.Run(context.Background(), []string{"unconfigure", "claude", "--force"}); err != nil {
		t.Fatalf("force-discard stale receipt: %v", err)
	}
	unchanged, err := os.ReadFile(stalePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unchanged, staleContent) {
		t.Fatalf("stale path was restored: %s", unchanged)
	}
	if _, err := os.Stat(receiptPath); !os.IsNotExist(err) {
		t.Fatalf("stale receipt was not discarded, stat error = %v", err)
	}
	if !strings.Contains(output.String(), "Discarded stale") {
		t.Fatalf("output = %q, want stale-receipt notice", output.String())
	}
}

// TestUnconfigureRejectsPathTraversalAgentID verifies a crafted agent ID
// cannot escape the receipts directory to load and restore an arbitrary
// planted receipt (CWE-22).
func TestUnconfigureRejectsPathTraversalAgentID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("unconfigure unexpectedly called %s", request.URL)
		return nil, nil
	}))

	// A file entirely unrelated to any supported agent's config, sitting
	// where a crafted agent ID's traversal would land a "receipt" lookup.
	victim := filepath.Join(home, "victim.txt")
	if err := os.WriteFile(victim, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}

	stateDir := filepath.Dir(runner.StatePath)
	if err := os.MkdirAll(filepath.Join(stateDir, "receipts"), 0o700); err != nil {
		t.Fatal(err)
	}
	maliciousReceipt := configReceipt{
		Version: 1, ContextID: "attacker", Agent: "attacker",
		Files: []configSnapshot{{Path: victim, Existed: false}},
	}
	body, err := json.Marshal(maliciousReceipt)
	if err != nil {
		t.Fatal(err)
	}
	// "test-x/../../planted.json" resolves (filepath.Join + Clean) to
	// stateDir/planted.json, escaping the receipts directory entirely.
	plantedPath := filepath.Join(stateDir, "planted.json")
	if err := os.WriteFile(plantedPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	runErr := runner.Run(context.Background(), []string{"unconfigure", "x/../../planted"})
	_, statErr := os.Stat(victim)
	if runErr == nil {
		t.Errorf("expected unconfigure to reject an unsupported/traversal agent ID, got nil error")
	}
	if statErr != nil {
		t.Errorf("path traversal deleted a file outside any supported agent's config: %v", statErr)
	}
	if t.Failed() {
		t.FailNow()
	}
}

// plantPendingReceipt writes a pending (interrupted) configuration receipt
// directly at the path configureHarness/unconfigureHarness would load, as if
// a prior configure attempt crashed after snapshotting but before finishing.
func plantPendingReceipt(t *testing.T, runner *Runner, agentID string, files []configSnapshot) {
	t.Helper()
	receiptPath := filepath.Join(filepath.Dir(runner.StatePath), "receipts", "test-"+agentID+".json")
	receipt := &configReceipt{Version: 1, ContextID: "test", Agent: agentID, Pending: true, Files: files}
	if err := saveConfigReceipt(receiptPath, receipt); err != nil {
		t.Fatal(err)
	}
}

// TestConfigureRequiresForceForPendingReceipt verifies a leftover pending
// receipt (from an interrupted prior configure) blocks a silent reconfigure,
// since an empty ConfiguredSHA256 can't otherwise detect user edits made in
// the meantime.
func TestConfigureRequiresForceForPendingReceipt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	userEdited := []byte(`{"theme":"user-edited-after-crash"}`)
	if err := os.WriteFile(settingsPath, userEdited, 0o600); err != nil {
		t.Fatal(err)
	}
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("configure unexpectedly called %s", request.URL)
		return nil, nil
	}))
	secretStore["test:virtual-key"] = "sk-bf-test"
	plantPendingReceipt(t, runner, "claude", []configSnapshot{
		{Path: settingsPath, Existed: true, Mode: 0o600, Original: []byte(`{"theme":"original"}`)},
	})

	if err := runner.Run(context.Background(), []string{"configure", "claude", "--model", "gpt-test"}); err == nil {
		t.Fatal("expected configure to require --force for a pending receipt")
	}
	current, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, userEdited) {
		t.Fatalf("settings were modified without --force: %s", current)
	}
}

// TestUnconfigureRequiresForceForPendingReceipt mirrors the configure-side
// check for restore.
func TestUnconfigureRequiresForceForPendingReceipt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	userEdited := []byte(`{"theme":"user-edited-after-crash"}`)
	if err := os.WriteFile(settingsPath, userEdited, 0o600); err != nil {
		t.Fatal(err)
	}
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("unconfigure unexpectedly called %s", request.URL)
		return nil, nil
	}))
	plantPendingReceipt(t, runner, "claude", []configSnapshot{
		{Path: settingsPath, Existed: true, Mode: 0o600, Original: []byte(`{"theme":"original"}`)},
	})

	if err := runner.Run(context.Background(), []string{"unconfigure", "claude"}); err == nil {
		t.Fatal("expected unconfigure to require --force for a pending receipt")
	}
	current, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, userEdited) {
		t.Fatalf("settings were restored without --force: %s", current)
	}
}

// TestUnconfigureForcePendingReceiptProceeds verifies --force still overrides
// the pending-receipt guard, restoring the captured originals.
func TestUnconfigureForcePendingReceiptProceeds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"theme":"user-edited-after-crash"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("unconfigure unexpectedly called %s", request.URL)
		return nil, nil
	}))
	plantPendingReceipt(t, runner, "claude", []configSnapshot{
		{Path: settingsPath, Existed: true, Mode: 0o600, Original: []byte(`{"theme":"original"}`)},
	})

	if err := runner.Run(context.Background(), []string{"unconfigure", "claude", "--force"}); err != nil {
		t.Fatalf("expected --force to override the pending-receipt guard: %v", err)
	}
	restored, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != `{"theme":"original"}` {
		t.Fatalf("settings were not restored: %s", restored)
	}
}

// TestGenerateVirtualKeyBuildsGovernancePayload verifies the Lite-compatible key workflow.
func TestGenerateVirtualKeyBuildsGovernancePayload(t *testing.T) {
	var receivedBody string
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		receivedBody = string(body)
		return jsonResponse(request, `{"id":"vk-1","value":"sk-bf-generated"}`), nil
	}))
	if err := runner.Run(context.Background(), []string{
		"keys", "generate", "--name", "developer", "--provider", "openai", "--allowed-model", "gpt-5", "--output", "raw",
	}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"name":"developer"`, `"provider":"openai"`, `"allowed_models":["gpt-5"]`} {
		if !strings.Contains(receivedBody, expected) {
			t.Fatalf("request body %q does not contain %q", receivedBody, expected)
		}
	}
}

// TestGenerateVirtualKeyRejectsAllowedModelWithoutProvider verifies
// --allowed-model without --provider is rejected instead of silently
// dropped: the API only attaches allowed_models inside provider_configs
// entries, so without --provider the restriction never reaches the
// request, leaving the created virtual key broader than intended.
func TestGenerateVirtualKeyRejectsAllowedModelWithoutProvider(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("virtual key generation unexpectedly called %s", request.URL)
		return nil, nil
	}))
	err := runner.Run(context.Background(), []string{
		"keys", "generate", "--name", "developer", "--allowed-model", "gpt-5", "--output", "raw",
	})
	if err == nil {
		t.Fatal("expected --allowed-model without --provider to be rejected")
	}
}

// TestAPICallRejectsUnexpectedTrailingArguments verifies a typo'd extra
// argument to "api call" is rejected instead of silently ignored.
func TestAPICallRejectsUnexpectedTrailingArguments(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("api call unexpectedly sent a request for %s", request.URL)
		return nil, nil
	}))
	err := runner.Run(context.Background(), []string{"api", "call", "getProvider", "--param", "provider=openai", "unexpected"})
	if err == nil {
		t.Fatal("expected an unexpected trailing argument to be rejected")
	}
}

// TestAPICallRequiresConfirmationForDestructivePOST verifies destructive
// semantics are not inferred solely from the HTTP verb.
func TestAPICallRequiresConfirmationForDestructivePOST(t *testing.T) {
	tests := []struct {
		operation string
		params    []string
	}{
		{operation: "cancelBatch", params: []string{"--param", "batch_id=batch-1"}},
		{operation: "cancelResponse", params: []string{"--param", "response_id=response-1"}},
		{operation: "bulkRotateVirtualKeys"},
	}
	for _, test := range tests {
		t.Run(test.operation, func(t *testing.T) {
			requestCount := 0
			runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				requestCount++
				return jsonResponse(request, `{}`), nil
			}))
			args := append([]string{"api", "call", test.operation}, test.params...)
			args = append(args, "--body", `{}`)
			err := runner.Run(context.Background(), args)
			if err == nil || !strings.Contains(err.Error(), "--yes") {
				t.Fatalf("error = %v, want confirmation error", err)
			}
			if requestCount != 0 {
				t.Fatalf("request count = %d, want 0", requestCount)
			}
		})
	}
}

// TestStatusAndDoctorRejectUnexpectedArguments ensures typo'd arguments do
// not silently trigger network diagnostics.
func TestStatusAndDoctorRejectUnexpectedArguments(t *testing.T) {
	for _, command := range []string{"status", "doctor"} {
		t.Run(command, func(t *testing.T) {
			requestCount := 0
			runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				requestCount++
				return jsonResponse(request, `{}`), nil
			}))
			if err := runner.Run(context.Background(), []string{command, "unexpected"}); err == nil {
				t.Fatal("expected an unexpected argument to be rejected")
			}
			if requestCount != 0 {
				t.Fatalf("request count = %d, want 0", requestCount)
			}
		})
	}
}

// TestStatusAndDoctorHelpIsOffline verifies command help neither requires a
// configured context nor contacts a gateway.
func TestStatusAndDoctorHelpIsOffline(t *testing.T) {
	for _, command := range []string{"status", "doctor"} {
		t.Run(command, func(t *testing.T) {
			output := &bytes.Buffer{}
			runner := &Runner{Out: output, ErrOut: &bytes.Buffer{}}
			if err := runner.Run(context.Background(), []string{command, "--help"}); err != nil {
				t.Fatalf("offline help: %v", err)
			}
			if !strings.Contains(output.String(), "Usage: bifrost "+command) {
				t.Fatalf("output = %q, want command usage", output.String())
			}
		})
	}
}

// TestRequestRejectsUnexpectedTrailingArguments verifies a typo'd extra
// argument to "request" is rejected instead of silently ignored.
func TestRequestRejectsUnexpectedTrailingArguments(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("request unexpectedly sent a request for %s", request.URL)
		return nil, nil
	}))
	err := runner.Run(context.Background(), []string{"request", "GET", "/api/providers", "unexpected"})
	if err == nil {
		t.Fatal("expected an unexpected trailing argument to be rejected")
	}
}

// TestCreateProviderCredentialRejectsUnexpectedTrailingArguments verifies a
// typo'd extra argument is rejected instead of silently ignored.
func TestCreateProviderCredentialRejectsUnexpectedTrailingArguments(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("credentials create unexpectedly sent a request for %s", request.URL)
		return nil, nil
	}))
	err := runner.Run(context.Background(), []string{"credentials", "create", "openai", "--name", "prod", "--keyless", "unexpected"})
	if err == nil {
		t.Fatal("expected an unexpected trailing argument to be rejected")
	}
}

// TestGenerateVirtualKeyRejectsUnexpectedTrailingArguments verifies a
// typo'd extra argument is rejected instead of silently ignored.
func TestGenerateVirtualKeyRejectsUnexpectedTrailingArguments(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("virtual key generation unexpectedly sent a request for %s", request.URL)
		return nil, nil
	}))
	err := runner.Run(context.Background(), []string{"keys", "generate", "--name", "developer", "unexpected"})
	if err == nil {
		t.Fatal("expected an unexpected trailing argument to be rejected")
	}
}

// TestExitCodeMapsGatewayFailures verifies automation can distinguish common failures.
func TestExitCodeMapsGatewayFailures(t *testing.T) {
	for status, expected := range map[int]int{
		http.StatusUnauthorized: 3,
		http.StatusForbidden:    3,
		http.StatusNotFound:     4,
		http.StatusConflict:     5,
		http.StatusBadGateway:   6,
		http.StatusBadRequest:   1,
	} {
		if got := ExitCode(&client.APIError{StatusCode: status}); got != expected {
			t.Fatalf("ExitCode(HTTP %d) = %d, want %d", status, got, expected)
		}
	}
	for name, err := range map[string]error{
		"connection failure": &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
		"DNS failure":        &net.DNSError{Err: "no such host", Name: "gateway.example"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ExitCode(err); got != ExitUnavailable {
				t.Fatalf("ExitCode(%v) = %d, want %d", err, got, ExitUnavailable)
			}
		})
	}
}

// TestEarlyDispatchHelpDoesNotError covers commands that are handled before
// the common command table and therefore need their own flag.ErrHelp handling.
func TestEarlyDispatchHelpDoesNotError(t *testing.T) {
	for _, args := range [][]string{
		{"context", "add", "--help"},
		{"context", "add", "test-name", "--help"},
		{"context", "delete", "--help"},
		{"context", "delete", "test", "--help"},
		{"api", "list", "--help"},
		{"resources", "list", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			runner, outputBuffer, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				t.Fatalf("help unexpectedly sent a request for %s", request.URL)
				return nil, nil
			}))
			if err := runner.Run(context.Background(), args); err != nil {
				t.Fatalf("Run(%v) error = %v", args, err)
			}
			helpText := outputBuffer.String() + runner.ErrOut.(*bytes.Buffer).String()
			if !strings.Contains(helpText, "Usage") {
				t.Fatalf("Run(%v) output = %q, want usage text", args, helpText)
			}
		})
	}
}

// TestResourceRegistryArgumentsMatchPathParameters prevents ergonomic commands from
// leaving an unsubstituted route placeholder in a live request.
func TestResourceRegistryArgumentsMatchPathParameters(t *testing.T) {
	for resourceName, descriptor := range resourceRegistry {
		for actionName, action := range descriptor.Actions {
			matches := pathParameterPattern.FindAllStringSubmatch(action.Path, -1)
			if len(matches) != len(action.Arguments) {
				t.Errorf("%s %s has %d path parameters and %d arguments", resourceName, actionName, len(matches), len(action.Arguments))
				continue
			}
			for index, match := range matches {
				expected := strings.ReplaceAll(action.Arguments[index], "-", "_")
				if match[1] != expected {
					t.Errorf("%s %s parameter %q does not match argument %q", resourceName, actionName, match[1], action.Arguments[index])
				}
			}
		}
	}
}

// TestCatalogedResourceRoutesMatchOpenAPI catches method/path drift for the
// curated commands covered by the bundled public OpenAPI document. Enterprise
// routes and known documentation gaps are explicit exceptions rather than
// being silently ignored.
func TestCatalogedResourceRoutesMatchOpenAPI(t *testing.T) {
	catalog := map[string]map[string]struct{}{}
	for _, operation := range operations.Catalog {
		path := normalizeRouteParameters(operation.Path)
		if catalog[path] == nil {
			catalog[path] = map[string]struct{}{}
		}
		catalog[path][strings.ToUpper(operation.Method)] = struct{}{}
	}
	for resourceName, descriptor := range resourceRegistry {
		for actionName, action := range descriptor.Actions {
			path := normalizeRouteParameters(action.Path)
			methods := catalog[path]
			if _, exists := methods[strings.ToUpper(action.Method)]; exists {
				continue
			}
			if len(methods) > 0 {
				t.Errorf("%s %s uses %s %s; OpenAPI methods are %#v", resourceName, actionName, action.Method, action.Path, methods)
				continue
			}
			if !knownPublicCatalogGap(path) {
				t.Errorf("%s %s route %s %s is absent from OpenAPI", resourceName, actionName, action.Method, action.Path)
			}
		}
	}
}

func normalizeRouteParameters(path string) string {
	return pathParameterPattern.ReplaceAllString(path, "{}")
}

func knownPublicCatalogGap(path string) bool {
	prefixes := []string{
		"/api/agent/", "/api/alerting/", "/api/apps", "/api/branding", "/api/cluster/",
		"/api/devices", "/api/edge/", "/api/feature-flags", "/api/guardrails",
		"/api/license", "/api/mcp-servers", "/api/network-trust", "/api/oauth2/sessions",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	known := map[string]struct{}{
		"/api/api-keys":                            {},
		"/api/api-keys/{}":                         {},
		"/api/config/metadata":                     {},
		"/api/governance/users/{}/business-units":  {},
		"/api/governance/virtual-keys/{}/copy":     {},
		"/api/governance/virtual-keys/{}/reveal":   {},
		"/api/large-payload-config":                {},
		"/api/load-balancer-config":                {},
		"/api/mcp/library":                         {},
		"/api/mcp/library/filterdata":              {},
		"/api/mcp/library/force-sync":              {},
		"/api/mcp/library/{}":                      {},
		"/api/mcp/virtual-mcps":                    {},
		"/api/mcp/virtual-mcps/{}":                 {},
		"/api/mcp/virtual-mcps/{}/virtual-keys/{}": {},
		"/api/models/catalog":                      {},
		"/api/plugins/loaded":                      {},
		"/api/prompt-repo/deployments":             {},
		"/api/prompt-repo/deployments/{}":          {},
		"/api/providers/{}/keys/{}/refresh-models": {},
		"/api/providers/{}/refresh-models":         {},
	}
	_, exists := known[path]
	return exists
}

// TestEnterpriseUserCommandUsesCanonicalGovernanceRoute pins the Enterprise route
// after the old CLI descriptor incorrectly targeted /api/users.
func TestEnterpriseUserCommandUsesCanonicalGovernanceRoute(t *testing.T) {
	var received *http.Request
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		received = request
		return jsonResponse(request, `{"data":[]}`), nil
	}))
	if err := runner.Run(context.Background(), []string{"users", "list", "--output", "raw"}); err != nil {
		t.Fatal(err)
	}
	if received.URL.Path != "/api/governance/users" {
		t.Fatalf("users list path = %q", received.URL.Path)
	}
	if _, exists := resourceRegistry["pricing-overrides"].Actions["get"]; exists {
		t.Fatal("pricing-overrides unexpectedly exposes a nonexistent item GET")
	}
}

// TestDynamicResourceDispatch exercises a resource that is intentionally absent
// from the root switch so registry additions remain immediately reachable.
func TestDynamicResourceDispatch(t *testing.T) {
	var received *http.Request
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		received = request
		return jsonResponse(request, `{"data":[]}`), nil
	}))
	if err := runner.Run(context.Background(), []string{"business-units", "customers", "bu/one", "--output", "raw"}); err != nil {
		t.Fatal(err)
	}
	if received.URL.EscapedPath() != "/api/governance/business-units/bu%2Fone/customers" {
		t.Fatalf("business-unit customers path = %q", received.URL.EscapedPath())
	}
}

// TestInferSupportsExtendedAndNonJSONRequests verifies newer and multipart-style
// inference contracts can use the shared first-class transport.
func TestInferSupportsExtendedAndNonJSONRequests(t *testing.T) {
	var received *http.Request
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		received = request
		return jsonResponse(request, `{"id":"job-1"}`), nil
	}))
	err := runner.Run(context.Background(), []string{
		"infer", "async-image-edit", "--body", "binary-payload", "--header", "Content-Type: multipart/form-data; boundary=test", "--output", "raw",
	})
	if err != nil {
		t.Fatal(err)
	}
	if received.URL.Path != "/v1/async/images/edits" {
		t.Fatalf("inference path = %q", received.URL.Path)
	}
	if got := received.Header.Get("Content-Type"); got != "multipart/form-data; boundary=test" {
		t.Fatalf("content type = %q", got)
	}
}

// TestInferRejectsUnexpectedTrailingArguments verifies a typo'd extra
// positional argument is rejected rather than silently ignored.
func TestInferRejectsUnexpectedTrailingArguments(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("infer unexpectedly sent a request for %s", request.URL)
		return nil, nil
	}))
	err := runner.Run(context.Background(), []string{"infer", "embeddings", "--body", "{}", "typo"})
	if err == nil {
		t.Fatal("expected an unexpected trailing argument to be rejected")
	}
}

// TestChatHelpDoesNotError verifies "bifrost chat --help" and bare
// "bifrost chat" print usage successfully instead of returning the
// missing-model/message error (which main would map to a non-zero exit).
func TestChatHelpDoesNotError(t *testing.T) {
	for _, args := range [][]string{{"chat", "--help"}, {"chat"}} {
		runner, out, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			t.Fatalf("chat help unexpectedly sent a request for %s", request.URL)
			return nil, nil
		}))
		if err := runner.Run(context.Background(), args); err != nil {
			t.Fatalf("Run(%v) error = %v", args, err)
		}
		if !strings.Contains(out.String(), "Usage") {
			t.Fatalf("Run(%v) output = %q, want usage text", args, out.String())
		}
	}
}

// TestCompletionIncludesRegistryResources prevents shell completion from lagging
// behind new first-class gateway resources.
func TestCompletionIncludesRegistryResources(t *testing.T) {
	runner := &Runner{In: strings.NewReader(""), Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}}
	if err := runner.Run(context.Background(), []string{"completion", "fish"}); err != nil {
		t.Fatal(err)
	}
	outputBuffer := runner.Out.(*bytes.Buffer)
	for _, command := range []string{"business-units", "access-profiles", "virtual-mcps", "guardrails"} {
		if !strings.Contains(outputBuffer.String(), "-a "+command+"\n") {
			t.Fatalf("completion does not include %q", command)
		}
	}
}

// TestLicenseUploadAcceptsRawBody verifies the Enterprise license workflow does
// not force the raw .bif payload through JSON validation.
func TestLicenseUploadAcceptsRawBody(t *testing.T) {
	var receivedBody string
	var receivedContentType string
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		receivedBody = string(body)
		receivedContentType = request.Header.Get("Content-Type")
		return jsonResponse(request, `{"valid":true}`), nil
	}))
	if err := runner.Run(context.Background(), []string{"license", "upload", "--body", "not-json-license", "--output", "raw"}); err != nil {
		t.Fatal(err)
	}
	if receivedBody != "not-json-license" || receivedContentType != "application/octet-stream" {
		t.Fatalf("license request body=%q content-type=%q", receivedBody, receivedContentType)
	}
}

// TestRawRequestStreaming verifies undocumented or newly added streaming routes
// remain usable without waiting for a generated operation catalog update.
func TestRawRequestStreaming(t *testing.T) {
	runner, outputBuffer, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader("data: first\n\ndata: second\n\n")), Request: request,
		}, nil
	}))
	if err := runner.Run(context.Background(), []string{"request", "POST", "/v1/responses", "--stream"}); err != nil {
		t.Fatal(err)
	}
	if outputBuffer.String() != "data: first\n\ndata: second\n\n" {
		t.Fatalf("stream output = %q", outputBuffer.String())
	}
}

// TestHarnessAliasArgumentNormalization verifies native agent flags are forwarded
// untouched while Bifrost wrapper flags remain parseable.
// TestLaunchHelpDoesNotError verifies "bifrost launch --help" prints usage
// successfully instead of exiting non-zero for flag.ErrHelp.
func TestLaunchHelpDoesNotError(t *testing.T) {
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("launch help unexpectedly sent a request for %s", request.URL)
		return nil, nil
	}))
	if err := runner.Run(context.Background(), []string{"launch", "--help"}); err != nil {
		t.Fatalf("Run([launch --help]) error = %v", err)
	}
}

// TestBareHarnessAliasLaunchesInsteadOfShowingHelp verifies "bifrost claude"
// with no arguments attempts to launch the agent (the primary invocation),
// instead of being caught by the offline-help dispatch and only printing
// usage. The preflight checks (binary lookup, then gateway ping) run before
// any real process would be spawned, so failing the gateway call from the
// mock transport proves a launch was attempted without depending on
// whether a "claude" binary happens to be on the test machine's PATH.
func TestBareHarnessAliasLaunchesInsteadOfShowingHelp(t *testing.T) {
	runner, outputBuffer, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable, Header: http.Header{},
			Body: io.NopCloser(strings.NewReader(`{"error":"unavailable"}`)), Request: request,
		}, nil
	}))
	secretStore["test:virtual-key"] = "sk-bf-test"
	err := runner.Run(context.Background(), []string{"claude"})
	if err == nil {
		t.Fatal("expected a launch-attempt error (binary lookup or gateway preflight), got nil")
	}
	if !strings.Contains(err.Error(), "not installed or not in PATH") && !strings.Contains(err.Error(), "gateway preflight failed") {
		t.Fatalf("expected a preflight error indicating the launch was attempted, got err=%v", err)
	}
	if strings.Contains(outputBuffer.String(), "Usage: bifrost claude") {
		t.Fatalf("bare invocation printed help instead of attempting to launch: %q", outputBuffer.String())
	}
}

func TestHarnessAliasArgumentNormalization(t *testing.T) {
	got := normalizeHarnessAliasArgs([]string{"--skip-verify", "--model", "gpt-test", "exec", "summarize", "--json"})
	want := []string{"--skip-verify", "--model", "gpt-test", "--", "exec", "summarize", "--json"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("normalized args = %#v, want %#v", got, want)
	}
	got = normalizeHarnessAliasArgs([]string{"--resume"})
	want = []string{"--", "--resume"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("normalized native flag = %#v, want %#v", got, want)
	}
}

// TestHarnessAliasPreservesGlobalLookingNativeFlags verifies arguments after a
// direct agent alias belong to that agent, including names shared with Bifrost.
func TestHarnessAliasPreservesGlobalLookingNativeFlags(t *testing.T) {
	runner := &Runner{ErrOut: &bytes.Buffer{}}
	globals, remaining, err := runner.parseGlobals([]string{"--base-url", "https://gateway.example", "codex", "exec", "--output", "json"})
	if err != nil {
		t.Fatal(err)
	}
	if globals.BaseURL != "https://gateway.example" {
		t.Fatalf("base URL = %q", globals.BaseURL)
	}
	want := []string{"codex", "exec", "--output", "json"}
	if strings.Join(remaining, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("remaining args = %#v, want %#v", remaining, want)
	}
}

// TestHTTPCompatibilityAlias verifies `http request` syntax uses
// the same safe gateway-relative transport as `request`.
func TestHTTPCompatibilityAlias(t *testing.T) {
	var received *http.Request
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		received = request
		return jsonResponse(request, `{"ok":true}`), nil
	}))
	if err := runner.Run(context.Background(), []string{"http", "request", "GET", "/api/version", "--auth", "none", "--output", "raw"}); err != nil {
		t.Fatal(err)
	}
	if received.URL.Path != "/api/version" || received.Header.Get("Authorization") != "" {
		t.Fatalf("unexpected compatibility request path=%q auth=%q", received.URL.Path, received.Header.Get("Authorization"))
	}
}

// TestRawRequestAgentAuth exposes existing Enterprise agent APIs without
// mistaking their /api prefix for administrative management authentication.
func TestRawRequestAgentAuth(t *testing.T) {
	var received *http.Request
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		received = request
		return jsonResponse(request, `{"virtual_keys":[]}`), nil
	}))
	secretStore["test:agent-token"] = "ck-bf-agent-test"

	if err := runner.Run(context.Background(), []string{"request", "GET", "/api/agent/virtual-keys", "--auth", "agent", "--output", "raw"}); err != nil {
		t.Fatal(err)
	}
	if got := received.Header.Get("Authorization"); got != "Bearer ck-bf-agent-test" {
		t.Fatalf("authorization = %q", got)
	}
}

// TestBaseURLOverrideWithoutContextDoesNotLeakStoredCredentials verifies
// --base-url without an explicit --context is treated as ad hoc: the
// implicit/default profile's stored credentials must not be sent to the
// overridden destination (CWE-200).
func TestBaseURLOverrideWithoutContextDoesNotLeakStoredCredentials(t *testing.T) {
	var received *http.Request
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		received = request
		return jsonResponse(request, `{"virtual_keys":[]}`), nil
	}))
	secretStore["test:agent-token"] = "ck-bf-agent-test"

	if err := runner.Run(context.Background(), []string{
		"--base-url", "https://other.example", "request", "GET", "/api/agent/virtual-keys", "--auth", "agent", "--output", "raw",
	}); err != nil {
		t.Fatal(err)
	}
	if got := received.URL.String(); !strings.HasPrefix(got, "https://other.example") {
		t.Fatalf("request URL = %q, want the overridden base URL", got)
	}
	if got := received.Header.Get("Authorization"); got != "" {
		t.Fatalf("authorization = %q, want no stored credential sent to an ad hoc base URL", got)
	}
}

// TestManagementFailureExplainsSSOScope prevents browser login from appearing
// to be a general administrator session.
func TestManagementFailureExplainsSSOScope(t *testing.T) {
	runner, _, secretStore := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`)),
			Request:    request,
		}, nil
	}))
	secretStore["test:agent-token"] = "ck-bf-agent-test"

	err := runner.Run(context.Background(), []string{"users", "list", "--output", "raw"})
	if err == nil || !strings.Contains(err.Error(), "does not grant management access") || !strings.Contains(err.Error(), "set-management-key") {
		t.Fatalf("error = %v", err)
	}
	if ExitCode(err) != 3 {
		t.Fatalf("exit code = %d", ExitCode(err))
	}
}

// TestChatCompatibilitySyntax verifies positional models,
// repeatable role messages, and generation controls map to OpenAI JSON.
func TestChatCompatibilitySyntax(t *testing.T) {
	var payload map[string]any
	runner, _, _ := newTestRunner(t, testRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return jsonResponse(request, `{"choices":[]}`), nil
	}))
	err := runner.Run(context.Background(), []string{
		"chat", "completions", "gpt-test", "-m", "system:Be terse", "-m", "user:Hello", "-t", "0.4", "--max-tokens", "25", "--output", "raw",
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload["model"] != "gpt-test" || payload["temperature"] != 0.4 || payload["max_tokens"] != float64(25) {
		t.Fatalf("chat payload = %#v", payload)
	}
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("chat messages = %#v", payload["messages"])
	}
}

// TestResourcesListWorksOffline verifies the full first-class command surface
// remains discoverable before a gateway context has been configured.
func TestResourcesListWorksOffline(t *testing.T) {
	outputBuffer := &bytes.Buffer{}
	runner := &Runner{In: strings.NewReader(""), Out: outputBuffer, ErrOut: &bytes.Buffer{}, StatePath: filepath.Join(t.TempDir(), "missing.json")}
	if err := runner.Run(context.Background(), []string{"resources", "list", "--search", "access", "--output", "json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outputBuffer.String(), `"name": "access-profiles"`) {
		t.Fatalf("resources output = %q", outputBuffer.String())
	}
}
