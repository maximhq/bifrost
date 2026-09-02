package governance

import (
	"sort"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
)

// mcpClientTools is one client's accumulated grant: every tool, or a named subset.
type mcpClientTools struct {
	everyTool bool
	named     []string
	seen      map[string]bool
	// name is the client's display name if a source carried one; else resolved at emit time.
	name string
}

// MCPToolAccumulator gathers per-client tool grants from a key's MCP configs and Virtual MCPs,
// applying the whole-client, per-client union, and wildcard-collapse rules in one place.
type MCPToolAccumulator struct {
	byClient map[string]*mcpClientTools
	order    []string
}

func NewMCPToolAccumulator(capacity int) *MCPToolAccumulator {
	return &MCPToolAccumulator{byClient: make(map[string]*mcpClientTools, capacity)}
}

// client returns a client's entry, registering it on first sight.
func (acc *MCPToolAccumulator) client(clientID string) *mcpClientTools {
	entry, ok := acc.byClient[clientID]
	if !ok {
		entry = &mcpClientTools{seen: make(map[string]bool)}
		acc.byClient[clientID] = entry
		acc.order = append(acc.order, clientID)
	}
	return entry
}

// GrantWholeClient grants every tool a client has.
func (acc *MCPToolAccumulator) GrantWholeClient(clientID string) {
	if clientID != "" {
		acc.client(clientID).everyTool = true
	}
}

// GrantTool grants one tool, or every tool on the wildcard.
func (acc *MCPToolAccumulator) GrantTool(clientID, tool string) {
	if clientID == "" || tool == "" {
		return
	}
	entry := acc.client(clientID)
	if tool == grant.Wildcard {
		entry.everyTool = true
		return
	}
	if !entry.seen[tool] {
		entry.seen[tool] = true
		entry.named = append(entry.named, tool)
	}
}

// addMCPConfig folds in a key's own config: the client is registered even with no tools, and carries
// its own name.
func (acc *MCPToolAccumulator) addMCPConfig(cfg *configstoreTables.TableVirtualKeyMCPConfig) {
	clientID := cfg.MCPClient.ClientID
	if clientID == "" {
		return
	}
	entry := acc.client(clientID)
	if cfg.MCPClient.Name != "" {
		entry.name = cfg.MCPClient.Name
	}
	for _, tool := range cfg.ToolsToExecute {
		acc.GrantTool(clientID, tool)
	}
}

// AddVirtualMCP folds in an enabled Virtual MCP. Each spec's tool names follow WhiteList semantics:
// ["*"] grants the whole client, [] grants nothing, and a named list grants those tools.
func (acc *MCPToolAccumulator) AddVirtualMCP(vmcp *configstoreTables.TableVirtualMCP) {
	if vmcp == nil || !vmcp.Enabled {
		return
	}
	for _, spec := range vmcp.ParsedTools {
		if spec.MCPClientID == "" {
			continue
		}
		// Register the client even with an empty list, so it counts as configured and the
		// allowed-by-default fallback can't reopen it.
		acc.client(spec.MCPClientID)
		for _, tool := range spec.ToolNames {
			acc.GrantTool(spec.MCPClientID, tool)
		}
	}
}

// ExcludeTool withdraws one tool from a client, registering the client either way so the
// allowed-by-default fallback treats it as configured and does not reopen it. The tool is withdrawn
// only from a named grant: a whole-client grant already means every tool, so the exclusion is ignored.
func (acc *MCPToolAccumulator) ExcludeTool(clientID, tool string) {
	if clientID == "" || tool == "" {
		return
	}
	entry := acc.client(clientID)
	if entry.everyTool || !entry.seen[tool] {
		return
	}
	delete(entry.seen, tool)
	remaining := make([]string, 0, len(entry.named))
	for _, t := range entry.named {
		if t != tool {
			remaining = append(remaining, t)
		}
	}
	entry.named = remaining
}

// ConfiguredClients is the set of clients named, whatever they granted; the allowed-by-default
// fallback must not reopen them.
func (acc *MCPToolAccumulator) ConfiguredClients() map[string]struct{} {
	configured := make(map[string]struct{}, len(acc.byClient))
	for clientID := range acc.byClient {
		configured[clientID] = struct{}{}
	}
	return configured
}

// mcpClientNames resolves client id → display name from the in-memory store, or nil.
func (gs *LocalGovernanceStore) mcpClientNames() map[string]string {
	if gs.inMemoryStore == nil {
		return nil
	}
	return gs.inMemoryStore.GetMCPClientNames()
}

// MCPPermitsFromAccumulator emits one permit per client, naming unnamed clients from clientNames. A
// client that reaches no tool is dropped (allowed-by-default stays blocked via ConfiguredClients, not a
// permit); an unresolvable name is dropped and logged. A free function so any store passes its own deps.
func MCPPermitsFromAccumulator(acc *MCPToolAccumulator, holderKind, holderName string, clientNames map[string]string, logger schemas.Logger) []schemas.MCPPermit {
	// Sorted for a stable permit across nodes and rebuilds.
	sort.Strings(acc.order)
	result := make([]schemas.MCPPermit, 0, len(acc.order))
	for _, clientID := range acc.order {
		entry := acc.byClient[clientID]
		tools := entry.named
		if entry.everyTool {
			tools = []string{grant.Wildcard}
		}
		if len(tools) == 0 {
			continue
		}
		clientName := entry.name
		if clientName == "" {
			clientName = clientNames[clientID]
		}
		if clientName == "" {
			if logger != nil {
				logger.Warn("governance: %s %q grants tools of MCP client %q, which has no configured name; those tools are not reachable until the client is configured", holderKind, holderName, clientID)
			}
			continue
		}
		result = append(result, schemas.MCPPermit{
			Client:     clientID,
			ClientName: clientName,
			Tools:      tools,
		})
	}
	return result
}

// AppendMCPPermitsAllowedByDefault adds a wildcard permit for every allowed-by-default client the
// holder did not configure itself; a configured client is left as-is, even if it grants nothing.
// Appended in id order for a stable permit. Every holder appends the same way, so this is one shared
// function.
func AppendMCPPermitsAllowedByDefault(mcpPermits []schemas.MCPPermit, configured map[string]struct{}, allowedByDefault map[string]string) []schemas.MCPPermit {
	if len(allowedByDefault) == 0 {
		return mcpPermits
	}
	clientIDs := make([]string, 0, len(allowedByDefault))
	for clientID := range allowedByDefault {
		if _, ok := configured[clientID]; ok {
			continue
		}
		clientIDs = append(clientIDs, clientID)
	}
	sort.Strings(clientIDs)
	for _, clientID := range clientIDs {
		mcpPermits = append(mcpPermits, schemas.MCPPermit{
			Client:     clientID,
			ClientName: allowedByDefault[clientID],
			Tools:      []string{grant.Wildcard},
		})
	}
	return mcpPermits
}
