//go:build !tinygo && !wasm

package mcp

// The code mode catalogue: the names and purposes of the code mode servers a caller can reach,
// told to the model up front. Code mode keeps every upstream tool out of the tool list, so
// without it the model learns that grafana or zoom exists only by calling listToolFiles, which
// it has no reason to do until a user tells it to look there. The catalogue carries no tool
// schemas: one line per server, appended to the listToolFiles description, and a names-only
// pointer in the server instructions.
//
// Both are built from the caller's CallerDiscovery, so a server the caller cannot reach is never
// named. Output is sorted and depends only on the servers, their descriptions and (for a server
// with no description) its tool names, so it is byte-stable across requests and does not churn
// the prompt cache.

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// codeModeCatalogueMaxServers bounds the catalogue and the pointer; the rest are counted, and
	// listToolFiles still lists them all.
	codeModeCatalogueMaxServers = 30
	// codeModeCatalogueSampleTools is how many tool names stand in for a missing description.
	codeModeCatalogueSampleTools = 5
)

// codeModeServer is one catalogue entry: a code mode client the caller can reach, with the tools
// it sees on it, sorted by name. Only model-visible tools count: code mode cannot call an app-only one.
type codeModeServer struct {
	name        string
	description string
	tools       []schemas.ChatTool
}

// listsCodeModeCatalogue reports whether a listing on ctx is for one caller. The gateway's own
// install and sync listings (WithAllTools, WithGatewayTools) are shared by every caller, so a
// catalogue built there would name servers to callers that cannot reach them.
func listsCodeModeCatalogue(ctx context.Context) bool {
	listing, _ := ctx.Value(schemas.BifrostContextKeyMCPToolListing).(string)
	return listing != listAllTools && listing != listGatewayTools
}

// codeModeCatalogue renders the block appended to the listToolFiles description, "" for no servers.
// servers must be sorted by name.
func codeModeCatalogue(servers []codeModeServer) string {
	if len(servers) == 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(64 * len(servers))
	b.WriteString("\n\nServers available through code mode:")
	for i, server := range servers {
		if i == codeModeCatalogueMaxServers {
			fmt.Fprintf(&b, "\n- ...and %d more; listToolFiles lists every server.", len(servers)-i)
			break
		}
		b.WriteString("\n- ")
		b.WriteString(server.name)
		if description := catalogueDescription(server.description); description != "" {
			b.WriteString(": ")
			b.WriteString(description)
			continue
		}
		// No description: the tool names say what the server does. They are the canonical names
		// listToolFiles shows, which also keeps an upstream's name from starting a line of its own.
		count := modelVisibleTools(server.tools)
		if count == 1 {
			b.WriteString(" (1 tool: ")
		} else {
			fmt.Fprintf(&b, " (%d tools: ", count)
		}
		written := 0
		for _, tool := range server.tools {
			if !tool.MCP.ModelVisible() {
				continue
			}
			if written == codeModeCatalogueSampleTools {
				b.WriteString(", ...")
				break
			}
			if written > 0 {
				b.WriteString(", ")
			}
			b.WriteString(parseToolName(StripClientPrefix(tool.Function.Name, server.name)))
			written++
		}
		b.WriteString(")")
	}
	return b.String()
}

// codeModePointer renders the one line the server instructions carry about code mode servers,
// "" for none. names must be sorted.
func codeModePointer(names []string) string {
	if len(names) == 0 {
		return ""
	}
	listed := names
	if len(listed) > codeModeCatalogueMaxServers {
		listed = listed[:codeModeCatalogueMaxServers]
	}
	pointer := "MCP servers available through code mode (start with listToolFiles to use them): " + strings.Join(listed, ", ")
	if more := len(names) - len(listed); more > 0 {
		pointer += fmt.Sprintf(", and %d more", more)
	}
	return pointer + "."
}

// aggregateWithCodeModePointer renders the instruction blocks and the code mode pointer within one
// budget, so max_instructions_total bounds the whole text the model is sent. The pointer is a
// single line that tells the model code mode servers exist: when both do not fit, the blocks give
// way and the pointer stays whole. A pointer longer than the whole budget is cut, like any text.
func aggregateWithCodeModePointer(parts []schemas.MCPServerInstructions, caps InstructionCaps, codeModeClients []string) string {
	pointer := codeModePointer(codeModeClients)
	if pointer == "" {
		return AggregateServerInstructions(parts, caps)
	}
	caps = caps.orDefaults()
	if len(pointer) >= caps.Total {
		cut, _ := truncateInstructions(pointer, caps.Total)
		return cut
	}
	if len(parts) == 0 {
		return pointer
	}
	caps.Total -= len(pointer) + len("\n\n")
	if caps.Total <= 0 {
		return pointer
	}
	blocks := AggregateServerInstructions(parts, caps)
	if blocks == "" {
		return pointer
	}
	return blocks + "\n\n" + pointer
}

// catalogueDescription flattens a description to one bounded line, so text written before the
// write-time check, or set through the Go SDK, cannot start a line of its own and pose as another
// server's entry.
func catalogueDescription(description string) string {
	flat := description
	if !isOneLine(description) {
		flat = strings.Join(strings.Fields(description), " ")
	}
	// Cut on characters, the unit the description is validated in, so a valid one is never cut.
	if utf8.RuneCountInString(flat) <= schemas.MaxMCPClientDescriptionLength {
		return flat
	}
	runes := 0
	for i := range flat {
		if runes == schemas.MaxMCPClientDescriptionLength {
			return flat[:i]
		}
		runes++
	}
	return flat
}

// modelVisibleTools counts the tools a model, and so code mode, may call.
func modelVisibleTools(tools []schemas.ChatTool) int {
	count := 0
	for _, tool := range tools {
		if tool.MCP.ModelVisible() {
			count++
		}
	}
	return count
}

// isOneLine reports whether s is already what catalogueDescription makes of it: no whitespace but
// single inner spaces. Descriptions are checked on write, so this is the common case and costs no
// allocation.
func isOneLine(s string) bool {
	for i, r := range s {
		if !unicode.IsSpace(r) {
			continue
		}
		if r != ' ' || i == 0 || i == len(s)-1 || s[i+1] == ' ' {
			return false
		}
	}
	return true
}

// withCatalogue returns tool with catalogue appended to its description. The function is copied,
// so the tool GetTools handed out is left as it was.
func withCatalogue(tool schemas.ChatTool, catalogue string) schemas.ChatTool {
	function := *tool.Function
	description := catalogue
	if function.Description != nil {
		description = *function.Description + catalogue
	}
	function.Description = &description
	tool.Function = &function
	tool.InvalidateSerialized() // a cached encoding would still carry the old description
	return tool
}
