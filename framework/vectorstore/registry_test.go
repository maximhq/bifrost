package vectorstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// closeCountingStore is a connected store that counts its closes.
type closeCountingStore struct {
	VectorStore
	location string
	closes   int
}

func (s *closeCountingStore) Close(context.Context, string) error {
	s.closes++
	return nil
}

// fakeConnector opens a closeCountingStore for each config, failing the
// locations in fail, and records what it opened.
type fakeConnector struct {
	fail   map[string]error
	opened []*closeCountingStore
}

func (f *fakeConnector) connect(_ context.Context, config *Config, _ schemas.Logger) (VectorStore, error) {
	if err := f.fail[config.Location()]; err != nil {
		return nil, err
	}
	store := &closeCountingStore{location: config.Location()}
	f.opened = append(f.opened, store)
	return store, nil
}

func qdrantAt(name, host string) Config {
	return Config{Name: name, Enabled: true, Type: VectorStoreTypeQdrant, Config: QdrantConfig{Host: *schemas.NewSecretVar(host)}}
}

func weaviateAt(name, host string) Config {
	return Config{Name: name, Enabled: true, Type: VectorStoreTypeWeaviate, Config: WeaviateConfig{Scheme: "http", Host: schemas.NewSecretVar(host)}}
}

// The top-level store is named after its type unless it is given a name, and
// every name is unique, written plainly and present on each vector_stores entry.
func TestValidateStoreNames(t *testing.T) {
	primary := weaviateAt("", "w:8080")
	assert.Equal(t, "weaviate", PrimaryStoreName(&primary))
	named := weaviateAt("shared", "w:8080")
	assert.Equal(t, "shared", PrimaryStoreName(&named))
	assert.Equal(t, "", PrimaryStoreName(nil))

	require.NoError(t, ValidateStoreNames(&primary, []Config{qdrantAt("warp-qdrant", "q")}))
	require.NoError(t, ValidateStoreNames(nil, []Config{qdrantAt("a", "q"), qdrantAt("b", "q")}))

	for name, tc := range map[string]struct {
		primary *Config
		named   []Config
		want    string
	}{
		"a vector_stores entry without a name":    {&primary, []Config{qdrantAt("", "q")}, "vector_stores[0]: name is required"},
		"two entries with one name":               {nil, []Config{qdrantAt("a", "q"), weaviateAt("a", "w")}, `vector_stores[1]: name "a" is already used by vector_stores[0]`},
		"an entry taking the primary's type name": {&primary, []Config{weaviateAt("weaviate", "other")}, `vector_stores[0]: name "weaviate" is already used by vector_store (named after its type)`},
		"a name with a space":                     {nil, []Config{qdrantAt("my store", "q")}, `name "my store" must be`},
	} {
		t.Run(name, func(t *testing.T) {
			assert.ErrorContains(t, ValidateStoreNames(tc.primary, tc.named), tc.want)
		})
	}
}

// Every enabled store is connected and found by name, in config.json order
// with the primary first; a disabled one is listed and holds no connection.
func TestOpenRegistryConnectsEveryEnabledStore(t *testing.T) {
	connector := &fakeConnector{}
	primary := weaviateAt("", "w:8080")
	disabled := qdrantAt("off", "q-off")
	disabled.Enabled = false
	registry, err := OpenRegistry(context.Background(), &primary, []Config{qdrantAt("warp-qdrant", "q"), disabled}, nil, RegistryOptions{Connect: connector.connect})
	require.NoError(t, err)

	all := registry.All()
	require.Len(t, all, 3)
	assert.Equal(t, []string{"weaviate", "warp-qdrant", "off"}, []string{all[0].Name, all[1].Name, all[2].Name})
	assert.Same(t, all[0], registry.Primary())
	assert.True(t, all[0].Primary)
	assert.Equal(t, StoreConnected, all[1].Status)
	assert.NotNil(t, all[1].Store)
	assert.Equal(t, StoreDisabled, all[2].Status)
	assert.Nil(t, all[2].Store)
	assert.Len(t, connector.opened, 2, "a disabled store is not connected")

	found, ok := registry.Get("warp-qdrant")
	require.True(t, ok)
	assert.Same(t, all[1], found)
	_, ok = registry.Get("nobody")
	assert.False(t, ok)

	withoutPrimary, err := OpenRegistry(context.Background(), nil, []Config{qdrantAt("only", "q")}, nil, RegistryOptions{Connect: connector.connect})
	require.NoError(t, err)
	assert.Nil(t, withoutPrimary.Primary(), "with no vector_store there is no primary store")
}

