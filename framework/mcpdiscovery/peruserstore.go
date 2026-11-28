// Package mcpdiscovery holds the database-backed MCP discovery stores a
// deployment hands the manager (schemas.MCPConfig): SharedStore for clients
// without per_user_discovery and PerUserStore for clients with it.
package mcpdiscovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/mark3labs/mcp-go/mcp"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
)

const (
	// readRetries and readRetryWait bound how a binding's re-read retries a
	// failed read, as the HTTP layer's lookup retry does for any change made
	// elsewhere: readRetries attempts, readRetryWait apart.
	readRetries   = 5
	readRetryWait = 2 * time.Second

	// writeRetryBudget bounds how long a binding this store wrote is re-read
	// after a failed read before the write reports that memory could not
	// follow, as the HTTP layer's admin retry does.
	writeRetryBudget = 5 * time.Second

	// prefixedTTL is how long a discovery prefixed with a client's name is
	// kept after it was built, so copies no caller is served any more age out.
	prefixedTTL = 10 * time.Minute
)

// errNoCaller refuses a write on a ctx with no user or virtual-key caller, such
// as the MCP manager's own: per-user discoveries are only ever written for the
// caller whose credential made them.
var errNoCaller = errors.New("a per-user discovery is written for a user or virtual-key caller only")

var _ schemas.MCPDiscoveryStore = (*PerUserStore)(nil)

// PerUserStore keeps the discoveries of MCP clients with per_user_discovery
// on: one per caller (a user or a virtual key) and client, persisted in
// mcp_per_user_discoveries, which is the source of truth, and held in the live
// store's MCPUserDiscoveries collection, loaded at boot and kept by the writes
// below. Callers holding the same discovery share one interned copy, so memory
// follows the discoveries callers hold, not every discovery ever made.
//
// The caller is resolved off ctx exactly as the per-user credential lookup
// resolves it, so a discovery written with the ctx its credential was looked
// up with lands under that credential's identity. A read never touches the
// database: a caller with no binding has no discovery, and one with a binding
// is served its discovery prefixed with the client's current name, built once
// per name and discovery and shared by every caller holding it.
//
// All methods are safe on a nil receiver: reads miss and writes are refused.
type PerUserStore struct {
	configStore configstore.ConfigStore
	live        *livestore.Store
	logger      schemas.Logger

	// prefixed holds each discovery as callers are served it, prefixed with a
	// client's name. The name is part of the key, so a renamed client's callers
	// are served copies under the new name, and the old ones age out.
	prefixed *livestore.Table[prefixKey, *schemas.MCPDiscovery]

	// onWritten, when set, runs after this store writes or deletes a
	// discovery; see SetOnDiscoveryWritten.
	onWritten atomic.Pointer[func(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string)]
}

// prefixKey names one discovery prefixed with one client name.
type prefixKey struct {
	name, hash string
}

// prefixedDiscoveries is the name of the table of prefixed discoveries.
const prefixedDiscoveries = "mcp_user_discovery_prefixed"

// NewPerUserStore returns a PerUserStore reading and writing through
// configStore, over the discoveries live holds; live is required. A nil
// configStore leaves every caller with no per-user discovery.
func NewPerUserStore(configStore configstore.ConfigStore, live *livestore.Store, logger schemas.Logger) (*PerUserStore, error) {
	if live == nil {
		return nil, fmt.Errorf("mcp discovery: %w", livestore.ErrNoLiveStore)
	}
	if logger == nil {
		logger = bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	}
	prefixed, err := live.NewTable[prefixKey, *schemas.MCPDiscovery](context.Background(), prefixedDiscoveries, livestore.WithTTL(prefixedTTL))
	if err != nil {
		return nil, err
	}
	return &PerUserStore{configStore: configStore, live: live, logger: logger, prefixed: prefixed}, nil
}

// callerIdentity returns the caller a per-user discovery is kept for: the mode
// and identity the per-user credential lookup resolves, limited to a user or a
// virtual key. A session-only or anonymous caller, and the MCP manager's own
// context, have none.
func callerIdentity(ctx context.Context) (schemas.MCPAuthMode, string, bool) {
	if ctx == nil {
		return "", "", false
	}
	bctx, ok := ctx.(*schemas.BifrostContext)
	if !ok {
		// Values only: WithoutCancel keeps the wrapper from starting a
		// cancellation watcher for a context it reads a few keys from.
		bctx = schemas.NewBifrostContext(context.WithoutCancel(ctx), schemas.NoDeadline)
	}
	mode := bctx.MCPAuthMode()
	if mode != schemas.MCPAuthModeUser && mode != schemas.MCPAuthModeVK {
		return "", "", false
	}
	identity := bctx.MCPIdentity(mode)
	return mode, identity, identity != ""
}

