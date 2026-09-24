package tables

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TableVirtualKeyProviderConfigKey is the join table for the many2many relationship
// between TableVirtualKeyProviderConfig and TableKey
type TableVirtualKeyProviderConfigKey struct {
	TableVirtualKeyProviderConfigID uint `gorm:"primaryKey;uniqueIndex:idx_vk_provider_config_key"`
	TableKeyID                      uint `gorm:"primaryKey;uniqueIndex:idx_vk_provider_config_key"`
}

// TableName sets the table name for the join table
func (TableVirtualKeyProviderConfigKey) TableName() string {
	return "governance_virtual_key_provider_config_keys"
}

// TableVirtualKeyProviderConfig represents a provider configuration for a virtual key
type TableVirtualKeyProviderConfig struct {
	ID                uint              `gorm:"primaryKey;autoIncrement" json:"id"`
	VirtualKeyID      string            `gorm:"type:varchar(255);not null" json:"virtual_key_id"`
	Provider          string            `gorm:"type:varchar(50);not null" json:"provider"`
	Weight            *float64          `json:"weight"`
	AllowedModels     schemas.WhiteList `gorm:"type:text;serializer:json" json:"allowed_models"`     // ["*"] allows all models; empty denies all (deny-by-default)
	BlacklistedModels schemas.BlackList `gorm:"type:text;serializer:json" json:"blacklisted_models"` // ["*"] blocks all models; empty blocks none
	AllowAllKeys      bool              `gorm:"default:false" json:"allow_all_keys"`                 // True means all keys allowed; false with empty Keys means no keys allowed (deny-by-default)
	RateLimitID       *string           `gorm:"type:varchar(255);index" json:"rate_limit_id,omitempty"`

	// Relationships
	RateLimit *TableRateLimit `gorm:"foreignKey:RateLimitID;onDelete:CASCADE" json:"rate_limit,omitempty"`
	Budgets   []TableBudget   `gorm:"foreignKey:ProviderConfigID;constraint:OnDelete:CASCADE" json:"budgets,omitempty"`              // Multiple budgets with different reset intervals
	Keys      []TableKey      `gorm:"many2many:governance_virtual_key_provider_config_keys;constraint:OnDelete:CASCADE" json:"keys"` // Empty means no keys allowed for this provider unless AllowAllKeys is set

	// ModelBudgets carries per-model budgets/rate-limits under this provider for serialization
	// only. They live in VK-scoped model configs (the source of truth), not this table; the
	// handler hydrates this field when returning a VK so the sheet can render/edit them.
	ModelBudgets []VKProviderModelBudget `gorm:"-" json:"model_budgets,omitempty"`
}

// KeyIDs returns the list of key IDs for this provider config, or ["*"] if AllowAllKeys is true.
func (pc *TableVirtualKeyProviderConfig) KeyIDs() []string {
	if pc.AllowAllKeys {
		return []string{"*"}
	}
	keyIDs := make([]string, len(pc.Keys))
	for i, key := range pc.Keys {
		keyIDs[i] = key.KeyID
	}
	return keyIDs
}

// VKProviderModelBudget is one per-model budget/rate-limit group under a VK provider config,
// used purely for serialization (reverse-mapped from a VK-scoped model config).
type VKProviderModelBudget struct {
	ModelName string          `json:"model_name"`
	Budgets   []TableBudget   `json:"budgets,omitempty"`
	RateLimit *TableRateLimit `json:"rate_limit,omitempty"`
}

// TableName sets the table name for each model
func (TableVirtualKeyProviderConfig) TableName() string {
	return "governance_virtual_key_provider_configs"
}

