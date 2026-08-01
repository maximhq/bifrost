package compat

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// maxToolNameLen is the strictest function-name limit across the providers
	// that receive flattened tools (OpenAI-compatible providers and Gemini cap
	// names at 64 characters; Anthropic allows 128).
	maxToolNameLen = 64
	// nameHashSuffixLen is the number of hex characters appended to a truncated
	// name to keep distinct inputs distinct.
	nameHashSuffixLen = 8
	// namespaceSeparator joins a namespace to the tool name it scopes.
	namespaceSeparator = "__"
	// namespaceKeySeparator joins a namespace and a tool name into a map key. It
	// is a byte that cannot appear in a sanitized name, so the pair is
	// unambiguous even when either half contains the namespace separator.
	namespaceKeySeparator = "\x00"
)

// namespaceToolOrigin records where a flattened-and-renamed tool came from.
type namespaceToolOrigin struct {
	Namespace    string
	OriginalName string
}

// namespaceToolMap holds the renames performed by flattenNamespaceTools so the
// original (namespace, name) pair can be restored on the way out and re-applied
// to conversation history on subsequent turns.
type namespaceToolMap struct {
	// toOrigin maps a flattened (prefixed) name back to its origin. Used to
	// restore provider responses.
	toOrigin map[string]namespaceToolOrigin
	// toPrefixed maps namespaceKey(namespace, originalName) to the prefixed
	// name. Used to rewrite historical function_call items in the request.
	toPrefixed map[string]string
	// namespacesByName maps an original tool name to every namespace that
	// declares it, so a bare name can be disambiguated (or detected as
	// ambiguous) when a client drops the namespace field.
	namespacesByName map[string][]string
}

func newNamespaceToolMap() *namespaceToolMap {
	return &namespaceToolMap{
		toOrigin:         make(map[string]namespaceToolOrigin),
		toPrefixed:       make(map[string]string),
		namespacesByName: make(map[string][]string),
	}
}

// len reports how many tools were renamed. It is nil-safe so callers can guard
// with a single check.
func (m *namespaceToolMap) len() int {
	if m == nil {
		return 0
	}
	return len(m.toOrigin)
}

func (m *namespaceToolMap) add(namespace, originalName, prefixedName string) {
	m.toOrigin[prefixedName] = namespaceToolOrigin{Namespace: namespace, OriginalName: originalName}
	m.toPrefixed[namespaceKey(namespace, originalName)] = prefixedName
	if !slices.Contains(m.namespacesByName[originalName], namespace) {
		m.namespacesByName[originalName] = append(m.namespacesByName[originalName], namespace)
	}
}

func namespaceKey(namespace, name string) string {
	return namespace + namespaceKeySeparator + name
}

