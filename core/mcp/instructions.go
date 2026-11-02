package mcp

// Forwarding of upstream MCP servers' own `instructions` — the string a server may return from
// the initialize handshake describing how its tools should be used. Kept in its own file rather
// than utils.go: the aggregation format, its size bounds and the scoping rule are one coherent
// decision, and burying them among unrelated helpers made both harder to find.

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// instructionTagPrefix names the block tag. Tag-shaped occurrences of it in upstream text
	// are neutralized so a server cannot draw a boundary and speak as another; bare mentions
	// in prose are left alone. See instructionTagPattern.
	instructionTagPrefix = "mcp_server"
)

// GetServerInstructions returns the initialize `instructions` of every MCP client this
// request may see, labeled with the client they came from and ordered deterministically.
//
// Visibility follows GetToolPerClient, so a narrowed caller never reads the guidance of a server it
// was not granted.
//
// Code mode clients are excluded: their tools are never sent, so their guidance rides in the
// stub instead. See InstructionsCommentBlock.
func (m *MCPManager) GetServerInstructions(ctx context.Context) []schemas.MCPServerInstructions {
	visible := m.GetToolPerClient(ctx)
	// Runs on every MCP-bearing request: reads three fields under the lock instead of copying client state.
	m.mu.RLock()
	var result []schemas.MCPServerInstructions
	for _, client := range m.clientMap {
		config := client.ExecutionConfig
		if config == nil || config.IsCodeModeClient || client.ServerInstructions == "" || len(visible[config.Name]) == 0 {
			continue
		}
		result = append(result, schemas.MCPServerInstructions{
			ClientName:   config.Name,
			Instructions: client.ServerInstructions,
			MaxLength:    config.MaxInstructionsLength,
		})
	}
	m.mu.RUnlock()
	slices.SortFunc(result, func(a, b schemas.MCPServerInstructions) int { return strings.Compare(a.ClientName, b.ClientName) })
	return result
}

// InstructionCaps bounds forwarded instructions in bytes. A zero field takes the built-in
// default, so callers that do not care can pass the zero value.
type InstructionCaps struct {
	PerClient int
	Total     int
}

func (c InstructionCaps) orDefaults() InstructionCaps {
	if c.PerClient <= 0 {
		c.PerClient = schemas.DefaultMaxInstructionsPerClient
	}
	if c.Total <= 0 {
		c.Total = schemas.DefaultMaxInstructionsTotal
	}
	// A per-client cap above the total is unspendable, so clamp rather than drop the server.
	if c.PerClient > c.Total {
		c.PerClient = c.Total
	}
	return c
}

// AggregateServerInstructions renders one tagged block per upstream, naming the client each
// came from so the reader can tell whose rule is whose. Returns "" for no parts, which callers
// must render as an absent field rather than an empty one.
//
// Tagged rather than markdown-headed on purpose. Upstream instructions are themselves usually
// markdown and routinely contain their own "## " headings (the MCP everything reference server
// has four), so a heading delimiter is indistinguishable from the content it wraps: a reader
// cannot tell where one server's guidance ends and the next begins. Worse, it would let an
// upstream forge a boundary and attribute its own text to a server the caller trusts. A tag the
// payload cannot contain — see sanitizeInstructionBody — is a boundary only this function can draw.
func AggregateServerInstructions(parts []schemas.MCPServerInstructions, caps InstructionCaps) string {
	caps = caps.orDefaults()
	var b strings.Builder
	remaining := caps.Total
	written := 0

	for i, part := range parts {
		body := sanitizeInstructionBody(part.Instructions)
		separator := 0
		if written > 0 {
			separator = 2 // the "\n\n" between blocks
		}
		// A server's own cap wins over the global one. The remaining-total clamp below still
		// applies, so an override raises one server's ceiling, never the aggregate's.
		limit := caps.PerClient
		if part.MaxLength > 0 {
			limit = part.MaxLength
		}
		if avail := remaining - separator - blockOverhead(part.ClientName, len(body)); avail < limit {
			limit = avail
		}
		if limit <= 0 {
			b.WriteString(fmt.Sprintf("\n[%d more server(s) omitted: instruction size limit reached]", len(parts)-i))
			break
		}
		text, omitted := truncateInstructions(body, limit)
		block := instructionBlock(part.ClientName, text, omitted)
		if separator+len(block) > remaining {
			// Out of budget: name what was dropped rather than trailing off silently,
			// so a truncated aggregate never reads like a complete one.
			b.WriteString(fmt.Sprintf("\n[%d more server(s) omitted: instruction size limit reached]", len(parts)-i))
			break
		}
		if separator > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(block)
		remaining -= separator + len(block)
		written++
	}
	return b.String()
}

