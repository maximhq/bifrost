package featureflags

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// FlagDef describes a feature flag at registration time.
//
//   - ID is the stable, machine-readable identifier used everywhere the
//     flag is referenced from code (IsEnabled, the URL path, the DB key,
//     gossip messages). Restricted to lowercase letters, digits, and
//     [._-] separators so it is URL-safe and visually unambiguous.
//   - DisplayName is the human-readable label rendered in the UI. Free
//     text; can be changed without breaking call sites.
//   - Description is the paragraph-level detail shown under the row.
//   - Default is the value used when no override (file or DB) is present.
//   - EnterpriseOnly marks flags that gate enterprise-only features: in
//     OSS mode such flags are inert (IsEnabled always returns false),
//     reject Set(), and surface in the UI with the toggle disabled and
//     an "Enterprise" badge so operators can see the feature exists.
type FlagDef struct {
	ID             string
	DisplayName    string
	Description    string
	Default        bool
	EnterpriseOnly bool
}

var (
	// ErrFlagIDInvalid is returned when a flag id does not match the
	// allowed character set: lowercase letters, digits, dots, dashes, with
	// no leading/trailing separators.
	ErrFlagIDInvalid = errors.New("feature flag id is invalid")
	// ErrFlagAlreadyRegistered is returned when the same flag id is
	// registered twice. We fail loud here so that two packages cannot
	// silently disagree on a default.
	ErrFlagAlreadyRegistered = errors.New("feature flag already registered")
)

// flagIDPattern restricts flag ids to a small grammar so the UI/API can
// rely on them being URL-safe and visually unambiguous. Dotted namespaces
// (e.g. "experimental.streaming-mux") are encouraged.
var flagIDPattern = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*$`)

var (
	registryMu sync.RWMutex
	registry   = map[string]FlagDef{}
)

// Register adds a flag definition to the process-wide registry. Call this
// from package init() so all known flags are present before the HTTP server
// boots. Returns an error rather than panicking so callers can decide.
func Register(def FlagDef) error {
	if !flagIDPattern.MatchString(def.ID) {
		return fmt.Errorf("%w: %q", ErrFlagIDInvalid, def.ID)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[def.ID]; exists {
		return fmt.Errorf("%w: %q", ErrFlagAlreadyRegistered, def.ID)
	}
	registry[def.ID] = def
	return nil
}

// MustRegister is the panicking variant intended for package init() use
// where a registration error is a programming bug, not a runtime condition.
func MustRegister(def FlagDef) {
	if err := Register(def); err != nil {
		panic(err)
	}
}

// LookupDef returns the registered definition for a flag, or false if the
// flag is not registered. Unregistered flags can still exist in DB/file as
// stale data; the store handles them as registered=false in List().
func LookupDef(id string) (FlagDef, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	def, ok := registry[id]
	return def, ok
}

// RegisteredDefs returns a snapshot of all registered flag definitions,
// sorted by id for deterministic output (UI list, tests).
func RegisteredDefs() []FlagDef {
	registryMu.RLock()
	defs := make([]FlagDef, 0, len(registry))
	for _, def := range registry {
		defs = append(defs, def)
	}
	registryMu.RUnlock()
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })
	return defs
}

// resetRegistryForTest clears the registry. Tests call this between cases
// to keep registrations isolated; not exported beyond the package.
func resetRegistryForTest() {
	registryMu.Lock()
	registry = map[string]FlagDef{}
	registryMu.Unlock()
}
