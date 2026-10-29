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
	"sync"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/lrucache"
)

const (
	// defaultCacheCapacity bounds the discovery cache to this many distinct
	// discoveries, one per (client, hash) however many callers hold it;
	// least-recently-used entries are dropped past it and read again on their
	// next use.
	defaultCacheCapacity = 4096

	// membershipRefreshInterval is how old a client's set may get before its
	// next read reloads it in the background. Writes and deletes keep it
	// current on the process that makes them and, through EvictDiscovery, on
	// the others; the reload corrects a process that missed one.
	membershipRefreshInterval = 2 * time.Minute

	// membershipQueryTimeout bounds one load of a client's set.
	membershipQueryTimeout = 10 * time.Second
)

var (
	// errNoCaller refuses a write on a ctx with no user or virtual-key
	// caller, such as the MCP manager's own: per-user discoveries are only
	// ever written for the caller whose credential made them.
	errNoCaller = errors.New("a per-user discovery is written for a user or virtual-key caller only")

	// errNoDiscovery is what reading a member's discovery returns when its
	// row is gone; it is never cached.
	errNoDiscovery = errors.New("no per-user discovery")

	// errHashMoved ends a fill for a hash whose reader's row no longer holds
	// it; it is never cached. See discoveryOf.
	errHashMoved = errors.New("per-user discovery no longer has the hash read for")

	// errLoadPanicked is the result a load ends with when its query panics.
	errLoadPanicked = errors.New("per-user discovery membership query panicked")
)

// cachedDiscovery is one distinct discovery of a client, cached under its hash
// and served to every caller holding it.
type cachedDiscovery struct {
	discovery  *schemas.MCPDiscovery // tools prefixed with clientName
	clientName string                // the client name the tools were prefixed with
	hash       string                // the discovery_hash it is cached under
}

// member is one caller holding a discovery on a client.
type member struct {
	mode     schemas.MCPAuthMode
	identity string
}

// clientMembers is what this process knows about who holds a discovery on
// one client. A member whose hash is "" is one this process was told changed
// and has not read since; its next read settles it against the database.
type clientMembers struct {
	members map[member]string   // member to its discovery_hash; nil until the first load completes
	due     atomic.Bool         // the next read reloads members in the background; see scheduleRefresh
	version uint64              // bumped on every change to members; see dropMember
	loading chan struct{}       // non-nil while a load runs, closed when it ends
	touched map[member]struct{} // members changed while that load runs; see load
}

// scheduleRefresh makes cm due for a background reload after
// membershipRefreshInterval. A timer sets the flag, so a read tells whether
// the set is due with one atomic load rather than a clock read; each
// successful load arms one timer.
func (cm *clientMembers) scheduleRefresh() {
	cm.due.Store(false)
	time.AfterFunc(membershipRefreshInterval, func() { cm.due.Store(true) })
}

// startLoadLocked marks a load of cm as running. Called with PerUserStore.mu
// held; the caller then runs load.
func (cm *clientMembers) startLoadLocked() {
	cm.loading = make(chan struct{})
	cm.touched = make(map[member]struct{})
}

// setLocked records m with hash, or removes it when present is false. Called
// with PerUserStore.mu held.
func (cm *clientMembers) setLocked(m member, hash string, present bool) {
	if cm.members != nil {
		if present {
			cm.members[m] = hash
		} else {
			delete(cm.members, m)
		}
	}
	cm.version++
	if cm.loading != nil {
		cm.touched[m] = struct{}{}
	}
}

var _ schemas.MCPDiscoveryStore = (*PerUserStore)(nil)

// PerUserStore keeps the discoveries of MCP clients with per_user_discovery
// on: one per caller (a user or a virtual key) and client, persisted in
// mcp_per_user_discoveries, which is the source of truth, and served from
// memory. Writes go to the database before they change what this process
// serves.
//
// The caller is resolved off ctx exactly as the per-user credential lookup
// resolves it, so a discovery written with the ctx its credential was looked
// up with lands under that credential's identity.
//
// Each process keeps two things per client: the set of callers that hold a
// discovery on it, with each one's discovery hash, loaded from the database
// the first time the client is read; and a bounded cache of the distinct
// discoveries those callers hold, keyed by (client, hash), so callers holding
// the same discovery share one copy. A caller outside the set has no
// discovery and costs no database read, and a member is served the copy
// cached under the hash the set records for it. The set changes on this
// process's own writes and deletes, on another process's (delivered through
// EvictDiscovery), and is reloaded from the database once it is older than
// membershipRefreshInterval, which corrects a process that missed one. A
// cached copy no member's hash names any more is never served again, and ages
// out of the cache.
//
// All methods are safe on a nil receiver: reads miss and writes are refused.
type PerUserStore struct {
	configStore configstore.ConfigStore
	logger      schemas.Logger
	cache       *lrucache.Cache[cachedDiscovery]

	mu      sync.RWMutex
	clients map[string]*clientMembers // by client ID

	// onWritten, when set, runs after this store writes or deletes a
	// discovery; see SetOnDiscoveryWritten.
	onWritten atomic.Pointer[func(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string)]
}

