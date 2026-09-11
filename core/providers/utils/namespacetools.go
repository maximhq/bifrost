package utils

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/cespare/xxhash/v2"
	"github.com/maximhq/bifrost/core/schemas"
)

// namespaceToolSeparator joins a namespace and a nested function name into one flat
// tool name. Double underscore matches the flat "mcp__server__tool" convention Codex
// used before the namespace type existed, and the separator Bedrock's own tool-name
// aliaser already splits on.
const namespaceToolSeparator = "__"

// ResponsesNamespaceToolsSupported reports whether the OpenAI Responses `namespace`
// tool type can be sent to baseProvider as is. It is the answer for providers that do
// not implement schemas.ResponsesNamespaceToolProvider; baseProvider must already be
// resolved through schemas.ResolveBaseProvider so a custom provider wrapping OpenAI is
// answered like OpenAI.
//
// Wires that structurally lack a namespace container answer false regardless of the
// datasheet (wireCannotCarryNamespaceTools). Elsewhere a datasheet row
// (supports_namespace_tools) for the canonical model wins, and with no row
// DefaultNamespaceToolSupport applies.
func ResponsesNamespaceToolsSupported(ctx *schemas.BifrostContext, baseProvider schemas.ModelProvider, model string) bool {
	if wireCannotCarryNamespaceTools(ctx, baseProvider, model) {
		return false
	}
	caps := schemas.ResolveModelCaps(baseProvider, schemas.ResolveCanonicalModel(ctx, model))
	return caps.SupportsNamespaceTools(DefaultNamespaceToolSupport(ctx, baseProvider, model))
}

// wireCannotCarryNamespaceTools reports the wires Bifrost itself converts to that have
// no namespace container at all: the Anthropic Messages API (Anthropic, and Claude on
// Azure or Bedrock Mantle) and the Gemini API (Gemini, Vertex). A datasheet row cannot
// enable namespace tools there; flattening is the only option. Third-party
// OpenAI-shaped wires are not listed, since a row is exactly how one of them declares
// that it has started accepting the container. Bedrock answers for itself through
// schemas.ResponsesNamespaceToolProvider, from its resolved surface.
func wireCannotCarryNamespaceTools(ctx *schemas.BifrostContext, baseProvider schemas.ModelProvider, model string) bool {
	switch baseProvider {
	case schemas.Anthropic, schemas.Gemini, schemas.Vertex:
		return true
	case schemas.Azure, schemas.BedrockMantle:
		return schemas.IsAnthropicModelFamily(ctx, model)
	default:
		return false
	}
}

// DefaultNamespaceToolSupport is the per-provider answer used when the datasheet has
// no row for the model. Only OpenAI's own wire accepts the type. Azure and Bedrock
// Mantle expose that wire for OpenAI-family models, but route Claude to the Anthropic
// Messages surface, which does not. Everything else, including every OpenAI-compatible
// third party, gets false: flattened function tools are valid on every wire, while a
// namespace object on a wire that does not know it is exactly the failure this file
// exists to prevent (#7048).
func DefaultNamespaceToolSupport(ctx *schemas.BifrostContext, baseProvider schemas.ModelProvider, model string) bool {
	switch baseProvider {
	case schemas.OpenAI:
		return true
	case schemas.Azure, schemas.BedrockMantle:
		return !schemas.IsAnthropicModelFamily(ctx, model)
	default:
		return false
	}
}