// GetDiscovery returns the caller's discovery for client, with tool and prompt
// names prefixed by the client's current name, from memory only. A caller with
// no binding on client gets nil. Callers holding the same discovery are served
// one copy of it.
func (s *PerUserStore) GetDiscovery(ctx context.Context, client *schemas.MCPClientConfig) *schemas.MCPDiscovery {
	if s == nil || s.configStore == nil || client == nil {
		return nil
	}
	mode, identity, ok := callerIdentity(ctx)
	if !ok {
		return nil
	}
	binding, ok := s.binding(mode, identity, client.ID)
	if !ok || binding.Discovery == nil {
		return nil
	}
	content, name := binding.Discovery, client.Name
	return s.prefixed.GetOrCompute(prefixKey{name: name, hash: content.Hash}, func() (*schemas.MCPDiscovery, time.Duration, bool) {
		return discoveryFromContent(content, name), prefixedTTL, true
	})
}

// binding returns the binding of a caller on a client. The key is built in a
// buffer on the stack and read in place: Get only reads its key, so a read of
// a caller allocates nothing for it.
func (s *PerUserStore) binding(mode schemas.MCPAuthMode, identity, mcpClientID string) (*registry.MCPUserDiscovery, bool) {
	var buf [256]byte
	key := registry.AppendMCPUserDiscoveryKey(buf[:0], string(mode), identity, mcpClientID)
	return s.live.Collections.MCPUserDiscoveries.Get(unsafe.String(unsafe.SliceData(key), len(key)))
}

// SetDiscovery replaces the caller's discovery for client. Tool names are
// stored without the client prefix. A ctx with no user or virtual-key caller
// is refused before any I/O. The binding is re-read into memory once the row is
// written; when that read keeps failing, the error says the discovery was saved
// but memory still holds the one before.
func (s *PerUserStore) SetDiscovery(ctx context.Context, client *schemas.MCPClientConfig, discovery *schemas.MCPDiscovery) error {
	if s == nil || s.configStore == nil {
		return fmt.Errorf("per-user discovery store is not configured")
	}
	if client == nil || discovery == nil {
		return fmt.Errorf("per-user discovery requires a client and a discovery")
	}
	mode, identity, ok := callerIdentity(ctx)
	if !ok {
		return errNoCaller
	}
	row, err := rowFromDiscovery(mode, identity, client, discovery)
	if err != nil {
		return err
	}
	if err := s.configStore.UpsertMCPPerUserDiscovery(ctx, row); err != nil {
		return fmt.Errorf("failed to store the per-user discovery of MCP client %s: %w", client.Name, err)
	}
	if err := s.live.Refresh(ctx, []livestore.Ref{s.ref(mode, identity, client.ID, false)}, livestore.RetryErrorsWithin(writeRetryBudget, readRetryWait)); err != nil {
		return fmt.Errorf("the per-user discovery of MCP client %s was saved but not reloaded: %w", client.Name, err)
	}
	s.written(ctx, mode, identity, client.ID)
	return nil
}

// DeleteDiscovery removes the caller's discovery for client, from the database
// and then from memory, with no read. A ctx with no user or virtual-key caller
// is refused before any I/O.
func (s *PerUserStore) DeleteDiscovery(ctx context.Context, client *schemas.MCPClientConfig) error {
	if s == nil || s.configStore == nil {
		return fmt.Errorf("per-user discovery store is not configured")
	}
	if client == nil {
		return nil
	}
	mode, identity, ok := callerIdentity(ctx)
	if !ok {
		return errNoCaller
	}
	if err := s.configStore.DeleteMCPPerUserDiscovery(ctx, mode, identity, client.ID); err != nil {
		return fmt.Errorf("failed to delete the per-user discovery of MCP client %s: %w", client.Name, err)
	}
	if err := s.live.Refresh(ctx, []livestore.Ref{s.ref(mode, identity, client.ID, true)}); err != nil {
		return fmt.Errorf("the per-user discovery of MCP client %s was deleted but memory could not follow: %w", client.Name, err)
	}
	s.written(ctx, mode, identity, client.ID)
	return nil
}

// ref is the live-store reference of a caller's binding on a client.
func (s *PerUserStore) ref(mode schemas.MCPAuthMode, identity, mcpClientID string, remove bool) livestore.Ref {
	return livestore.Ref{Kind: livestore.KindMCPUserDiscovery, ID: registry.MCPUserDiscoveryKey(string(mode), identity, mcpClientID), Remove: remove}
}

