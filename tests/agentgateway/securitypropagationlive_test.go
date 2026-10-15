package agentgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/maximhq/bifrost/tests/agentgateway/driver"
	"os"
	"strings"
	"testing"
	"time"
)

// TestSecurityLivePropagation checks live node-local inspection and origin updates while the finite UI fixture is active.
func TestSecurityLivePropagation(t *testing.T) {
	if os.Getenv("A2A_SECURITY_LIVE") != "1" {
		t.Skip("live validation disabled")
	}
	raw, e := os.ReadFile("/tmp/a2a-security-session")
	if e != nil {
		t.Fatal(e)
	}
	fixture, e := os.ReadFile("/tmp/a2a-ui-fixture")
	if e != nil {
		t.Fatal(e)
	}
	token := strings.TrimSpace(string(raw))
	cardURL := strings.TrimSpace(string(fixture))
	gw := driver.NewGateway("http://localhost:8080", token)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	expect := func(g *driver.Gateway, method, path string, body any, want int) []byte {
		s, b, e := g.Do(ctx, method, path, body, nil)
		if e != nil {
			t.Fatal(e)
		}
		if s != want {
			t.Fatalf("%s %s HTTP %d want %d", method, path, s, want)
		}
		return b
	}
	data := expect(gw, "GET", "/api/config?from_db=true", nil, 200)
	var cfg map[string]json.RawMessage
	json.Unmarshal(data, &cfg)
	var cc map[string]json.RawMessage
	json.Unmarshal(cfg["client_config"], &cc)
	record := func(line string) {
		f, e := os.OpenFile("../../tmp/agentgateway-security-20261015-mutations.md", os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		fmt.Fprintln(f, line)
	}
	name := fmt.Sprintf("security-propagation-%d", time.Now().UnixNano())
	body := map[string]any{"name": name, "agent_card_url": cardURL, "enabled": true, "allow_by_default": false, "credential_origins": []string{}}
	expect(gw, "POST", "/api/agents", body, 201)
	record("POST /api/agents name=" + name + " card=" + cardURL)
	body["credential_origins"] = []string{"https://credential-approved.example.com:9000"}
	expect(gw, "PUT", "/api/agents/"+name, body, 200)
	record("PUT /api/agents/" + name + " credential_origins=[https://credential-approved.example.com:9000]")
	for _, port := range []int{8091, 8092, 8093} {
		node := driver.NewGateway(fmt.Sprintf("http://localhost:%d", port), token)
		deadline := time.Now().Add(10 * time.Second)
		for {
			status, data, e := node.Do(ctx, "GET", "/api/agents/"+name, nil, nil)
			if e != nil {
				t.Fatal(e)
			}
			var view map[string]any
			json.Unmarshal(data, &view)
			if status == 200 {
				if origins, ok := view["credential_origins"].([]any); ok && len(origins) == 1 && origins[0] == "https://credential-approved.example.com:9000" {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("origins did not propagate node %d", port)
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("PASS create + credential-origin update node %d", port)
	}
	var baseline []string
	json.Unmarshal(cc["a2a_private_allowlist"], &baseline)
	denied := []string{}
	for _, entry := range baseline {
		if entry != "127.0.0.1" && entry != "localhost" {
			denied = append(denied, entry)
		}
	}
	for _, phase := range []struct {
		values []string
		want   int
	}{{denied, 400}, {baseline, 200}} {
		v, _ := json.Marshal(phase.values)
		cc["a2a_private_allowlist"] = v
		record(fmt.Sprintf("attempt PUT /api/config a2a_private_allowlist=%s", v))
		expect(gw, "PUT", "/api/config", map[string]any{"client_config": cc, "framework_config": cfg["framework_config"]}, 200)
		record(fmt.Sprintf("PUT /api/config a2a_private_allowlist=%s", v))
		for _, port := range []int{8091, 8092, 8093} {
			node := driver.NewGateway(fmt.Sprintf("http://localhost:%d", port), token)
			deadline := time.Now().Add(10 * time.Second)
			for {
				status, _, e := node.Do(ctx, "POST", "/api/agents/inspect", map[string]any{"agent_card_url": cardURL}, nil)
				if e != nil {
					t.Fatal(e)
				}
				if status == phase.want {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("node %d inspect permission HTTP %d want %d", port, status, phase.want)
				}
				time.Sleep(100 * time.Millisecond)
			}
			t.Logf("PASS node-local inspect node %d HTTP %d", port, phase.want)
		}
	}
}