// UnmarshalJSON custom unmarshaller to handle "key_ids" ([]string) config-file format
func (pc *TableVirtualKeyProviderConfig) UnmarshalJSON(data []byte) error {
	type Alias TableVirtualKeyProviderConfig
	type TempProviderConfig struct {
		Alias
		KeyIDs []string `json:"key_ids"` // Config file format: key identifiers (TableKey.KeyID); use ["*"] to allow all keys, empty denies all
	}

	var temp TempProviderConfig
	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	// Copy all standard fields
	*pc = TableVirtualKeyProviderConfig(temp.Alias)

	// If key_ids is provided, convert to Keys or set AllowAllKeys
	if len(temp.KeyIDs) > 0 && len(pc.Keys) == 0 {
		// ["*"] means allow all keys
		if len(temp.KeyIDs) == 1 && temp.KeyIDs[0] == "*" {
			pc.AllowAllKeys = true
			pc.Keys = nil
		} else {
			pc.AllowAllKeys = false
			pc.Keys = make([]TableKey, len(temp.KeyIDs))
			for i, keyID := range temp.KeyIDs {
				pc.Keys[i] = TableKey{KeyID: keyID}
			}
		}
	}

	return nil
}

// BeforeSave validates WhiteList and BlackList fields before GORM persists the record.
func (pc *TableVirtualKeyProviderConfig) BeforeSave(tx *gorm.DB) error {
	if err := pc.AllowedModels.Validate(); err != nil {
		return fmt.Errorf("invalid allowed_models: %w", err)
	}
	if err := pc.BlacklistedModels.Validate(); err != nil {
		return fmt.Errorf("invalid blacklisted_models: %w", err)
	}
	return nil
}

// MarshalJSON custom marshaller to ensure AllowedModels and BlacklistedModels are always arrays (never null)
func (pc TableVirtualKeyProviderConfig) MarshalJSON() ([]byte, error) {
	type Alias TableVirtualKeyProviderConfig

	// Ensure arrays are empty slices instead of nil
	allowedModels := pc.AllowedModels
	if allowedModels == nil {
		allowedModels = []string{}
	}
	blacklistedModels := pc.BlacklistedModels
	if blacklistedModels == nil {
		blacklistedModels = []string{}
	}

	return json.Marshal(&struct {
		Alias
		AllowedModels     []string `json:"allowed_models"`
		BlacklistedModels []string `json:"blacklisted_models"`
	}{
		Alias:             Alias(pc),
		AllowedModels:     allowedModels,
		BlacklistedModels: blacklistedModels,
	})
}

// AfterFind hook for TableVirtualKeyProviderConfig to clear sensitive data from associated keys
func (pc *TableVirtualKeyProviderConfig) AfterFind(tx *gorm.DB) error {
	if pc.Keys != nil {
		// Clear sensitive data from associated keys, keeping only key IDs and non-sensitive metadata
		for i := range pc.Keys {
			key := &pc.Keys[i]

			// Clear the actual API key value
			key.Value = *schemas.NewSecretVar("")

			// Clear all Azure-related sensitive fields
			key.AzureEndpoint = nil
			key.AzureClientID = nil
			key.AzureClientSecret = nil
			key.AzureTenantID = nil
			key.AzureScopesJSON = nil
			key.AzureKeyConfig = nil

			// Clear all Vertex-related sensitive fields
			key.VertexProjectID = nil
			key.VertexProjectNumber = nil
			key.VertexRegion = nil
			key.VertexAuthCredentials = nil
			key.VertexKeyConfig = nil

			// Clear all Bedrock-related sensitive fields
			key.BedrockAccessKey = nil
			key.BedrockSecretKey = nil
			key.BedrockSessionToken = nil
			key.BedrockRegion = nil
			key.BedrockARN = nil
			key.BedrockRoleARN = nil
			key.BedrockExternalID = nil
			key.BedrockRoleSessionName = nil
			key.BedrockKeyConfig = nil

			// Clear all Bedrock Mantle-related sensitive fields
			key.BedrockMantleAccessKey = nil
			key.BedrockMantleSecretKey = nil
			key.BedrockMantleSessionToken = nil
			key.BedrockMantleRegion = nil
			key.BedrockMantleRoleARN = nil
			key.BedrockMantleExternalID = nil
			key.BedrockMantleRoleSessionName = nil
			key.BedrockMantleKeyConfig = nil

			pc.Keys[i] = *key
		}
	}
	return nil
}