// SetOnDiscoveryWritten registers a callback run after this store writes or
// deletes a discovery, once this process's own copy reflects it. That reaches
// only this process; a deployment running several processes over one
// database uses the callback to have the others apply EvictDiscovery too. The
// callback runs inline on the write path, so it must return promptly. nil
// clears it.
func (s *PerUserStore) SetOnDiscoveryWritten(cb func(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string)) {
	if s == nil {
		return
	}
	if cb == nil {
		s.onWritten.Store(nil)
		return
	}
	s.onWritten.Store(&cb)
}

// written runs the SetOnDiscoveryWritten callback after this store wrote or
// deleted a binding's discovery. EvictDiscovery is not called here, so a
// process applying another's change never announces it.
func (s *PerUserStore) written(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) {
	if cb := s.onWritten.Load(); cb != nil {
		(*cb)(ctx, mode, identity, mcpClientID)
	}
}

// reread refreshes refs from the database for a change another process made,
// retrying a failed read; a row found gone removes its binding, which is how a
// delete elsewhere reaches this process. A read that keeps failing is logged:
// these calls have no caller to report to.
func (s *PerUserStore) reread(what string, refs []livestore.Ref) {
	if len(refs) == 0 {
		return
	}
	if err := s.live.Refresh(context.Background(), refs, livestore.RetryErrors(readRetries, readRetryWait)); err != nil {
		s.logger.Warn("failed to reload the per-user MCP discoveries of %s: %v", what, err)
	}
}

// EvictDiscovery applies another process's write or delete of one binding: the
// binding is read again, so memory holds what the database holds, whichever
// order the messages of several processes arrive in.
func (s *PerUserStore) EvictDiscovery(mode schemas.MCPAuthMode, identity, mcpClientID string) {
	if s == nil || s.configStore == nil {
		return
	}
	s.reread(string(mode)+" "+identity, []livestore.Ref{s.ref(mode, identity, mcpClientID, false)})
}

// EvictDiscoveriesByMCPClient reads one client's bindings again: its own
// refresh re-reads them. Used when the client is removed, or every discovery
// of it is.
func (s *PerUserStore) EvictDiscoveriesByMCPClient(mcpClientID string) {
	if s == nil || s.configStore == nil || mcpClientID == "" {
		return
	}
	s.reread("MCP client "+mcpClientID, []livestore.Ref{{Kind: livestore.KindMCPClient, ID: mcpClientID}})
}

// EvictDiscoveriesByVirtualKey reads every vk-mode binding of one virtual key
// again, across clients: a deleted key's rows are gone, and a key that only
// changed keeps its discoveries.
func (s *PerUserStore) EvictDiscoveriesByVirtualKey(virtualKeyID string) {
	if s == nil || s.configStore == nil || virtualKeyID == "" {
		return
	}
	s.rereadCaller(schemas.MCPAuthModeVK, virtualKeyID)
}

// EvictDiscoveriesByUser reads every user-mode binding of one user again,
// across clients, the same way as EvictDiscoveriesByVirtualKey.
func (s *PerUserStore) EvictDiscoveriesByUser(userID string) {
	if s == nil || s.configStore == nil || userID == "" {
		return
	}
	s.rereadCaller(schemas.MCPAuthModeUser, userID)
}

// rereadCaller reads every binding this process holds for one caller again.
func (s *PerUserStore) rereadCaller(mode schemas.MCPAuthMode, identity string) {
	held := s.live.Collections.MCPUserDiscoveries.List(registry.MCPUserDiscoveryByCaller, registry.MCPUserDiscoveryCaller(string(mode), identity))
	refs := make([]livestore.Ref, 0, len(held))
	for _, b := range held {
		refs = append(refs, livestore.Ref{Kind: livestore.KindMCPUserDiscovery, ID: b.ID})
	}
	s.reread(string(mode)+" "+identity, refs)
}

// FlushDiscoveryCache reads every binding again and drops every prefixed copy.
func (s *PerUserStore) FlushDiscoveryCache() {
	if s == nil || s.configStore == nil {
		return
	}
	s.prefixed.Flush()
	if err := s.live.RefreshKind(context.Background(), livestore.KindMCPUserDiscovery); err != nil {
		s.logger.Warn("failed to reload the per-user MCP discoveries: %v", err)
	}
}

