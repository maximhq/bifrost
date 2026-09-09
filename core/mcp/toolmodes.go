//go:build !tinygo && !wasm

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/maximhq/bifrost/core/schemas"
)

// ============================================================================
// TOOL MODES: COMPACT + SEARCH
//
// Code mode hides a client's tools behind four meta-tools and a Starlark
// sandbox. The two modes here attack the same context-bloat problem without
// asking the model to write code:
//
//   - compact: tools stay in the request as real, directly callable entries,
//     but the tool description and every parameter description are stripped.
//     Nothing else changes — execution is the plain MCP path.
//   - search:  tools are hidden behind three meta-tools. searchTools runs a
//     BM25 query over tool names, server names, and descriptions and returns a
//     short ranked list; getToolDetails returns the full docs for one tool;
//     executeTool invokes one tool by name. Bifrost is stateless per request,
//     so executeTool is what lets the model call a tool that was never in the
//     request's tool list.
// ============================================================================

// Search mode meta-tool names.
const (
	ToolTypeSearchTools    string = "searchTools"
	ToolTypeGetToolDetails string = "getToolDetails"
	ToolTypeExecuteTool    string = "executeTool"
)

// SearchModeLogPrefix is the log prefix for search mode operations.
const SearchModeLogPrefix = "[SEARCH MODE]"

const (
	searchToolsDefaultLimit = 10
	searchToolsMaxLimit     = 30
	searchShortDescMaxRunes = 140
)

var searchModeToolNames = map[string]bool{
	ToolTypeSearchTools:    true,
	ToolTypeGetToolDetails: true,
	ToolTypeExecuteTool:    true,
}

// IsSearchModeTool reports whether toolName is one of the search mode meta-tools.
func IsSearchModeTool(toolName string) bool {
	return searchModeToolNames[toolName]
}

// ============================================================================
// COMPACT MODE
// ============================================================================

// compactToolDefinition returns a copy of tool with the function description
// and every parameter description removed. The JSON schema skeleton (types,
// required, enums, nesting) is preserved so the model can still produce a
// valid call. Falls back to the original tool if the schema cannot be
// round-tripped, since a full definition beats a broken one.
func compactToolDefinition(tool schemas.ChatTool) schemas.ChatTool {
	if tool.Function == nil {
		return tool
	}
	out := tool
	fn := *tool.Function
	fn.Description = nil
	if fn.Parameters != nil {
		if stripped, ok := stripSchemaDescriptions(fn.Parameters); ok {
			fn.Parameters = stripped
		}
	}
	out.Function = &fn
	return out
}

// stripSchemaDescriptions round-trips params through generic JSON, deletes
// every "description" key that is a schema keyword (never a property that
// happens to be named "description"), and parses the result back.
func stripSchemaDescriptions(params *schemas.ToolFunctionParameters) (*schemas.ToolFunctionParameters, bool) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, false
	}
	var generic interface{}
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, false
	}
	stripDescriptionKeys(generic, false)
	cleaned, err := json.Marshal(generic)
	if err != nil {
		return nil, false
	}
	var stripped schemas.ToolFunctionParameters
	if err := json.Unmarshal(cleaned, &stripped); err != nil {
		return nil, false
	}
	return &stripped, true
}

// schemaKeywordsHoldingNamedChildren are JSON Schema keywords whose object
// values map user-chosen names to sub-schemas. Keys under them are names, not
// keywords, so a child called "description" must survive.
var schemaKeywordsHoldingNamedChildren = map[string]bool{
	"properties":        true,
	"patternProperties": true,
	"$defs":             true,
	"definitions":       true,
}

func stripDescriptionKeys(v interface{}, keysAreNames bool) {
	switch node := v.(type) {
	case map[string]interface{}:
		if !keysAreNames {
			delete(node, "description")
		}
		for key, child := range node {
			stripDescriptionKeys(child, !keysAreNames && schemaKeywordsHoldingNamedChildren[key])
		}
	case []interface{}:
		for _, child := range node {
			stripDescriptionKeys(child, false)
		}
	}
}