// FlattenResponsesNamespaceTools rewrites a Responses request for a wire that does not
// understand namespace tools. Every nested function tool is hoisted to the top level
// under the name "<namespace>__<function>", so two namespaces that both define "js"
// no longer collide. The alias map rides on the prepared copy
// (NamespaceToolAliases) so the response path can hand the caller back the OpenAI shape.
//
// The result is a SHALLOW COPY whenever anything changes, never the shared request:
// req survives across retries and fallbacks, and a later attempt against OpenAI must
// still see the caller's namespaces. When nothing needs flattening the same pointer is
// returned, and it carries no alias map.
//
// Input history is rewritten to match: a function_call item carrying `namespace` is
// re-aliased so a multi-turn conversation keeps referring to the tool definitions the
// provider will see. tool_choice names are resolved the same way. A name that is still
// duplicated after flattening, or a tool_choice that matches functions in more than one
// namespace, is a 400 here rather than an opaque upstream rejection.
func FlattenResponsesNamespaceTools(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesRequest, *schemas.BifrostError) {
	if req == nil || req.Params == nil || !hasNamespaceTool(req.Params.Tools) {
		return req, nil
	}

	limit := ResolveToolNameLimit(ctx, schemas.ResolveBaseProvider(ctx, req.Provider), req.Model)
	aliases := make(map[string]schemas.NamespaceToolAlias)
	// byFunction resolves a bare nested name to every alias that carries it, for
	// tool_choice and for history items that name a function without a namespace.
	byFunction := make(map[string][]string)
	flattened := make([]schemas.ResponsesTool, 0, len(req.Params.Tools))
	for _, tool := range req.Params.Tools {
		if tool.Type != schemas.ResponsesToolTypeNamespace {
			flattened = append(flattened, tool)
			continue
		}
		if tool.Name == nil || *tool.Name == "" || tool.ResponsesToolNamespace == nil || len(tool.ResponsesToolNamespace.Tools) == 0 {
			schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedUnsupportedTools, string(schemas.ResponsesToolTypeNamespace))
			continue
		}
		namespace := *tool.Name
		for _, nested := range tool.ResponsesToolNamespace.Tools {
			if nested.Type != schemas.ResponsesToolTypeFunction || nested.Name == nil || *nested.Name == "" {
				schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedUnsupportedTools, namespace+namespaceToolSeparator+string(nested.Type))
				continue
			}
			alias := namespaceToolAlias(namespace, *nested.Name, limit)
			hoisted := nested
			hoisted.Name = new(alias)
			flattened = append(flattened, hoisted)
			aliases[alias] = schemas.NamespaceToolAlias{Namespace: namespace, Name: *nested.Name}
			byFunction[*nested.Name] = append(byFunction[*nested.Name], alias)
		}
	}

	if bifrostErr := checkUniqueToolNames(flattened, aliases); bifrostErr != nil {
		return nil, bifrostErr
	}

	cp := *req
	params := *req.Params
	params.Tools = flattened
	cp.Params = &params

	if len(aliases) > 0 {
		cp.Input = realiasNamespacedFunctionCalls(req.Input, aliases, limit)
		toolChoice, bifrostErr := realiasToolChoice(req.Params.ToolChoice, flattened, byFunction, aliases)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		params.ToolChoice = toolChoice
		cp.NamespaceToolAliases = aliases
	}
	return &cp, nil
}

// RestoreResponsesNamespaceToolCalls rewrites function_call items whose name is a
// flattened alias back to the caller's shape: the bare function name plus the
// namespace field, which is how OpenAI reports a namespaced call. aliases is the map
// the prepared request carried (BifrostResponsesRequest.NamespaceToolAliases). It
// covers the unary response, the stream item events (output_item.added /
// output_item.done) and the terminal stream events that carry the full response.
// No-op when the map is empty, so plain function tools are never touched.
func RestoreResponsesNamespaceToolCalls(aliases map[string]schemas.NamespaceToolAlias, resp *schemas.BifrostResponse) {
	if resp == nil || len(aliases) == 0 {
		return
	}
	if resp.ResponsesResponse != nil {
		restoreNamespacedItems(resp.ResponsesResponse.Output, aliases)
	}
	if stream := resp.ResponsesStreamResponse; stream != nil {
		if stream.Item != nil {
			restoreNamespacedItem(stream.Item, aliases)
		}
		if stream.Response != nil {
			restoreNamespacedItems(stream.Response.Output, aliases)
		}
	}
}