type TableVirtualKeyMCPConfig struct {
	ID             uint              `gorm:"primaryKey;autoIncrement" json:"id"`
	VirtualKeyID   string            `gorm:"type:varchar(255);not null;uniqueIndex:idx_vk_mcpclient" json:"virtual_key_id"`
	MCPClientID    uint              `gorm:"not null;uniqueIndex:idx_vk_mcpclient" json:"mcp_client_id"`
	MCPClient      TableMCPClient    `gorm:"foreignKey:MCPClientID" json:"mcp_client"`
	ToolsToExecute schemas.WhiteList `gorm:"type:text;serializer:json" json:"tools_to_execute"`

	// MCPClientName is used during config file parsing to resolve the MCP client by name.
	// This field is not persisted to the database - it's only used to capture
	// "mcp_client_name" from config.json and then resolve it to MCPClientID.
	MCPClientName string `gorm:"-" json:"-"`
}

// TableName sets the table name for each model
func (TableVirtualKeyMCPConfig) TableName() string {
	return "governance_virtual_key_mcp_configs"
}

// BeforeSave validates WhiteList fields before GORM persists the record.
func (mc *TableVirtualKeyMCPConfig) BeforeSave(tx *gorm.DB) error {
	if err := mc.ToolsToExecute.Validate(); err != nil {
		return fmt.Errorf("invalid tools_to_execute: %w", err)
	}
	return nil
}

// UnmarshalJSON custom unmarshaller to handle both "mcp_client_id" (database format)
// and "mcp_client_name" (config file format) for MCP client references.
func (mc *TableVirtualKeyMCPConfig) UnmarshalJSON(data []byte) error {
	// Temporary struct to capture all fields including mcp_client_name
	type Alias TableVirtualKeyMCPConfig
	type TempMCPConfig struct {
		Alias
		MCPClientName string `json:"mcp_client_name"` // Config file format: MCP client name
	}
	var temp TempMCPConfig
	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}
	// Copy all standard fields
	*mc = TableVirtualKeyMCPConfig(temp.Alias)
	// Capture mcp_client_name for later resolution to MCPClientID
	if temp.MCPClientName != "" {
		mc.MCPClientName = temp.MCPClientName
	}
	return nil
}

