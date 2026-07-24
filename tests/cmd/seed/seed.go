// Package seed creates deterministic OSS fixtures for API and DAC tests.
package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/framework/logstore"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Options controls the shared seed dataset.
type Options struct {
	Prefix          string
	ConfigPath      string
	EncryptionKey   string
	ConfigDialect   string
	ConfigDSN       string
	LogsDialect     string
	LogsDSN         string
	LogRowsPerShape int
	BatchSize       int
	OutputEnvPath   string
	DryRun          bool
}

// Shape describes one DAC ownership combination.
type Shape struct {
	Name           string   `json:"name"`
	UserID         string   `json:"user_id,omitempty"`
	TeamID         string   `json:"team_id,omitempty"`
	CustomerID     string   `json:"customer_id,omitempty"`
	BusinessUnitID string   `json:"business_unit_id,omitempty"`
	VirtualKeyID   string   `json:"virtual_key_id,omitempty"`
	Marker         string   `json:"marker"`
	VisibleTo      []string `json:"visible_to"`
}

// ExpectedManifest records seeded IDs and expected DAC visibility.
type ExpectedManifest struct {
	Prefix    string              `json:"prefix"`
	Personas  map[string]string   `json:"personas"`
	Shapes    []Shape             `json:"shapes"`
	LogIDs    map[string][]string `json:"log_ids"`
	MCPLogIDs map[string][]string `json:"mcp_log_ids"`
}

// Summary is returned after a seed run.
type Summary struct {
	Prefix          string            `json:"prefix"`
	LogRowsPerShape int               `json:"log_rows_per_shape"`
	SeedEnv         map[string]string `json:"seed_env"`
	Expected        ExpectedManifest  `json:"expected"`
	TableCounts     map[string]int64  `json:"table_counts"`
	DryRun          bool              `json:"dry_run"`
	GeneratedAt     time.Time         `json:"generated_at"`
}

// seedBaseTime is the per-run anchor timestamp for seeded log rows. SeedBase
// refreshes it to time.Now().UTC() on every invocation so the rows land inside
// the dashboard's default time-range filters (Last hour / Last day) instead of
// drifting toward the original package-load timestamp on repeated seeds.
var seedBaseTime = time.Now().UTC()

// DefaultOptions returns defaults for local e2e seeding.
func DefaultOptions() Options {
	return Options{
		Prefix:          "e2e-seed",
		ConfigDialect:   "postgres",
		LogsDialect:     "postgres",
		LogRowsPerShape: 30,
		BatchSize:       1000,
		OutputEnvPath:   "tmp/e2e-seed.env",
	}
}

// NormalizeOptions applies default values to unset options.
func NormalizeOptions(opts Options) (Options, error) {
	def := DefaultOptions()
	if opts.Prefix == "" {
		opts.Prefix = def.Prefix
	}
	if opts.ConfigPath != "" {
		resolved, err := optionsFromConfigFile(opts.ConfigPath)
		if err != nil {
			return Options{}, fmt.Errorf("load config path %q: %w", opts.ConfigPath, err)
		}
		if opts.EncryptionKey == "" {
			opts.EncryptionKey = resolved.EncryptionKey
		}
		if opts.ConfigDialect == "" && resolved.ConfigDialect != "" {
			opts.ConfigDialect = resolved.ConfigDialect
		}
		if opts.ConfigDSN == "" && resolved.ConfigDSN != "" {
			opts.ConfigDSN = resolved.ConfigDSN
		}
		if opts.LogsDialect == "" && resolved.LogsDialect != "" {
			opts.LogsDialect = resolved.LogsDialect
		}
		if opts.LogsDSN == "" && resolved.LogsDSN != "" {
			opts.LogsDSN = resolved.LogsDSN
		}
	}
	if opts.ConfigDialect == "" {
		opts.ConfigDialect = def.ConfigDialect
	}
	if opts.EncryptionKey == "" {
		opts.EncryptionKey = os.Getenv("BIFROST_ENCRYPTION_KEY")
	}
	if opts.ConfigDSN == "" {
		opts.ConfigDSN = "postgres://bifrost:bifrost_password@localhost:5432/bifrost?sslmode=disable"
	}
	if opts.LogsDialect == "" {
		opts.LogsDialect = def.LogsDialect
	}
	if opts.LogsDSN == "" {
		opts.LogsDSN = "postgres://bifrost:bifrost_password@localhost:5432/bifrost?sslmode=disable"
	}
	if opts.LogRowsPerShape <= 0 {
		opts.LogRowsPerShape = def.LogRowsPerShape
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = def.BatchSize
	}
	if opts.OutputEnvPath == "" {
		opts.OutputEnvPath = def.OutputEnvPath
	}
	return opts, nil
}