// discoveryFromContent builds the discovery a caller is served from a stored
// one, its tools and prompts prefixed with clientName, and the tools serialized
// once here so injecting them into a request does not marshal them again.
func discoveryFromContent(row *registry.MCPUserDiscoveryContent, clientName string) *schemas.MCPDiscovery {
	tools := make(map[string]schemas.ChatTool, len(row.Tools))
	for name, tool := range row.Tools {
		prefixed := clientName + "-" + name
		if tool.Function != nil {
			function := *tool.Function
			function.Name = prefixed
			tool.Function = &function
		}
		_ = tool.EnsureSerialized()
		tools[prefixed] = tool
	}
	var prompts []mcp.Prompt
	if row.Prompts != nil {
		prompts = make([]mcp.Prompt, len(row.Prompts))
		for i, prompt := range row.Prompts {
			prompt.Name = clientName + "-" + prompt.Name
			prompts[i] = prompt
		}
	}
	return &schemas.MCPDiscovery{
		Tools:             tools,
		Instructions:      row.Instructions,
		Resources:         row.Resources,
		ResourceTemplates: row.ResourceTemplates,
		Prompts:           prompts,
		DiscoveredAt:      row.DiscoveredAt,
	}
}

// rowFromDiscovery builds the row for a caller's discovery, with the client
// prefix stripped from every tool and prompt name. Only user and virtual-key
// callers keep per-user discoveries.
func rowFromDiscovery(mode schemas.MCPAuthMode, identity string, client *schemas.MCPClientConfig, discovery *schemas.MCPDiscovery) (*tables.TableMCPPerUserDiscovery, error) {
	if identity == "" {
		return nil, fmt.Errorf("per-user discovery requires an identity")
	}
	prefix := client.Name + "-"
	tools := make(map[string]schemas.ChatTool, len(discovery.Tools))
	for name, tool := range discovery.Tools {
		bare := strings.TrimPrefix(name, prefix)
		if tool.Function != nil {
			function := *tool.Function
			function.Name = strings.TrimPrefix(function.Name, prefix)
			tool.Function = &function
			// Cached JSON would still carry the prefixed name.
			tool.InvalidateSerialized()
		}
		tools[bare] = tool
	}
	var prompts []mcp.Prompt
	if discovery.Prompts != nil {
		prompts = make([]mcp.Prompt, len(discovery.Prompts))
		for i, prompt := range discovery.Prompts {
			prompt.Name = strings.TrimPrefix(prompt.Name, prefix)
			prompts[i] = prompt
		}
	}
	hash, err := discoveryHash(&schemas.MCPDiscovery{
		Tools:             tools,
		Instructions:      discovery.Instructions,
		Resources:         discovery.Resources,
		ResourceTemplates: discovery.ResourceTemplates,
		Prompts:           prompts,
	})
	if err != nil {
		return nil, err
	}
	discoveredAt := discovery.DiscoveredAt
	if discoveredAt.IsZero() {
		discoveredAt = time.Now().UTC()
	}
	row := &tables.TableMCPPerUserDiscovery{
		MCPClientID:       client.ID,
		AuthMode:          string(mode),
		Tools:             tools,
		Instructions:      discovery.Instructions,
		Resources:         discovery.Resources,
		ResourceTemplates: discovery.ResourceTemplates,
		Prompts:           prompts,
		DiscoveryHash:     hash,
		DiscoveredAt:      discoveredAt,
	}
	switch mode {
	case schemas.MCPAuthModeUser:
		row.UserID = &identity
	case schemas.MCPAuthModeVK:
		row.VirtualKeyID = &identity
	default:
		return nil, fmt.Errorf("per-user discovery is kept for user and virtual-key callers only, not %q", mode)
	}
	return row, nil
}

// discoveryHash is the content hash of a discovery as stored: its tool names
// and schemas with their annotations and metadata, instructions, resources,
// resource templates and prompts. encoding/json writes map keys
// sorted, so equal discoveries hash equal.
func discoveryHash(discovery *schemas.MCPDiscovery) (string, error) {
	data, err := json.Marshal(struct {
		Tools             map[string]schemas.ChatTool       `json:"tools"`
		ToolDetails       map[string]schemas.MCPToolDetails `json:"tool_details,omitempty"` // ChatTool's JSON leaves these out
		Instructions      string                            `json:"instructions"`
		Resources         []mcp.Resource                    `json:"resources,omitempty"`
		ResourceTemplates []mcp.ResourceTemplate            `json:"resource_templates,omitempty"`
		Prompts           []mcp.Prompt                      `json:"prompts,omitempty"`
	}{discovery.Tools, schemas.ToolDetails(discovery.Tools), discovery.Instructions,
		discovery.Resources, discovery.ResourceTemplates, discovery.Prompts})
	if err != nil {
		return "", fmt.Errorf("failed to hash the discovery: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
