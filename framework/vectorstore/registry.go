package vectorstore

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// A Registry is every vector store configured in config.json, by name: the
// top-level vector_store, which consumers use when they select none, and each
// entry of vector_stores. It is built once at boot and only read afterwards,
// so it is safe for concurrent use without a lock.
//
// Two entries that reach the same data (the same Location) share one
// connection, so a consumer selecting either reads and writes the same store.
// A memory-only Chromem store has no shared data to reach, so each entry of
// one gets its own.

// StoreStatus is what became of a configured store at boot.
type StoreStatus string

const (
	StoreConnected StoreStatus = "connected" // its connection is open
	StoreFailed    StoreStatus = "failed"    // it could not be reached at boot, or its config is invalid
	StoreDisabled  StoreStatus = "disabled"  // enabled is false
)

// DefaultConnectTimeout bounds connecting one vector_stores entry at boot: a
// store that accepts the connection and never answers would otherwise hold
// startup forever.
const DefaultConnectTimeout = 30 * time.Second

// storeNamePattern is what a store name may be: it is written in config.json
// and selected by name, so it stays short and plain.
var storeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// NamedStore is one configured store.
type NamedStore struct {
	Name     string
	Type     VectorStoreType
	Location string // see Config.Location; "" when unknown
	Primary  bool   // the top-level vector_store
	Status   StoreStatus
	Err      error // why it failed; nil otherwise
	// Store is the open connection; nil unless Status is StoreConnected.
	Store VectorStore
	// SharedWith names the earlier entry whose connection this one uses; ""
	// when it opened its own.
	SharedWith string
	// settings is connectionSettings of the entry's config, compared before sharing.
	settings string
}

// Registry holds the configured stores in config.json order, the primary first.
type Registry struct {
	stores []*NamedStore
}

// RegistryOptions controls how OpenRegistry connects.
type RegistryOptions struct {
	// Connect opens one store; NewVectorStore when nil.
	Connect func(ctx context.Context, config *Config, logger schemas.Logger) (VectorStore, error)
	// ConnectTimeout bounds each vector_stores entry; DefaultConnectTimeout
	// when zero. The primary store connects with the caller's context, as it
	// always has.
	ConnectTimeout time.Duration
}

// PrimaryStoreName is the primary store's name: its configured name, or its
// type when it has none.
func PrimaryStoreName(primary *Config) string {
	if primary == nil {
		return ""
	}
	if primary.Name != "" {
		return primary.Name
	}
	return string(primary.Type)
}

// ValidateStoreNames checks that every store has a usable name and no two
// share one, before anything connects.
func ValidateStoreNames(primary *Config, named []Config) error {
	seen := map[string]string{}
	check := func(name, where string) error {
		if !storeNamePattern.MatchString(name) {
			return fmt.Errorf("%s: name %q must be 1-64 letters, digits, '.', '_' or '-', starting with a letter or digit", where, name)
		}
		if first, ok := seen[name]; ok {
			return fmt.Errorf("%s: name %q is already used by %s", where, name, first)
		}
		seen[name] = where
		return nil
	}
	if primary != nil {
		where := "vector_store"
		if primary.Name == "" {
			where = "vector_store (named after its type)"
		}
		if err := check(PrimaryStoreName(primary), where); err != nil {
			return err
		}
	}
	for i := range named {
		where := fmt.Sprintf("vector_stores[%d]", i)
		if named[i].Name == "" {
			return fmt.Errorf("%s: name is required", where)
		}
		if err := check(named[i].Name, where); err != nil {
			return err
		}
	}
	return nil
}

