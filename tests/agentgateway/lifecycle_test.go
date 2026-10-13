// Package agentgateway holds end-to-end tests for the Agent Gateway. They run
// against a deployed Bifrost whose config sets
// server.a2a_allow_private_push_callbacks to true, and skip when
// AGENTGATEWAY_URL is unset.
package agentgateway

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/maximhq/bifrost/tests/agentgateway/driver"
	"github.com/maximhq/bifrost/tests/agentgateway/fixture"
)

// envOr returns an environment value or the fallback.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// TestLifecycle runs the complete Agent Gateway lifecycle and verifies the
// resulting Agent logs.
//
// Environment:
//
//	AGENTGATEWAY_URL         gateway base URL (required)
//	AGENTGATEWAY_ADMIN_TOKEN session token, or AGENTGATEWAY_ADMIN_USER/_PASS to log in
//	AGENTGATEWAY_VK          optional virtual key
//	AGENTGATEWAY_HOST        host the gateway uses to reach this process (default 127.0.0.1)
//	AGENTGATEWAY_BIND        local bind address for fixture and callback (default 127.0.0.1)
func TestLifecycle(t *testing.T) {
	gwURL := os.Getenv("AGENTGATEWAY_URL")
	if gwURL == "" {
		t.Skip("AGENTGATEWAY_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	token := os.Getenv("AGENTGATEWAY_ADMIN_TOKEN")
	if user := os.Getenv("AGENTGATEWAY_ADMIN_USER"); token == "" && user != "" {
		var err error
		if token, err = driver.Login(ctx, gwURL, user, os.Getenv("AGENTGATEWAY_ADMIN_PASS")); err != nil {
			t.Fatal(err)
		}
	}
	gw := driver.NewGateway(gwURL, token)
	bind, host := envOr("AGENTGATEWAY_BIND", "127.0.0.1")+":0", envOr("AGENTGATEWAY_HOST", "127.0.0.1")

	fx, err := fixture.Start(bind, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fx.Close() })
	sink, err := driver.StartSink(bind, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() })

	agent := fmt.Sprintf("lifecycle-fixture-%d", time.Now().UnixNano())
	if err := gw.RegisterAgent(ctx, agent, fx.CardURL, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := gw.DeleteAgent(c, agent); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})

	vk := os.Getenv("AGENTGATEWAY_VK")
	res, err := (&driver.Lifecycle{Gateway: gw, AgentName: agent, VirtualKey: vk, Sink: sink, Release: fx.ReleaseCheckpoint}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.AssertLogs(ctx, gw, agent, res, true, driver.Tags{}); err != nil {
		t.Fatal(err)
	}
}