// TableVirtualKey represents a virtual key with budget, rate limits, and team/customer association
type TableVirtualKey struct {
	ID              string                          `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name            string                          `gorm:"uniqueIndex:idx_virtual_key_name;type:varchar(255);not null" json:"name"`
	Description     string                          `gorm:"type:text" json:"description,omitempty"`
	Value           schemas.SecretVar               `gorm:"uniqueIndex:idx_virtual_key_value;type:text;not null" json:"value"`
	IsActive        *bool                           `gorm:"default:true" json:"is_active,omitempty"`                                     // Nil means true (DB default); false means inactive
	ExpiresAt       *time.Time                      `gorm:"type:timestamp;null" json:"expires_at,omitempty"`                             // Optional expiry; nil means never expires
	ProviderConfigs []TableVirtualKeyProviderConfig `gorm:"foreignKey:VirtualKeyID;constraint:OnDelete:CASCADE" json:"provider_configs"` // Empty means no providers allowed (deny-by-default)
	MCPConfigs      []TableVirtualKeyMCPConfig      `gorm:"foreignKey:VirtualKeyID;constraint:OnDelete:CASCADE" json:"mcp_configs"`

	// Foreign key relationships. TeamID, CustomerID and BusinessUnitID are mutually exclusive: a
	// key belongs to at most one owner, which is what decides whose money it spends and whose
	// access profile it answers to.
	TeamID     *string `gorm:"type:varchar(255);index" json:"team_id,omitempty"`
	CustomerID *string `gorm:"type:varchar(255);index" json:"customer_id,omitempty"`
	// BusinessUnitID is a bare indexed column rather than a GORM association: business units are
	// an enterprise table this package does not know, so the column records the owner without this
	// side being able to preload it. Whoever owns the business unit resolves the name.
	BusinessUnitID *string `gorm:"type:varchar(255);index" json:"business_unit_id,omitempty"`
	RateLimitID    *string `gorm:"type:varchar(255);index" json:"rate_limit_id,omitempty"`

	CalendarAligned bool `gorm:"default:false" json:"calendar_aligned"`

	AllowAllProviders bool `gorm:"default:false" json:"allow_all_providers"`

	// DisableContentLogging is the key's own say on whether request and response content is
	// persisted for its traffic. Tri-state on purpose: nil inherits client.disable_content_logging,
	// true forces content off for every sink, false forces content on for the log store only (each
	// observability connector keeps its own flag). No gorm default: a default tag would make GORM
	// write the default for a nil pointer on insert and collapse "inherit" into "false".
	DisableContentLogging *bool `gorm:"type:boolean" json:"disable_content_logging,omitempty"`

	// Relationships
	Team      *TableTeam      `gorm:"foreignKey:TeamID" json:"team,omitempty"`
	Customer  *TableCustomer  `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
	RateLimit *TableRateLimit `gorm:"foreignKey:RateLimitID;onDelete:CASCADE" json:"rate_limit,omitempty"`
	Budgets   []TableBudget   `gorm:"foreignKey:VirtualKeyID;constraint:OnDelete:CASCADE" json:"budgets,omitempty"` // Multiple budgets with different reset intervals

	// IsAccessProfileManaged is a read-only, computed flag (never persisted): true when
	// the VK is governed by an access profile, so the UI can lock edits and show the
	// managed-key notice without a separate, differently-gated access-profile lookup.
	// Populated on the governance read paths from the external resolver; false in OSS.
	IsAccessProfileManaged bool `gorm:"-" json:"is_access_profile_managed,omitempty"`

	// AssignedUser is the user this key is assigned to, when any. Like
	// IsAccessProfileManaged it is read-only and never persisted: the VK-user link
	// lives in an enterprise table, so it is filled in on the governance read paths
	// by a downstream resolver and stays nil in OSS. No omitempty - "no assignee" has
	// to reach the UI as an explicit null. Absence carries the other half of the
	// meaning, "not resolved", and is produced by MarshalJSON off AssigneeResolved
	// rather than by a struct tag, which cannot tell the two nils apart.
	AssignedUser *AssignedUser `gorm:"-" json:"assigned_user"`

	// AssigneeResolved records whether AssignedUser is an answer or an absence of one.
	// True means the assignee lookup ran and settled the question, so AssignedUser is
	// authoritative (a user, or nil for genuinely unassigned) and marshals as
	// `assigned_user`. False means nobody asked, or the resolver failed, and
	// MarshalJSON drops the field so callers refetch instead of reading nil as
	// "unassigned". Never persisted; set by the governance read paths.
	AssigneeResolved bool `gorm:"-" json:"-"`

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	EncryptionStatus string `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	ValueHash        string `gorm:"type:varchar(64);index:idx_virtual_key_value_hash,unique" json:"-"`

	// Rotation grace-period state. When a VK is rotated with a non-zero
	// vk_rotation_cooldown, the retired value is kept here and keeps
	// authenticating until PreviousValueExpiresAt. Runtime state only: these
	// fields are excluded from GenerateVirtualKeyHash so config.json sync never
	// sees rotation as drift. The hash index is intentionally non-unique - a
	// retiring value cannot reserve uniqueness against live values.
	PreviousValue          schemas.SecretVar `gorm:"type:text" json:"-"`
	PreviousValueHash      string            `gorm:"type:varchar(64);index:idx_virtual_key_previous_value_hash" json:"-"`
	PreviousValueExpiresAt *time.Time        `gorm:"type:timestamp;null" json:"previous_value_expires_at,omitempty"`
	RotatedAt              *time.Time        `gorm:"type:timestamp;null" json:"rotated_at,omitempty"`

	CreatedByUserID *string `gorm:"type:varchar(255);index:idx_virtual_key_created_by" json:"created_by_user_id,omitempty"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// AssignedUser is the minimal projection of the user a virtual key is assigned to,
// carried on read responses so callers do not need a second, per-key lookup. It is
// deliberately not the full user row: a list response has no business shipping
// claims, config, or role.
type AssignedUser struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// TableName sets the table name for each model
func (TableVirtualKey) TableName() string { return "governance_virtual_keys" }

// IsActiveValue returns the effective IsActive bool, treating nil as true (DB default).
func (vk *TableVirtualKey) IsActiveValue() bool {
	if vk == nil {
		return false
	}
	if vk.IsActive == nil {
		return true
	}
	return *vk.IsActive
}

// VaultPathKey implements schemas.VaultPathKeyer so vault callbacks can compute the
// vault base path for this model automatically.
func (vk *TableVirtualKey) VaultPathKey() string { return vk.ID }

// VaultStoreSelfManaged marks TableVirtualKey as storing its own vault secrets from
// within BeforeSave, so the global vault callback skips it.
func (vk *TableVirtualKey) VaultStoreSelfManaged() {}

// MarshalJSON serializes TableVirtualKey with Value emitted as a resolved plain string,
// never as a SecretVar object. This ensures all REST API responses return "bfvk-xxx"
// rather than {"value":"bfvk-xxx","type":"plain_text"}.
//
// It also enforces the tri-state assigned_user contract: the field is emitted (as a user
// or as null) only when AssigneeResolved says the lookup actually settled the question,
// and is dropped otherwise. Without that, an unresolved assignee would serialize as null
// and be indistinguishable from a genuinely unassigned key, so the UI would render "no
// assignee" for a key that has one instead of refetching it.
func (vk TableVirtualKey) MarshalJSON() ([]byte, error) {
	type Alias TableVirtualKey
	b, err := json.Marshal(&struct {
		Alias
		Value string `json:"value"`
	}{
		Alias: Alias(vk),
		Value: vk.Value.GetValue(),
	})
	if err != nil || vk.AssigneeResolved {
		return b, err
	}
	// Unresolved: drop the key. Only this branch pays the extra round-trip, and the
	// governance read paths mark every key they return as resolved (OSS included, where
	// "no user tables" is itself a settled answer), so it stays off the common path.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, err
	}
	delete(fields, "assigned_user")
	return json.Marshal(fields)
}

