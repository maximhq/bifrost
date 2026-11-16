package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// TestVectorStoreHealth_LiveManual is a throwaway, manually-invoked verification against the
// real bifrost-pgvector-test docker container (see framework/vectorstore/pgvector_test.go for
// how it is created). It is gated behind an env var and is NOT part of the normal test suite.
//
// Run with:
//
//	BIFROST_LIVE_PGVECTOR_HEALTH_TEST=1 go test ./transports/bifrost-http/handlers/ -run TestVectorStoreHealth_LiveManual -v -count=1
func TestVectorStoreHealth_LiveManual(t *testing.T) {
	if os.Getenv("BIFROST_LIVE_PGVECTOR_HEALTH_TEST") == "" {
		t.Skip("set BIFROST_LIVE_PGVECTOR_HEALTH_TEST=1 to run this against the live bifrost-pgvector-test container")
	}

	logger := bifrost.NewNoOpLogger()
	cfg := &vectorstore.PgVectorConfig{
		Host:     *schemas.NewSecretVar("127.0.0.1"),
		Port:     *schemas.NewSecretVar("5433"),
		User:     *schemas.NewSecretVar("bifrost"),
		Password: *schemas.NewSecretVar("bifrost_password"),
		Database: *schemas.NewSecretVar("bifrost"),
		SSLMode:  *schemas.NewSecretVar("disable"),
	}

	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true,
		Type:    vectorstore.VectorStoreTypePgVector,
		Config:  *cfg,
	}, logger)
	if err != nil {
		t.Fatalf("failed to connect to the live pgvector container on 127.0.0.1:5433 - is `docker ps` showing bifrost-pgvector-test running? err: %v", err)
	}
	defer store.Close(context.Background(), "")

	h := NewHealthHandler(&lib.Config{
		ClientConfig: &configstore.ClientConfig{},
		VectorStore:  store,
	})

	// --- Healthy case: container is up ---
	ctx := &fasthttp.RequestCtx{}
	h.getVectorStoreHealth(ctx)
	fmt.Printf("HEALTHY CASE  status=%d body=%s\n", ctx.Response.StatusCode(), ctx.Response.Body())
	var healthyResp VectorStoreHealthResponse
	if err := json.Unmarshal(ctx.Response.Body(), &healthyResp); err != nil {
		t.Fatalf("failed to decode healthy response: %v", err)
	}
	if healthyResp.Status != VectorStoreHealthStatusHealthy {
		t.Fatalf("expected healthy, got %q: %s", healthyResp.Status, ctx.Response.Body())
	}
	if healthyResp.Pool == nil {
		t.Fatalf("expected real pool stats from pgxpool, got none")
	}

	// --- Unhealthy case: stop the real container, hit the real pool, then restart it ---
	if err := exec.Command("docker", "stop", "bifrost-pgvector-test").Run(); err != nil {
		t.Fatalf("failed to stop bifrost-pgvector-test for the unhealthy-path check: %v", err)
	}
	defer func() {
		if err := exec.Command("docker", "start", "bifrost-pgvector-test").Run(); err != nil {
			t.Fatalf("failed to restart bifrost-pgvector-test after the unhealthy-path check - restart it manually: %v", err)
		}
		// Give Postgres a moment to accept connections again before any later test uses it.
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if pingErr := store.Ping(context.Background()); pingErr == nil {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()

	unhealthyCtx := &fasthttp.RequestCtx{}
	h.getVectorStoreHealth(unhealthyCtx)
	fmt.Printf("UNHEALTHY CASE status=%d body=%s\n", unhealthyCtx.Response.StatusCode(), unhealthyCtx.Response.Body())
	if unhealthyCtx.Response.StatusCode() != fasthttp.StatusServiceUnavailable {
		t.Fatalf("expected 503 with the container stopped, got %d: %s", unhealthyCtx.Response.StatusCode(), unhealthyCtx.Response.Body())
	}
	var unhealthyResp VectorStoreHealthResponse
	if err := json.Unmarshal(unhealthyCtx.Response.Body(), &unhealthyResp); err != nil {
		t.Fatalf("failed to decode unhealthy response: %v", err)
	}
	if unhealthyResp.Status != VectorStoreHealthStatusUnhealthy {
		t.Fatalf("expected unhealthy with the container stopped, got %q", unhealthyResp.Status)
	}
}