// WrapNamespaceRestorePostHookRunner returns a PostHookRunner that restores the
// caller's tool names on every streaming chunk before delegating, so the post hooks
// and the client both see bare names plus namespaces. Returns the runner unchanged
// when there is nothing to restore.
func WrapNamespaceRestorePostHookRunner(runner schemas.PostHookRunner, aliases map[string]schemas.NamespaceToolAlias) schemas.PostHookRunner {
	if len(aliases) == 0 || runner == nil {
		return runner
	}
	return func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		RestoreResponsesNamespaceToolCalls(aliases, result)
		return runner(ctx, result, bifrostErr)
	}
}

// ToolNameLimit is what one wire accepts as a tool name: a maximum length and the
// character set. Anything outside the set is replaced with "_" before the length
// check.
type ToolNameLimit struct {
	MaxLength int
	unsafe    *regexp.Regexp
}

// Sanitize replaces every run of characters the wire rejects with a single "_".
func (l ToolNameLimit) Sanitize(name string) string {
	return l.unsafe.ReplaceAllString(name, "_")
}

var (
	// [A-Za-z0-9_-] is the charset OpenAI, Anthropic, Bedrock Converse and Fireworks
	// document; Gemini and Vertex also accept "." and ":".
	toolNameUnsafeStrict = regexp.MustCompile(`[^A-Za-z0-9_-]+`)
	toolNameUnsafeGemini = regexp.MustCompile(`[^A-Za-z0-9_.:-]+`)
)

// DefaultToolNameLimit is the documented limit per base provider, used when the
// datasheet row says nothing:
//   - OpenAI: 64, [A-Za-z0-9_-] (openai-openapi FunctionObject.name)
//   - Bedrock and Bedrock Mantle: 64, [A-Za-z0-9_-] (Converse ToolSpecification.name)
//   - Fireworks: 64, [A-Za-z0-9_-] (function-calling guide)
//   - Anthropic: 128, [A-Za-z0-9_-] (Messages API tools[].name)
//   - Gemini and Vertex: 128, [A-Za-z0-9_.:-] (FunctionDeclaration.name)
//   - everything else, including custom providers: the OpenAI-compatible 64. Mistral,
//     Groq, xAI, Cohere, DeepSeek, Cerebras and Ollama document no limit of their own.
func DefaultToolNameLimit(baseProvider schemas.ModelProvider) ToolNameLimit {
	switch baseProvider {
	case schemas.Anthropic:
		return ToolNameLimit{MaxLength: 128, unsafe: toolNameUnsafeStrict}
	case schemas.Gemini, schemas.Vertex:
		return ToolNameLimit{MaxLength: 128, unsafe: toolNameUnsafeGemini}
	default:
		return ToolNameLimit{MaxLength: 64, unsafe: toolNameUnsafeStrict}
	}
}

// ResolveToolNameLimit is DefaultToolNameLimit with the datasheet row's
// tool_name_max_length applied when present. The charset stays the provider's.
func ResolveToolNameLimit(ctx *schemas.BifrostContext, baseProvider schemas.ModelProvider, model string) ToolNameLimit {
	limit := DefaultToolNameLimit(baseProvider)
	caps := schemas.ResolveModelCaps(baseProvider, schemas.ResolveCanonicalModel(ctx, model))
	// A row below the hashed form's floor cannot hold "<hash>_<char>", so it is
	// ignored rather than allowed to make every over-limit alias unrepresentable.
	if row := caps.ToolNameMaxLength(limit.MaxLength); row >= minToolNameLimit {
		limit.MaxLength = row
	}
	return limit
}

// minToolNameLimit is the smallest limit the hashed alias form can honour: the
// 8-hex hash, the "_" separator and one character of the function name.
const minToolNameLimit = namespaceAliasHashLength + 2

