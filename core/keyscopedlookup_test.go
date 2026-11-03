package bifrost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// keyScopedUpstream serves stored responses only to the key that owns them, as OpenAI does across projects,
// and records which bearer each call carried.
type keyScopedUpstream struct {
	mu    sync.Mutex
	owner string
	seen  []string
}

func (u *keyScopedUpstream) handler(w http.ResponseWriter, r *http.Request) {
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	u.mu.Lock()
	u.seen = append(u.seen, auth)
	owner := u.owner
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if auth != owner {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"Response with id 'resp_1' not found.","type":"invalid_request_error","param":null,"code":null}}`))
		return
	}
	status := "completed"
	if strings.HasSuffix(r.URL.Path, "/cancel") {
		status = "cancelled"
	}
	_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":1,"status":"` + status + `","model":"gpt-4o","output":[]}`))
}

func (u *keyScopedUpstream) calls() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.seen...)
}

func newKeyScopedClient(t *testing.T, owner string) (*Bifrost, *keyScopedUpstream) {
	t.Helper()
	upstream := &keyScopedUpstream{owner: owner}
	server := httptest.NewServer(http.HandlerFunc(upstream.handler))
	t.Cleanup(server.Close)

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 10, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.AllowPrivateNetwork = true
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "k1", Name: "one", Value: *schemas.NewSecretVar("sk-one"), Models: schemas.WhiteList{"gpt-4o"}, Weight: 1},
		{ID: "k2", Name: "two", Value: *schemas.NewSecretVar("sk-two"), Models: schemas.WhiteList{"gpt-4o-mini"}, Weight: 1},
		{ID: "k3", Name: "three", Value: *schemas.NewSecretVar("sk-three"), Models: schemas.WhiteList{"*"}, Weight: 1},
	})
	return newStreamTestClient(t, account), upstream
}

// A stored response is found on whichever key created it: the follow-up asks each key in turn and the
// one that owns it serves, whatever the key weights would have picked.
func TestResponsesLifecycle_FindsTheOwningKey(t *testing.T) {
	client, upstream := newKeyScopedClient(t, "sk-three")

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := client.ResponsesRetrieveRequest(ctx, &schemas.BifrostResponsesRetrieveRequest{Provider: schemas.OpenAI, ResponseID: "resp_1"})
	if err != nil {
		t.Fatalf("retrieve failed: %+v", err.Error)
	}
	if resp.ID == nil || *resp.ID != "resp_1" {
		t.Fatalf("retrieve returned %+v", resp)
	}
	if got, _ := ctx.Value(schemas.BifrostContextKeySelectedKeyID).(string); got != "k3" {
		t.Fatalf("selected key = %q, want k3", got)
	}
	if got := upstream.calls(); strings.Join(got, ",") != "sk-one,sk-two,sk-three" {
		t.Fatalf("upstream calls = %v, want each key once in id order", got)
	}

	ctx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	cancelled, err := client.ResponsesCancelRequest(ctx, &schemas.BifrostResponsesCancelRequest{Provider: schemas.OpenAI, ResponseID: "resp_1"})
	if err != nil {
		t.Fatalf("cancel failed: %+v", err.Error)
	}
	if cancelled.Status == nil || *cancelled.Status != "cancelled" {
		t.Fatalf("cancel returned %+v", cancelled)
	}
}

// A response no key owns comes back as the provider's 404 after one call per key, not one per retry.
func TestResponsesLifecycle_NotFoundOnEveryKey(t *testing.T) {
	client, upstream := newKeyScopedClient(t, "sk-nobody")

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, err := client.ResponsesRetrieveRequest(ctx, &schemas.BifrostResponsesRetrieveRequest{Provider: schemas.OpenAI, ResponseID: "resp_1"})
	if err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusNotFound {
		t.Fatalf("error = %+v, want 404", err)
	}
	if got := upstream.calls(); len(got) != 3 {
		t.Fatalf("upstream calls = %v, want one per key", got)
	}
}

