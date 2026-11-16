package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/cli/internal/config"
	"github.com/maximhq/bifrost/cli/internal/harness"
	"github.com/maximhq/bifrost/cli/internal/runtime"
	"github.com/maximhq/bifrost/cli/internal/secrets"
)

type testSessionStore map[string]string

func (store testSessionStore) Get(profileID string, kind secrets.Kind) (string, error) {
	return store[profileID+":"+string(kind)], nil
}

func (store testSessionStore) Set(profileID string, kind secrets.Kind, value string) error {
	store[profileID+":"+string(kind)] = value
	return nil
}

func (store testSessionStore) Delete(profileID string, kind secrets.Kind) error {
	delete(store, profileID+":"+string(kind))
	return nil
}

type failingDeleteSessionStore struct {
	testSessionStore
}

func (store failingDeleteSessionStore) Delete(string, secrets.Kind) error {
	return errors.New("keyring delete failed")
}

type testRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip testRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestEnterpriseSSOAvailableRequiresGatewayCapability(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       bool
	}{
		{name: "enabled", statusCode: http.StatusOK, body: `{"idp_configured":true}`, want: true},
		{name: "disabled", statusCode: http.StatusOK, body: `{"idp_configured":false}`, want: false},
		{name: "endpoint unavailable", statusCode: http.StatusNotFound, body: `404 - File not found`, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			application := New(nil, io.Discard, io.Discard, Options{Version: "test"})
			application.httpClient = &http.Client{Transport: testRoundTripper(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path != "/api/agent/auth/status" {
					t.Fatalf("status request path = %q", request.URL.Path)
				}
				return &http.Response{
					StatusCode: test.statusCode,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(test.body)),
					Request:    request,
				}, nil
			})}

			if got := application.enterpriseSSOAvailable(context.Background(), "https://gateway.example"); got != test.want {
				t.Fatalf("enterpriseSSOAvailable() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestListModelsUsesEnterpriseAgentSession(t *testing.T) {
	store := testSessionStore{
		"test:agent-token":          "ck-bf-agent-test",
		"test:agent-virtual-key-id": "vk-assigned",
	}
	application := New(nil, io.Discard, io.Discard, Options{Version: "test"})
	application.statePath = t.TempDir() + "/state.json"
	application.sessionStore = store
	application.httpClient = &http.Client{Transport: testRoundTripper(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Authorization"); got != "Bearer ck-bf-agent-test" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := request.Header.Get("x-bf-agent-vk-id"); got != "vk-assigned" {
			t.Fatalf("selected virtual key header = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"z-model"},{"id":"a-model"}]}`)), Request: request,
		}, nil
	})}

	models, err := application.listModels(context.Background(), "test", "https://gateway.example", "https://gateway.example", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "a-model,z-model" {
		t.Fatalf("models = %#v", models)
	}
}

func TestRunNativeSessionUsesInteractivePassthrough(t *testing.T) {
	want := runtime.LaunchSpec{Harness: harness.Harness{ID: "opencode"}, BaseURL: "https://gateway.example"}
	out := &strings.Builder{}
	errOut := &strings.Builder{}

	launched, err := runNativeSession(context.Background(), out, errOut,
		func(_ context.Context, notify func(runtime.TabNoticeLevel, string), tabBarLine func() string, input io.Reader, seed *runtime.LaunchSpec) (*runtime.LaunchSpec, error) {
			if notify != nil || tabBarLine != nil || input != nil || seed != nil {
				t.Fatal("native chooser received tab-manager state")
			}
			return &want, nil
		},
		func(_ context.Context, gotOut, gotErrOut io.Writer, got runtime.LaunchSpec) error {
			if gotOut != out || gotErrOut != errOut {
				t.Fatal("interactive runner did not receive application writers")
			}
			if got.Harness.ID != want.Harness.ID || got.BaseURL != want.BaseURL {
				t.Fatalf("launch spec = %#v, want %#v", got, want)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if !launched {
		t.Fatal("expected native session to launch")
	}
}

func TestRunNativeSessionTreatsCancelledChooserAsQuit(t *testing.T) {
	launched, err := runNativeSession(context.Background(), io.Discard, io.Discard,
		func(context.Context, func(runtime.TabNoticeLevel, string), func() string, io.Reader, *runtime.LaunchSpec) (*runtime.LaunchSpec, error) {
			return nil, nil
		},
		func(context.Context, io.Writer, io.Writer, runtime.LaunchSpec) error {
			t.Fatal("interactive runner called after chooser quit")
			return nil
		})
	if launched {
		t.Fatal("chooser quit reported a launched session")
	}
	if !errors.Is(err, runtime.ErrQuit) {
		t.Fatalf("error = %v, want ErrQuit", err)
	}
}

// TestLaunchAgentTokenWithheldFromEditedBaseURL verifies the Enterprise SSO
// agent token is not carried into a LaunchSpec whose base URL was edited
// away from the profile's trusted origin in the chooser, since a coding
// agent launched against an edited URL would send that live bearer token to
// it for the entire session (CWE-522).
func TestLaunchAgentTokenWithheldFromEditedBaseURL(t *testing.T) {
	if got := launchAgentToken("ck-bf-agent-test", "https://gateway.example", "https://typo-gateway.example"); got != "" {
		t.Fatalf("launchAgentToken() = %q, want empty for an edited base URL", got)
	}
}

// TestLaunchAgentTokenKeptForTrustedBaseURL verifies the ordinary case,
// where the base URL was not edited, still carries the token.
func TestLaunchAgentTokenKeptForTrustedBaseURL(t *testing.T) {
	if got := launchAgentToken("ck-bf-agent-test", "https://gateway.example", "https://gateway.example"); got != "ck-bf-agent-test" {
		t.Fatalf("launchAgentToken() = %q, want the token preserved for the trusted base URL", got)
	}
}

// TestLaunchAgentTokenUsesFinalChoiceURL verifies a stale tab seed cannot
// suppress a valid token after the user restores the chooser to the active
// profile URL. The launch decision must use the final choice, not the seed.
func TestLaunchAgentTokenUsesFinalChoiceURL(t *testing.T) {
	const (
		token          = "ck-bf-agent-test"
		seededBaseURL  = "https://stale-tab.example"
		profileBaseURL = "https://gateway.example"
		choiceBaseURL  = "https://gateway.example"
	)

	if got := launchAgentToken(token, profileBaseURL, choiceBaseURL); got != token {
		t.Fatalf("launchAgentToken() = %q, want token for final trusted choice", got)
	}
	if got := launchAgentToken(token, seededBaseURL, choiceBaseURL); got != "" {
		t.Fatalf("stale seed unexpectedly authorized token %q", got)
	}
}

// TestListModelsDoesNotSendAgentTokenToUntrustedBaseURL verifies the live
// Enterprise SSO bearer token is only attached when the requested base URL
// matches the profile's configured (trusted) base URL, not an edited or
// typo'd one the user has not yet confirmed (CWE-522).
func TestListModelsDoesNotSendAgentTokenToUntrustedBaseURL(t *testing.T) {
	store := testSessionStore{
		"test:agent-token":          "ck-bf-agent-test",
		"test:agent-virtual-key-id": "vk-assigned",
	}
	application := New(nil, io.Discard, io.Discard, Options{Version: "test"})
	application.statePath = t.TempDir() + "/state.json"
	application.sessionStore = store
	application.httpClient = &http.Client{Transport: testRoundTripper(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q, want no agent bearer sent to an untrusted base URL", got)
		}
		if got := request.Header.Get("x-bf-agent-vk-id"); got != "" {
			t.Fatalf("selected virtual key header = %q, want empty", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Request: request,
		}, nil
	})}

	if _, err := application.listModels(context.Background(), "test", "https://gateway.example", "https://typo-gateway.example", "sk-bf-vk"); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateProfileBaseURLRevokesAndClearsEnterpriseSession(t *testing.T) {
	store := testSessionStore{
		"test:agent-token":          "ck-bf-agent-test",
		"test:agent-refresh-token":  "refresh-test",
		"test:agent-user":           `{"email":"developer@example.com"}`,
		"test:agent-virtual-key-id": "vk-assigned",
	}
	application := New(nil, io.Discard, io.Discard, Options{Version: "test"})
	application.statePath = t.TempDir() + "/state.json"
	application.sessionStore = store
	application.httpClient = &http.Client{Transport: testRoundTripper(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/agent/auth/logout":
			if request.URL.Host != "old-gateway.example" {
				t.Fatalf("logout destination = %s", request.URL)
			}
			if got := request.Header.Get("Authorization"); got != "Bearer ck-bf-agent-test" {
				t.Fatalf("logout authorization = %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{}`)), Request: request,
			}, nil
		case "/v1/models":
			if request.URL.Host != "new-gateway.example" {
				t.Fatalf("model destination = %s", request.URL)
			}
			if got := request.Header.Get("Authorization"); got != "" {
				t.Fatalf("restarted model authorization = %q, want empty", got)
			}
			if got := request.Header.Get("x-bf-agent-vk-id"); got != "" {
				t.Fatalf("restarted selected virtual key = %q, want empty", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Request: request,
			}, nil
		default:
			t.Fatalf("unexpected request to %s", request.URL)
			return nil, nil
		}
	})}
	profile := &config.Profile{ID: "test", BaseURL: "https://old-gateway.example"}

	if err := application.updateProfileBaseURL(context.Background(), profile, "https://new-gateway.example"); err != nil {
		t.Fatal(err)
	}
	if profile.BaseURL != "https://new-gateway.example" {
		t.Fatalf("base URL = %q", profile.BaseURL)
	}
	for _, kind := range []secrets.Kind{
		secrets.AgentToken, secrets.AgentRefreshToken, secrets.AgentUser, secrets.AgentVirtualKeyID,
	} {
		if got := store["test:"+string(kind)]; got != "" {
			t.Fatalf("%s was retained after origin change", kind)
		}
	}

	state := &config.State{
		Profiles: []config.Profile{*profile}, LastProfileID: profile.ID,
		Selections: map[string]config.Selection{},
	}
	if err := config.SaveState(application.statePath, state); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.LoadState(application.statePath)
	if err != nil {
		t.Fatal(err)
	}
	reloadedProfile := reloaded.ProfileByID("test")
	if reloadedProfile == nil || reloadedProfile.BaseURL != "https://new-gateway.example" {
		t.Fatalf("reloaded profile = %#v", reloadedProfile)
	}
	if _, err := application.listModels(
		context.Background(), reloadedProfile.ID, reloadedProfile.BaseURL, reloadedProfile.BaseURL, "",
	); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateProfileBaseURLDoesNotTrustNewOriginWhenSessionClearFails(t *testing.T) {
	store := failingDeleteSessionStore{testSessionStore: testSessionStore{
		"test:agent-token": "ck-bf-agent-test",
	}}
	application := New(nil, io.Discard, io.Discard, Options{Version: "test"})
	application.sessionStore = store
	application.httpClient = &http.Client{Transport: testRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{}`)), Request: request,
		}, nil
	})}
	profile := &config.Profile{ID: "test", BaseURL: "https://old-gateway.example"}

	err := application.updateProfileBaseURL(context.Background(), profile, "https://new-gateway.example")
	if err == nil || !strings.Contains(err.Error(), "keyring delete failed") {
		t.Fatalf("update error = %v", err)
	}
	if profile.BaseURL != "https://old-gateway.example" {
		t.Fatalf("base URL changed despite retained session: %q", profile.BaseURL)
	}
}
