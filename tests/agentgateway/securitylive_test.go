package agentgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	a2agrpc "github.com/a2aproject/a2a-go/v2/a2agrpc/v1"
	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/maximhq/bifrost/tests/agentgateway/driver"
	"github.com/maximhq/bifrost/tests/agentgateway/fixture"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type securityHeader struct{ key string }

// Before attaches the new test virtual key to each native SDK request.
func (h securityHeader) Before(ctx context.Context, r *a2aclient.Request) (context.Context, any, error) {
	r.ServiceParams.Append("x-bf-vk", h.key)
	return ctx, nil, nil
}

// After preserves the native response.
func (securityHeader) After(context.Context, *a2aclient.Response) error { return nil }

// TestSecurityLive validates outbound policies against the authorized running gateway using finite fixtures.
func TestSecurityLive(t *testing.T) {
	if os.Getenv("A2A_SECURITY_LIVE") != "1" {
		t.Skip("live validation disabled")
	}
	raw, err := os.ReadFile("/tmp/a2a-security-session")
	if err != nil {
		t.Fatal(err)
	}
	gw := driver.NewGateway("http://localhost:8080", strings.TrimSpace(string(raw)))
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	var requests int
	var logObservations []string
	call := func(method, path string, body any) (int, []byte) {
		requests++
		s, b, e := gw.Do(ctx, method, path, body, nil)
		if e != nil {
			t.Fatal(e)
		}
		return s, b
	}
	expect := func(want int, method, path string, body any) []byte {
		s, b := call(method, path, body)
		if s != want {
			t.Fatalf("%s %s HTTP %d want %d: %s", method, path, s, want, b)
		}
		return b
	}
	baselineRaw := expect(200, "GET", "/api/config?from_db=true", nil)
	var cfg map[string]json.RawMessage
	json.Unmarshal(baselineRaw, &cfg)
	var cc map[string]json.RawMessage
	json.Unmarshal(cfg["client_config"], &cc)
	var baseline []string
	json.Unmarshal(cc["a2a_private_allowlist"], &baseline)
	filtered := baseline[:0]
	for _, entry := range baseline {
		if entry != "127.0.0.1" && entry != "localhost" {
			filtered = append(filtered, entry)
		}
	}
	baseline = filtered
	originalCC := make(map[string]json.RawMessage, len(cc))
	for k, v := range cc {
		originalCC[k] = append(json.RawMessage(nil), v...)
	}
	t.Cleanup(func() {
		cleanCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		status, body, err := gw.Do(cleanCtx, "PUT", "/api/config", map[string]any{"client_config": originalCC, "framework_config": cfg["framework_config"]}, nil)
		if err != nil || status != http.StatusOK {
			t.Errorf("restore original allowlist HTTP %d: %v: %s", status, err, body)
			return
		}
		var original []string
		json.Unmarshal(originalCC["a2a_private_allowlist"], &original)
		for _, port := range []int{8091, 8092, 8093} {
			node := driver.NewGateway(fmt.Sprintf("http://localhost:%d", port), gw.AdminToken)
			deadline := time.Now().Add(5 * time.Second)
			for {
				status, body, err := node.Do(cleanCtx, "GET", "/api/config?from_db=true", nil, nil)
				var out map[string]json.RawMessage
				json.Unmarshal(body, &out)
				var current map[string]json.RawMessage
				json.Unmarshal(out["client_config"], &current)
				var permitted []string
				json.Unmarshal(current["a2a_private_allowlist"], &permitted)
				if err == nil && status == http.StatusOK && reflect.DeepEqual(permitted, original) {
					break
				}
				if time.Now().After(deadline) {
					t.Errorf("original allowlist cleanup did not propagate to node %d", port)
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	})
	id := fmt.Sprintf("security-live-%d", time.Now().UnixNano())
	recordPath := filepath.Join("..", "..", "tmp", "agentgateway-security-20261015-mutations.md")
	record := func(line string) {
		f, e := os.OpenFile(recordPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		fmt.Fprintln(f, line)
	}
	record(fmt.Sprintf("\nrun %s; baseline a2a_private_allowlist=%s; other settings preserved; temporary resources retained for keep/discard decision", id, cc["a2a_private_allowlist"]))
	setAllow := func(values []string) {
		v, _ := json.Marshal(values)
		cc["a2a_private_allowlist"] = v
		record(fmt.Sprintf("attempt PUT /api/config a2a_private_allowlist=%s", v))
		expect(200, "PUT", "/api/config", map[string]any{"client_config": cc, "framework_config": cfg["framework_config"]})
		record(fmt.Sprintf("PUT /api/config a2a_private_allowlist=%s", v))
		for _, port := range []int{8091, 8092, 8093} {
			node := driver.NewGateway(fmt.Sprintf("http://localhost:%d", port), gw.AdminToken)
			deadline := time.Now().Add(8 * time.Second)
			for {
				requests++
				status, body, e := node.Do(ctx, "GET", "/api/config?from_db=true", nil, nil)
				if e != nil {
					t.Fatal(e)
				}
				var out map[string]json.RawMessage
				json.Unmarshal(body, &out)
				var current map[string]json.RawMessage
				json.Unmarshal(out["client_config"], &current)
				var permitted []string
				json.Unmarshal(current["a2a_private_allowlist"], &permitted)
				if status == 200 && reflect.DeepEqual(values, permitted) {
					for key, before := range originalCC {
						if key == "a2a_private_allowlist" {
							continue
						}
						var a, b any
						json.Unmarshal(before, &a)
						json.Unmarshal(current[key], &b)
						if !reflect.DeepEqual(a, b) {
							t.Fatalf("unrelated client setting changed on %d: %s", port, key)
						}
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("allowlist did not propagate to %d", port)
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		t.Logf("PASS allowlist propagation all 3 nodes %v", values)
	}
	setAllow(baseline)
	mux := http.NewServeMux()
	var runtimeCalls, discoveryCalls, tokenCalls atomic.Int64
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	lifecycleCard := &a2a.AgentCard{Name: "security-fixture", Version: "1", Capabilities: a2a.AgentCapabilities{Streaming: true, ExtendedAgentCard: true}, DefaultInputModes: []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"}}
	lifecycleCard.Skills = []a2a.AgentSkill{{ID: "fixture", Name: "Lifecycle", Description: "Finite lifecycle validation", Tags: []string{"validation"}}}
	lifecycleCard.SupportedInterfaces = []*a2a.AgentInterface{a2a.NewAgentInterface(upstream.URL+"/jsonrpc", a2a.TransportProtocolJSONRPC)}
	handler, _ := fixture.NewLifecycleHandler(lifecycleCard)
	grpcServer := grpc.NewServer()
	a2apb.RegisterA2AServiceServer(grpcServer, a2agrpc.NewHandler(handler))
	go grpcServer.Serve(listener)
	defer grpcServer.Stop()
	grpcURL := "http://" + listener.Addr().String()
	card := func(target string, binding a2a.TransportProtocol) *a2a.AgentCard {
		return &a2a.AgentCard{Name: "security-fixture", Version: "1", SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(target, binding)}, DefaultInputModes: []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"}, Capabilities: lifecycleCard.Capabilities, Skills: lifecycleCard.Skills}
	}
	mux.Handle("/jsonrpc", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimeCalls.Add(1)
		a2asrv.NewJSONRPCHandler(handler).ServeHTTP(w, r)
	}))
	mux.Handle("/rest/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtimeCalls.Add(1)
		http.StripPrefix("/rest", a2asrv.NewRESTHandler(handler)).ServeHTTP(w, r)
	}))
	for _, binding := range []a2a.TransportProtocol{a2a.TransportProtocolJSONRPC, a2a.TransportProtocolHTTPJSON, a2a.TransportProtocolGRPC} {
		b := binding
		target := upstream.URL + "/jsonrpc"
		if b == a2a.TransportProtocolHTTPJSON {
			target = upstream.URL + "/rest/"
		}
		if b == a2a.TransportProtocolGRPC {
			target = grpcURL
		}
		mux.Handle("/card/"+string(b), a2asrv.NewStaticAgentCardHandler(card(target, b)))
	}
	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"fixture-token","token_type":"Bearer","expires_in":1}`)
		} else {
			discoveryCalls.Add(1)
			fmt.Fprintf(w, `{"token_endpoint":"http://localhost:%s/token"}`, strings.Split(r.Host, ":")[1])
		}
	}))
	defer oauth.Close()
	oauthURL := strings.Replace(oauth.URL, "127.0.0.1", "localhost", 1)
	auth := func(endpoint string, direct bool) map[string]any {
		o := map[string]any{"client_id": "fixture-client", "client_secret": "fixture-secret"}
		if direct {
			o["token_url"] = endpoint
		} else {
			o["discovery_url"] = endpoint
		}
		return map[string]any{"type": "oauth", "oauth": o}
	}
	inspect := func(url string, auth any, want int) {
		expect(want, "POST", "/api/agents/inspect", map[string]any{"agent_card_url": url, "discovery_auth": auth})
	}
	inspect(upstream.URL+"/card/JSONRPC", nil, 400)
	setAllow(append(append([]string{}, baseline...), "127.0.0.1"))
	inspect(upstream.URL+"/card/JSONRPC", nil, 200)
	inspect(upstream.URL+"/card/JSONRPC", auth(oauthURL+"/discovery", false), 400)
	if discoveryCalls.Load() != 0 {
		t.Fatal("blocked OAuth discovery reached fixture")
	}
	inspect(upstream.URL+"/card/JSONRPC", auth(oauthURL+"/token", true), 400)
	if tokenCalls.Load() != 0 {
		t.Fatal("blocked OAuth token reached fixture")
	}
	setAllow(append(append([]string{}, baseline...), "127.0.0.1", "localhost"))
	inspect(upstream.URL+"/card/JSONRPC", auth(oauthURL+"/discovery", false), 200)
	inspect(upstream.URL+"/card/JSONRPC", auth(oauthURL+"/token", true), 200)
	inspect("http://169.254.169.254/latest/meta-data/", nil, 400)
	for _, invalid := range []string{"http://localhost", "169.254.169.254", "*", "localhost:9000"} {
		copyCC := make(map[string]json.RawMessage, len(cc))
		for k, v := range cc {
			copyCC[k] = v
		}
		v, _ := json.Marshal([]string{invalid})
		copyCC["a2a_private_allowlist"] = v
		expect(400, "PUT", "/api/config", map[string]any{"client_config": copyCC, "framework_config": cfg["framework_config"]})
	}
	vkRaw := expect(200, "POST", "/api/governance/virtual-keys", map[string]any{"name": id, "is_active": true, "allow_all_providers": false})
	var vk map[string]any
	json.Unmarshal(vkRaw, &vk)
	if nested, ok := vk["virtual_key"].(map[string]any); ok {
		vk = nested
	}
	key, _ := vk["value"].(string)
	if key == "" {
		if v, ok := vk["value"].(map[string]any); ok {
			key, _ = v["value"].(string)
		}
	}
	if key == "" {
		t.Fatal("new virtual key missing value")
	}
	record(fmt.Sprintf("POST /api/governance/virtual-keys id=%v name=%s", vk["id"], id))
	create := func(name, url string, origins []string, want int) {
		body := map[string]any{"name": name, "agent_card_url": url, "enabled": true, "allow_by_default": true, "credential_origins": origins}
		expect(want, "POST", "/api/agents", body)
		if want == 201 {
			record(fmt.Sprintf("POST /api/agents name=%s card=%s origins=%v", name, url, origins))
		}
	}
	for _, binding := range []a2a.TransportProtocol{a2a.TransportProtocolJSONRPC, a2a.TransportProtocolHTTPJSON, a2a.TransportProtocolGRPC} {
		name := id + "-" + strings.ToLower(strings.ReplaceAll(string(binding), "+", "-"))
		origins := []string{}
		if binding == a2a.TransportProtocolGRPC {
			origins = []string{grpcURL}
			create(name, upstream.URL+"/card/"+string(binding), nil, 400)
		}
		create(name, upstream.URL+"/card/"+string(binding), origins, 201)
		for _, port := range []int{8091, 8092, 8093} {
			node := driver.NewGateway(fmt.Sprintf("http://localhost:%d", port), gw.AdminToken)
			requests++
			if _, err := node.WaitForCard(ctx, name, key, ""); err != nil {
				t.Fatalf("agent/card propagation node %d: %v", port, err)
			}
		}
		served, e := gw.WaitForCard(ctx, name, key, "")
		requests++
		if e != nil {
			t.Fatal(e)
		}
		for _, inbound := range []a2a.TransportProtocol{a2a.TransportProtocolJSONRPC, a2a.TransportProtocolHTTPJSON, a2a.TransportProtocolGRPC} {
			var iface *a2a.AgentInterface
			for _, candidate := range served.SupportedInterfaces {
				if candidate.ProtocolBinding == inbound {
					iface = candidate
					break
				}
			}
			if iface == nil {
				t.Fatalf("missing gateway binding %s", inbound)
			}
			client, e := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{iface}, a2aclient.WithJSONRPCTransport(http.DefaultClient), a2aclient.WithRESTTransport(http.DefaultClient), a2agrpc.WithGRPCTransport(grpc.WithTransportCredentials(insecure.NewCredentials())), a2aclient.WithCallInterceptors(securityHeader{key: key}))
			if e != nil {
				t.Fatal(e)
			}
			result, e := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("live check"))})
			requests++
			client.Destroy()
			if e != nil {
				t.Fatalf("%s -> %s: %v", inbound, binding, e)
			}
			if result == nil {
				t.Fatal("missing response")
			}
			t.Logf("PASS pairing %s -> %s", inbound, binding)
			lifecycleName := name + "-" + strings.ToLower(strings.ReplaceAll(string(inbound), "+", "-")) + "-lifecycle"
			// Each run owns its checkpoint gate, so earlier subscriptions cannot release later streams.
			lifecycleHandler, release := fixture.NewLifecycleHandler(lifecycleCard)
			lifecycleMux := http.NewServeMux()
			lifecycleServer := httptest.NewServer(lifecycleMux)
			t.Cleanup(lifecycleServer.Close)
			target := lifecycleServer.URL + "/jsonrpc"
			if binding == a2a.TransportProtocolGRPC {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				server := grpc.NewServer()
				a2apb.RegisterA2AServiceServer(server, a2agrpc.NewHandler(lifecycleHandler))
				go server.Serve(listener)
				t.Cleanup(server.Stop)
				target = "http://" + listener.Addr().String()
			} else if binding == a2a.TransportProtocolHTTPJSON {
				target = lifecycleServer.URL + "/rest/"
				lifecycleMux.Handle("/rest/", http.StripPrefix("/rest", a2asrv.NewRESTHandler(lifecycleHandler)))
			} else {
				lifecycleMux.Handle("/jsonrpc", a2asrv.NewJSONRPCHandler(lifecycleHandler))
			}
			lifecycleMux.Handle("/card", a2asrv.NewStaticAgentCardHandler(card(target, binding)))
			lifecycleOrigins := []string{}
			if binding == a2a.TransportProtocolGRPC {
				lifecycleOrigins = []string{target}
			}
			create(lifecycleName, lifecycleServer.URL+"/card", lifecycleOrigins, 201)
			lifecycle := &driver.Lifecycle{Gateway: gw, AgentName: lifecycleName, VirtualKey: key, Binding: inbound, Release: release}
			lifecycleResult, e := lifecycle.Run(ctx)
			requests += 13
			if e != nil {
				t.Fatalf("lifecycle %s -> %s: %v", inbound, binding, e)
			}
			logCtx, stopLogs := context.WithTimeout(ctx, 15*time.Second)
			e = driver.AssertLogs(logCtx, gw, lifecycleName, lifecycleResult, false, driver.Tags{})
			stopLogs()
			if e != nil {
				logObservations = append(logObservations, fmt.Sprintf("%s -> %s: %v", inbound, binding, e))
				t.Logf("OBSERVATION logs %s -> %s: %v", inbound, binding, e)
			}
			if e == nil {
				t.Logf("PASS lifecycle + logs %s -> %s", inbound, binding)
			} else {
				t.Logf("PASS native lifecycle %s -> %s; log correlation unresolved", inbound, binding)
			}
		}
	}
	// A distinct HTTP origin must be approved even with no configured runtime credential.
	alternate := httptest.NewServer(a2asrv.NewStaticAgentCardHandler(card(upstream.URL+"/jsonrpc", a2a.TransportProtocolJSONRPC)))
	defer alternate.Close()
	create(id+"-origin", alternate.URL, nil, 400)
	create(id+"-origin", alternate.URL, []string{upstream.URL}, 201)

	// Warm each node's cached runtime client, then call it directly after revocation.
	name := id + "-jsonrpc"
	served, err := gw.WaitForCard(ctx, name, key, "")
	if err != nil {
		t.Fatal(err)
	}
	clients := []*a2aclient.Client{}
	for _, port := range []int{8091, 8092, 8093} {
		var endpoint *a2a.AgentInterface
		for _, candidate := range served.SupportedInterfaces {
			if candidate.ProtocolBinding == a2a.TransportProtocolJSONRPC {
				copy := *candidate
				endpoint = &copy
				break
			}
		}
		u, _ := url.Parse(endpoint.URL)
		u.Host = fmt.Sprintf("localhost:%d", port)
		endpoint.URL = u.String()
		client, err := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{endpoint}, a2aclient.WithJSONRPCTransport(http.DefaultClient), a2aclient.WithCallInterceptors(securityHeader{key: key}))
		if err != nil {
			t.Fatal(err)
		}
		defer client.Destroy()
		clients = append(clients, client)
		requests++
		if _, err = client.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("warm pool"))}); err != nil {
			t.Fatalf("warm node %d: %v", port, err)
		}
	}
	before := runtimeCalls.Load()
	setAllow(baseline)
	for i, client := range clients {
		requests++
		_, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("revoked pool"))})
		if err == nil {
			t.Fatalf("node %d cached runtime bypassed revocation", 8091+i)
		}
	}
	if runtimeCalls.Load() != before {
		t.Fatal("revoked cached runtime reached fixture")
	}
	setAllow(append(append([]string{}, baseline...), "127.0.0.1", "localhost"))
	for i, client := range clients {
		requests++
		if _, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("regranted pool"))}); err != nil {
			t.Fatalf("node %d did not resume: %v", 8091+i, err)
		}
	}
	t.Log("PASS cached runtime permission revocation and regrant on all 3 nodes")
	record("original allowlist restoration scheduled in cleanup; test agents and VK retained; finite upstream fixtures stopped at test completion")
	if os.Getenv("A2A_UI_PAUSE") == "1" {
		t.Logf("UI fixture ready card=%s/card/JSONRPC", upstream.URL)
		if err := os.WriteFile("/tmp/a2a-ui-fixture", []byte(upstream.URL+"/card/JSONRPC"), 0600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Minute)
		for time.Now().Before(deadline) {
			if _, err := os.Stat("/tmp/a2a-ui-complete"); err == nil {
				break
			}
			time.Sleep(time.Second)
		}
	}
	t.Logf("log observations=%d", len(logObservations))
	t.Logf("PASS live explicitOperations=%d runtimeHTTP=%d oauthDiscovery=%d oauthToken=%d; resources=%s", requests, runtimeCalls.Load(), discoveryCalls.Load(), tokenCalls.Load(), id)
}

// TestRESTTaskIDLive verifies REST subscription log ownership after later task and URI activity on each node and the load balancer.
func TestRESTTaskIDLive(t *testing.T) {
	if os.Getenv("A2A_REST_TASKID_LIVE") != "1" {
		t.Skip("live REST validation disabled")
	}
	raw, err := os.ReadFile("/tmp/a2a-security-session")
	if err != nil {
		t.Fatal(err)
	}
	gw := driver.NewGateway("http://localhost:8080", strings.TrimSpace(string(raw)))
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	record := func(line string) {
		f, e := os.OpenFile("../../tmp/rest-taskid-20261015-mutations.md", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		fmt.Fprintln(f, line)
	}
	do := func(c context.Context, method, path string, body any, want int) []byte {
		s, b, e := gw.Do(c, method, path, body, nil)
		if e != nil {
			t.Fatal(e)
		}
		if s != want {
			t.Fatalf("%s %s HTTP %d want %d: %s", method, path, s, want, b)
		}
		return b
	}
	var cfg map[string]json.RawMessage
	json.Unmarshal(do(ctx, "GET", "/api/config?from_db=true", nil, 200), &cfg)
	var cc map[string]json.RawMessage
	json.Unmarshal(cfg["client_config"], &cc)
	original := append(json.RawMessage(nil), cc["a2a_private_allowlist"]...)
	var permitted []string
	json.Unmarshal(original, &permitted)
	stamp := fmt.Sprintf("rest-taskid-%d", time.Now().UnixNano())
	names := []string{}
	vkID := ""
	record(fmt.Sprintf("run %s baseline a2a_private_allowlist=%s", stamp, original))
	t.Cleanup(func() {
		cleanCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for _, name := range names {
			s, _, e := gw.Do(cleanCtx, "DELETE", "/api/agents/"+name, nil, nil)
			if e != nil || s != 204 {
				t.Errorf("cleanup agent %s HTTP %d: %v", name, s, e)
			} else {
				record("DELETE /api/agents/" + name)
			}
		}
		if vkID != "" {
			s, _, e := gw.Do(cleanCtx, "DELETE", "/api/governance/virtual-keys/"+vkID, nil, nil)
			if e != nil || s < 200 || s >= 300 {
				t.Errorf("cleanup VK HTTP %d: %v", s, e)
			} else {
				record("DELETE /api/governance/virtual-keys/" + vkID)
			}
		}
		cc["a2a_private_allowlist"] = original
		s, _, e := gw.Do(cleanCtx, "PUT", "/api/config", map[string]any{"client_config": cc, "framework_config": cfg["framework_config"]}, nil)
		if e != nil || s != 200 {
			t.Errorf("restore allowlist HTTP %d: %v", s, e)
		} else {
			record("RESTORE /api/config a2a_private_allowlist=" + string(original))
		}
		for _, port := range []int{8091, 8092, 8093} {
			node := driver.NewGateway(fmt.Sprintf("http://localhost:%d", port), gw.AdminToken)
			deadline := time.Now().Add(5 * time.Second)
			for {
				s, b, e := node.Do(cleanCtx, "GET", "/api/config?from_db=true", nil, nil)
				var out map[string]json.RawMessage
				json.Unmarshal(b, &out)
				var current map[string]json.RawMessage
				json.Unmarshal(out["client_config"], &current)
				var a, before []string
				json.Unmarshal(current["a2a_private_allowlist"], &a)
				json.Unmarshal(original, &before)
				if e == nil && s == 200 && reflect.DeepEqual(a, before) {
					break
				}
				if time.Now().After(deadline) {
					t.Errorf("allowlist cleanup did not propagate node %d", port)
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			for _, name := range names {
				s, _, e := node.Do(cleanCtx, "GET", "/api/agents/"+name, nil, nil)
				if e != nil || s != 404 {
					t.Errorf("agent cleanup node %d %s HTTP %d", port, name, s)
				}
			}
			if vkID != "" {
				s, _, e := node.Do(cleanCtx, "GET", "/api/governance/virtual-keys/"+vkID, nil, nil)
				if e != nil || s != 404 {
					t.Errorf("VK cleanup node %d HTTP %d", port, s)
				}
			}
		}
		t.Log("cleanup verified agents/VK absent and baseline allowlist restored on all 3 nodes; logs retained")
	})
	found := false
	for _, entry := range permitted {
		found = found || entry == "127.0.0.1"
	}
	if !found {
		permitted = append(permitted, "127.0.0.1")
	}
	v, _ := json.Marshal(permitted)
	cc["a2a_private_allowlist"] = v
	record("attempt PUT /api/config a2a_private_allowlist=" + string(v))
	do(ctx, "PUT", "/api/config", map[string]any{"client_config": cc, "framework_config": cfg["framework_config"]}, 200)
	var vk map[string]any
	json.Unmarshal(do(ctx, "POST", "/api/governance/virtual-keys", map[string]any{"name": stamp, "is_active": true}, 200), &vk)
	if nested, ok := vk["virtual_key"].(map[string]any); ok {
		vk = nested
	}
	vkID, _ = vk["id"].(string)
	key, _ := vk["value"].(string)
	if value, ok := vk["value"].(map[string]any); ok {
		key, _ = value["value"].(string)
	}
	if key == "" || vkID == "" {
		t.Fatal("new VK missing identity/value")
	}
	record("POST /api/governance/virtual-keys id=" + vkID + " name=" + stamp)
	passes := 0
	for _, port := range []int{8091, 8092, 8093, 8080} {
		node := driver.NewGateway(fmt.Sprintf("http://localhost:%d", port), gw.AdminToken)
		for iteration := 0; iteration < 3; iteration++ {
			fx, e := fixture.Start("127.0.0.1:0", "127.0.0.1")
			if e != nil {
				t.Fatal(e)
			}
			name := fmt.Sprintf("%s-%d-%d", stamp, port, iteration)
			do(ctx, "POST", "/api/agents", map[string]any{"name": name, "agent_card_url": fx.CardURL, "enabled": true, "allow_by_default": true}, 201)
			names = append(names, name)
			record("POST /api/agents name=" + name + " card=" + fx.CardURL)
			result, e := (&driver.Lifecycle{Gateway: node, AgentName: name, VirtualKey: key, Binding: a2a.TransportProtocolHTTPJSON, CallURL: node.URL, Release: fx.ReleaseCheckpoint}).Run(ctx)
			if e != nil {
				fx.Close()
				t.Fatalf("node %d iteration %d lifecycle: %v", port, iteration, e)
			}
			// Churn the same server's request URI memory after SubscribeToTask has completed.
			for n := 0; n < 20; n++ {
				s, _, e := node.Do(ctx, "GET", fmt.Sprintf("/health?rest-task-id-churn=%s-%d-%s", name, n, strings.Repeat("q", 128+n)), nil, nil)
				if e != nil || s != 200 {
					fx.Close()
					t.Fatalf("URI churn HTTP %d: %v", s, e)
				}
			}
			logCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			e = driver.AssertLogs(logCtx, gw, name, result, false, driver.Tags{})
			stop()
			fx.Close()
			if e != nil {
				t.Fatalf("node %d iteration %d strict logs: %v", port, iteration, e)
			}
			passes++
			t.Logf("PASS node %d iteration %d full REST lifecycle + exact subscription task/context logs after 20 URI churn requests", port, iteration)
		}
	}
	t.Logf("PASS %d REST lifecycles, 12 subscriptions, 60 SendMessage operations including later cancel/fail/reject tasks, 240 URI churn requests; all strict log checks passed", passes)
}