// The primary store failing stops startup, as it always has; any other store
// failing is kept with the reason and the rest connect.
func TestOpenRegistryOnlyThePrimaryStoreFailingIsFatal(t *testing.T) {
	down := errors.New("connection refused")
	primary := weaviateAt("", "w:8080")

	connector := &fakeConnector{fail: map[string]error{primary.Location(): down}}
	_, err := OpenRegistry(context.Background(), &primary, []Config{qdrantAt("warp-qdrant", "q")}, nil, RegistryOptions{Connect: connector.connect})
	require.ErrorIs(t, err, down)
	assert.ErrorContains(t, err, `vector store "weaviate"`)
	assert.Empty(t, connector.opened, "nothing else is connected once startup stops")

	failing := qdrantAt("warp-qdrant", "q")
	connector = &fakeConnector{fail: map[string]error{failing.Location(): down}}
	registry, err := OpenRegistry(context.Background(), &primary, []Config{failing, qdrantAt("other", "q2")}, nil, RegistryOptions{Connect: connector.connect})
	require.NoError(t, err)
	entry, _ := registry.Get("warp-qdrant")
	assert.Equal(t, StoreFailed, entry.Status)
	assert.ErrorIs(t, entry.Err, down)
	assert.Nil(t, entry.Store)
	other, _ := registry.Get("other")
	assert.Equal(t, StoreConnected, other.Status, "a store after a failed one still connects")
	assert.Equal(t, StoreConnected, registry.Primary().Status)
}

