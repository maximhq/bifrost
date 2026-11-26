package tables

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TableRelay stores one relay. Routes are rows of TableRelayRoute; the remaining routing and
// pricing settings live in ConfigJSON. Every secret is its own top-level SecretVar column (or the ExtraHeaders map), so the
// global vault callbacks store and remove them like any other owned secret, and BeforeSave encrypts
// the literal values that remain (env and vault references are left as references). The name is the
// primary key and is immutable: it is the URL segment callers use and the key grants reference.
type TableRelay struct {
	Name        string `gorm:"primaryKey;type:varchar(64)" json:"name"`
	Description string `gorm:"type:text" json:"description"`
	ConfigJSON  string `gorm:"type:text;not null" json:"-"`

	// Credential: Value for token auth types, the AWS* fields for aws_sigv4.
	CredentialValue  *schemas.SecretVar           `gorm:"column:credential_value;type:text" json:"-"`
	AWSAccessKey     *schemas.SecretVar           `gorm:"column:aws_access_key;type:text" json:"-"`
	AWSSecretKey     *schemas.SecretVar           `gorm:"column:aws_secret_key;type:text" json:"-"`
	AWSSessionToken  *schemas.SecretVar           `gorm:"column:aws_session_token;type:text" json:"-"`
	AWSRegion        *schemas.SecretVar           `gorm:"column:aws_region;type:text" json:"-"`
	AWSRoleARN       *schemas.SecretVar           `gorm:"column:aws_role_arn;type:text" json:"-"`
	AWSExternalID    *schemas.SecretVar           `gorm:"column:aws_external_id;type:text" json:"-"`
	AWSSessionName   *schemas.SecretVar           `gorm:"column:aws_session_name;type:text" json:"-"`
	HasAWSCredential bool                         `gorm:"column:has_aws_credential;default:false" json:"-"`
	ExtraHeaders     map[string]schemas.SecretVar `gorm:"-" json:"-"`
	ExtraHeadersJSON string                       `gorm:"column:extra_headers_json;type:text" json:"-"`
	ProxyType        string                       `gorm:"column:proxy_type;type:varchar(32)" json:"-"`
	ProxyURL         *schemas.SecretVar           `gorm:"column:proxy_url;type:text" json:"-"`
	ProxyUsername    *schemas.SecretVar           `gorm:"column:proxy_username;type:text" json:"-"`
	ProxyPassword    *schemas.SecretVar           `gorm:"column:proxy_password;type:text" json:"-"`
	ProxyCACertPEM   *schemas.SecretVar           `gorm:"column:proxy_ca_cert_pem;type:text" json:"-"`

	EncryptionStatus string    `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	ConfigHash       string    `gorm:"type:varchar(255);null" json:"-"` // detects changes synced from config.json
	CreatedAt        time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time `gorm:"index;not null" json:"updated_at"`

	// Routes is loaded and written by the store alongside the row, ordered by (created_at, id).
	Routes []TableRelayRoute `gorm:"-" json:"-"`
}

// TableName pins the physical table name.
func (TableRelay) TableName() string { return "config_relays" }

// TableRelayRoute stores one route of a relay under a stable ID, so grants can name it. There is
// no position column: the gateway ranks routes by specificity in memory, and routes of equal rank
// keep (created_at, id) order, which an edit never changes because a save keeps created_at.
type TableRelayRoute struct {
	RelayName            string                     `gorm:"primaryKey;type:varchar(64);not null" json:"relay_name"`
	ID                   string                     `gorm:"primaryKey;type:varchar(64);not null" json:"id"`
	Method               string                     `gorm:"type:varchar(16);not null" json:"method"`
	Path                 string                     `gorm:"type:text;not null" json:"path"`
	Headers              map[string]string          `gorm:"type:text;serializer:json" json:"headers,omitempty"`
	Enabled              *bool                      `json:"enabled,omitempty"`
	Billable             *bool                      `json:"billable,omitempty"`
	Stream               *bool                      `json:"stream,omitempty"`
	Usage                *schemas.RelayUsageConfig  `gorm:"type:text;serializer:json" json:"usage,omitempty"`
	Job                  *schemas.RelayJobConfig    `gorm:"type:text;serializer:json" json:"job,omitempty"`
	BodyOverrides        map[string]json.RawMessage `gorm:"type:text;serializer:json" json:"body_overrides,omitempty"`
	InheritBodyOverrides *bool                      `json:"inherit_body_overrides,omitempty"`
	CreatedAt            time.Time                  `gorm:"not null" json:"created_at"`
	UpdatedAt            time.Time                  `gorm:"not null" json:"updated_at"`

	// Relay exists to declare the cascade constraint and is never serialized.
	Relay *TableRelay `gorm:"foreignKey:RelayName;references:Name;constraint:OnDelete:CASCADE" json:"-"`
}

// TableName pins the physical table name.
func (TableRelayRoute) TableName() string { return "config_relay_routes" }

// NewTableRelayRoute converts one route config into its row; the caller sets the timestamps.
func NewTableRelayRoute(relayName string, route schemas.RelayRouteConfig) TableRelayRoute {
	return TableRelayRoute{
		RelayName: relayName, ID: route.ID, Method: route.Method, Path: route.Path, Headers: route.Headers,
		Enabled: route.Enabled, Billable: route.Billable, Stream: route.Stream, Usage: route.Usage, Job: route.Job,
		BodyOverrides: route.BodyOverrides, InheritBodyOverrides: route.InheritBodyOverrides,
	}
}

// ToConfig converts the row back into a route config.
func (r *TableRelayRoute) ToConfig() schemas.RelayRouteConfig {
	return schemas.RelayRouteConfig{
		ID: r.ID, Method: r.Method, Path: r.Path, Headers: r.Headers, Enabled: r.Enabled, Billable: r.Billable,
		Stream: r.Stream, Usage: r.Usage, Job: r.Job, BodyOverrides: r.BodyOverrides, InheritBodyOverrides: r.InheritBodyOverrides,
	}
}

// SortRelayRoutes puts routes in their stored order, (created_at, id), the order equal-rank
// routes keep when the gateway ranks them.
func SortRelayRoutes(routes []TableRelayRoute) {
	slices.SortStableFunc(routes, func(a, b TableRelayRoute) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// VaultPathKey scopes this row's secrets in an external vault by endpoint name, its stable identity.
func (t *TableRelay) VaultPathKey() string { return t.Name }

// NewTableRelay splits cfg into its row form. Routes become TableRelayRoute rows in cfg order,
// without timestamps: the store sets those, keeping each existing route's created_at.
func NewTableRelay(cfg *schemas.RelayConfig) (*TableRelay, error) {
	plain := cfg.Clone()
	row := &TableRelay{Name: cfg.Name, Description: cfg.Description, ExtraHeaders: plain.ExtraHeaders}
	for _, route := range plain.Routes {
		row.Routes = append(row.Routes, NewTableRelayRoute(cfg.Name, route))
	}
	plain.Routes = nil
	if c := plain.Credential; c != nil {
		row.CredentialValue = c.Value.Clone()
		if a := c.AWS; a != nil {
			row.HasAWSCredential = true
			row.AWSAccessKey, row.AWSSecretKey, row.AWSSessionToken = a.AccessKey.Clone(), a.SecretKey.Clone(), a.SessionToken.Clone()
			row.AWSRegion, row.AWSRoleARN, row.AWSExternalID, row.AWSSessionName = a.Region.Clone(), a.RoleARN.Clone(), a.ExternalID.Clone(), a.SessionName.Clone()
		}
	}
	if p := plain.ProxyConfig; p != nil {
		row.ProxyType = string(p.Type)
		row.ProxyURL, row.ProxyUsername, row.ProxyPassword, row.ProxyCACertPEM = p.URL.Clone(), p.Username.Clone(), p.Password.Clone(), p.CACertPEM.Clone()
	}
	plain.Credential, plain.ExtraHeaders, plain.ProxyConfig = nil, nil, nil
	configJSON, err := sonic.Marshal(plain)
	if err != nil {
		return nil, fmt.Errorf("marshal relay config: %w", err)
	}
	row.ConfigJSON = string(configJSON)
	return row, nil
}

// ToConfig rebuilds the endpoint config from the row.
func (t *TableRelay) ToConfig() (*schemas.RelayConfig, error) {
	var cfg schemas.RelayConfig
	if err := sonic.Unmarshal([]byte(t.ConfigJSON), &cfg); err != nil {
		return nil, fmt.Errorf("relay %s: decode config: %w", t.Name, err)
	}
	cfg.Name, cfg.Description = t.Name, t.Description
	cfg.Routes = nil
	if len(t.Routes) > 0 {
		routes := slices.Clone(t.Routes)
		SortRelayRoutes(routes)
		cfg.Routes = make([]schemas.RelayRouteConfig, len(routes))
		for i := range routes {
			cfg.Routes[i] = routes[i].ToConfig()
		}
	}
	if t.CredentialValue != nil || t.HasAWSCredential {
		cfg.Credential = &schemas.RelayCredential{Value: t.CredentialValue.Clone()}
		if t.HasAWSCredential {
			cfg.Credential.AWS = &schemas.RelayAWSCredential{
				AccessKey: t.AWSAccessKey.Clone(), SecretKey: t.AWSSecretKey.Clone(), SessionToken: t.AWSSessionToken.Clone(),
				Region: t.AWSRegion.Clone(), RoleARN: t.AWSRoleARN.Clone(), ExternalID: t.AWSExternalID.Clone(), SessionName: t.AWSSessionName.Clone(),
			}
		}
	}
	if len(t.ExtraHeaders) > 0 {
		cfg.ExtraHeaders = make(map[string]schemas.SecretVar, len(t.ExtraHeaders))
		for k, v := range t.ExtraHeaders {
			cfg.ExtraHeaders[k] = v
		}
	}
	if t.ProxyType != "" {
		cfg.ProxyConfig = &schemas.ProxyConfig{
			Type: schemas.ProxyType(t.ProxyType), URL: t.ProxyURL.Clone(), Username: t.ProxyUsername.Clone(),
			Password: t.ProxyPassword.Clone(), CACertPEM: t.ProxyCACertPEM.Clone(),
		}
	}
	return &cfg, nil
}

// secretFields lists every pointer secret column, for encryption.
func (t *TableRelay) secretFields() []**schemas.SecretVar {
	return []**schemas.SecretVar{
		&t.CredentialValue, &t.AWSAccessKey, &t.AWSSecretKey, &t.AWSSessionToken, &t.AWSRegion,
		&t.AWSRoleARN, &t.AWSExternalID, &t.AWSSessionName,
		&t.ProxyURL, &t.ProxyUsername, &t.ProxyPassword, &t.ProxyCACertPEM,
	}
}

// BeforeSave runs after the vault callback has swapped vault-managed values for references. It
// encrypts each literal secret that remains, then serializes the extra headers (each value
// encrypted the same way) into their column.
func (t *TableRelay) BeforeSave(*gorm.DB) error {
	// In-memory values are always plaintext (AfterFind and AfterSave decrypt), so a save encrypts
	// whenever encryption is on.
	encrypting := encrypt.IsEnabled()
	// Stored in reference-preserving form, as provider keys and webhook headers are: an env or
	// vault header keeps only its reference (the resolved value never reaches the column), a literal
	// keeps its value, encrypted when encryption is on.
	headers := make(map[string]string, len(t.ExtraHeaders))
	for k, v := range t.ExtraHeaders {
		v := *v.Clone()
		if encrypting {
			if err := encryptSecretVar(&v); err != nil {
				return fmt.Errorf("encrypt relay header %s: %w", k, err)
			}
		}
		headers[k] = schemas.SecretVarAsString(&v)
	}
	data, err := sonic.Marshal(headers)
	if err != nil {
		return fmt.Errorf("marshal relay extra headers: %w", err)
	}
	t.ExtraHeadersJSON = string(data)
	if !encrypting {
		return nil
	}
	for _, field := range t.secretFields() {
		if err := encryptSecretVarPtr(field); err != nil {
			return fmt.Errorf("encrypt relay secret: %w", err)
		}
	}
	t.EncryptionStatus = EncryptionStatusEncrypted
	return nil
}

// AfterSave restores plaintext on the in-memory row, so a caller reusing it sees what it saved.
func (t *TableRelay) AfterSave(tx *gorm.DB) error {
	return t.AfterFind(tx)
}

// AfterFind loads the extra headers and decrypts the secrets of rows marked encrypted; rows written
// before encryption was enabled load unchanged.
func (t *TableRelay) AfterFind(*gorm.DB) error {
	encrypted := t.EncryptionStatus == EncryptionStatusEncrypted
	t.ExtraHeaders = nil
	if t.ExtraHeadersJSON != "" && t.ExtraHeadersJSON != "{}" {
		// Rows written before references were kept as references hold full SecretVar objects;
		// current rows hold the reference-preserving string. Both decode here.
		var raw map[string]json.RawMessage
		if err := sonic.Unmarshal([]byte(t.ExtraHeadersJSON), &raw); err != nil {
			return fmt.Errorf("decode relay extra headers: %w", err)
		}
		headers := make(map[string]schemas.SecretVar, len(raw))
		for k, entry := range raw {
			var v schemas.SecretVar
			if len(entry) > 0 && entry[0] == '"' {
				var stored string
				if err := sonic.Unmarshal(entry, &stored); err != nil {
					return fmt.Errorf("decode relay extra header %s: %w", k, err)
				}
				v = *schemas.NewSecretVar(stored)
			} else if err := sonic.Unmarshal(entry, &v); err != nil {
				return fmt.Errorf("decode relay extra header %s: %w", k, err)
			}
			if encrypted {
				if err := decryptSecretVar(&v); err != nil {
					return fmt.Errorf("decrypt relay header %s: %w", k, err)
				}
			}
			headers[k] = v
		}
		t.ExtraHeaders = headers
	}
	if !encrypted {
		return nil
	}
	for _, field := range t.secretFields() {
		if err := decryptSecretVarPtr(field); err != nil {
			return fmt.Errorf("decrypt relay secret: %w", err)
		}
	}
	return nil
}

// TableVirtualKeyRelayGrant lets a virtual key call routes of one relay; a key with no rows
// can call none. Routes follows WhiteList semantics with route IDs: ["*"] allows every route
// (and calls no route matched), a list allows only those routes. The composite primary key makes
// a grant idempotent, and both foreign keys cascade so deleting either side leaves no dangling grant.
type TableVirtualKeyRelayGrant struct {
	VirtualKeyID string            `gorm:"primaryKey;type:varchar(255);not null" json:"virtual_key_id"`
	RelayName    string            `gorm:"primaryKey;type:varchar(64);not null;index" json:"relay_name"`
	Routes       schemas.WhiteList `gorm:"type:text;serializer:json" json:"routes"`
	CreatedAt    time.Time         `gorm:"not null" json:"created_at"`

	// Endpoint exists to declare the cascade constraint and is never serialized.
	Endpoint *TableRelay `gorm:"foreignKey:RelayName;references:Name;constraint:OnDelete:CASCADE" json:"-"`
}

// BeforeSave stores an unset route list as every route, the meaning a grant had before routes.
func (g *TableVirtualKeyRelayGrant) BeforeSave(*gorm.DB) error {
	g.Routes = RelayGrantRoutes(g.Routes)
	return ValidateRelayGrantRoutes(g.Routes)
}

// GetRelayName implements RelayGrant.
func (g TableVirtualKeyRelayGrant) GetRelayName() string { return g.RelayName }

// GetRoutes implements RelayGrant.
func (g TableVirtualKeyRelayGrant) GetRoutes() schemas.WhiteList { return g.Routes }

// RelayGrant is one holder's grant on one relay, whatever table holds it.
type RelayGrant interface {
	GetRelayName() string
	GetRoutes() schemas.WhiteList
}

// RelayGrantRoutes is a grant's route list with nil read as every route: grants written before
// routes existed, and requests or config files that omit them, mean the whole relay.
func RelayGrantRoutes(routes schemas.WhiteList) schemas.WhiteList {
	if routes == nil {
		return schemas.WhiteList{"*"}
	}
	return routes
}

// ValidateRelayGrantRoutes refuses a route list that grants nothing or names something other
// than "*" or route IDs. Whether each ID belongs to the relay is checked against the store.
func ValidateRelayGrantRoutes(routes schemas.WhiteList) error {
	if routes == nil {
		return nil
	}
	if len(routes) == 0 {
		return fmt.Errorf("routes must name at least one route, or [\"*\"] for every route")
	}
	if err := routes.Validate(); err != nil {
		return fmt.Errorf("routes: %w", err)
	}
	if routes.IsUnrestricted() {
		return nil
	}
	for _, id := range routes {
		if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
			return fmt.Errorf("routes: %q is not a route ID", id)
		}
	}
	return nil
}

// RelayGrantPermits turns a holder's relay grants into permits, each with its routes.
func RelayGrantPermits[G RelayGrant](grants []G) []schemas.RelayPermit {
	out := make([]schemas.RelayPermit, 0, len(grants))
	for _, g := range grants {
		out = append(out, schemas.RelayPermit{Relay: g.GetRelayName(), Routes: RelayGrantRoutes(g.GetRoutes())})
	}
	return out
}

// TableName namespaces the grant under governance, since it is an authorization record.
func (TableVirtualKeyRelayGrant) TableName() string {
	return "governance_virtual_key_relay_grants"
}