// namespaceAliasHashLength is the width of the "%08x" prefix in a hashed alias.
const namespaceAliasHashLength = 8

// namespaceToolAlias returns the flat tool name for one nested function under the
// wire's limit. It is a pure function of (namespace, function, limit): history items
// and tool_choice names are re-aliased by recomputing it within the same attempt, so
// the same inputs must always yield the same string.
//
// The plain "<namespace>__<function>" form is used when it fits. Over the limit the
// name becomes "<8-hex xxhash>_<function>", the same scheme bedrockAliasToolName uses:
// the hash of the full alias keeps the name unique per (namespace, function), and the
// tail keeps the function readable to the model. The response-side alias map is keyed
// on whichever string was produced, so restore never needs to invert this.
func namespaceToolAlias(namespace, function string, limit ToolNameLimit) string {
	full := namespace + namespaceToolSeparator + function
	sanitized := limit.Sanitize(full)
	if len(sanitized) <= limit.MaxLength {
		return sanitized
	}
	// Hash the unsanitized alias so namespaces that differ only in a replaced
	// character still hash apart.
	hash := fmt.Sprintf("%08x", uint32(xxhash.Sum64String(full)))
	semantic := strings.Trim(limit.Sanitize(function), "_")
	if semantic == "" {
		semantic = "tool"
	}
	room := limit.MaxLength - len(hash) - 1
	if room <= 0 {
		// A limit that cannot hold the hash plus a separator keeps the unique part.
		return hash[:min(len(hash), limit.MaxLength)]
	}
	if len(semantic) > room {
		semantic = semantic[:room]
	}
	return hash + "_" + semantic
}

func hasNamespaceTool(tools []schemas.ResponsesTool) bool {
	for _, tool := range tools {
		if tool.Type == schemas.ResponsesToolTypeNamespace {
			return true
		}
	}
	return false
}

// checkUniqueToolNames rejects a flat tool list that still carries a duplicate name,
// which happens when a top-level tool is literally named "<ns>__<fn>" or when two
// namespaces compose to the same alias ("a" + "b__c" vs "a__b" + "c").
func checkUniqueToolNames(tools []schemas.ResponsesTool, aliases map[string]schemas.NamespaceToolAlias) *schemas.BifrostError {
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if tool.Name == nil || *tool.Name == "" {
			continue
		}
		if tool.Type != schemas.ResponsesToolTypeFunction && tool.Type != schemas.ResponsesToolTypeCustom {
			continue
		}
		name := *tool.Name
		if seen[name] {
			message := fmt.Sprintf("tool name %q is not unique after flattening namespace tools", name)
			if alias, ok := aliases[name]; ok {
				message += fmt.Sprintf("; rename function %q in namespace %q or the top-level tool that shares the name", alias.Name, alias.Namespace)
			}
			return NewBifrostBadRequestError(message)
		}
		seen[name] = true
	}
	return nil
}

// realiasNamespacedFunctionCalls returns req.Input with every function_call item that
// carries a namespace rewritten to its alias, so history matches the flattened tool
// definitions. The input slice is copied lazily: when no item changes the original
// slice is returned as is.
func realiasNamespacedFunctionCalls(input []schemas.ResponsesMessage, aliases map[string]schemas.NamespaceToolAlias, limit ToolNameLimit) []schemas.ResponsesMessage {
	var out []schemas.ResponsesMessage
	for i, msg := range input {
		if msg.Type == nil || *msg.Type != schemas.ResponsesMessageTypeFunctionCall || msg.ResponsesToolMessage == nil {
			continue
		}
		call := msg.ResponsesToolMessage
		if call.Namespace == nil || *call.Namespace == "" || call.Name == nil {
			continue
		}
		alias := namespaceToolAlias(*call.Namespace, *call.Name, limit)
		// Sanitization is many-to-one, so alias existence alone does not prove this
		// call's namespace owns it; only the exact (namespace, function) is rewritten.
		owner := schemas.NamespaceToolAlias{Namespace: *call.Namespace, Name: *call.Name}
		if mapped, known := aliases[alias]; !known || mapped != owner {
			continue
		}
		if out == nil {
			out = make([]schemas.ResponsesMessage, len(input))
			copy(out, input)
		}
		rewritten := *call
		rewritten.Name = new(alias)
		rewritten.Namespace = nil
		out[i].ResponsesToolMessage = &rewritten
	}
	if out == nil {
		return input
	}
	return out
}