// InitEncryption initializes Bifrost field encryption for DBs containing encrypted rows.
func InitEncryption(opts Options) {
	if opts.EncryptionKey == "" {
		return
	}
	encrypt.Init(opts.EncryptionKey, bifrost.NewDefaultLogger(schemas.LogLevelWarn))
}

// optionsFromConfigFile extracts config/log store connection settings from a Bifrost config.json.
func optionsFromConfigFile(path string) (Options, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Options{}, err
	}
	var cfg struct {
		EncryptionKey json.RawMessage `json:"encryption_key"`
		ConfigStore   storeConfig     `json:"config_store"`
		LogsStore     storeConfig     `json:"logs_store"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Options{}, err
	}
	out := Options{}
	out.EncryptionKey = rawConfigValue(cfg.EncryptionKey)
	configDialect, configDSN, err := dsnFromStoreConfig(cfg.ConfigStore)
	if err != nil {
		return Options{}, err
	}
	out.ConfigDialect = configDialect
	out.ConfigDSN = configDSN
	logsDialect, logsDSN, err := dsnFromStoreConfig(cfg.LogsStore)
	if err != nil {
		return Options{}, err
	}
	out.LogsDialect = logsDialect
	out.LogsDSN = logsDSN
	return out, nil
}

// storeConfig is the subset of config.json needed to locate a DB-backed store.
type storeConfig struct {
	Enabled bool                       `json:"enabled"`
	Type    string                     `json:"type"`
	Config  map[string]json.RawMessage `json:"config"`
}

// dsnFromStoreConfig converts a raw store config into the dialect and DSN used by gorm.
func dsnFromStoreConfig(store storeConfig) (string, string, error) {
	if !store.Enabled {
		return "", "", nil
	}
	switch store.Type {
	case "postgres":
		host := configValue(store.Config, "host")
		port := configValue(store.Config, "port")
		user := configValue(store.Config, "user")
		password := configValue(store.Config, "password")
		dbName := configValue(store.Config, "db_name")
		sslMode := configValue(store.Config, "ssl_mode")
		if sslMode == "" {
			sslMode = "disable"
		}
		return "postgres", fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s", url.QueryEscape(user), url.QueryEscape(password), host, port, dbName, sslMode), nil
	case "sqlite":
		return "sqlite", configValue(store.Config, "path"), nil
	default:
		return "", "", fmt.Errorf("unsupported store type %q", store.Type)
	}
}

// configValue returns a raw JSON config value, resolving Bifrost env-var wrappers.
func configValue(config map[string]json.RawMessage, key string) string {
	return rawConfigValue(config[key])
}

// rawConfigValue returns a raw JSON config value, resolving Bifrost env-var wrappers.
func rawConfigValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return schemas.NewSecretVar(text).GetValue()
	}
	var env schemas.SecretVar
	if err := json.Unmarshal(raw, &env); err == nil {
		return env.GetValue()
	}
	return strings.Trim(string(raw), `"`)
}

// OpenDB opens a supported GORM database.
func OpenDB(dialect, dsn string) (*gorm.DB, error) {
	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "postgres", "postgresql":
		if dsn == "" {
			return nil, fmt.Errorf("postgres dsn is required")
		}
		return gorm.Open(postgres.Open(dsn), &gorm.Config{})
	case "sqlite":
		if dsn == "" {
			return nil, fmt.Errorf("sqlite dsn is required")
		}
		return gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	default:
		return nil, fmt.Errorf("unsupported db dialect %q", dialect)
	}
}

