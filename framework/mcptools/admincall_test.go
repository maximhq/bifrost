package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// adminCall is one request a tool sent through the dispatcher.
type adminCall struct {
	method string
	path   string
	body   map[string]any
}

// fakeAdmin stands in for the admin routes: it records what a tool sent and
// answers with what the test queued. What the routes do with a request is the
// handlers' own tests; here the subject is the request a tool builds.
type fakeAdmin struct {
	calls []adminCall
	// respond answers a call. Nil answers 200 with an empty object.
	respond func(call adminCall) (int, string)
	err     error
}

func (f *fakeAdmin) Do(_ context.Context, method, path string, body []byte) (int, []byte, error) {
	call := adminCall{method: method, path: path}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &call.body); err != nil {
			return 0, nil, err
		}
	}
	f.calls = append(f.calls, call)
	if f.err != nil {
		return 0, nil, f.err
	}
	if f.respond == nil {
		return 200, []byte(`{}`), nil
	}
	status, response := f.respond(call)
	return status, []byte(response), nil
}

// only returns the single call a tool made.
func (f *fakeAdmin) only(t *testing.T) adminCall {
	t.Helper()
	require.Len(t, f.calls, 1, "the tool should make exactly one admin call")
	return f.calls[0]
}

func TestAdminPath(t *testing.T) {
	path, err := adminPath("/api/providers/{provider}/keys/{key_id}", "openai", "k-1")
	require.NoError(t, err)
	require.Equal(t, "/api/providers/openai/keys/k-1", path)

	// An id is one segment whatever it holds.
	path, err = adminPath("/api/governance/virtual-keys/{vk_id}", "a/../b c")
	require.NoError(t, err)
	require.Equal(t, "/api/governance/virtual-keys/a%2F..%2Fb%20c", path)

	for _, id := range []string{"", ".", ".."} {
		_, err = adminPath("/api/governance/virtual-keys/{vk_id}", id)
		require.Error(t, err, "id %q", id)
	}
	_, err = adminPath("/api/providers/{provider}/keys/{key_id}", "openai")
	require.Error(t, err, "too few ids")
	_, err = adminPath("/api/providers", "openai")
	require.Error(t, err, "too many ids")
}

// Absent, null and empty are three different instructions to an update route,
// and the body must carry whichever one the caller gave.
func TestAdminBodyKeepsAbsentNullAndEmptyApart(t *testing.T) {
	args := map[string]any{"name": "", "description": nil, "limit": float64(5), "ignored": "x"}
	body := adminBody{}.copyPresent(args, "name", "description", "limit", "missing").rename(args, "limit", "max_limit")

	require.Equal(t, adminBody{"name": "", "description": nil, "limit": float64(5), "max_limit": float64(5)}, body)
	_, carried := body["missing"]
	require.False(t, carried, "an argument the caller did not send must stay out of the body")
	_, carried = body["ignored"]
	require.False(t, carried, "an argument the tool did not name must stay out of the body")
}

func TestCallAdmin(t *testing.T) {
	t.Run("no dispatcher", func(t *testing.T) {
		_, err := callAdmin(context.Background(), &Deps{}, "PUT", "/api/warp/config", adminBody{})
		require.ErrorIs(t, err, errAdminUnavailable)
	})

	t.Run("sends the body and returns the response", func(t *testing.T) {
		admin := &fakeAdmin{respond: func(adminCall) (int, string) { return 200, `{"id":"x"}` }}
		response, err := callAdmin(context.Background(), &Deps{Admin: admin}, "PUT", "/api/feature-flags/x", adminBody{"enabled": true})
		require.NoError(t, err)
		require.JSONEq(t, `{"id":"x"}`, string(response))
		require.Equal(t, adminCall{method: "PUT", path: "/api/feature-flags/x", body: map[string]any{"enabled": true}}, admin.only(t))
	})

	t.Run("a refusal carries the route's message and status", func(t *testing.T) {
		admin := &fakeAdmin{respond: func(adminCall) (int, string) {
			return 409, `{"is_bifrost_error":false,"status_code":409,"error":{"message":"Feature flag is locked by config.json / Helm"}}`
		}}
		_, err := callAdmin(context.Background(), &Deps{Admin: admin}, "PUT", "/api/feature-flags/x", adminBody{"enabled": true})
		require.EqualError(t, err, "Feature flag is locked by config.json / Helm")
		require.Equal(t, 409, adminStatus(err))
	})

	t.Run("a refusal with no readable body is reported by status", func(t *testing.T) {
		admin := &fakeAdmin{respond: func(adminCall) (int, string) { return 404, `Not Found` }}
		_, err := callAdmin(context.Background(), &Deps{Admin: admin}, "PUT", "/api/feature-flags/x", adminBody{"enabled": true})
		require.ErrorContains(t, err, "status 404")
	})

	t.Run("a dispatch failure is returned as is", func(t *testing.T) {
		boom := errors.New("not an admin route")
		_, err := callAdmin(context.Background(), &Deps{Admin: &fakeAdmin{err: boom}}, "PUT", "/api/feature-flags/x", nil)
		require.ErrorIs(t, err, boom)
		require.Zero(t, adminStatus(err))
	})
}