// NewPerUserStore returns a PerUserStore reading and writing through
// configStore. A nil configStore leaves every caller with no per-user
// discovery.
func NewPerUserStore(configStore configstore.ConfigStore, logger schemas.Logger) *PerUserStore {
	if logger == nil {
		logger = bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	}
	return &PerUserStore{
		configStore: configStore,
		logger:      logger,
		cache:       lrucache.New[cachedDiscovery](defaultCacheCapacity),
		clients:     make(map[string]*clientMembers),
	}
}

// contentKey builds the cache key for one distinct discovery of a client. The
// parts are length-prefixed (lrucache.EncodeKey) rather than joined with a
// separator, so a client ID cannot run into the hash.
func contentKey(mcpClientID, hash string) string {
	return lrucache.EncodeKey(mcpClientID, hash)
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

// GetDiscovery returns the caller's discovery for client, with tool names
// prefixed by the client's current name. A caller outside the client's set
// gets nil without a database read. Callers holding the same discovery are
// served one cached copy of it (see discoveryOf). A read error is logged and
// answered with nil, so the caller sees no tools rather than someone else's.
func (s *PerUserStore) GetDiscovery(ctx context.Context, client *schemas.MCPClientConfig) *schemas.MCPDiscovery {
	if s == nil || s.configStore == nil || client == nil {
		return nil
	}
	mode, identity, ok := callerIdentity(ctx)
	if !ok {
		return nil
	}
	m := member{mode: mode, identity: identity}
	hash, isMember, version, ok := s.membership(ctx, client.ID, m)
	if !ok || !isMember {
		return nil
	}
	cached, err := s.discoveryOf(ctx, mode, identity, client, hash)
	if errors.Is(err, errNoDiscovery) {
		s.dropMember(client.ID, m, version)
		return nil
	}
	if err != nil {
		s.logger.Warn("failed to read the per-user discovery of MCP client %s: %v", client.Name, err)
		return nil
	}
	if cached.hash != hash {
		s.learnHash(client.ID, m, version, cached.hash)
	}
	return cached.discovery
}

// discoveryOf returns the discovery of a member whose set entry holds hash
// ("" when this process does not know it). The copy cached under that hash
// and the client's current name is served as it is. Otherwise the caller's
// own row is read and its discovery cached under the row's hash. Members
// missing the same hash at once share one fill, which reads its leader's row:
// equal hashes are equal discoveries, so that row serves them all. If the
// leader's row no longer holds the hash (the set was stale), or the leader's
// request ends before its read does, the fill fails for the others, and each
// still waiting reads its own row instead.
func (s *PerUserStore) discoveryOf(ctx context.Context, mode schemas.MCPAuthMode, identity string, client *schemas.MCPClientConfig, hash string) (cachedDiscovery, error) {
	clientName := client.Name
	if hash != "" {
		key := contentKey(client.ID, hash)
		if cached, hit := s.cache.Get(key); hit && cached.clientName == clientName {
			return cached, nil
		}
		// The loader runs on this goroutine when it leads the fill, so these
		// are only ever set by it.
		var (
			led    bool
			own    *tables.TableMCPPerUserDiscovery
			ownErr error
		)
		cached, err := s.cache.Fill(ctx, key, func() (cachedDiscovery, string, error) {
			led = true
			own, ownErr = s.configStore.GetMCPPerUserDiscoveryByMode(ctx, mode, identity, client.ID)
			if ownErr != nil {
				return cachedDiscovery{}, "", ownErr
			}
			if own == nil || own.DiscoveryHash != hash {
				return cachedDiscovery{}, "", errHashMoved
			}
			return newCachedDiscovery(own, clientName), "", nil
		})
		switch {
		case err == nil:
			return cached, nil
		case led && ownErr != nil:
			return cachedDiscovery{}, ownErr
		case led:
			return s.cacheRow(ctx, client.ID, clientName, own)
		case ctx.Err() != nil:
			return cachedDiscovery{}, ctx.Err()
		case !errors.Is(err, errHashMoved) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded):
			return cachedDiscovery{}, err
		}
		// The fill this read waited on found its leader's row moved, or ended
		// with its leader's request; neither says anything of this caller's
		// row: read that.
	}
	row, err := s.configStore.GetMCPPerUserDiscoveryByMode(ctx, mode, identity, client.ID)
	if err != nil {
		return cachedDiscovery{}, err
	}
	return s.cacheRow(ctx, client.ID, clientName, row)
}