// nameOnlyToolDefinition returns a copy of tool carrying only its name and an
// open, empty parameter schema. The model learns parameters through
// getToolDetails and then calls the tool directly by name.
func nameOnlyToolDefinition(tool schemas.ChatTool) schemas.ChatTool {
	if tool.Function == nil {
		return tool
	}
	out := tool
	out.Function = &schemas.ChatToolFunction{
		Name: tool.Function.Name,
		Parameters: &schemas.ToolFunctionParameters{
			Type:                 "object",
			Properties:           schemas.NewOrderedMap(),
			AdditionalProperties: &schemas.AdditionalPropertiesStruct{AdditionalPropertiesBool: schemas.Ptr(true)},
		},
	}
	return out
}

// ============================================================================
// BM25 INDEX
// ============================================================================

// bm25 parameters: the usual defaults. The corpus is a few hundred short
// documents, so there is nothing to tune here for the barebones version.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// Each tool is indexed as three fields scored separately and summed with
// these weights (a BM25F-style split). Scoring per field matters more than
// the weights: with one shared length normalization, a 300-word Apollo
// description drowned an exact hit on the tool name, so the field with the
// strongest signal was the one being penalized hardest.
const (
	bm25WeightToolName = 3.0
	bm25WeightServer   = 1.5
	bm25WeightDesc     = 1.0
)

// Only the head of a description is indexed. Tool descriptions front-load
// what the tool does; the tail is caveats and parameter prose that mostly
// adds generic terms.
const bm25DescMaxTokens = 60

type bm25Field int

const (
	bm25FieldName bm25Field = iota
	bm25FieldServer
	bm25FieldDesc
	bm25FieldCount
)

var bm25FieldWeights = [bm25FieldCount]float64{bm25WeightToolName, bm25WeightServer, bm25WeightDesc}

type bm25Doc struct {
	toolName    string // prefixed name, e.g. "Apollo-contacts_search"
	server      string
	description string
	terms       [bm25FieldCount]map[string]int
	length      [bm25FieldCount]int
}

type bm25Index struct {
	docs   []bm25Doc
	df     map[string]int // documents containing the term in any field
	avgLen [bm25FieldCount]float64
}

type bm25Hit struct {
	doc   *bm25Doc
	score float64
}

// searchIndexEntry is one tool to index: its prefixed name, server, and description.
type searchIndexEntry struct {
	toolName    string
	server      string
	description string
}

func newBM25Index(entries []searchIndexEntry) *bm25Index {
	idx := &bm25Index{df: make(map[string]int)}
	var total [bm25FieldCount]int
	for _, e := range entries {
		doc := bm25Doc{toolName: e.toolName, server: e.server, description: e.description}
		fields := [bm25FieldCount][]string{
			tokenizeForSearch(e.toolName),
			tokenizeForSearch(e.server),
			tokenizeForSearch(e.description),
		}
		if len(fields[bm25FieldDesc]) > bm25DescMaxTokens {
			fields[bm25FieldDesc] = fields[bm25FieldDesc][:bm25DescMaxTokens]
		}
		seen := make(map[string]bool)
		for f := bm25Field(0); f < bm25FieldCount; f++ {
			doc.terms[f] = make(map[string]int)
			for _, tok := range fields[f] {
				doc.terms[f][tok]++
				seen[tok] = true
			}
			doc.length[f] = len(fields[f])
			total[f] += len(fields[f])
		}
		for tok := range seen {
			idx.df[tok]++
		}
		idx.docs = append(idx.docs, doc)
	}
	if len(idx.docs) > 0 {
		for f := bm25Field(0); f < bm25FieldCount; f++ {
			idx.avgLen[f] = float64(total[f]) / float64(len(idx.docs))
		}
	}
	return idx
}