// SeedBase writes the OSS-owned seed graph.
func SeedBase(ctx context.Context, configDB, logsDB *gorm.DB, opts Options) (*Summary, error) {
	opts, err := NormalizeOptions(opts)
	if err != nil {
		return nil, err
	}
	seedBaseTime = time.Now().UTC()
	// Encryption is initialized by the caller (see cmd/e2eseed/main.go) before
	// SeedBase runs, so we do not re-initialize it here.
	env := SeedEnv(opts.Prefix)
	manifest := BuildExpectedManifest(opts.Prefix, opts.LogRowsPerShape)
	summary := &Summary{
		Prefix:          opts.Prefix,
		LogRowsPerShape: opts.LogRowsPerShape,
		SeedEnv:         env,
		Expected:        manifest,
		TableCounts:     map[string]int64{},
		DryRun:          opts.DryRun,
		GeneratedAt:     time.Now().UTC(),
	}
	if opts.DryRun {
		return summary, nil
	}
	if err := seedConfig(ctx, configDB, opts); err != nil {
		return nil, err
	}
	if err := seedLogs(ctx, logsDB, opts, manifest); err != nil {
		return nil, err
	}
	if err := WriteEnvFile(opts.OutputEnvPath, env); err != nil {
		return nil, err
	}
	counts, err := CountKnownTables(ctx, configDB, logsDB)
	if err != nil {
		return nil, err
	}
	summary.TableCounts = counts
	return summary, nil
}

// SeedEnv returns deterministic values shared by seeders and tests.
func SeedEnv(prefix string) map[string]string {
	return map[string]string{
		"e2e_seed_prefix":                    prefix,
		"enterprise_dac_model":               "openai/gpt-4o-mini",
		"enterprise_dac_visible_virtual_key": prefix + "-vk-user-team-secret",
		"enterprise_dac_hidden_virtual_key":  prefix + "-vk-outside-secret",
		"e2e_seed_team_tiggings":             prefix + "-team-tiggings",
		"e2e_seed_team_outside":              prefix + "-team-outside",
		"e2e_seed_user_tiggings":             prefix + "-user-tiggings",
		"e2e_seed_user_outside":              prefix + "-user-outside",
		"e2e_seed_vk_user_team":              prefix + "-vk-user-team",
		"e2e_seed_vk_outside":                prefix + "-vk-outside",
	}
}

// BuildShapes returns the DAC ownership matrix.
//
// The set is built by enumerating all 15 non-empty subsets of the four DAC
// dimensions {VirtualKey, UserID, TeamID, BusinessUnitID}, all flavoured with
// tiggings-side values so the tiggings-flavoured personas see them, plus
// three appended shapes for negative coverage:
//   - user-not-in-tiggings: outside user + BU (cross-team isolation)
//   - outside-team-virtual-key: outside user + team + BU + VK
//   - legacy-unowned: no DAC dimensions (fail-closed for non-admin)
//
// CustomerID is set together with BusinessUnitID when the B dimension is
// present; the two columns are conceptually paired in the seeded dataset.
//
// The VirtualKey value for each tiggings shape is teamVK when the team
// dimension is present without a user, so the team-only VK ownership path
// (governance_virtual_keys.team_id) gets exercised. Every other VK-bearing
// shape uses userVK so the user-attached VK ownership path is exercised too.
func BuildShapes(prefix string) []Shape {
	tiggingsUser := prefix + "-user-tiggings"
	outsideUser := prefix + "-user-outside"
	tiggingsTeam := prefix + "-team-tiggings"
	outsideTeam := prefix + "-team-outside"
	tiggingsCustomer := prefix + "-customer-tiggings"
	outsideCustomer := prefix + "-customer-outside"
	tiggingsBU := prefix + "-bu-tiggings"
	outsideBU := prefix + "-bu-outside"
	userVK := prefix + "-vk-user-team"
	teamVK := prefix + "-vk-team-only"
	outsideVK := prefix + "-vk-outside"

	shapes := make([]Shape, 0, 18)
	for mask := 1; mask <= 15; mask++ {
		hasVK := mask&0x1 != 0
		hasU := mask&0x2 != 0
		hasT := mask&0x4 != 0
		hasB := mask&0x8 != 0

		shape := Shape{Name: shapeNameFromDims(hasVK, hasU, hasT, hasB)}
		if hasVK {
			if hasT && !hasU {
				shape.VirtualKeyID = teamVK
			} else {
				shape.VirtualKeyID = userVK
			}
		}
		if hasU {
			shape.UserID = tiggingsUser
		}
		if hasT {
			shape.TeamID = tiggingsTeam
		}
		if hasB {
			shape.BusinessUnitID = tiggingsBU
			shape.CustomerID = tiggingsCustomer
		}
		shape.Marker = prefix + "-shape-" + shape.Name
		shape.VisibleTo = computeVisibleTo(shape, tiggingsUser, outsideUser, tiggingsTeam, outsideTeam, userVK, teamVK, outsideVK)
		shapes = append(shapes, shape)
	}

	shapes = append(shapes,
		Shape{
			Name:           "user-not-in-tiggings",
			UserID:         outsideUser,
			CustomerID:     outsideCustomer,
			BusinessUnitID: outsideBU,
			Marker:         prefix + "-shape-user-not-in-tiggings",
			VisibleTo:      []string{"all_data_admin", "own_reader_outside", "team_reader_outside"},
		},
		Shape{
			Name:           "outside-team-virtual-key",
			UserID:         outsideUser,
			TeamID:         outsideTeam,
			CustomerID:     outsideCustomer,
			BusinessUnitID: outsideBU,
			VirtualKeyID:   outsideVK,
			Marker:         prefix + "-shape-outside-team-vk",
			VisibleTo:      []string{"all_data_admin", "own_reader_outside", "team_reader_outside"},
		},
		Shape{
			Name:      "legacy-unowned",
			Marker:    prefix + "-shape-legacy-unowned",
			VisibleTo: []string{"all_data_admin"},
		},
	)
	return shapes
}