// cacheRow returns the discovery row holds, cached under the row's hash. A
// copy another caller holding the same discovery already made is served
// instead of building another. A nil row is errNoDiscovery.
func (s *PerUserStore) cacheRow(ctx context.Context, clientID, clientName string, row *tables.TableMCPPerUserDiscovery) (cachedDiscovery, error) {
	if row == nil {
		return cachedDiscovery{}, errNoDiscovery
	}
	key := contentKey(clientID, row.DiscoveryHash)
	if cached, hit := s.cache.Get(key); hit && cached.clientName == clientName {
		return cached, nil
	}
	cached, err := s.cache.Fill(ctx, key, func() (cachedDiscovery, string, error) {
		return newCachedDiscovery(row, clientName), "", nil
	})
	if err != nil {
		// This call waited on another fill of the hash, which failed or which
		// this request stopped waiting for; the row in hand is still good.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return cachedDiscovery{}, ctxErr
		}
		return newCachedDiscovery(row, clientName), nil
	}
	return cached, nil
}

// newCachedDiscovery builds the cached copy of row's discovery, prefixed with
// clientName.
func newCachedDiscovery(row *tables.TableMCPPerUserDiscovery, clientName string) cachedDiscovery {
	return cachedDiscovery{discovery: discoveryFromRow(row, clientName), clientName: clientName, hash: row.DiscoveryHash}
}

// membership looks m up in clientID's set, returning its hash and the set's
// version for dropMember and learnHash. The first read of a client loads its
// set, and concurrent reads wait for that one load; a set due for a refresh
// starts a background reload and is answered from as it stands. ok is false
// when the set cannot be had: the load this read ran or waited for failed
// (the next read tries again), or ctx ended while waiting.
func (s *PerUserStore) membership(ctx context.Context, clientID string, m member) (hash string, isMember bool, version uint64, ok bool) {
	s.mu.RLock()
	if cm := s.clients[clientID]; cm != nil && cm.members != nil {
		hash, isMember = cm.members[m]
		version = cm.version
		due := cm.loading == nil && cm.due.Load()
		s.mu.RUnlock()
		if due {
			s.refresh(clientID)
		}
		return hash, isMember, version, true
	}
	s.mu.RUnlock()

	for {
		s.mu.Lock()
		cm := s.clients[clientID]
		if cm == nil {
			cm = &clientMembers{}
			s.clients[clientID] = cm
		}
		if cm.members != nil {
			hash, isMember = cm.members[m]
			version = cm.version
			s.mu.Unlock()
			return hash, isMember, version, true
		}
		if wait := cm.loading; wait != nil {
			s.mu.Unlock()
			select {
			case <-wait:
			case <-ctx.Done():
				return "", false, 0, false
			}
		} else {
			cm.startLoadLocked()
			s.mu.Unlock()
			s.load(clientID, cm)
		}
		// The load ended. It failed if it left this client unloaded; one that
		// was forgotten meanwhile is loaded again.
		s.mu.Lock()
		failed := s.clients[clientID] == cm && cm.members == nil
		s.mu.Unlock()
		if failed {
			return "", false, 0, false
		}
	}
}

// refresh starts a background reload of clientID's set, unless one is running
// or the set was forgotten.
func (s *PerUserStore) refresh(clientID string) {
	s.mu.Lock()
	cm := s.clients[clientID]
	if cm == nil || cm.members == nil || cm.loading != nil || !cm.due.Load() {
		s.mu.Unlock()
		return
	}
	cm.startLoadLocked()
	s.mu.Unlock()
	go func() {
		// A panicking query has already failed the load (see load); a
		// background reload must not take the process down with it.
		defer func() {
			if r := recover(); r != nil {
				s.logger.Error("panic while reloading who holds a per-user discovery of MCP client %s: %v", clientID, r)
			}
		}()
		s.load(clientID, cm)
	}()
}