// search returns up to limit documents ranked by BM25 score for query. When
// server is non-empty only that server's tools are considered (case-insensitive).
// Documents with a zero score are dropped, so an off-topic query can return
// nothing rather than noise.
func (idx *bm25Index) search(query, server string, limit int) []bm25Hit {
	qTokens := tokenizeForSearch(query)
	if len(qTokens) == 0 || len(idx.docs) == 0 {
		return nil
	}
	n := float64(len(idx.docs))
	var hits []bm25Hit
	for i := range idx.docs {
		doc := &idx.docs[i]
		if server != "" && !strings.EqualFold(doc.server, server) {
			continue
		}
		score := 0.0
		for _, tok := range qTokens {
			df := float64(idx.df[tok])
			if df == 0 {
				continue
			}
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			for f := bm25Field(0); f < bm25FieldCount; f++ {
				tf := doc.terms[f][tok]
				if tf == 0 || idx.avgLen[f] == 0 {
					continue
				}
				num := float64(tf) * (bm25K1 + 1)
				den := float64(tf) + bm25K1*(1-bm25B+bm25B*float64(doc.length[f])/idx.avgLen[f])
				score += bm25FieldWeights[f] * idf * num / den
			}
		}
		if score > 0 {
			hits = append(hits, bm25Hit{doc: doc, score: score})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].score != hits[b].score {
			return hits[a].score > hits[b].score
		}
		return hits[a].doc.toolName < hits[b].doc.toolName
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// tokenizeForSearch lowercases, splits on non-alphanumerics, drops
// single-character tokens, and applies a naive plural strip so "events" and
// "event" meet. A camelCase word yields its parts as well as the whole word,
// so "GitHub" matches both "github" and "hub", and "listCalendarEvents" matches
// "calendar". Deliberately no stemmer library.
func tokenizeForSearch(text string) []string {
	var tokens []string
	emit := func(word []rune) {
		if len(word) < 2 {
			return
		}
		whole := strings.ToLower(string(word))
		tokens = append(tokens, normalizeSearchToken(whole))
		// camelCase parts, only when the word actually has a case boundary.
		var part []rune
		prevLower := false
		split := false
		var parts []string
		for _, r := range word {
			if unicode.IsUpper(r) && prevLower {
				split = true
				if len(part) >= 2 {
					parts = append(parts, normalizeSearchToken(strings.ToLower(string(part))))
				}
				part = part[:0]
			}
			part = append(part, r)
			prevLower = unicode.IsLower(r) || unicode.IsDigit(r)
		}
		if split {
			if len(part) >= 2 {
				parts = append(parts, normalizeSearchToken(strings.ToLower(string(part))))
			}
			tokens = append(tokens, parts...)
		}
	}
	var cur []rune
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur = append(cur, r)
			continue
		}
		emit(cur)
		cur = cur[:0]
	}
	emit(cur)
	return tokens
}

func normalizeSearchToken(tok string) string {
	if len(tok) > 3 && strings.HasSuffix(tok, "ies") {
		return tok[:len(tok)-3] + "y"
	}
	if len(tok) > 3 && strings.HasSuffix(tok, "s") && !strings.HasSuffix(tok, "ss") {
		return tok[:len(tok)-1]
	}
	return tok
}

// ============================================================================
// SEARCH MODE: TOOL DEFINITIONS
// ============================================================================

// searchModeTools returns the three search mode meta-tools. servers and
// toolCount describe the catalog hidden behind them and are baked into the
// searchTools description so the model knows which domains exist without
// seeing a single tool definition.
func searchModeTools(servers []string, toolCount int) []schemas.ChatTool {
	return []schemas.ChatTool{
		createSearchToolsTool(servers, toolCount),
		createGetToolDetailsTool(),
		createExecuteToolTool(),
	}
}

func createSearchToolsTool(servers []string, toolCount int) schemas.ChatTool {
	serverList := strings.Join(servers, ", ")
	if serverList == "" {
		serverList = "none"
	}
	props := schemas.NewOrderedMapFromPairs(
		schemas.KV("query", map[string]interface{}{
			"type":        "string",
			"description": "What you want to do, in a few words (e.g. 'create linear issue', 'list calendar events tomorrow'). Matched against tool names and descriptions.",
		}),
		schemas.KV("server", map[string]interface{}{
			"type":        "string",
			"description": "Optional. Restrict results to one server by name.",
		}),
		schemas.KV("limit", map[string]interface{}{
			"type":        "integer",
			"description": fmt.Sprintf("Optional. Maximum results to return (default %d, max %d).", searchToolsDefaultLimit, searchToolsMaxLimit),
		}),
	)
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name: ToolTypeSearchTools,
			Description: schemas.Ptr(fmt.Sprintf(
				"Search a catalog of %d tools across these servers: %s. "+
					"Returns the best matching tool names with one-line descriptions. "+
					"Workflow: searchTools -> getToolDetails (for parameters) -> executeTool. "+
					"Run several searches if the first does not find what you need.",
				toolCount, serverList,
			)),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: props,
				Required:   []string{"query"},
			},
		},
	}
}