// shapeNameFromDims returns a deterministic kebab-case name for the given
// dimension presence flags. Single-dimension shapes are prefixed with "only-"
// so the matrix reads naturally in the manifest.
func shapeNameFromDims(hasVK, hasU, hasT, hasB bool) string {
	var parts []string
	if hasVK {
		parts = append(parts, "virtual-key")
	}
	if hasU {
		parts = append(parts, "user")
	}
	if hasT {
		parts = append(parts, "team")
	}
	if hasB {
		parts = append(parts, "business-unit")
	}
	if len(parts) == 1 {
		return "only-" + parts[0]
	}
	return strings.Join(parts, "-")
}

// computeVisibleTo returns the sorted set of personas that can see rows of the
// given shape, derived directly from the shape's DAC dimensions and the
// well-known seeded principal/VK values.
func computeVisibleTo(s Shape, tigU, outU, tigT, outT, userVK, teamVK, outVK string) []string {
	set := map[string]struct{}{"all_data_admin": {}}

	switch s.UserID {
	case tigU:
		set["own_reader_tiggings"] = struct{}{}
		set["team_reader_tiggings"] = struct{}{}
	case outU:
		set["own_reader_outside"] = struct{}{}
		set["team_reader_outside"] = struct{}{}
	}

	switch s.TeamID {
	case tigT:
		set["team_reader_tiggings"] = struct{}{}
	case outT:
		set["team_reader_outside"] = struct{}{}
	}

	switch s.VirtualKeyID {
	case userVK:
		set["vk_user_owned"] = struct{}{}
		set["own_reader_tiggings"] = struct{}{}
		set["team_reader_tiggings"] = struct{}{}
	case teamVK:
		set["vk_team_owned"] = struct{}{}
		set["team_reader_tiggings"] = struct{}{}
	case outVK:
		set["own_reader_outside"] = struct{}{}
		set["team_reader_outside"] = struct{}{}
	}

	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// BuildExpectedManifest returns the expected DAC visibility document. Each
// shape gets logRowsPerShape contiguous log IDs and an equal number of MCP
// log IDs so visibility tests can assert exact set membership for either
// table.
func BuildExpectedManifest(prefix string, logRowsPerShape int) ExpectedManifest {
	shapes := BuildShapes(prefix)
	logIDs := make(map[string][]string, len(shapes))
	mcpLogIDs := make(map[string][]string, len(shapes))
	for shapeIdx, shape := range shapes {
		for j := 0; j < logRowsPerShape; j++ {
			i := shapeIdx*logRowsPerShape + j
			logIDs[shape.Name] = append(logIDs[shape.Name], fmt.Sprintf("%s-log-%06d", prefix, i))
			mcpLogIDs[shape.Name] = append(mcpLogIDs[shape.Name], fmt.Sprintf("%s-mcp-log-%06d", prefix, i))
		}
	}
	return ExpectedManifest{
		Prefix: prefix,
		Personas: map[string]string{
			"own_reader_tiggings":  prefix + "-apikey-own-tiggings",
			"team_reader_tiggings": prefix + "-apikey-team-tiggings",
			"own_reader_outside":   prefix + "-apikey-own-outside",
			"team_reader_outside":  prefix + "-apikey-team-outside",
			"all_data_admin":       prefix + "-apikey-admin",
			"vk_user_owned":        prefix + "-vk-user-team",
			"vk_team_owned":        prefix + "-vk-team-only",
		},
		Shapes:    shapes,
		LogIDs:    logIDs,
		MCPLogIDs: mcpLogIDs,
	}
}

// WriteEnvFile writes deterministic environment values.
func WriteEnvFile(path string, env map[string]string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s=%s", key, quoteEnv(env[key])))
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// WriteJSONFile writes a pretty JSON file.
func WriteJSONFile(path string, value any) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// CountKnownTables returns row counts for tables touched by the shared seed.
func CountKnownTables(ctx context.Context, configDB, logsDB *gorm.DB) (map[string]int64, error) {
	out := map[string]int64{}
	for _, table := range []string{"config_providers", "config_keys", "config_models", "governance_customers", "governance_teams", "governance_virtual_keys", "governance_virtual_key_provider_configs", "governance_budgets", "governance_rate_limits", "folders", "prompts", "prompt_versions", "config_mcp_clients"} {
		var count int64
		if err := configDB.WithContext(ctx).Table(table).Count(&count).Error; err != nil {
			return nil, fmt.Errorf("count table %s: %w", table, err)
		}
		out[table] = count
	}
	for _, table := range []string{"logs", "mcp_tool_logs", "async_jobs"} {
		var count int64
		if err := logsDB.WithContext(ctx).Table(table).Count(&count).Error; err != nil {
			return nil, fmt.Errorf("count table %s: %w", table, err)
		}
		out[table] = count
	}
	return out, nil
}

// seedConfig writes OSS-owned relational fixtures.
func seedConfig(ctx context.Context, db *gorm.DB, opts Options) error {
	if db == nil {
		return fmt.Errorf("config db is required")
	}
	now := seedBaseTime
	if err := seedProviders(ctx, db, opts.Prefix, now); err != nil {
		return err
	}
	if err := seedGovernance(ctx, db, opts.Prefix, now); err != nil {
		return err
	}
	if err := seedPrompts(ctx, db, opts.Prefix, now); err != nil {
		return err
	}
	return seedMCP(ctx, db, opts.Prefix, now)
}

// seedProviders writes providers, keys, and models.
func seedProviders(ctx context.Context, db *gorm.DB, prefix string, now time.Time) error {
	for _, providerName := range []string{"openai", "anthropic", "gemini"} {
		provider := tables.TableProvider{Name: providerName, Status: "active", Description: "e2e seeded provider", CreatedAt: now, UpdatedAt: now}
		if err := db.WithContext(ctx).Where("name = ?", providerName).Assign(provider).FirstOrCreate(&provider).Error; err != nil {
			return err
		}
		key := tables.TableKey{
			Name:        prefix + "-" + providerName + "-key",
			ProviderID:  provider.ID,
			Provider:    providerName,
			KeyID:       prefix + "-" + providerName + "-key",
			Value:       *schemas.NewSecretVar(strings.ToUpper(providerName) + "_API_KEY"),
			Models:      schemas.WhiteList{"*"},
			Status:      "active",
			Description: "e2e seeded provider key",
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := db.WithContext(ctx).Where("key_id = ?", key.KeyID).Assign(key).FirstOrCreate(&key).Error; err != nil {
			return err
		}
		model := tables.TableModel{ID: prefix + "-" + providerName + "-model", ProviderID: provider.ID, Name: defaultModel(providerName), CreatedAt: now, UpdatedAt: now}
		if err := db.WithContext(ctx).Where("id = ?", model.ID).Assign(model).FirstOrCreate(&model).Error; err != nil {
			return err
		}
	}
	return nil
}

// seedGovernance writes customers, teams, budgets, rate limits, VKs, and VK provider configs.
func seedGovernance(ctx context.Context, db *gorm.DB, prefix string, now time.Time) error {
	tiggingsCustomer := prefix + "-customer-tiggings"
	outsideCustomer := prefix + "-customer-outside"
	tiggingsTeam := prefix + "-team-tiggings"
	outsideTeam := prefix + "-team-outside"
	for _, customer := range []tables.TableCustomer{{ID: tiggingsCustomer, Name: "Tiggings Customer", CreatedAt: now, UpdatedAt: now}, {ID: outsideCustomer, Name: "Outside Customer", CreatedAt: now, UpdatedAt: now}} {
		if err := db.WithContext(ctx).Where("id = ?", customer.ID).Assign(customer).FirstOrCreate(&customer).Error; err != nil {
			return err
		}
	}
	for _, team := range []tables.TableTeam{{ID: tiggingsTeam, Name: "Tiggings", CustomerID: &tiggingsCustomer, CreatedAt: now, UpdatedAt: now}, {ID: outsideTeam, Name: "Outside Team", CustomerID: &outsideCustomer, CreatedAt: now, UpdatedAt: now}} {
		if err := db.WithContext(ctx).Where("id = ?", team.ID).Assign(team).FirstOrCreate(&team).Error; err != nil {
			return err
		}
	}
	active := new(true)
	for _, vk := range []tables.TableVirtualKey{
		{ID: prefix + "-vk-user-team", Name: "E2E User Team VK", Value: *schemas.NewSecretVar(prefix + "-vk-user-team-secret"), IsActive: active, TeamID: &tiggingsTeam, CreatedAt: now, UpdatedAt: now},
		{ID: prefix + "-vk-team-only", Name: "E2E Team Only VK", Value: *schemas.NewSecretVar(prefix + "-vk-team-only-secret"), IsActive: active, TeamID: &tiggingsTeam, CreatedAt: now, UpdatedAt: now},
		{ID: prefix + "-vk-outside", Name: "E2E Outside VK", Value: *schemas.NewSecretVar(prefix + "-vk-outside-secret"), IsActive: active, TeamID: &outsideTeam, CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.WithContext(ctx).Where("id = ?", vk.ID).Assign(vk).FirstOrCreate(&vk).Error; err != nil {
			return err
		}
		pc := tables.TableVirtualKeyProviderConfig{VirtualKeyID: vk.ID, Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, AllowAllKeys: true}
		if err := db.WithContext(ctx).Where("virtual_key_id = ? AND provider = ?", vk.ID, "openai").Assign(pc).FirstOrCreate(&pc).Error; err != nil {
			return err
		}
	}
	return nil
}

// seedPrompts writes a folder, prompt, and prompt version.
func seedPrompts(ctx context.Context, db *gorm.DB, prefix string, now time.Time) error {
	folder := tables.TableFolder{ID: prefix + "-folder", Name: "E2E Seed Folder", CreatedAt: now, UpdatedAt: now}
	if err := db.WithContext(ctx).Where("id = ?", folder.ID).Assign(folder).FirstOrCreate(&folder).Error; err != nil {
		return err
	}
	prompt := tables.TablePrompt{ID: prefix + "-prompt", Name: "E2E Seed Prompt", FolderID: &folder.ID, CreatedAt: now, UpdatedAt: now}
	if err := db.WithContext(ctx).Where("id = ?", prompt.ID).Assign(prompt).FirstOrCreate(&prompt).Error; err != nil {
		return err
	}
	version := tables.TablePromptVersion{PromptID: prompt.ID, VersionNumber: 1, CommitMessage: "seed", Provider: "openai", Model: "gpt-4o-mini", IsLatest: true, CreatedAt: now}
	return db.WithContext(ctx).Where("prompt_id = ? AND version_number = ?", prompt.ID, 1).Assign(version).FirstOrCreate(&version).Error
}

// seedMCP writes a minimal MCP client.
func seedMCP(ctx context.Context, db *gorm.DB, prefix string, now time.Time) error {
	client := tables.TableMCPClient{
		ClientID:         prefix + "-mcp-client",
		Name:             "E2E Seed MCP",
		ConnectionType:   "sse",
		ConnectionString: schemas.NewSecretVar("https://mcp.e2e.local/sse"),
		ToolsToExecute:   schemas.WhiteList{"*"},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	return db.WithContext(ctx).Where("client_id = ?", client.ClientID).Assign(client).FirstOrCreate(&client).Error
}

// seedLogs writes the DAC matrix LLM logs. Each shape gets exactly
// opts.LogRowsPerShape rows, so admin sees len(shapes) * LogRowsPerShape
// total.
func seedLogs(ctx context.Context, db *gorm.DB, opts Options, manifest ExpectedManifest) error {
	if db == nil {
		return fmt.Errorf("logs db is required")
	}
	shapes := manifest.Shapes
	batch := make([]logstore.Log, 0, opts.BatchSize)
	for shapeIdx, shape := range shapes {
		for j := 0; j < opts.LogRowsPerShape; j++ {
			i := shapeIdx*opts.LogRowsPerShape + j
			batch = append(batch, buildLog(opts.Prefix, shape, i))
			if len(batch) == opts.BatchSize {
				if err := db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&batch).Error; err != nil {
					return err
				}
				batch = batch[:0]
			}
		}
	}
	if len(batch) > 0 {
		if err := db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&batch).Error; err != nil {
			return err
		}
	}
	if err := seedMCPLogs(ctx, db, opts, manifest); err != nil {
		return err
	}
	return seedAsyncJobCompanion(ctx, db, opts.Prefix)
}

// seedMCPLogs writes the DAC matrix MCP tool logs in the same shape and
// volume as the LLM logs so MCP visibility tests can assert against the
// expected.mcp_log_ids manifest the same way the LLM tests do.
func seedMCPLogs(ctx context.Context, db *gorm.DB, opts Options, manifest ExpectedManifest) error {
	shapes := manifest.Shapes
	batch := make([]logstore.MCPToolLog, 0, opts.BatchSize)
	for shapeIdx, shape := range shapes {
		for j := 0; j < opts.LogRowsPerShape; j++ {
			i := shapeIdx*opts.LogRowsPerShape + j
			batch = append(batch, buildMCPLog(opts.Prefix, shape, i))
			if len(batch) == opts.BatchSize {
				if err := db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&batch).Error; err != nil {
					return err
				}
				batch = batch[:0]
			}
		}
	}
	if len(batch) > 0 {
		if err := db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&batch).Error; err != nil {
			return err
		}
	}
	return nil
}

// seedAsyncJobCompanion writes one async-job row tied to the seeded user VK
// so the /api/async-jobs CRUD coverage has a deterministic fixture to read.
func seedAsyncJobCompanion(ctx context.Context, db *gorm.DB, prefix string) error {
	now := seedBaseTime
	vk := prefix + "-vk-user-team"
	completed := now
	job := logstore.AsyncJob{ID: prefix + "-async-job", Status: schemas.AsyncJobStatusCompleted, RequestType: schemas.ChatCompletionRequest, Response: `{}`, StatusCode: 200, VirtualKeyID: &vk, ResultTTL: 3600, CreatedAt: now, CompletedAt: &completed}
	return db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&job).Error
}