// OpenRegistry connects every enabled store. The primary store failing is an
// error, as it has always stopped startup. Any other store that fails is kept
// as StoreFailed with the reason, so only what selects it is affected.
func OpenRegistry(ctx context.Context, primary *Config, named []Config, logger schemas.Logger, opts RegistryOptions) (*Registry, error) {
	if err := ValidateStoreNames(primary, named); err != nil {
		return nil, err
	}
	connect := opts.Connect
	if connect == nil {
		connect = NewVectorStore
	}
	timeout := opts.ConnectTimeout
	if timeout <= 0 {
		timeout = DefaultConnectTimeout
	}
	registry := &Registry{}
	if primary != nil {
		entry := &NamedStore{Name: PrimaryStoreName(primary), Type: primary.Type, Location: primary.Location(), Primary: true, Status: StoreDisabled, settings: connectionSettings(primary)}
		if primary.Enabled {
			store, err := connect(ctx, primary, logger)
			if err != nil {
				return nil, fmt.Errorf("failed to connect to vector store %q: %w", entry.Name, err)
			}
			entry.Status, entry.Store = StoreConnected, store
		}
		registry.stores = append(registry.stores, entry)
	}
	for i := range named {
		config := &named[i]
		entry := &NamedStore{Name: config.Name, Type: config.Type, Location: config.Location(), Status: StoreDisabled, settings: connectionSettings(config)}
		registry.stores = append(registry.stores, entry)
		if !config.Enabled {
			continue
		}
		if shared := registry.sharedConnection(config); shared != nil {
			entry.Status, entry.Store, entry.SharedWith = StoreConnected, shared.Store, shared.Name
			continue
		}
		connectCtx, cancel := context.WithTimeout(ctx, timeout)
		store, err := connect(connectCtx, config, logger)
		cancel()
		if err != nil {
			entry.Status, entry.Err = StoreFailed, err
			if logger != nil {
				logger.Error("vector store %q (%s) could not be reached, so what selects it is unavailable until it is fixed and Bifrost restarts (the rest of Bifrost is running): %v", entry.Name, entry.Type, err)
			}
			continue
		}
		entry.Status, entry.Store = StoreConnected, store
	}
	return registry, nil
}

// connectionSettings is what shapes a connection besides where it goes and its credentials: its
// TLS settings, and for Redis whether it is a cluster client. Two entries for one location share
// a connection only when these match too, so an entry that verifies certificates never reuses a
// connection made without verification.
func connectionSettings(config *Config) string {
	text := func(v *schemas.SecretVar) string {
		if v == nil {
			return ""
		}
		return strings.TrimSpace(v.GetValue())
	}
	flag := func(v *schemas.SecretVar) string {
		if v == nil {
			return "false"
		}
		return strconv.FormatBool(v.CoerceBool(false))
	}
	switch cfg := config.Config.(type) {
	case RedisConfig:
		return "tls=" + flag(cfg.UseTLS) + "|skip_verify=" + flag(cfg.InsecureSkipVerify) + "|ca=" + text(cfg.CACertPEM) + "|cluster=" + flag(cfg.ClusterMode)
	case QdrantConfig:
		return "tls=" + flag(&cfg.UseTLS)
	case WeaviateConfig:
		if cfg.GrpcConfig != nil {
			return "grpc=" + text(cfg.GrpcConfig.Host) + "|secured=" + strconv.FormatBool(cfg.GrpcConfig.Secured)
		}
	}
	return ""
}

// sharedConnection is an earlier connected entry reaching the same data as
// config with the same connection settings, or nil.
func (r *Registry) sharedConnection(config *Config) *NamedStore {
	location := config.Location()
	if location == "" || config.InMemory() {
		return nil
	}
	settings := connectionSettings(config)
	for _, entry := range r.stores {
		if entry.Status == StoreConnected && entry.Type == config.Type && entry.Location == location && entry.settings == settings && entry.SharedWith == "" {
			return entry
		}
	}
	return nil
}

// Get returns the store with this name.
func (r *Registry) Get(name string) (*NamedStore, bool) {
	if r == nil {
		return nil, false
	}
	for _, entry := range r.stores {
		if entry.Name == name {
			return entry, true
		}
	}
	return nil, false
}

// Primary returns the top-level vector_store, or nil when none is configured.
func (r *Registry) Primary() *NamedStore {
	if r == nil || len(r.stores) == 0 || !r.stores[0].Primary {
		return nil
	}
	return r.stores[0]
}

// All returns every configured store in config.json order, the primary first.
// The slice is a copy; the entries are shared and must not be changed.
func (r *Registry) All() []*NamedStore {
	if r == nil {
		return nil
	}
	return append([]*NamedStore(nil), r.stores...)
}

// Close closes each open connection once, shared or not.
func (r *Registry) Close(ctx context.Context) {
	if r == nil {
		return
	}
	for _, entry := range r.stores {
		if entry.Store != nil && entry.SharedWith == "" {
			_ = entry.Store.Close(ctx, "")
		}
	}
}