// realiasToolChoice resolves a forced or allowed tool name against the flattened
// list. A name that already exists at the top level is left alone. A bare nested name
// that lives in exactly one namespace is rewritten to its alias; one that lives in
// several is ambiguous and rejected, since the upstream would otherwise pick, or
// reject, silently. Unknown names pass through for the upstream to report.
func realiasToolChoice(choice *schemas.ResponsesToolChoice, tools []schemas.ResponsesTool, byFunction map[string][]string, aliases map[string]schemas.NamespaceToolAlias) (*schemas.ResponsesToolChoice, *schemas.BifrostError) {
	if choice == nil || choice.ResponsesToolChoiceStruct == nil {
		return choice, nil
	}
	topLevel := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if tool.Name != nil {
			if _, isAlias := aliases[*tool.Name]; !isAlias {
				topLevel[*tool.Name] = true
			}
		}
	}
	resolve := func(name *string) (*string, *schemas.BifrostError) {
		if name == nil || *name == "" || topLevel[*name] {
			return name, nil
		}
		candidates := byFunction[*name]
		switch len(candidates) {
		case 0:
			return name, nil
		case 1:
			return new(candidates[0]), nil
		}
		namespaces := make([]string, 0, len(candidates))
		for _, alias := range candidates {
			namespaces = append(namespaces, aliases[alias].Namespace)
		}
		sort.Strings(namespaces)
		return nil, NewBifrostBadRequestError(fmt.Sprintf("tool_choice %q is ambiguous across namespaces [%s]; the target provider does not support namespace tools, so the choice must name a single function", *name, strings.Join(namespaces, ", ")))
	}

	structCopy := *choice.ResponsesToolChoiceStruct
	resolvedName, bifrostErr := resolve(structCopy.Name)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	structCopy.Name = resolvedName
	if len(structCopy.Tools) > 0 {
		allowed := make([]schemas.ResponsesToolChoiceAllowedToolDef, len(structCopy.Tools))
		copy(allowed, structCopy.Tools)
		for i := range allowed {
			resolved, bifrostErr := resolve(allowed[i].Name)
			if bifrostErr != nil {
				return nil, bifrostErr
			}
			allowed[i].Name = resolved
		}
		structCopy.Tools = allowed
	}
	return &schemas.ResponsesToolChoice{
		ResponsesToolChoiceStr:    choice.ResponsesToolChoiceStr,
		ResponsesToolChoiceStruct: &structCopy,
	}, nil
}

func restoreNamespacedItems(items []schemas.ResponsesMessage, aliases map[string]schemas.NamespaceToolAlias) {
	for i := range items {
		restoreNamespacedItem(&items[i], aliases)
	}
}

func restoreNamespacedItem(item *schemas.ResponsesMessage, aliases map[string]schemas.NamespaceToolAlias) {
	if item == nil || item.Type == nil || *item.Type != schemas.ResponsesMessageTypeFunctionCall || item.ResponsesToolMessage == nil || item.ResponsesToolMessage.Name == nil {
		return
	}
	alias, ok := aliases[*item.ResponsesToolMessage.Name]
	if !ok {
		return
	}
	item.ResponsesToolMessage.Name = new(alias.Name)
	item.ResponsesToolMessage.Namespace = new(alias.Namespace)
}