// buildMCPLog returns one deterministic MCP tool log row mirroring the DAC
// dimensions of the shape so the same persona/visibility logic that drives
// LLM log tests applies here.
func buildMCPLog(prefix string, shape Shape, index int) logstore.MCPToolLog {
	timestamp := seedBaseTime.Add(-time.Duration(index) * time.Second)
	vkName := ""
	if shape.VirtualKeyID != "" {
		vkName = "E2E " + shape.VirtualKeyID
	}
	latency := float64(20 + index%200)
	cost := float64(1+index%50) / 100000
	status := "success"
	if index%19 == 0 {
		status = "error"
	}
	return logstore.MCPToolLog{
		ID:              fmt.Sprintf("%s-mcp-log-%06d", prefix, index),
		RequestID:       fmt.Sprintf("%s-mcp-req-%06d", prefix, index),
		Timestamp:       timestamp,
		ToolName:        "e2e-tool",
		ServerLabel:     "e2e-mcp",
		VirtualKeyID:    emptyPtr(shape.VirtualKeyID),
		VirtualKeyName:  emptyPtr(vkName),
		UserID:          emptyPtr(shape.UserID),
		TeamID:          emptyPtr(shape.TeamID),
		CustomerID:      emptyPtr(shape.CustomerID),
		BusinessUnitID:  emptyPtr(shape.BusinessUnitID),
		ArgumentsParsed: map[string]any{"seed_prefix": prefix, "shape": shape.Name, "marker": shape.Marker, "index": index},
		ResultParsed:    map[string]any{"ok": true},
		Latency:         &latency,
		Cost:            &cost,
		Status:          status,
		MetadataParsed:  map[string]any{"seed_prefix": prefix, "shape": shape.Name, "marker": shape.Marker},
		CreatedAt:       timestamp,
	}
}

