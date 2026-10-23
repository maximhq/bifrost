// Package agentgateway holds end-to-end tests for the Agent Gateway. They run
// against a deployed Bifrost whose config sets
// server.a2a_allow_private_push_callbacks to true, and skip when
// AGENTGATEWAY_URL is unset.
package agentgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	a2agrpc "github.com/a2aproject/a2a-go/v2/a2agrpc/v1"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/maximhq/bifrost/tests/agentgateway/driver"
	"github.com/maximhq/bifrost/tests/agentgateway/fixture"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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

// TestPushAuthorizationThroughGateway exercises real gateway authentication,
// grants, storage, forwarding, and all advertised bindings. No model is called.
// AGENTGATEWAY_NEWMAN_BIN and AGENTGATEWAY_HARNESS_COLLECTION optionally run the
// filtered Postman cases while the authenticated upstream and task remain live.
func TestPushAuthorizationThroughGateway(t *testing.T) {
	base := os.Getenv("AGENTGATEWAY_URL")
	if base == "" {
		t.Skip("AGENTGATEWAY_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	token := os.Getenv("AGENTGATEWAY_ADMIN_TOKEN")
	if user := os.Getenv("AGENTGATEWAY_ADMIN_USER"); token == "" && user != "" {
		var err error
		token, err = driver.Login(ctx, base, user, os.Getenv("AGENTGATEWAY_ADMIN_PASS"))
		if err != nil {
			t.Fatal(err)
		}
	}
	admin := driver.NewGateway(base, token)
	nonce := time.Now().UnixNano()
	ownerID, ownerKey := createPushTestKey(t, ctx, admin, fmt.Sprintf("push-owner-%d", nonce))
	readerID, readerKey := createPushTestKey(t, ctx, admin, fmt.Sprintf("push-reader-%d", nonce))
	fx, err := fixture.StartWithCredentials(envOr("AGENTGATEWAY_BIND", "127.0.0.1")+":0", envOr("AGENTGATEWAY_HOST", "127.0.0.1"), map[string]string{ownerKey: "owner", readerKey: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fx.Close() })
	name := fmt.Sprintf("push-authz-%d", nonce)
	status, body, err := admin.Do(ctx, http.MethodPost, "/api/agents", map[string]any{
		"name": name, "agent_card_url": fx.CardURL, "enabled": true,
		"allow_by_default": false, "virtual_key_ids": []string{ownerID, readerID},
		"forward_accepted_credential": true, "runtime_auth": map[string]any{"type": "none", "advanced": true},
	}, nil)
	if err != nil || status != http.StatusCreated {
		t.Fatalf("register authenticated agent: status=%d error=%v body=%s", status, err, body)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := admin.DeleteAgent(cleanupCtx, name); err != nil {
			t.Errorf("delete test agent: %v", err)
		}
	})
	card, err := admin.WithoutCredentials().WaitForCard(ctx, name, readerKey, "")
	if err != nil {
		t.Fatalf("reader's agent grant was not admitted: %v", err)
	}
	owner := a2aclient.AttachServiceParams(ctx, a2aclient.ServiceParams{"x-bf-vk": {ownerKey}})
	reader := a2aclient.AttachServiceParams(ctx, a2aclient.ServiceParams{"x-bf-vk": {readerKey}})
	clients := map[a2a.TransportProtocol]*a2aclient.Client{}
	gatewayURL, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range card.SupportedInterfaces {
		endpoint := *iface
		options := []a2aclient.FactoryOption{a2aclient.WithJSONRPCTransport(admin.HTTP), a2aclient.WithRESTTransport(admin.HTTP)}
		if iface.ProtocolBinding == a2a.TransportProtocolGRPC {
			// Dial the local gateway without DNS while preserving its agent-routing authority.
			endpoint.URL = "passthrough:///" + iface.URL
			options = append(options, a2agrpc.WithGRPCTransport(grpc.WithAuthority(iface.URL), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(callCtx context.Context, address string) (net.Conn, error) {
				_, port, err := net.SplitHostPort(address)
				if err != nil {
					return nil, err
				}
				return (&net.Dialer{}).DialContext(callCtx, "tcp", net.JoinHostPort(gatewayURL.Hostname(), port))
			})))
		}
		client, err := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{&endpoint}, options...)
		if err != nil {
			t.Fatal(err)
		}
		clients[iface.ProtocolBinding] = client
		t.Cleanup(func() { _ = client.Destroy() })
	}
	primary := clients[a2a.TransportProtocolJSONRPC]
	if primary == nil {
		t.Fatal("gateway did not advertise JSON-RPC")
	}
	result, err := primary.SendMessage(owner, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("start lifecycle"))})
	if err != nil {
		t.Fatal(err)
	}
	task, ok := result.(*a2a.Task)
	if !ok {
		t.Fatalf("owner send returned %T, want task", result)
	}
	if _, err := primary.SendMessage(reader, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("start lifecycle"))}); err != nil {
		t.Fatalf("reader cannot invoke the same agent: %v", err)
	}
	for _, id := range []string{"private", "shared"} {
		_, err := primary.CreateTaskPushConfig(owner, &a2a.PushConfig{TaskID: task.ID, ID: id, Tenant: "callback-tenant", URL: "https://callback.example/events?secret=" + id, Token: "dummy-token", Auth: &a2a.PushAuthInfo{Scheme: "Bearer", Credentials: "dummy-credential"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	direct, err := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{a2a.NewAgentInterface(strings.TrimSuffix(fx.CardURL, a2asrv.WellKnownAgentCardPath)+"/jsonrpc", a2a.TransportProtocolJSONRPC)})
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Destroy()
	get := &a2a.GetTaskPushConfigRequest{TaskID: task.ID, ID: "private"}
	list := &a2a.ListTaskPushConfigRequest{TaskID: task.ID}
	if _, err := direct.GetTaskPushConfig(reader, get); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("independent upstream get must deny reader: %v", err)
	}
	if _, err := direct.ListTaskPushConfigs(reader, list); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("independent upstream list must deny reader: %v", err)
	}
	status, body, err = admin.Do(ctx, http.MethodGet, "/api/agents/push-configs?agent_names="+url.QueryEscape(name), nil, nil)
	var saved struct {
		Count int `json:"count"`
	}
	if err != nil || status != 200 || json.Unmarshal(body, &saved) != nil || saved.Count != 2 {
		t.Fatalf("real gateway store must contain both callbacks: status=%d error=%v body=%s", status, err, body)
	}
	for _, binding := range []a2a.TransportProtocol{a2a.TransportProtocolJSONRPC, a2a.TransportProtocolHTTPJSON, a2a.TransportProtocolGRPC} {
		t.Run(string(binding), func(t *testing.T) {
			client := clients[binding]
			if client == nil {
				t.Skipf("gateway did not advertise %s", binding)
			}
			cfg, err := client.GetTaskPushConfig(owner, get)
			if err != nil || cfg == nil || cfg.URL != "https://callback.example/events?secret=private" || cfg.Tenant != "callback-tenant" || cfg.Token != "" || cfg.Auth == nil || cfg.Auth.Scheme != "Bearer" || cfg.Auth.Credentials != "" {
				t.Fatalf("owner callback/redaction failed: %v", err)
			}
			configs, err := client.ListTaskPushConfigs(owner, list)
			if err != nil || len(configs) != 2 {
				t.Fatalf("owner list: count=%d error=%v", len(configs), err)
			}
			for _, cfg := range configs {
				if cfg.URL != "https://callback.example/events?secret="+cfg.ID || cfg.Tenant != "callback-tenant" || cfg.Token != "" || cfg.Auth == nil || cfg.Auth.Scheme != "Bearer" || cfg.Auth.Credentials != "" {
					t.Fatal("owner list did not preserve/redact a client callback")
				}
			}
			if cfg, err := client.GetTaskPushConfig(reader, get); !errors.Is(err, a2a.ErrTaskNotFound) || cfg != nil {
				t.Fatalf("reader get disclosed a private callback: %v", err)
			}
			if configs, err := client.ListTaskPushConfigs(reader, list); !errors.Is(err, a2a.ErrTaskNotFound) || len(configs) != 0 {
				t.Fatalf("reader list disclosed private callbacks: count=%d error=%v", len(configs), err)
			}
			if configs, err := client.ListTaskPushConfigs(owner, &a2a.ListTaskPushConfigRequest{TaskID: "missing-task"}); !errors.Is(err, a2a.ErrTaskNotFound) || len(configs) != 0 {
				t.Fatalf("missing task list: count=%d error=%v", len(configs), err)
			}
			if cfg, err := client.GetTaskPushConfig(owner, &a2a.GetTaskPushConfigRequest{TaskID: "missing-task", ID: "private"}); !errors.Is(err, a2a.ErrTaskNotFound) || cfg != nil {
				t.Fatalf("missing task get: %v", err)
			}
			if cfg, err := client.GetTaskPushConfig(owner, &a2a.GetTaskPushConfigRequest{TaskID: task.ID, ID: "missing-config"}); err == nil || cfg != nil {
				t.Fatalf("missing config get: %v", err)
			}
		})
	}
	t.Run("provider harness", func(t *testing.T) {
		binary, collection := os.Getenv("AGENTGATEWAY_NEWMAN_BIN"), os.Getenv("AGENTGATEWAY_HARNESS_COLLECTION")
		if binary == "" || collection == "" {
			t.Skip("AGENTGATEWAY_NEWMAN_BIN and AGENTGATEWAY_HARNESS_COLLECTION are not set")
		}
		values := []map[string]any{}
		for key, value := range map[string]string{"baseUrl": base, "include_preview": "1", "a2aAgentName": name, "a2aTaskId": string(task.ID), "a2aPushConfigId": "private", "a2aOwnerKey": ownerKey, "a2aReaderKey": readerKey, "a2aExpectedCallbackUrl": "https://callback.example/events?secret=private"} {
			values = append(values, map[string]any{"key": key, "value": value, "enabled": true})
		}
		environment := filepath.Join(t.TempDir(), "environment.json")
		data, err := json.Marshal(map[string]any{"name": "isolated A2A authorization", "values": values})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(environment, data, 0600); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, binary, "run", collection, "--environment", environment, "--reporters", "cli", "--timeout-request", "15000")
		output, err := command.CombinedOutput()
		t.Log(string(output))
		if err != nil {
			t.Fatalf("filtered A2A Newman execution failed: %v", err)
		}
	})
	fx.SharePushConfigs(task.ID, "reader", "shared")
	for binding, client := range clients {
		t.Run("shared/"+string(binding), func(t *testing.T) {
			if _, err := client.GetTask(reader, &a2a.GetTaskRequest{ID: task.ID}); !errors.Is(err, a2a.ErrTaskNotFound) {
				t.Fatalf("sharing push configs must not grant task content: %v", err)
			}
			cfg, err := client.GetTaskPushConfig(reader, &a2a.GetTaskPushConfigRequest{TaskID: task.ID, ID: "shared"})
			if err != nil || cfg == nil || cfg.ID != "shared" || cfg.Token != "" || cfg.Auth == nil || cfg.Auth.Credentials != "" {
				t.Fatalf("shared callback failed: %v", err)
			}
			configs, err := client.ListTaskPushConfigs(reader, list)
			if err != nil || len(configs) != 1 || configs[0].ID != "shared" || configs[0].URL != "https://callback.example/events?secret=shared" || configs[0].Token != "" || configs[0].Auth == nil || configs[0].Auth.Credentials != "" {
				t.Fatalf("shared list included unauthorized configurations: count=%d error=%v", len(configs), err)
			}
		})
	}
	fx.SharePushConfigs(task.ID, "reader")
	t.Run("revoked sharing", func(t *testing.T) {
		if cfg, err := primary.GetTaskPushConfig(reader, &a2a.GetTaskPushConfigRequest{TaskID: task.ID, ID: "shared"}); !errors.Is(err, a2a.ErrTaskNotFound) || cfg != nil {
			t.Fatalf("revoked get still allowed: %v", err)
		}
		if configs, err := primary.ListTaskPushConfigs(reader, list); !errors.Is(err, a2a.ErrTaskNotFound) || len(configs) != 0 {
			t.Fatalf("revoked list still allowed: %v", err)
		}
	})
	seen := map[string]bool{}
	for _, call := range fx.Calls() {
		if call.Method == "GetTaskPushConfig" || call.Method == "ListTaskPushConfigs" {
			if call.Caller == "owner" && call.Error == "" || call.Caller == "reader" && strings.Contains(call.Error, "task not found") {
				seen[call.Caller+"/"+call.Method] = true
			}
		}
	}
	for _, identity := range []string{"owner", "reader"} {
		for _, method := range []string{"GetTaskPushConfig", "ListTaskPushConfigs"} {
			if !seen[identity+"/"+method] {
				t.Fatalf("upstream never observed %s calling %s", identity, method)
			}
		}
	}
	if !t.Failed() {
		t.Log("real gateway: two admitted callers, persisted callbacks, independent upstream denials, redaction, all advertised bindings, limited sharing and revocation verified")
	}
}

func createPushTestKey(t *testing.T, ctx context.Context, gateway *driver.Gateway, name string) (string, string) {
	t.Helper()
	status, body, err := gateway.Do(ctx, http.MethodPost, "/api/governance/virtual-keys", map[string]any{"name": name, "is_active": true}, nil)
	var response struct {
		Key struct {
			ID    string `json:"id"`
			Value string `json:"value"`
		} `json:"virtual_key"`
	}
	if err != nil || status != http.StatusOK {
		t.Fatalf("create virtual key: status=%d error=%v", status, err)
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode created virtual key: %v", err)
	}
	if response.Key.ID == "" || response.Key.Value == "" {
		t.Fatal("created virtual key response is incomplete")
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		status, _, err := gateway.Do(cleanupCtx, http.MethodDelete, "/api/governance/virtual-keys/"+response.Key.ID, nil, nil)
		if err != nil || (status != http.StatusOK && status != http.StatusNoContent) {
			t.Errorf("delete test virtual key: status=%d error=%v", status, err)
		}
	})
	return response.Key.ID, response.Key.Value
}