// instructionBlock wraps one upstream's text in its tagged block. The client name is attribute
// escaped for the same reason the body is sanitized: it is operator-supplied, and a quote in it
// would otherwise break out of the attribute.
func instructionBlock(clientName, text string, omitted int) string {
	var b strings.Builder
	b.WriteString(`<mcp_server name="`)
	b.WriteString(escapeInstructionAttr(clientName))
	b.WriteString("\">\n")
	b.WriteString(text)
	if omitted > 0 {
		b.WriteString(fmt.Sprintf("\n[truncated: %d bytes omitted]", omitted))
	}
	b.WriteString("\n</mcp_server>")
	return b.String()
}

// blockOverhead is the byte cost of the tags plus a worst-case truncation notice.
func blockOverhead(clientName string, bodyLen int) int {
	return len(instructionBlock(clientName, "", 0)) + len(fmt.Sprintf("\n[truncated: %d bytes omitted]", bodyLen))
}

// instructionTagPattern matches a literal block tag, open or close, in any case. Bare mentions of
// the name in prose are not matched: without a bracket they cannot draw a boundary, and rewriting
// them would corrupt the server's own words.
var instructionTagPattern = regexp.MustCompile(`(?i)<\s*/?\s*` + instructionTagPrefix + `\b`)

// sanitizeInstructionBody neutralizes any literal block tag in an upstream's text, so a boundary
// cannot be forged by the content it delimits. Both forms matter, not just the close: an opening
// tag left intact nests inside the current block and captures the real close as its own, so the
// text after it reads as though it came from whichever server the forged tag names. The bracket is
// escaped rather than the name removed, so the text still reads as written but cannot delimit.
func sanitizeInstructionBody(s string) string {
	return instructionTagPattern.ReplaceAllStringFunc(s, func(m string) string {
		return "&lt;" + m[1:]
	})
}

// escapeInstructionAttr makes clientName safe inside the name="..." attribute.
func escapeInstructionAttr(s string) string {
	r := strings.NewReplacer(`&`, "&amp;", `"`, "&quot;", `<`, "&lt;", `>`, "&gt;")
	return r.Replace(s)
}

// truncateInstructions cuts s to at most limit bytes on a rune boundary, returning the kept
// text and how many bytes were dropped. Cutting mid-rune would emit invalid UTF-8, which some
// MCP clients reject for the whole initialize response rather than just the field.
func truncateInstructions(s string, limit int) (string, int) {
	if len(s) <= limit {
		return s, 0
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], len(s) - cut
}

// InstructionsCommentBlock renders one upstream's instructions for the code mode stub files. The
// "#" prefix is also the sanitization: commented text cannot forge a def line for the model to call.
//
// maxLength is the client's override, zero taking DefaultMaxInstructionsPerClient. The total cap
// does not apply — a stub carries one server, not the set.
func InstructionsCommentBlock(instructions string, maxLength int) string {
	if instructions == "" {
		return ""
	}
	limit := maxLength
	if limit <= 0 {
		limit = schemas.DefaultMaxInstructionsPerClient
	}
	// CR normalized to LF before the cut, so a bare \r cannot carry text past the "#" on a
	// reader that treats it as a line break — the whole point of commenting the block.
	normalized := strings.ReplaceAll(strings.ReplaceAll(instructions, "\r\n", "\n"), "\r", "\n")
	text, omitted := truncateInstructions(normalized, limit)

	var b strings.Builder
	b.WriteString("# ---- server instructions (from the upstream handshake) ----\n")
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			b.WriteString("#\n")
			continue
		}
		b.WriteString("# ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	if omitted > 0 {
		b.WriteString(fmt.Sprintf("# [truncated: %d bytes omitted]\n", omitted))
	}
	b.WriteString("# ---- end server instructions ----\n")
	return b.String()
}

// InstructionCaps returns the configured caps as-is, so a caller that only needs the per-client
// bound is not also clamped by the aggregate total.
func (m *MCPManager) InstructionCaps() InstructionCaps {
	return m.toolsManager.instructionCaps()
}

// GetAggregatedServerInstructions renders the instructions of every client visible to ctx
// into a single labeled, size-bounded block. See GetServerInstructions for the scoping rule.
func (m *MCPManager) GetAggregatedServerInstructions(ctx context.Context) string {
	return AggregateServerInstructions(m.GetServerInstructions(ctx), m.toolsManager.instructionCaps())
}