func createGetToolDetailsTool() schemas.ChatTool {
	props := schemas.NewOrderedMapFromPairs(
		schemas.KV("tool", map[string]interface{}{
			"type":        "string",
			"description": "Exact tool name as returned by searchTools (e.g. 'Linear-create_issue').",
		}),
	)
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name: ToolTypeGetToolDetails,
			Description: schemas.Ptr(
				"Get the full documentation for one tool: description, every parameter with type, " +
					"whether it is required, allowed values, and an example executeTool call. " +
					"Call this before executeTool for any tool you have not used yet.",
			),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: props,
				Required:   []string{"tool"},
			},
		},
	}
}

func createExecuteToolTool() schemas.ChatTool {
	props := schemas.NewOrderedMapFromPairs(
		schemas.KV("tool", map[string]interface{}{
			"type":        "string",
			"description": "Exact tool name as returned by searchTools (e.g. 'Linear-create_issue').",
		}),
		schemas.KV("arguments", map[string]interface{}{
			"type":                 "object",
			"description":          "Arguments for the tool, matching the parameters from getToolDetails.",
			"additionalProperties": true,
		}),
	)
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name: ToolTypeExecuteTool,
			Description: schemas.Ptr(
				"Execute one tool by name with the given arguments and return its result. " +
					"Use the exact name from searchTools and the parameters from getToolDetails.",
			),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: props,
				Required:   []string{"tool"},
			},
		},
	}
}

// ============================================================================
// SEARCH MODE: HANDLERS
// ============================================================================

// searchModeCatalog collects every tool exposed through search mode for the
// current request context, keyed by prefixed tool name, plus the sorted list
// of contributing server names.
func (m *ToolsManager) searchModeCatalog(ctx context.Context) (map[string]searchCatalogEntry, []string) {
	availableToolsPerClient := m.clientManager.GetToolPerClient(ctx)
	catalog := make(map[string]searchCatalogEntry)
	var servers []string
	for clientName, tools := range availableToolsPerClient {
		client := m.clientManager.GetClientByName(clientName)
		if client == nil {
			continue
		}
		mode := client.ExecutionConfig.ResolvedToolMode()
		// Search-mode tools are searchable and executable through the
		// meta-tools; compact_names tools are only describable (the model
		// calls them directly), so they join the catalog but not the index.
		if mode != schemas.MCPToolModeSearch && mode != schemas.MCPToolModeCompactNames {
			continue
		}
		contributed := false
		for _, tool := range tools {
			if tool.Function == nil || tool.Function.Name == "" {
				continue
			}
			if _, dup := catalog[tool.Function.Name]; dup {
				continue
			}
			catalog[tool.Function.Name] = searchCatalogEntry{server: clientName, tool: tool, searchable: mode == schemas.MCPToolModeSearch}
			contributed = mode == schemas.MCPToolModeSearch
		}
		if contributed {
			servers = append(servers, clientName)
		}
	}
	sort.Strings(servers)
	return catalog, servers
}

type searchCatalogEntry struct {
	server     string
	tool       schemas.ChatTool
	searchable bool // false for compact_names tools: describable via getToolDetails, not listed by searchTools
}

// executeSearchModeTool dispatches one of the search mode meta-tools. Failures
// the model can act on (unknown tool, bad arguments) come back as an error
// tool message rather than a Go error, so the agent loop feeds them back to
// the model instead of aborting the turn.
func (m *ToolsManager) executeSearchModeTool(ctx *schemas.BifrostContext, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	if toolCall.Function.Name == nil {
		return nil, fmt.Errorf("tool call missing function name")
	}
	var arguments map[string]interface{}
	if strings.TrimSpace(toolCall.Function.Arguments) == "" {
		arguments = map[string]interface{}{}
	} else if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &arguments); err != nil {
		return createToolResponseMessage(toolCall, fmt.Sprintf("Invalid arguments for %s: %v", *toolCall.Function.Name, err), true), nil
	}
	switch *toolCall.Function.Name {
	case ToolTypeSearchTools:
		return m.handleSearchTools(ctx, toolCall, arguments), nil
	case ToolTypeGetToolDetails:
		return m.handleGetToolDetails(ctx, toolCall, arguments), nil
	case ToolTypeExecuteTool:
		return m.handleExecuteTool(ctx, toolCall, arguments)
	default:
		return nil, fmt.Errorf("unknown search mode tool: %s", *toolCall.Function.Name)
	}
}

