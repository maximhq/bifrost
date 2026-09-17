package sessionauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/cli/internal/browserauth"
	"github.com/maximhq/bifrost/cli/internal/secrets"
)

type memoryStore map[string]string

func (store memoryStore) Get(profileID string, kind secrets.Kind) (string, error) {
	return store[profileID+":"+string(kind)], nil
}

func (store memoryStore) Set(profileID string, kind secrets.Kind, value string) error {
	store[profileID+":"+string(kind)] = value
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestRefreshRotatesPairUnderProfileLock(t *testing.T) {
	store := memoryStore{
		"test:agent-token":         "ck-bf-agent-old",
		"test:agent-refresh-token": "refresh-old",
	}
	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"agent_access_token":"ck-bf-agent-new","refresh_token":"refresh-new"}`,
			)), Request: request,
		}, nil
	})}
	refresher := Refresher{
		Store: store, ProfileID: "test", StatePath: t.TempDir() + "/state.json",
		Client: &browserauth.Client{BaseURL: "https://gateway.example", HTTPClient: httpClient},
	}
	token, err := refresher.Refresh(context.Background(), "ck-bf-agent-old")
	if err != nil {
		t.Fatal(err)
	}
	if token != "ck-bf-agent-new" || requests != 1 {
		t.Fatalf("token=%q requests=%d", token, requests)
	}
	if store["test:agent-token"] != "ck-bf-agent-new" || store["test:agent-refresh-token"] != "refresh-new" {
		t.Fatalf("rotated store = %#v", store)
	}
}

func TestRefreshReusesPairRotatedByAnotherProcess(t *testing.T) {
	store := memoryStore{
		"test:agent-token":         "ck-bf-agent-new",
		"test:agent-refresh-token": "refresh-new",
	}
	refresher := Refresher{
		Store: store, ProfileID: "test", StatePath: t.TempDir() + "/state.json",
		Client: &browserauth.Client{BaseURL: "https://gateway.example", HTTPClient: &http.Client{
			Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				t.Fatalf("unexpected refresh request to %s", request.URL)
				return nil, nil
			}),
		}},
	}
	token, err := refresher.Refresh(context.Background(), "ck-bf-agent-old")
	if err != nil {
		t.Fatal(err)
	}
	if token != "ck-bf-agent-new" {
		t.Fatalf("token = %q", token)
	}
}