// load reads clientID's set from the database and installs it. A failed load
// keeps the set as it was and leaves it due, so the next read tries again. A
// query that panics fails the load the same way before the panic goes on, so
// no read is left waiting on a load that never ends.
func (s *PerUserStore) load(clientID string, cm *clientMembers) {
	var rows []tables.TableMCPPerUserDiscovery
	err := errLoadPanicked
	defer func() { s.install(clientID, cm, rows, err) }()
	ctx, cancel := context.WithTimeout(context.Background(), membershipQueryTimeout)
	defer cancel()
	rows, err = s.configStore.GetMCPPerUserDiscoveryBindings(ctx, clientID)
}

// install ends a load of clientID's set with its result. A member changed
// while the query ran keeps what this process knows of it (its hash, or "" to
// check the database), since the query may predate the change. Members the
// database no longer lists leave the set; the cached copies of their
// discoveries stay for any other member holding them.
func (s *PerUserStore) install(clientID string, cm *clientMembers, rows []tables.TableMCPPerUserDiscovery, err error) {
	s.mu.Lock()
	if err == nil && s.clients[clientID] == cm {
		members := make(map[member]string, len(rows)+len(cm.touched))
		for i := range rows {
			if identity := rows[i].Identity(); identity != "" {
				members[member{mode: schemas.MCPAuthMode(rows[i].AuthMode), identity: identity}] = rows[i].DiscoveryHash
			}
		}
		for m := range cm.touched {
			members[m] = cm.members[m] // "" when this process no longer lists it: check the database
		}
		cm.members = members
		cm.scheduleRefresh()
		cm.version++
	}
	cm.touched = nil
	close(cm.loading)
	cm.loading = nil
	s.mu.Unlock()

	if err != nil && err != errLoadPanicked { // a panic is reported where it is recovered
		s.logger.Warn("failed to load who holds a per-user discovery of MCP client %s: %v", clientID, err)
	}
}

// setMember records m in clientID's set with hash, or removes it when present
// is false. A client this process has not loaded is left alone: its first
// load reads the database.
func (s *PerUserStore) setMember(clientID string, m member, hash string, present bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cm := s.clients[clientID]; cm != nil {
		cm.setLocked(m, hash, present)
	}
}

// dropMember takes m out of clientID's set after a read found its row gone,
// unless the set changed since the read began: a write may have landed after
// the row was read, and the next read checks again.
func (s *PerUserStore) dropMember(clientID string, m member, version uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cm := s.clients[clientID]
	if cm == nil || cm.members == nil || cm.version != version {
		return
	}
	delete(cm.members, m)
	cm.version++
}

// learnHash records the hash of the row a read found for m, unless the set
// changed since the read began. Without it, a set holding an older hash than
// the database would send every read of m to the database until the next
// refresh.
func (s *PerUserStore) learnHash(clientID string, m member, version uint64, hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cm := s.clients[clientID]
	if cm == nil || cm.members == nil || cm.version != version {
		return
	}
	if _, ok := cm.members[m]; ok {
		cm.members[m] = hash
	}
}

// SetDiscovery replaces the caller's discovery for client. Tool names are
// stored without the client prefix. A ctx with no user or virtual-key caller
// is refused before any I/O.
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
	s.setMember(client.ID, member{mode: mode, identity: identity}, row.DiscoveryHash, true)
	s.written(ctx, mode, identity, client.ID)
	return nil
}

// DeleteDiscovery removes the caller's discovery for client. A ctx with no
// user or virtual-key caller is refused before any I/O.
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
	s.setMember(client.ID, member{mode: mode, identity: identity}, "", false)
	s.written(ctx, mode, identity, client.ID)
	return nil
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
// deleted a binding's discovery. No cached copy is dropped: copies are keyed
// by content, and the caller's set entry already names the one it holds now.
// EvictDiscovery is not called here, so a process applying another's change
// never announces it.
func (s *PerUserStore) written(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) {
	if cb := s.onWritten.Load(); cb != nil {
		(*cb)(ctx, mode, identity, mcpClientID)
	}
}