func (m *ToolsManager) handleSearchTools(ctx *schemas.BifrostContext, toolCall schemas.ChatAssistantMessageToolCall, arguments map[string]interface{}) *schemas.ChatMessage {
	query, _ := arguments["query"].(string)
	query = strings.TrimSpace(query)
	server, _ := arguments["server"].(string)
	server = strings.TrimSpace(server)
	limit := searchToolsDefaultLimit
	if raw, ok := arguments["limit"].(float64); ok && raw > 0 {
		limit = int(raw)
	}
	if limit > searchToolsMaxLimit {
		limit = searchToolsMaxLimit
	}

	fullCatalog, servers := m.searchModeCatalog(ctx)
	catalog := make(map[string]searchCatalogEntry, len(fullCatalog))
	for name, entry := range fullCatalog {
		if entry.searchable {
			catalog[name] = entry
		}
	}
	if len(catalog) == 0 {
		return createToolResponseMessage(toolCall, "No tools are available through searchTools for this request.", true)
	}
	if server != "" && !containsFold(servers, server) {
		return createToolResponseMessage(toolCall, fmt.Sprintf("Unknown server %q. Available servers: %s", server, strings.Join(servers, ", ")), true)
	}

	// An empty query with a server filter is a plain listing: the model wants
	// to browse one server. Anything else needs a query to rank against.
	var lines []string
	if query == "" {
		if server == "" {
			return createToolResponseMessage(toolCall, "query is required (or pass server to list that server's tools).", true)
		}
		names := make([]string, 0)
		for name, entry := range catalog {
			if strings.EqualFold(entry.server, server) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) > limit {
			names = names[:limit]
		}
		for _, name := range names {
			lines = append(lines, formatSearchHit(name, catalog[name].tool))
		}
		header := fmt.Sprintf("%d tools on %s (showing %d). Call getToolDetails(tool) for parameters.", countServerTools(catalog, server), server, len(lines))
		return createToolResponseMessage(toolCall, header+"\n"+strings.Join(lines, "\n"), false)
	}

	entries := make([]searchIndexEntry, 0, len(catalog))
	for name, entry := range catalog {
		entries = append(entries, searchIndexEntry{
			toolName:    name,
			server:      entry.server,
			description: toolDescription(entry.tool),
		})
	}
	// Deterministic index order keeps tie-breaking stable across calls.
	sort.Slice(entries, func(i, j int) bool { return entries[i].toolName < entries[j].toolName })
	hits := newBM25Index(entries).search(query, server, limit)
	if len(hits) == 0 {
		return createToolResponseMessage(toolCall, fmt.Sprintf(
			"No tools matched %q across %d tools. Try different words, or pass server (one of: %s) with an empty query to list a server's tools.",
			query, len(catalog), strings.Join(servers, ", "),
		), false)
	}
	for _, hit := range hits {
		lines = append(lines, formatSearchHit(hit.doc.toolName, catalog[hit.doc.toolName].tool))
	}
	header := fmt.Sprintf("Top %d of %d tools for %q. Call getToolDetails(tool) for parameters before executeTool.", len(hits), len(catalog), query)
	m.logger.Debug("%s searchTools query=%q server=%q hits=%d", SearchModeLogPrefix, query, server, len(hits))
	return createToolResponseMessage(toolCall, header+"\n"+strings.Join(lines, "\n"), false)
}

func (m *ToolsManager) handleGetToolDetails(ctx *schemas.BifrostContext, toolCall schemas.ChatAssistantMessageToolCall, arguments map[string]interface{}) *schemas.ChatMessage {
	name, _ := arguments["tool"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return createToolResponseMessage(toolCall, "tool is required: pass the exact name returned by searchTools.", true)
	}
	catalog, _ := m.searchModeCatalog(ctx)
	entry, ok := catalog[name]
	if !ok {
		suggestion := ""
		if hits := searchCatalogByName(catalog, name, 3); len(hits) > 0 {
			suggestion = " Did you mean: " + strings.Join(hits, ", ") + "?"
		}
		return createToolResponseMessage(toolCall, fmt.Sprintf("Unknown tool %q.%s Use searchTools to find the exact name.", name, suggestion), true)
	}
	return createToolResponseMessage(toolCall, renderToolDetails(name, entry.server, entry.tool), false)
}