// A vector_stores entry that never answers is given up on at the connect
// timeout, and startup goes on.
func TestOpenRegistryBoundsEachConnect(t *testing.T) {
	hang := func(ctx context.Context, _ *Config, _ schemas.Logger) (VectorStore, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	started := time.Now()
	registry, err := OpenRegistry(context.Background(), nil, []Config{qdrantAt("slow", "q")}, nil, RegistryOptions{Connect: hang, ConnectTimeout: 50 * time.Millisecond})
	require.NoError(t, err)
	assert.Less(t, time.Since(started), 5*time.Second)
	entry, _ := registry.Get("slow")
	assert.Equal(t, StoreFailed, entry.Status)
	assert.ErrorIs(t, entry.Err, context.DeadlineExceeded)
}

// Two names reaching the same data share one connection, whatever their
// credentials, and it is closed once; a memory-only Chromem store has no
// shared data, so each of its entries is its own store.
func TestOpenRegistrySharesOneConnectionPerLocation(t *testing.T) {
	connector := &fakeConnector{}
	primary := weaviateAt("", "w:8080")
	sameHostOtherKey := weaviateAt("warp", "w:8080")
	sameHostOtherKey.Config = WeaviateConfig{Scheme: "http", Host: schemas.NewSecretVar("w:8080"), APIKey: schemas.NewSecretVar("rotated")}
	memoryA := Config{Name: "mem-a", Enabled: true, Type: VectorStoreTypeChromem}
	memoryB := Config{Name: "mem-b", Enabled: true, Type: VectorStoreTypeChromem}
	registry, err := OpenRegistry(context.Background(), &primary, []Config{sameHostOtherKey, memoryA, memoryB}, nil, RegistryOptions{Connect: connector.connect})
	require.NoError(t, err)

	warp, _ := registry.Get("warp")
	assert.Same(t, registry.Primary().Store, warp.Store, "the same Weaviate is one store")
	assert.Equal(t, "weaviate", warp.SharedWith)
	a, _ := registry.Get("mem-a")
	b, _ := registry.Get("mem-b")
	assert.NotSame(t, a.Store, b.Store, "two memory-only stores are two stores")
	assert.Equal(t, registry.Primary().Location, registry.Primary().ID, "a store on a server is identified by where it is")
	assert.Equal(t, registry.Primary().ID, warp.ID, "two names for one store share its ID")
	assert.NotEqual(t, a.ID, b.ID, "each memory-only store is a store of its own")
	assert.NotEmpty(t, a.ID)
	assert.Len(t, connector.opened, 3)

	registry.Close(context.Background())
	for _, store := range connector.opened {
		assert.Equal(t, 1, store.closes, "%s is closed once", store.location)
	}
	var nilRegistry *Registry
	nilRegistry.Close(context.Background())
	assert.Nil(t, nilRegistry.All())
}

// Entries for one Redis address share a connection only when their TLS settings
// match too: one that verifies certificates never reuses a connection made
// without verification. Credentials alone do not split them.
func TestOpenRegistryDoesNotShareAcrossTLSSettings(t *testing.T) {
	connector := &fakeConnector{}
	redisAt := func(name string, skipVerify bool, password string) Config {
		cfg := RedisConfig{Addr: schemas.NewSecretVar("r:6379"), UseTLS: schemas.NewSecretVar("true"), Password: schemas.NewSecretVar(password)}
		if skipVerify {
			cfg.InsecureSkipVerify = schemas.NewSecretVar("true")
		}
		return Config{Name: name, Enabled: true, Type: VectorStoreTypeRedis, Config: cfg}
	}
	registry, err := OpenRegistry(context.Background(), nil, []Config{
		redisAt("lax", true, "one"), redisAt("strict", false, "one"), redisAt("strict-other-key", false, "two"),
	}, nil, RegistryOptions{Connect: connector.connect})
	require.NoError(t, err)
	strict, _ := registry.Get("strict")
	other, _ := registry.Get("strict-other-key")
	assert.Empty(t, strict.SharedWith, "a verifying entry opens its own connection")
	assert.Equal(t, "strict", other.SharedWith, "the same TLS settings share, whatever the credentials")
	assert.Len(t, connector.opened, 2)
}

// Chromem opens the directory it is compared by: a path written with spaces or
// a trailing slash is the cleaned one.
// A relative Chromem path is the directory it names under the working directory, so an entry
// written relative and one written absolute are one store: one connection, one ID, and the
// directory that holds the data is that one. Two handles on one directory would each keep their
// own copy of its collections, and a write through one would not show through the other.
func TestChromemRelativePathIsTheSameStoreAsItsAbsoluteForm(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(dir)
	absolute := filepath.Join(dir, "vectors")
	chromemAt := func(name, path string) Config {
		return Config{Name: name, Enabled: true, Type: VectorStoreTypeChromem, Config: ChromemConfig{Path: path}}
	}
	primary := chromemAt("relative", "vectors")
	registry, err := OpenRegistry(context.Background(), &primary, []Config{chromemAt("absolute", absolute)}, nil, RegistryOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { registry.Close(context.Background()) })

	relative, abs := registry.Primary(), mustGet(t, registry, "absolute")
	assert.Equal(t, "chromem://"+absolute, relative.Location)
	assert.Same(t, relative.Store, abs.Store, "one directory is one store")
	assert.Equal(t, "relative", abs.SharedWith)
	assert.Equal(t, relative.ID, abs.ID)
	require.NoError(t, relative.Store.CreateNamespace(context.Background(), "ns", 2, nil))
	require.NoError(t, relative.Store.Add(context.Background(), "ns", "a", []float32{1, 0}, nil))
	_, err = os.Stat(absolute)
	require.NoError(t, err, "the data is in the directory both names reach")
}

// A link to a Chromem directory is that directory: two entries reaching it, one through the link,
// share one connection, rather than each keeping its own copy of its collections.
func TestChromemSymlinkIsTheSameStoreAsItsTarget(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	target := filepath.Join(dir, "vectors")
	require.NoError(t, os.Mkdir(target, 0o755))
	link := filepath.Join(dir, "vector-link")
	require.NoError(t, os.Symlink(target, link))
	chromemAt := func(name, path string) Config {
		return Config{Name: name, Enabled: true, Type: VectorStoreTypeChromem, Config: ChromemConfig{Path: path}}
	}
	primary := chromemAt("direct", target)
	registry, err := OpenRegistry(context.Background(), &primary, []Config{chromemAt("linked", link)}, nil, RegistryOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { registry.Close(context.Background()) })

	linked := mustGet(t, registry, "linked")
	assert.Equal(t, "chromem://"+target, linked.Location, "the link resolves to its target")
	assert.Same(t, registry.Primary().Store, linked.Store, "one directory is one store")
	assert.Equal(t, "direct", linked.SharedWith)

	// A directory not created yet, under a linked parent, names the same place before and after
	// it is created, so its ID does not change on the next boot.
	notYet := &Config{Type: VectorStoreTypeChromem, Config: ChromemConfig{Path: filepath.Join(link, "later")}}
	before := notYet.Location()
	require.NoError(t, os.Mkdir(filepath.Join(target, "later"), 0o755))
	assert.Equal(t, before, notYet.Location())
	assert.Equal(t, "chromem://"+filepath.Join(target, "later"), before)
}

// mustGet is a registry entry the test configured.
func mustGet(t *testing.T, registry *Registry, name string) *NamedStore {
	t.Helper()
	entry, ok := registry.Get(name)
	require.True(t, ok, name)
	return entry
}

func TestChromemOpensTheCleanedPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vectors")
	store, err := NewVectorStore(context.Background(), &Config{Enabled: true, Type: VectorStoreTypeChromem, Config: ChromemConfig{Path: "  " + dir + "/  "}}, nil)
	require.NoError(t, err)
	require.NoError(t, store.CreateNamespace(context.Background(), "ns", 2, nil))
	require.NoError(t, store.Add(context.Background(), "ns", "a", []float32{1, 0}, nil))
	_, err = os.Stat(dir)
	require.NoError(t, err, "the cleaned directory holds the data")
}