// sanitizeNameSegment maps an arbitrary string onto [a-zA-Z0-9_-] and strips
// leading and trailing underscores. The result is always ASCII, which makes the
// byte-wise truncation in capToolNameLen safe.
func sanitizeNameSegment(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// makePrefixedName builds the flattened name for a tool declared inside a
// namespace, e.g. namespace "mcp__node_repl__" + tool "bash" -> "mcp__node_repl__bash".
// Namespaces are prefixed unconditionally so two namespaces exposing the same
// tool name stay distinguishable to both the provider and the model.
func makePrefixedName(namespace, name string) string {
	ns := sanitizeNameSegment(namespace)
	base := sanitizeNameSegment(name)

	candidate := base
	if ns != "" {
		candidate = ns + namespaceSeparator + base
	}
	if candidate == "" {
		candidate = "tool"
	}
	candidate = capToolNameLen(candidate)
	// Gemini additionally requires the first character to be a letter or "_".
	if candidate[0] >= '0' && candidate[0] <= '9' {
		candidate = capToolNameLen("_" + candidate)
	}
	return candidate
}

// capToolNameLen truncates an over-long name and appends a hash of the full name
// so that names sharing a long prefix do not collapse onto each other.
func capToolNameLen(name string) string {
	if len(name) <= maxToolNameLen {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return name[:maxToolNameLen-1-nameHashSuffixLen] + "_" + hex.EncodeToString(sum[:nameHashSuffixLen/2])
}

// resolveUniqueName returns a name that is not already claimed, rehashing until
// it finds a free one. This covers a generated prefix colliding with a top-level
// function tool or with a tool injected by the MCP layer.
func resolveUniqueName(candidate string, taken map[string]struct{}) string {
	for i := 2; ; i++ {
		if _, exists := taken[candidate]; !exists {
			return candidate
		}
		sum := sha256.Sum256([]byte(candidate + strconv.Itoa(i)))
		trimmed := candidate
		if len(trimmed) > maxToolNameLen-1-nameHashSuffixLen {
			trimmed = trimmed[:maxToolNameLen-1-nameHashSuffixLen]
		}
		candidate = trimmed + "_" + hex.EncodeToString(sum[:nameHashSuffixLen/2])
	}
}

// flattenNamespaceTools expands namespace scoped tools into a flat list of
// tools, renaming each expanded tool to "<namespace>__<name>". It returns the
// mapping needed to undo the rename on the response (see
// restoreNamespaceOnResponse) and to keep conversation history consistent (see
// rewriteHistoryToolNames). Returns nil when no namespace tool was flattened.
//
// Renaming is what keeps two namespaces that expose the same tool name apart:
// without it the flattened list contains duplicate function names, which
// OpenAI-compatible providers reject outright and which makes it impossible to
// map a returned function_call back to the namespace it came from.
func (p *CompatPlugin) flattenNamespaceTools(req *schemas.BifrostResponsesRequest) *namespaceToolMap {
	if req == nil || req.Params == nil {
		return nil
	}
	// ignore openai models or azure hosted openai models
	if req.Provider == schemas.OpenAI || (req.Provider == schemas.Azure && !schemas.IsAnthropicModel(req.Model)) {
		return nil
	}
	hasNamespace := false
	finalSize := len(req.Params.Tools)
	for _, tool := range req.Params.Tools {
		if tool.Type != schemas.ResponsesToolTypeNamespace || tool.ResponsesToolNamespace == nil || tool.ResponsesToolNamespace.Tools == nil {
			continue
		}
		finalSize += len(tool.ResponsesToolNamespace.Tools)
		hasNamespace = true
	}
	if !hasNamespace {
		return nil
	}

	// Names already claimed by non-namespace tools must not be shadowed by a
	// generated prefixed name.
	taken := make(map[string]struct{}, finalSize)
	for _, tool := range req.Params.Tools {
		if tool.Type == schemas.ResponsesToolTypeNamespace || tool.Name == nil {
			continue
		}
		taken[*tool.Name] = struct{}{}
	}

	var nsMap *namespaceToolMap
	flattened := make([]schemas.ResponsesTool, 0, finalSize)
	for _, tool := range req.Params.Tools {
		if tool.Type != schemas.ResponsesToolTypeNamespace {
			flattened = append(flattened, tool)
			continue
		}
		if tool.ResponsesToolNamespace == nil || tool.ResponsesToolNamespace.Tools == nil {
			continue
		}
		namespace := ""
		if tool.Name != nil {
			namespace = *tool.Name
		}
		for _, inner := range tool.ResponsesToolNamespace.Tools {
			if namespace == "" || inner.Name == nil || *inner.Name == "" {
				flattened = append(flattened, inner)
				continue
			}
			original := *inner.Name
			prefixed := resolveUniqueName(makePrefixedName(namespace, original), taken)
			taken[prefixed] = struct{}{}
			// Assign a fresh pointer: inner.Name is shared with the caller's
			// tool definition, so writing through it would mutate their request.
			inner.Name = &prefixed
			flattened = append(flattened, inner)
			if nsMap == nil {
				nsMap = newNamespaceToolMap()
			}
			nsMap.add(namespace, original, prefixed)
		}
	}
	req.Params.Tools = flattened
	return nsMap
}

// rewriteHistoryToolNames renames historical function_call items in the request
// input to the flattened names used for this turn. Providers match tool calls by
// name only and ignore the namespace field (see
// core/providers/anthropic/responses.go convertBifrostFunctionCallToAnthropicToolUse),
// so leaving a prior turn's bare name in place would break the conversation as
// soon as the tool list is renamed.
//
// The rewrite is copy-on-write: cloneBifrostReq does not copy Input, and
// ResponsesToolMessage is a pointer shared with the caller's request, so
// mutating it in place would corrupt their data. Turns without a function_call
// pay nothing.
func (p *CompatPlugin) rewriteHistoryToolNames(req *schemas.BifrostResponsesRequest, nsMap *namespaceToolMap) {
	if req == nil || nsMap.len() == 0 || len(req.Input) == 0 {
		return
	}
	cloned := false
	for i := range req.Input {
		msg := &req.Input[i]
		if msg.Type == nil || *msg.Type != schemas.ResponsesMessageTypeFunctionCall {
			continue
		}
		tm := msg.ResponsesToolMessage
		if tm == nil || tm.Name == nil || *tm.Name == "" {
			continue
		}
		prefixed, ok := p.lookupPrefixedName(nsMap, tm.Namespace, *tm.Name)
		if !ok || prefixed == *tm.Name {
			continue
		}
		if !cloned {
			req.Input = slices.Clone(req.Input)
			cloned = true
		}
		rewritten := *tm
		rewritten.Name = &prefixed
		req.Input[i].ResponsesToolMessage = &rewritten
	}
}

// lookupPrefixedName resolves the flattened name for a historical function_call.
// The namespace field is the reliable signal (it is what restoreNamespaceOnMessage
// wrote back on the previous turn); when a client drops it, a bare name can only
// be resolved if exactly one namespace declares it.
func (p *CompatPlugin) lookupPrefixedName(nsMap *namespaceToolMap, namespace *string, name string) (string, bool) {
	if namespace != nil && *namespace != "" {
		prefixed, ok := nsMap.toPrefixed[namespaceKey(*namespace, name)]
		return prefixed, ok
	}
	namespaces := nsMap.namespacesByName[name]
	switch len(namespaces) {
	case 0:
		return "", false
	case 1:
		prefixed, ok := nsMap.toPrefixed[namespaceKey(namespaces[0], name)]
		return prefixed, ok
	default:
		if p.logger != nil {
			p.logger.Warn("compat: historical function_call has no namespace and its tool name exists in multiple namespaces; leaving the name unchanged",
				"tool", name, "namespaces", namespaces)
		}
		return "", false
	}
}

// restoreNamespaceOnResponse undoes the flattening rename on function_call items
// in a responses result (streaming or non-streaming). Providers echo back the
// flattened name and know nothing about namespaces, but clients that use
// namespace tools (e.g. Codex) identify a tool by its (namespace, name) pair and
// fail with "unsupported tool <name>" on anything else.
func restoreNamespaceOnResponse(result *schemas.BifrostResponse, nsMap *namespaceToolMap) {
	if result == nil || nsMap.len() == 0 {
		return
	}
	if result.ResponsesResponse != nil {
		restoreNamespaceOnMessages(result.ResponsesResponse.Output, nsMap)
	}
	if stream := result.ResponsesStreamResponse; stream != nil {
		// output_item.added / output_item.done events carry the function_call item.
		restoreNamespaceOnMessage(stream.Item, nsMap)
		// response.completed carries the full output array.
		if stream.Response != nil {
			restoreNamespaceOnMessages(stream.Response.Output, nsMap)
		}
	}
}

// restoreNamespaceOnMessages restores every function_call item in the slice.
func restoreNamespaceOnMessages(messages []schemas.ResponsesMessage, nsMap *namespaceToolMap) {
	for i := range messages {
		restoreNamespaceOnMessage(&messages[i], nsMap)
	}
}

// restoreNamespaceOnMessage restores the original name and namespace of a single
// function_call item that was renamed during flattening.
func restoreNamespaceOnMessage(msg *schemas.ResponsesMessage, nsMap *namespaceToolMap) {
	if msg == nil || msg.Type == nil || *msg.Type != schemas.ResponsesMessageTypeFunctionCall {
		return
	}
	tm := msg.ResponsesToolMessage
	if tm == nil || tm.Name == nil {
		return
	}
	origin, ok := nsMap.toOrigin[*tm.Name]
	if !ok {
		return
	}
	// The name is always restored: the provider only ever saw the prefixed form,
	// while the client only knows the original one.
	originalName := origin.OriginalName
	tm.Name = &originalName
	// Never overwrite a namespace the provider already supplied.
	if tm.Namespace == nil || *tm.Namespace == "" {
		namespace := origin.Namespace
		tm.Namespace = &namespace
	}
}