// handleExecuteTool runs one real tool on behalf of the model. The inner tool
// goes through the same allow-list checks as a direct call
// (prepareToolExecution in exec.go) and the same plugin gate as a nested code
// mode call, so it is observationally a normal tool execution with a
// different outer envelope.
func (m *ToolsManager) handleExecuteTool(ctx *schemas.BifrostContext, toolCall schemas.ChatAssistantMessageToolCall, arguments map[string]interface{}) (*schemas.ChatMessage, error) {
	name, _ := arguments["tool"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return createToolResponseMessage(toolCall, "tool is required: pass the exact name returned by searchTools.", true), nil
	}
	innerArgs := map[string]interface{}{}
	switch raw := arguments["arguments"].(type) {
	case nil:
	case map[string]interface{}:
		innerArgs = raw
	case string:
		// Some models serialize nested objects as a JSON string; accept it.
		if strings.TrimSpace(raw) != "" {
			if err := json.Unmarshal([]byte(raw), &innerArgs); err != nil {
				return createToolResponseMessage(toolCall, fmt.Sprintf("arguments must be a JSON object, got a string that is not valid JSON: %v", err), true), nil
			}
		}
	default:
		return createToolResponseMessage(toolCall, "arguments must be a JSON object.", true), nil
	}

	client := m.clientManager.GetClientForTool(name)
	if client == nil || client.ExecutionConfig == nil {
		return createToolResponseMessage(toolCall, fmt.Sprintf("Unknown tool %q. Use searchTools to find the exact name.", name), true), nil
	}
	clientName := client.ExecutionConfig.Name
	if client.ExecutionConfig.ResolvedToolMode() != schemas.MCPToolModeSearch {
		return createToolResponseMessage(toolCall, fmt.Sprintf("Tool %q is not exposed through executeTool; call it directly.", name), true), nil
	}
	if client.State == schemas.MCPConnectionStateDisabled {
		return createToolResponseMessage(toolCall, fmt.Sprintf("Tool %q is not available: server %s is disabled.", name, clientName), true), nil
	}
	var includeClients []string
	if v, ok := ctx.Value(schemas.MCPContextKeyIncludeClients).([]string); ok {
		includeClients = v
	}
	if !shouldIncludeClient(clientName, includeClients, m.logger) ||
		shouldSkipToolForConfig(name, client.ExecutionConfig) ||
		shouldSkipToolForRequest(ctx, clientName, name) {
		return createToolResponseMessage(toolCall, fmt.Sprintf("Tool %q is not permitted for this request.", name), true), nil
	}

	argsJSON, err := schemas.MarshalSorted(innerArgs)
	if err != nil {
		return createToolResponseMessage(toolCall, fmt.Sprintf("Failed to encode arguments: %v", err), true), nil
	}

	// Nested request id + parent link, mirroring code mode's callMCPTool so the
	// inner call gets its own log entry tied back to the executeTool call.
	originalRequestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	var nestedRequestID string
	if m.fetchNewRequestIDFunc != nil {
		nestedRequestID = m.fetchNewRequestIDFunc(ctx)
	} else {
		nestedRequestID = fmt.Sprintf("exec_%d_%s", time.Now().UnixNano(), name)
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = schemas.NoDeadline
	}
	nestedCtx := schemas.NewBifrostContext(ctx, deadline)
	nestedCtx.SetValue(schemas.BifrostContextKeyRequestID, nestedRequestID)
	if originalRequestID != "" {
		nestedCtx.SetValue(schemas.BifrostContextKeyParentMCPRequestID, originalRequestID)
	}

	innerCall := schemas.ChatAssistantMessageToolCall{
		ID: schemas.Ptr(nestedRequestID),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr(name),
			Arguments: string(argsJSON),
		},
	}
	mcpRequest := &schemas.BifrostMCPRequest{
		RequestType:                  schemas.MCPRequestTypeChatToolCall,
		ClientName:                   clientName,
		ChatAssistantMessageToolCall: &innerCall,
	}

	conn, release, err := m.clientManager.AcquireClientConn(nestedCtx, client)
	if err != nil {
		return nil, err
	}
	defer release()

	resp, bifrostErr := m.clientManager.RunWithPluginPipeline(nestedCtx, mcpRequest, func(preReq *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		return m.ExecuteTool(nestedCtx, preReq, conn, client.ExecutionConfig, client.ToolNameMapping)
	})
	if bifrostErr != nil {
		msg := "tool execution failed"
		if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
			msg = bifrostErr.Error.Message
		}
		m.logger.Debug("%s executeTool %s failed: %s", SearchModeLogPrefix, name, msg)
		return createToolResponseMessage(toolCall, fmt.Sprintf("Tool %q failed: %s", name, msg), true), nil
	}
	if resp == nil || resp.ChatMessage == nil {
		return createToolResponseMessage(toolCall, fmt.Sprintf("Tool %q returned no result.", name), true), nil
	}

	// Re-envelope the inner result under the outer executeTool call id.
	text := ""
	if resp.ChatMessage.Content != nil {
		if resp.ChatMessage.Content.ContentStr != nil {
			text = *resp.ChatMessage.Content.ContentStr
		} else if len(resp.ChatMessage.Content.ContentBlocks) > 0 {
			var parts []string
			for _, block := range resp.ChatMessage.Content.ContentBlocks {
				if block.Text != nil {
					parts = append(parts, *block.Text)
				}
			}
			text = strings.Join(parts, "\n")
		}
	}
	isError := resp.ChatMessage.ChatToolMessage != nil &&
		resp.ChatMessage.ChatToolMessage.IsError != nil &&
		*resp.ChatMessage.ChatToolMessage.IsError
	return createToolResponseMessage(toolCall, text, isError), nil
}