// HasActivePreviousValue reports whether the VK carries a rotated-out value
// that is still inside its grace window. now == expiry is treated as expired.
func (vk *TableVirtualKey) HasActivePreviousValue(now time.Time) bool {
	if vk == nil || !vk.PreviousValue.IsSet() || vk.PreviousValueExpiresAt == nil {
		return false
	}
	return now.UTC().Before(vk.PreviousValueExpiresAt.UTC())
}

// ClearPreviousValue drops the grace-period state, leaving only the current value.
func (vk *TableVirtualKey) ClearPreviousValue() {
	vk.PreviousValue = schemas.SecretVar{}
	vk.PreviousValueHash = ""
	vk.PreviousValueExpiresAt = nil
}

// IsExpiredAt reports whether the virtual key has passed its expiry.
// now == expires_at is treated as expired; nil ExpiresAt means never expires.
func (vk *TableVirtualKey) IsExpiredAt(now time.Time) bool {
	if vk == nil || vk.ExpiresAt == nil {
		return false
	}
	return !now.UTC().Before(vk.ExpiresAt.UTC())
}

// NormalizeVirtualKeyOwnerID is normalizeVirtualKeyOwnerID for callers outside this package: an
// owner id that is blank, or only whitespace, means no owner.
func NormalizeVirtualKeyOwnerID(id *string) *string { return normalizeVirtualKeyOwnerID(id) }

func normalizeVirtualKeyOwnerID(id *string) *string {
	if id != nil && strings.TrimSpace(*id) == "" {
		return nil
	}
	return id
}

