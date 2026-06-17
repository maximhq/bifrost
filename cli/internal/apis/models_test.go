package apis

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestListVirtualKeysParsesAssignedKeys(t *testing.T) {
	var gotAuth string
	var gotSession string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/virtual-keys" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotSession = r.Header.Get("x-bf-cli-session-id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"virtual_keys":[{"id":"vk_2","name":"Support","key":"sk-support"},{"id":"vk_1","name":"Engineering","value":"sk-eng"}]}`))
	}))
	defer server.Close()

	client := NewClient()
	keys, err := client.ListVirtualKeys(context.Background(), server.URL, "session-123")
	if err != nil {
		t.Fatalf("ListVirtualKeys returned error: %v", err)
	}

	if gotAuth != "Bearer session-123" || gotSession != "session-123" {
		t.Fatalf("unexpected auth headers Authorization=%q x-bf-cli-session-id=%q", gotAuth, gotSession)
	}
	if len(keys) != 2 {
		t.Fatalf("expected two keys, got %d", len(keys))
	}
	if keys[0].Name != "Engineering" || keys[0].Value != "sk-eng" {
		t.Fatalf("expected sorted Engineering key first, got %#v", keys[0])
	}
	if keys[1].Name != "Support" || keys[1].Value != "sk-support" {
		t.Fatalf("expected key fallback to be used, got %#v", keys[1])
	}
}

func TestSignInWithBifrostHandoverCallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/virtual-keys" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("x-bf-cli-session-id"); got != "session-abc" {
			t.Fatalf("expected callback session to be used, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"virtual_keys":[{"id":"vk_1","name":"Engineering","value":"sk-eng"}]}`))
	}))
	defer server.Close()

	client := NewClient()
	client.openURL = func(rawURL string) error {
		u, err := url.Parse(rawURL)
		if err != nil {
			return err
		}
		if u.Path != "/cli/handover" {
			t.Fatalf("expected handover URL, got %q", u.Path)
		}
		callback := u.Query().Get("redirect_uri")
		state := u.Query().Get("state")
		if callback == "" || state == "" {
			t.Fatalf("expected redirect_uri and state in handover URL: %q", rawURL)
		}
		go func() {
			resp, err := http.Get(callback + "?session_id=session-abc&state=" + url.QueryEscape(state))
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}

	keys, err := client.SignInWithBifrost(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("SignInWithBifrost returned error: %v", err)
	}
	if len(keys) != 1 || keys[0].Value != "sk-eng" {
		t.Fatalf("unexpected keys: %#v", keys)
	}
}