// ============================================================================
// SEARCH MODE: RENDERING HELPERS
// ============================================================================

func toolDescription(tool schemas.ChatTool) string {
	if tool.Function == nil || tool.Function.Description == nil {
		return ""
	}
	return strings.TrimSpace(*tool.Function.Description)
}

// shortDescription takes the first sentence, capped at searchShortDescMaxRunes.
func shortDescription(desc string) string {
	desc = strings.Join(strings.Fields(desc), " ")
	if desc == "" {
		return ""
	}
	if i := strings.Index(desc, ". "); i > 0 && i < searchShortDescMaxRunes {
		return desc[:i+1]
	}
	runes := []rune(desc)
	if len(runes) > searchShortDescMaxRunes {
		return string(runes[:searchShortDescMaxRunes-1]) + "…"
	}
	return desc
}

func formatSearchHit(name string, tool schemas.ChatTool) string {
	short := shortDescription(toolDescription(tool))
	if short == "" {
		return "- " + name
	}
	return "- " + name + ": " + short
}

func countServerTools(catalog map[string]searchCatalogEntry, server string) int {
	n := 0
	for _, entry := range catalog {
		if strings.EqualFold(entry.server, server) {
			n++
		}
	}
	return n
}

func containsFold(list []string, want string) bool {
	for _, item := range list {
		if strings.EqualFold(item, want) {
			return true
		}
	}
	return false
}

// searchCatalogByName is the "did you mean" helper for getToolDetails: a
// BM25 pass over names only, so a slightly wrong name still finds its target.
func searchCatalogByName(catalog map[string]searchCatalogEntry, name string, limit int) []string {
	entries := make([]searchIndexEntry, 0, len(catalog))
	for toolName, entry := range catalog {
		entries = append(entries, searchIndexEntry{toolName: toolName, server: entry.server})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].toolName < entries[j].toolName })
	hits := newBM25Index(entries).search(name, "", limit)
	out := make([]string, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.doc.toolName)
	}
	return out
}

// schemaProperty is one rendered parameter from a tool's JSON schema.
type schemaProperty struct {
	name     string
	typ      string
	required bool
	desc     string
	enum     []interface{}
	def      interface{}
}

