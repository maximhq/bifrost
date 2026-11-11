package chaosmock_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// TestRealClientReadsToolMetadata: a tool's title, icons and output schema reach a real client as
// the scenario set them, and a tool that sets none arrives without them.
func TestRealClientReadsToolMetadata(t *testing.T) {
	fleet, _ := newFleetWithTools(t,
		chaosmock.Tool{
			Name:         "echo",
			Title:        "Echo",
			Icons:        json.RawMessage(`[{"src":"https://icons.example.invalid/echo.png","mimeType":"image/png","sizes":["48x48"]}]`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"echoed":{"type":"string"}},"required":["echoed"]}`),
		},
		chaosmock.Tool{Name: "plain"},
	)
	c, ctx := connectAt(t, fleet.ServerURL("alpha"), "")
	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil || len(res.Tools) != 2 {
		t.Fatalf("tools/list = %+v, %v", res, err)
	}
	echo, plain := res.Tools[0], res.Tools[1]
	if echo.Title != "Echo" {
		t.Errorf("title = %q, want Echo", echo.Title)
	}
	if len(echo.Icons) != 1 || echo.Icons[0].Src != "https://icons.example.invalid/echo.png" || echo.Icons[0].MIMEType != "image/png" || strings.Join(echo.Icons[0].Sizes, ",") != "48x48" {
		t.Errorf("icons = %+v", echo.Icons)
	}
	if echo.OutputSchema.Type != "object" || echo.OutputSchema.Properties["echoed"] == nil || strings.Join(echo.OutputSchema.Required, ",") != "echoed" {
		t.Errorf("outputSchema = %+v", echo.OutputSchema)
	}
	if plain.Title != "" || plain.Icons != nil || plain.OutputSchema.Type != "" {
		t.Errorf("a tool without metadata must arrive without it, got %+v", plain)
	}
}

// TestToolMetadataIsValidated: icons that are not a list, or an output schema that is not an
// object, are refused rather than served.
func TestToolMetadataIsValidated(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	for _, body := range []string{
		`{"tools":[{"name":"bad","icons":{"src":"x"}}]}`,
		`{"tools":[{"name":"bad","outputSchema":["string"]}]}`,
	} {
		resp, err := http.Post(fleet.URL()+"/control/alpha/tools/set", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, resp.StatusCode)
		}
	}
}
