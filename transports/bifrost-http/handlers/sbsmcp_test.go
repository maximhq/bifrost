package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

func sbsStr(s string) *string { return &s }

func TestSBSApplyOverride(t *testing.T) {
	readOnly := false
	tool := mcp.Tool{Name: "kali-nmap", Description: "upstream", Annotations: mcp.ToolAnnotation{ReadOnlyHint: &readOnly, Title: "Nmap"}}
	sbsApplyOverride(&tool, configstoreTables.MCPToolOverride{
		Name:        sbsStr("port_scan"),
		Title:       sbsStr("Port scan"),
		Description: sbsStr("Scan ports"),
		Annotations: map[string]any{"destructiveHint": true},
	})
	if tool.Name != "port_scan" || tool.Description != "Scan ports" || tool.Title != "Port scan" {
		t.Fatalf("fields not overridden: %+v", tool)
	}
	if tool.Annotations.Title != "Port scan" {
		t.Fatalf("annotation title = %q", tool.Annotations.Title)
	}
	if tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
		t.Fatalf("destructiveHint not merged")
	}
	if tool.Annotations.ReadOnlyHint == nil || *tool.Annotations.ReadOnlyHint {
		t.Fatalf("upstream readOnlyHint lost")
	}
}

func TestSBSApplyOverrideEmptyKeepsUpstream(t *testing.T) {
	tool := mcp.Tool{Name: "a-b", Description: "upstream"}
	sbsApplyOverride(&tool, configstoreTables.MCPToolOverride{Name: sbsStr(""), Description: sbsStr("")})
	if tool.Name != "a-b" || tool.Description != "upstream" {
		t.Fatalf("empty override changed the tool: %+v", tool)
	}
}

func TestSBSPreserveOverrides(t *testing.T) {
	existing := []configstoreTables.MCPToolSpec{
		{MCPClientID: "c1", ToolNames: []string{"*"}, Overrides: map[string]configstoreTables.MCPToolOverride{"x": {Name: sbsStr("y")}}},
	}
	incoming := []configstoreTables.MCPToolSpec{{MCPClientID: "c1", ToolNames: []string{"x"}}, {MCPClientID: "c2", ToolNames: []string{"*"}}}
	got := SBSPreserveOverrides(existing, incoming)
	if got[0].Overrides["x"].Name == nil || *got[0].Overrides["x"].Name != "y" {
		t.Fatalf("overrides of c1 not preserved: %+v", got[0])
	}
	if got[1].Overrides != nil {
		t.Fatalf("c2 gained overrides: %+v", got[1])
	}

	explicit := []configstoreTables.MCPToolSpec{{MCPClientID: "c1", Overrides: map[string]configstoreTables.MCPToolOverride{}}}
	if got := SBSPreserveOverrides(existing, explicit); len(got[0].Overrides) != 0 {
		t.Fatalf("an explicit empty map must clear overrides")
	}
}

func TestSBSReadSSEResponse(t *testing.T) {
	stream := ": ping\n\n" +
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n" +
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"ok\":true}}\n\n"
	frame, err := sbsReadSSEResponse(strings.NewReader(stream), 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(frame), `"ok":true`) {
		t.Fatalf("wrong frame: %s", frame)
	}
	if _, err := sbsReadSSEResponse(strings.NewReader(stream), 3); err == nil {
		t.Fatalf("expected an error for a missing id")
	}
}

func TestSBSEndpointNamePattern(t *testing.T) {
	for _, ok := range []string{"chatbot", "UnifiedOperationDEMO", "a_b-c"} {
		if !sbsEndpointNamePattern.MatchString(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "a/b", "a b", strings.Repeat("x", 65)} {
		if sbsEndpointNamePattern.MatchString(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestSBSApplyOverrideKeepsCustomAnnotations(t *testing.T) {
	tool := mcp.Tool{Name: "a-b"}
	sbsApplyOverride(&tool, configstoreTables.MCPToolOverride{Annotations: map[string]any{"readOnlyHint": true, "x-sbs-risk": "high"}})
	raw, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Annotations map[string]any `json:"annotations"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Annotations["x-sbs-risk"] != "high" || out.Annotations["readOnlyHint"] != true {
		t.Fatalf("annotations on the wire = %v", out.Annotations)
	}
}

func TestSBSPublicGrant(t *testing.T) {
	vk := "vk1"
	ep := TableSBSMCPEndpoint{AuthMode: SBSEndpointAuthPublic, PublicVirtualKeyID: &vk, VirtualMCPID: 7}
	if g, ok := publicGrant(&ep); !ok || g != (sbsPublicGrant{vkID: "vk1", vmcpID: 7}) {
		t.Fatalf("grant = %+v, %v", g, ok)
	}
	ep.AuthMode = SBSEndpointAuthVirtualKey
	if _, ok := publicGrant(&ep); ok {
		t.Fatalf("a virtual-key endpoint needs no public grant")
	}
}