// EvictDiscovery applies another process's write or delete of one binding:
// the caller is kept in the client's set with no hash, so its next read checks
// the database. Deletes are not taken out of the set directly: messages from
// two processes can arrive in another order than their writes landed, and a
// delete applied after a later write would hide a discovery the database has
// until the next refresh. Cached copies stay for the other callers holding
// them.
func (s *PerUserStore) EvictDiscovery(mode schemas.MCPAuthMode, identity, mcpClientID string) {
	if s == nil {
		return
	}
	s.setMember(mcpClientID, member{mode: mode, identity: identity}, "", true)
}

// EvictDiscoveriesByMCPClient forgets one client: its set and every cached
// discovery of it. Its next read loads it again. Used when the client is
// removed, or every discovery of it is.
func (s *PerUserStore) EvictDiscoveriesByMCPClient(mcpClientID string) {
	if s == nil || mcpClientID == "" {
		return
	}
	s.mu.Lock()
	delete(s.clients, mcpClientID)
	s.mu.Unlock()
	s.cache.EvictWhere(func(key string) bool {
		parts, ok := lrucache.DecodeKey(key, 2)
		return ok && parts[0] == mcpClientID
	})
}

// EvictDiscoveriesByVirtualKey makes every vk-mode discovery of one virtual
// key, across clients, read again: its next read of each checks the database.
// A deleted key's rows are gone, and a key that only changed keeps its
// discoveries. See forgetHashes.
func (s *PerUserStore) EvictDiscoveriesByVirtualKey(virtualKeyID string) {
	if s == nil || virtualKeyID == "" {
		return
	}
	s.forgetHashes(member{mode: schemas.MCPAuthModeVK, identity: virtualKeyID})
}

// EvictDiscoveriesByUser makes every user-mode discovery of one user, across
// clients, read again, the same way as EvictDiscoveriesByVirtualKey.
func (s *PerUserStore) EvictDiscoveriesByUser(userID string) {
	if s == nil || userID == "" {
		return
	}
	s.forgetHashes(member{mode: schemas.MCPAuthModeUser, identity: userID})
}

// forgetHashes keeps m in every loaded set it is in, with no hash, so its next
// read of each client checks the database. A set whose load is running counts
// m as changed too, since the query may have read a row that is gone. Cached
// copies stay for the other callers holding them.
func (s *PerUserStore) forgetHashes(m member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cm := range s.clients {
		if _, isMember := cm.members[m]; isMember || cm.loading != nil {
			cm.setLocked(m, "", true)
		}
	}
}

// FlushDiscoveryCache forgets every client and every cached discovery.
func (s *PerUserStore) FlushDiscoveryCache() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.clients = make(map[string]*clientMembers)
	s.mu.Unlock()
	s.cache.Flush()
}

// discoveryFromRow builds the discovery a row describes, its tools and prompts
// prefixed with clientName, and the tools serialized once here so injecting
// them into a request does not marshal them again.
func discoveryFromRow(row *tables.TableMCPPerUserDiscovery, clientName string) *schemas.MCPDiscovery {
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
		ToolNameMapping:   row.ToolNameMapping,
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
		ToolNameMapping:   discovery.ToolNameMapping,
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
		ToolNameMapping:   discovery.ToolNameMapping,
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
// and schemas with their annotations and metadata, name mapping, instructions,
// resources, resource templates and prompts. encoding/json writes map keys
// sorted, so equal discoveries hash equal.
func discoveryHash(discovery *schemas.MCPDiscovery) (string, error) {
	data, err := json.Marshal(struct {
		Tools             map[string]schemas.ChatTool       `json:"tools"`
		ToolDetails       map[string]schemas.MCPToolDetails `json:"tool_details,omitempty"` // ChatTool's JSON leaves these out
		Mapping           map[string]string                 `json:"mapping"`
		Instructions      string                            `json:"instructions"`
		Resources         []mcp.Resource                    `json:"resources,omitempty"`
		ResourceTemplates []mcp.ResourceTemplate            `json:"resource_templates,omitempty"`
		Prompts           []mcp.Prompt                      `json:"prompts,omitempty"`
	}{discovery.Tools, schemas.ToolDetails(discovery.Tools), discovery.ToolNameMapping, discovery.Instructions,
		discovery.Resources, discovery.ResourceTemplates, discovery.Prompts})
	if err != nil {
		return "", fmt.Errorf("failed to hash the discovery: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
