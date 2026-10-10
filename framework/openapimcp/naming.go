package openapimcp

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

var (
	nonNameRunes   = regexp.MustCompile(`[^A-Za-z0-9_]+`)
	repeatedUnders = regexp.MustCompile(`_{2,}`)
	pathVariable   = regexp.MustCompile(`\{([^}]*)\}`)
)

// toolNameBudget is the longest operation part of a tool name that keeps
// "<client>-<tool>" within MaxToolNameLen, never below minToolNameBudget.
func toolNameBudget(clientName string) int {
	budget := MaxToolNameLen - len(clientName) - 1
	if budget < minToolNameBudget {
		return minToolNameBudget
	}
	return budget
}

// ToolName derives the tool name for an operation: the sanitized operationId,
// or "<method>_<path>" when the spec declares none. Only [A-Za-z0-9_] survive
// (hyphens would collide with the gateway's "<client>-<tool>" prefix and break
// code mode's tool-name parsing), a leading digit is prefixed with "op_", and
// the result is cut to maxLen.
func ToolName(op Operation, maxLen int) string {
	base := op.ID
	if strings.TrimSpace(base) == "" {
		base = fallbackName(op)
	}
	name := sanitizeName(base)
	if name == "" {
		name = sanitizeName(fallbackName(op))
	}
	if name == "" {
		name = strings.ToLower(op.Method) + "_op"
	}
	return truncateName(name, maxLen)
}

func fallbackName(op Operation) string {
	path := strings.Trim(op.Path, "/")
	path = pathVariable.ReplaceAllStringFunc(path, func(m string) string {
		inner := strings.Trim(m, "{}")
		return "by_" + inner
	})
	path = strings.ReplaceAll(path, "/", "_")
	if path == "" {
		path = "root"
	}
	return strings.ToLower(op.Method) + "_" + path
}

func sanitizeName(s string) string {
	s = nonNameRunes.ReplaceAllString(s, "_")
	s = repeatedUnders.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		return ""
	}
	if s[0] >= '0' && s[0] <= '9' {
		s = "op_" + s
	}
	return s
}

func truncateName(name string, maxLen int) string {
	if maxLen <= 0 || len(name) <= maxLen {
		return name
	}
	return strings.TrimRight(name[:maxLen], "_")
}

// DedupeToolNames gives colliding names a deterministic "_2", "_3", ... suffix
// in walk order (paths sorted, methods in fixed order), re-truncating so the
// suffixed name still fits. It returns one warning per renamed tool.
func DedupeToolNames(tools []Tool, maxLen int) []string {
	seen := map[string]int{}
	taken := map[string]bool{}
	for _, t := range tools {
		seen[t.Name]++
	}
	// Names that are already unique keep themselves; duplicates must steer
	// around them, so they are reserved before any suffixing starts.
	for name, n := range seen {
		if n == 1 {
			taken[name] = true
		}
	}
	var warnings []string
	counters := map[string]int{}
	for i := range tools {
		name := tools[i].Name
		if seen[name] == 1 {
			continue
		}
		base := name
		for {
			counters[base]++
			n := counters[base]
			candidate := base
			if n > 1 {
				suffix := "_" + strconv.Itoa(n)
				candidate = truncateName(base, maxLen-len(suffix)) + suffix
			}
			if !taken[candidate] {
				if candidate != name {
					warnings = append(warnings, fmt.Sprintf("tool name %q is used by more than one operation; %s %s was renamed to %q", name, tools[i].Operation.Method, tools[i].Operation.Path, candidate))
				}
				tools[i].Name = candidate
				taken[candidate] = true
				break
			}
		}
	}
	return warnings
}

// BuildDescription composes the tool description from the operation's summary,
// description (capped) and its HTTP shape, so the model always sees the verb
// and path even when the spec carries no prose.
func BuildDescription(op Operation) string {
	var parts []string
	if op.Summary != "" {
		parts = append(parts, op.Summary)
	}
	if op.Description != "" && op.Description != op.Summary {
		desc := op.Description
		if len(desc) > maxDescriptionLen {
			desc = strings.TrimSpace(desc[:maxDescriptionLen]) + "…"
		}
		parts = append(parts, desc)
	}
	shape := op.Method + " " + op.Path
	if op.Deprecated {
		shape += " [deprecated]"
	}
	parts = append(parts, shape)
	return strings.Join(parts, "\n\n")
}

// annotationsFor derives MCP tool annotations from the HTTP method, which is
// what drives the gateway's auto-retry opt-out and clients' confirmation UX.
func annotationsFor(method string) mcp.ToolAnnotation {
	b := func(v bool) *bool { return &v }
	ann := mcp.ToolAnnotation{OpenWorldHint: b(true)}
	switch method {
	case "GET", "HEAD", "QUERY", "OPTIONS":
		ann.ReadOnlyHint = b(true)
		ann.DestructiveHint = b(false)
		ann.IdempotentHint = b(true)
	case "PUT":
		ann.ReadOnlyHint = b(false)
		ann.DestructiveHint = b(false)
		ann.IdempotentHint = b(true)
	case "DELETE":
		ann.ReadOnlyHint = b(false)
		ann.DestructiveHint = b(true)
		ann.IdempotentHint = b(true)
	default: // POST, PATCH, custom methods
		ann.ReadOnlyHint = b(false)
		ann.DestructiveHint = b(true)
		ann.IdempotentHint = b(false)
	}
	return ann
}