// renderToolDetails produces the getToolDetails text: description, a
// parameter list with types and required flags, and an example call. MCP
// schemas carry no examples, so the example is synthesized from the required
// parameters with placeholder values.
func renderToolDetails(name, server string, tool schemas.ChatTool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Tool: %s (server: %s)\n", name, server)
	if desc := toolDescription(tool); desc != "" {
		fmt.Fprintf(&b, "Description: %s\n", desc)
	}
	props, required := schemaProperties(tool)
	if len(props) == 0 {
		b.WriteString("Parameters: none\n")
	} else {
		b.WriteString("Parameters:\n")
		for _, p := range props {
			flag := "optional"
			if p.required {
				flag = "required"
			}
			fmt.Fprintf(&b, "- %s (%s, %s)", p.name, p.typ, flag)
			if p.desc != "" {
				fmt.Fprintf(&b, ": %s", p.desc)
			}
			if len(p.enum) > 0 {
				fmt.Fprintf(&b, " [one of: %s]", joinValues(p.enum))
			}
			if p.def != nil {
				fmt.Fprintf(&b, " [default: %v]", p.def)
			}
			b.WriteString("\n")
		}
	}
	example := map[string]interface{}{}
	for _, p := range props {
		if p.required {
			example[p.name] = placeholderForProperty(p)
		}
	}
	call := map[string]interface{}{"tool": name, "arguments": example}
	// encoding/json sorts map keys and, with HTML escaping off, keeps the
	// "<title>" placeholders readable instead of \u003ctitle\u003e.
	var encoded strings.Builder
	enc := json.NewEncoder(&encoded)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(call); err == nil {
		fmt.Fprintf(&b, "Example: executeTool(%s)\n", strings.TrimSpace(encoded.String()))
	}
	_ = required
	return strings.TrimRight(b.String(), "\n")
}

// schemaProperties flattens the top-level properties of a tool's parameter
// schema. Required parameters come first, then the schema's own order.
func schemaProperties(tool schemas.ChatTool) ([]schemaProperty, []string) {
	if tool.Function == nil || tool.Function.Parameters == nil {
		return nil, nil
	}
	params := tool.Function.Parameters
	requiredSet := make(map[string]bool, len(params.Required))
	for _, r := range params.Required {
		requiredSet[r] = true
	}
	var props []schemaProperty
	if params.Properties != nil {
		params.Properties.Range(func(key string, value interface{}) bool {
			p := schemaProperty{name: key, required: requiredSet[key], typ: "any"}
			if spec := toGenericMap(value); spec != nil {
				if t, ok := spec["type"].(string); ok && t != "" {
					p.typ = t
				} else if types, ok := spec["type"].([]interface{}); ok && len(types) > 0 {
					p.typ = joinValues(types)
				} else if _, ok := spec["properties"]; ok {
					p.typ = "object"
				} else if _, ok := spec["enum"]; ok {
					p.typ = "string"
				}
				if items := toGenericMap(spec["items"]); p.typ == "array" && items != nil {
					if it, ok := items["type"].(string); ok && it != "" {
						p.typ = "array of " + it
					}
				}
				if d, ok := spec["description"].(string); ok {
					p.desc = strings.Join(strings.Fields(d), " ")
				}
				if e, ok := spec["enum"].([]interface{}); ok {
					p.enum = e
				}
				p.def = spec["default"]
			}
			props = append(props, p)
			return true
		})
	}
	sort.SliceStable(props, func(i, j int) bool {
		return props[i].required && !props[j].required
	})
	return props, params.Required
}

// toGenericMap normalizes a property spec that may be a plain map or an
// OrderedMap (depending on how the schema was parsed) into a plain map.
func toGenericMap(v interface{}) map[string]interface{} {
	switch spec := v.(type) {
	case map[string]interface{}:
		return spec
	case *schemas.OrderedMap:
		if spec == nil {
			return nil
		}
		return spec.ToMap()
	case schemas.OrderedMap:
		return spec.ToMap()
	}
	return nil
}

func placeholderForProperty(p schemaProperty) interface{} {
	if len(p.enum) > 0 {
		return p.enum[0]
	}
	if p.def != nil {
		return p.def
	}
	switch {
	case p.typ == "string":
		return "<" + p.name + ">"
	case p.typ == "integer":
		return 0
	case p.typ == "number":
		return 0.0
	case p.typ == "boolean":
		return false
	case strings.HasPrefix(p.typ, "array"):
		return []interface{}{}
	case p.typ == "object":
		return map[string]interface{}{}
	}
	return "<" + p.name + ">"
}

func joinValues(values []interface{}) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, fmt.Sprintf("%v", v))
	}
	return strings.Join(parts, ", ")
}