// buildLog returns one deterministic log row.
func buildLog(prefix string, shape Shape, index int) logstore.Log {
	timestamp := seedBaseTime.Add(-time.Duration(index) * time.Second)
	vkName := ""
	if shape.VirtualKeyID != "" {
		vkName = "E2E " + shape.VirtualKeyID
	}
	latency := float64(100 + index%500)
	cost := float64(1+index%100) / 100000
	promptTokens := 10 + index%30
	completionTokens := 5 + index%20
	status := "success"
	if index%17 == 0 {
		status = "error"
	}
	return logstore.Log{
		ID:               fmt.Sprintf("%s-log-%06d", prefix, index),
		Timestamp:        timestamp,
		Object:           "chat.completion",
		Provider:         "openai",
		Model:            "gpt-4o-mini",
		SelectedKeyID:    prefix + "-openai-key",
		SelectedKeyName:  "E2E OpenAI Key",
		VirtualKeyID:     emptyPtr(shape.VirtualKeyID),
		VirtualKeyName:   emptyPtr(vkName),
		UserID:           emptyPtr(shape.UserID),
		UserName:         emptyPtr(nameFromID(shape.UserID)),
		TeamID:           emptyPtr(shape.TeamID),
		TeamName:         emptyPtr(nameFromID(shape.TeamID)),
		CustomerID:       emptyPtr(shape.CustomerID),
		CustomerName:     emptyPtr(nameFromID(shape.CustomerID)),
		BusinessUnitID:   emptyPtr(shape.BusinessUnitID),
		BusinessUnitName: emptyPtr(nameFromID(shape.BusinessUnitID)),
		InputHistoryParsed: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: ptr(shape.Marker + " prompt")},
		}},
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: ptr("seeded response")}},
		Latency:             &latency,
		Cost:                &cost,
		Status:              status,
		ContentSummary:      shape.Marker,
		MetadataParsed:      map[string]any{"seed_prefix": prefix, "shape": shape.Name, "marker": shape.Marker},
		PromptTokens:        promptTokens,
		CompletionTokens:    completionTokens,
		TotalTokens:         promptTokens + completionTokens,
		RoutingEnginesUsed:  []string{"governance"},
		CreatedAt:           timestamp,
	}
}

// defaultModel returns a model for a provider.
func defaultModel(provider string) string {
	switch provider {
	case "anthropic":
		return "claude-3-5-haiku"
	case "gemini":
		return "gemini-1.5-flash"
	default:
		return "gpt-4o-mini"
	}
}

// emptyPtr returns nil for empty strings.
func emptyPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// ptr returns a string pointer.
func ptr(value string) *string {
	return &value
}

// nameFromID derives a display name from an id.
func nameFromID(id string) string {
	if id == "" {
		return ""
	}
	return strings.Title(strings.ReplaceAll(id, "-", " "))
}

// quoteEnv returns a shell-safe env value.
func quoteEnv(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