// A caller that pins a key is answered by that key alone, as single-key selection does.
func TestResponsesLifecycle_PinnedKeyIsTheOnlyOneAsked(t *testing.T) {
	client, upstream := newKeyScopedClient(t, "sk-three")

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyAPIKeyName, "one")
	_, err := client.ResponsesRetrieveRequest(ctx, &schemas.BifrostResponsesRetrieveRequest{Provider: schemas.OpenAI, ResponseID: "resp_1"})
	if err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusNotFound {
		t.Fatalf("error = %+v, want 404 from the pinned key", err)
	}
	if got := upstream.calls(); strings.Join(got, ",") != "sk-one" {
		t.Fatalf("upstream calls = %v, want only the pinned key", got)
	}
}

// videoUpstream owns each video on one key and pages each key's listing one video at a time.
type videoUpstream struct {
	mu     sync.Mutex
	owners map[string][]string // bearer -> video ids it owns, in listing order
	seen   []string
}

func (u *videoUpstream) handler(w http.ResponseWriter, r *http.Request) {
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	u.mu.Lock()
	u.seen = append(u.seen, auth+" "+r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	owned := u.owners[auth]
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	notFound := func() {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"Video not found","type":"invalid_request_error"}}`))
	}
	if r.URL.Path == "/v1/videos" {
		start := 0
		if after := r.URL.Query().Get("after"); after != "" {
			start = -1
			for i, id := range owned {
				if id == after {
					start = i + 1
				}
			}
			if start < 0 {
				notFound()
				return
			}
		}
		data, last, more := "", "", false
		if start < len(owned) {
			data = `{"id":"` + owned[start] + `","object":"video","status":"completed","model":"sora-2","created_at":1}`
			last = owned[start]
			more = start+1 < len(owned)
		}
		body := `{"object":"list","data":[` + data + `],"has_more":` + map[bool]string{true: "true", false: "false"}[more]
		if last != "" {
			body += `,"last_id":"` + last + `"`
		}
		_, _ = w.Write([]byte(body + `}`))
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/videos/")
	for _, owned := range owned {
		if owned == id {
			if r.Method == http.MethodDelete {
				_, _ = w.Write([]byte(`{"id":"` + id + `","object":"video.deleted","deleted":true}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"` + id + `","object":"video","status":"completed","model":"sora-2","created_at":1}`))
			return
		}
	}
	notFound()
}

func (u *videoUpstream) calls() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.seen...)
}

func newVideoClient(t *testing.T, owners map[string][]string) (*Bifrost, *videoUpstream) {
	t.Helper()
	upstream := &videoUpstream{owners: owners}
	server := httptest.NewServer(http.HandlerFunc(upstream.handler))
	t.Cleanup(server.Close)

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 10, server.URL)
	account.configs[schemas.OpenAI].NetworkConfig.AllowPrivateNetwork = true
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "k1", Name: "one", Value: *schemas.NewSecretVar("sk-one"), Models: schemas.WhiteList{"*"}, Weight: 1},
		{ID: "k2", Name: "two", Value: *schemas.NewSecretVar("sk-two"), Models: schemas.WhiteList{"*"}, Weight: 1},
		{ID: "k3", Name: "three", Value: *schemas.NewSecretVar("sk-three"), Models: schemas.WhiteList{"*"}, Weight: 1},
	})
	return newStreamTestClient(t, account), upstream
}

// A video is polled and deleted through whichever key created it.
func TestVideo_RetrieveAndDeleteFindTheOwningKey(t *testing.T) {
	client, _ := newVideoClient(t, map[string][]string{"sk-two": {"video_b"}})

	for i := 0; i < 3; i++ {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, err := client.VideoRetrieveRequest(ctx, &schemas.BifrostVideoRetrieveRequest{Provider: schemas.OpenAI, ID: "video_b"})
		if err != nil {
			t.Fatalf("retrieve %d failed: %+v", i, err.Error)
		}
		if !strings.HasPrefix(resp.ID, "video_b") {
			t.Fatalf("retrieve returned %q", resp.ID)
		}
		if got, _ := ctx.Value(schemas.BifrostContextKeySelectedKeyID).(string); got != "k2" {
			t.Fatalf("selected key = %q, want k2", got)
		}
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, err := client.VideoDeleteRequest(ctx, &schemas.BifrostVideoDeleteRequest{Provider: schemas.OpenAI, ID: "video_b"}); err != nil {
		t.Fatalf("delete failed: %+v", err.Error)
	}
}

// Video listing walks every key's videos once, page by page, ending with has_more false.
func TestVideo_ListPagesAcrossKeys(t *testing.T) {
	client, _ := newVideoClient(t, map[string][]string{"sk-one": {"v1", "v2"}, "sk-three": {"v3"}})

	var ids []string
	var after *string
	for page := 0; page < 10; page++ {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, err := client.VideoListRequest(ctx, &schemas.BifrostVideoListRequest{Provider: schemas.OpenAI, After: after})
		if err != nil {
			t.Fatalf("page %d failed: %+v", page, err.Error)
		}
		for _, v := range resp.Data {
			ids = append(ids, strings.SplitN(v.ID, ":", 2)[0])
		}
		if resp.HasMore == nil || !*resp.HasMore {
			if resp.After != nil {
				t.Fatalf("last page carried a cursor %q", *resp.After)
			}
			break
		}
		if resp.After == nil {
			t.Fatalf("page %d has more but no cursor", page)
		}
		after = resp.After
	}
	if strings.Join(ids, ",") != "v1,v2,v3" {
		t.Fatalf("listed %v, want v1,v2,v3", ids)
	}
}

// A provider-native cursor (the last video id, as SDKs send it) resumes on the key that knows that video.
func TestVideo_ListResumesFromNativeCursor(t *testing.T) {
	client, _ := newVideoClient(t, map[string][]string{"sk-one": {"v1"}, "sk-two": {"v2", "v3"}})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := client.VideoListRequest(ctx, &schemas.BifrostVideoListRequest{Provider: schemas.OpenAI, After: schemas.Ptr("v2")})
	if err != nil {
		t.Fatalf("list failed: %+v", err.Error)
	}
	if len(resp.Data) != 1 || !strings.HasPrefix(resp.Data[0].ID, "v3") {
		t.Fatalf("list returned %+v, want v3", resp.Data)
	}
	if resp.HasMore == nil || !*resp.HasMore || resp.After == nil {
		t.Fatalf("expected the listing to continue to the next key, got has_more=%v after=%v", resp.HasMore, resp.After)
	}
	decoded, decodeErr := schemas.DecodeSerialCursor(*resp.After)
	if decodeErr != nil || decoded.KeyIndex != 2 {
		t.Fatalf("cursor = %+v (%v), want the third key", decoded, decodeErr)
	}
}

// Only a video edit that names an existing video is walked across keys; one that uploads content keeps normal key selection.
func TestIsKeyScopedLookupRequest_VideoEdit(t *testing.T) {
	byID := &schemas.BifrostRequest{RequestType: schemas.VideoEditRequest, VideoEditRequest: &schemas.BifrostVideoEditRequest{Input: &schemas.VideoEditInput{Video: schemas.VideoInput{ID: "video_1"}}}}
	byBytes := &schemas.BifrostRequest{RequestType: schemas.VideoEditRequest, VideoEditRequest: &schemas.BifrostVideoEditRequest{Input: &schemas.VideoEditInput{Video: schemas.VideoInput{Video: []byte("x")}}}}
	if !isKeyScopedLookupRequest(byID) {
		t.Fatal("edit by id should be walked across keys")
	}
	if isKeyScopedLookupRequest(byBytes) {
		t.Fatal("edit with uploaded content should keep normal key selection")
	}
}
