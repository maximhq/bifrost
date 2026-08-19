package handlers

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

func TestSkillsServingGenericFileDownloadDecodesEncodedPathParams(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	blobID := "encoded-file-blob"
	content := []byte("encoded file content")

	if err := store.CreateSkillFileBlob(ctx, &tables.TableSkillFileBlob{ID: blobID, Data: content}); err != nil {
		t.Fatalf("create blob: %v", err)
	}
	if err := store.CreateSkill(ctx, &tables.TableSkill{
		Name:        "encoded-file-skill",
		Description: "skill with encoded file paths",
		SkillMDBody: "body",
		Files: []tables.TableSkillFile{{
			Path:          "nested dir/file with spaces.txt",
			SourceType:    tables.SkillSourceTypeText,
			BlobID:        &blobID,
			MimeType:      "text/plain",
			FileSizeBytes: int64(len(content)),
		}},
	}, "1.0.0", nil); err != nil {
		t.Fatalf("create skill: %v", err)
	}

	handler := NewSkillsServingHandler(store, nil)
	r := router.New()
	handler.RegisterRoutes(r)

	server := &fasthttp.Server{Handler: r.Handler}
	ln := fasthttputil.NewInmemoryListener()
	go server.Serve(ln) //nolint:errcheck
	defer ln.Close()
	defer server.Shutdown()

	client := &fasthttp.Client{
		Dial: func(addr string) (net.Conn, error) {
			return ln.Dial()
		},
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(fasthttp.MethodGet)
	req.SetRequestURI("http://test.local/api/skills/serve/encoded-file-skill/files/nested%20dir/file%20with%20spaces.txt")

	if err := client.Do(req, resp); err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("status got %d, want %d; body=%s", resp.StatusCode(), fasthttp.StatusOK, string(resp.Body()))
	}
	if got := string(resp.Body()); got != string(content) {
		t.Fatalf("body got %q, want %q", got, string(content))
	}
}
