package agentgateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/maximhq/bifrost/tests/agentgateway/driver"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestSecurityLiveCapture generates fresh unary and SSE traffic with capture enabled and disabled.
func TestSecurityLiveCapture(t *testing.T) {
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
	gw := driver.NewGateway("http://localhost:8080", strings.TrimSpace(string(raw)))
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	expect := func(method, path string, body any, want int) []byte {
		s, b, e := gw.Do(ctx, method, path, body, nil)
		if e != nil {
			t.Fatal(e)
		}
		if s != want {
			t.Fatalf("%s %s HTTP %d want %d", method, path, s, want)
		}
		return b
	}
	cfgRaw := expect("GET", "/api/config?from_db=true", nil, 200)
	var cfg map[string]json.RawMessage
	json.Unmarshal(cfgRaw, &cfg)
	var cc map[string]json.RawMessage
	json.Unmarshal(cfg["client_config"], &cc)
	original := cc["a2a_store_raw_request_response"]
	record := func(line string) {
		f, e := os.OpenFile("../../tmp/agentgateway-security-20261015-mutations.md", os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		fmt.Fprintln(f, line)
	}
	record(fmt.Sprintf("capture baseline a2a_store_raw_request_response=%s", original))
	stamp := fmt.Sprintf("security-capture-%d", time.Now().UnixNano())
	vkRaw := expect("POST", "/api/governance/virtual-keys", map[string]any{"name": stamp, "is_active": true}, 200)
	var vk map[string]any
	json.Unmarshal(vkRaw, &vk)
	if nested, ok := vk["virtual_key"].(map[string]any); ok {
		vk = nested
	}
	key, _ := vk["value"].(string)
	if v, ok := vk["value"].(map[string]any); ok {
		key, _ = v["value"].(string)
	}
	if key == "" {
		t.Fatal("missing newVK")
	}
	record(fmt.Sprintf("POST /api/governance/virtual-keys name=%s id=%v", stamp, vk["id"]))
	for _, enabled := range []bool{true, false} {
		mode := fmt.Sprint(enabled)
		cc["a2a_store_raw_request_response"] = json.RawMessage(mode)
		record("attempt PUT /api/config a2a_store_raw_request_response=" + mode)
		expect("PUT", "/api/config", map[string]any{"client_config": cc, "framework_config": cfg["framework_config"]}, 200)
		record("PUT /api/config a2a_store_raw_request_response=" + mode)
		name := stamp + "-" + mode
		expect("POST", "/api/agents", map[string]any{"name": name, "agent_card_url": strings.TrimSpace(string(fixture)), "enabled": true, "allow_by_default": true}, 201)
		record("POST /api/agents name=" + name + " card=" + strings.TrimSpace(string(fixture)))
		card, e := gw.WaitForCard(ctx, name, key, "")
		if e != nil {
			t.Fatal(e)
		}
		var rpc *a2a.AgentInterface
		for _, iface := range card.SupportedInterfaces {
			if iface.ProtocolBinding == a2a.TransportProtocolJSONRPC {
				rpc = iface
				break
			}
		}
		client, e := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{rpc}, a2aclient.WithJSONRPCTransport(http.DefaultClient), a2aclient.WithCallInterceptors(securityHeader{key: key}))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = client.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("fresh capture "+mode))}); e != nil {
			t.Fatal(e)
		}
		events := 0
		for _, err := range client.SendStreamingMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("interruptible stream"))}) {
			if err != nil {
				t.Fatal(err)
			}
			events++
		}
		client.Destroy()
		if events < 3 {
			t.Fatalf("SSE events=%d", events)
		}
		deadline := time.Now().Add(15 * time.Second)
		var checked bool
		for time.Now().Before(deadline) {
			ops, e := gw.FetchOperations(ctx, name)
			if e != nil {
				t.Fatal(e)
			}
			found := 0
			valid := true
			for _, op := range ops {
				if op.Operation != "SendMessage" && op.Operation != "SendStreamingMessage" {
					continue
				}
				detailRaw := expect("GET", "/api/agents/history/"+op.ID, nil, 200)
				var detail map[string]any
				json.Unmarshal(detailRaw, &detail)
				request, _ := detail["raw_request"].(string)
				response, _ := detail["raw_response"].(string)
				if enabled {
					if request == "" || response == "" {
						valid = false
						continue
					}
					rb, e := base64.StdEncoding.DecodeString(request)
					if e != nil {
						t.Fatal(e)
					}
					sb, e := base64.StdEncoding.DecodeString(response)
					if e != nil {
						t.Fatal(e)
					}
					if len(rb) == 0 || len(sb) == 0 {
						t.Fatal("emptydecodedcapture")
					}
					if op.Operation == "SendStreamingMessage" && !strings.Contains(string(sb), "data:") {
						t.Fatal("SSEcapture lacks dataframes")
					}
				} else if request != "" || response != "" {
					t.Fatal("capture-disabled freshtraffic contains rawbytes")
				}
				found++
			}
			if found == 2 && valid {
				checked = true
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if !checked {
			t.Fatalf("freshcapture checks did not converge mode=%t", enabled)
		}
		t.Logf("PASS fresh capture=%t unary+SSE events=%d; ordinarylogs present", enabled, events)
	}
	cc["a2a_store_raw_request_response"] = original
	record(fmt.Sprintf("attempt restore /api/config a2a_store_raw_request_response=%s", original))
	expect("PUT", "/api/config", map[string]any{"client_config": cc, "framework_config": cfg["framework_config"]}, 200)
	record(fmt.Sprintf("restored /api/config a2a_store_raw_request_response=%s", original))
}