// BeforeSave is a GORM hook that enforces mutual exclusion (team vs customer vs business unit),
// computes a SHA-256 hash of the plaintext value for indexed lookups, and encrypts the virtual key
// value before writing to the database.
func (vk *TableVirtualKey) BeforeSave(tx *gorm.DB) error {
	// A blank owner id is no owner. JSON decoding turns "team_id": "" into a non-nil pointer to an
	// empty string, and a caller that builds the row itself can do the same, so normalize before
	// counting: otherwise a blank id both persists as an owner nothing resolves and makes a request
	// naming one real owner alongside a blank one look like two.
	vk.TeamID = normalizeVirtualKeyOwnerID(vk.TeamID)
	vk.CustomerID = normalizeVirtualKeyOwnerID(vk.CustomerID)
	vk.BusinessUnitID = normalizeVirtualKeyOwnerID(vk.BusinessUnitID)

	// Enforce mutual exclusion: a VK belongs to at most one of Team, Customer or Business Unit.
	// Checked as a count rather than pairwise so adding a fourth owner cannot silently leave a
	// pair unguarded.
	owners := 0
	if vk.TeamID != nil {
		owners++
	}
	if vk.CustomerID != nil {
		owners++
	}
	if vk.BusinessUnitID != nil {
		owners++
	}
	if owners > 1 {
		return fmt.Errorf("virtual key cannot belong to more than one of team, customer or business unit")
	}

	// Hash must be computed before encryption (from plaintext value).
	if vk.Value.IsSet() {
		resolved := vk.Value.GetValue()
		if resolved == "" {
			return fmt.Errorf("virtual key %s: env/vault ref %q could not be resolved", vk.ID, vk.Value.GetRawRef())
		}
		vk.ValueHash = encrypt.HashSHA256(resolved)
	}
	// PreviousValue is always a plain retired value (never an env/vault ref at
	// this point), but guard resolution anyway: persisting a set-but-unresolved
	// value would leave an empty hash that grace-period auth can never match.
	if vk.PreviousValue.IsSet() {
		resolved := vk.PreviousValue.GetValue()
		if resolved == "" {
			return fmt.Errorf("virtual key %s: previous env/vault ref %q could not be resolved", vk.ID, vk.PreviousValue.GetRawRef())
		}
		vk.PreviousValueHash = encrypt.HashSHA256(resolved)
	}
	// Store plaintext SecretVar into vault and rewrite to vault ref before encrypting.
	if schemas.VaultStoreWriteEnabled() {
		base := schemas.VaultBasePath(vk.TableName(), vk.VaultPathKey())
		if err := schemas.StoreOwnedVaultSecretVars(tx.Statement.Context, base, vk); err != nil {
			return fmt.Errorf("failed to store virtual key secrets to vault: %w", err)
		}
	}
	if encrypt.IsEnabled() && vk.Value.IsSet() {
		if err := encryptSecretVar(&vk.Value); err != nil {
			return fmt.Errorf("failed to encrypt virtual key value: %w", err)
		}
		vk.EncryptionStatus = EncryptionStatusEncrypted
	}
	if encrypt.IsEnabled() && vk.PreviousValue.IsSet() {
		if err := encryptSecretVar(&vk.PreviousValue); err != nil {
			return fmt.Errorf("failed to encrypt virtual key previous value: %w", err)
		}
	}
	return nil
}

// AfterFind is a GORM hook that decrypts the virtual key value after reading
// from the database and propagates VK-level calendar_aligned down to owned
// budgets / rate_limit and to each provider config's budgets / rate_limit.
// The reset path reads the stamped value; Update*InMemory paths re-stamp on
// every VK update.
func (vk *TableVirtualKey) AfterFind(tx *gorm.DB) error {
	switch vk.EncryptionStatus {
	case EncryptionStatusEncrypted:
		if err := decryptSecretVar(&vk.Value); err != nil {
			return fmt.Errorf("failed to decrypt virtual key value: %w", err)
		}
		if vk.PreviousValue.IsSet() {
			if err := decryptSecretVar(&vk.PreviousValue); err != nil {
				return fmt.Errorf("failed to decrypt virtual key previous value: %w", err)
			}
		}
	}
	StampCalendarAlignment(vk.CalendarAligned, vk.Budgets, vk.RateLimit)
	for i := range vk.ProviderConfigs {
		pc := &vk.ProviderConfigs[i]
		StampCalendarAlignment(vk.CalendarAligned, pc.Budgets, pc.RateLimit)
	}
	return nil
}
