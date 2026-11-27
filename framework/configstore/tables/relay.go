package tables

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TableRelay stores one relay. Routing and pricing settings live in
// ConfigJSON. Every secret is its own top-level SecretVar column (or the ExtraHeaders map), so the
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
}

// TableName pins the physical table name.
func (TableRelay) TableName() string { return "config_relays" }

// VaultPathKey scopes this row's secrets in an external vault by endpoint name, its stable identity.
func (t *TableRelay) VaultPathKey() string { return t.Name }

// NewTableRelay splits cfg into its row form.
func NewTableRelay(cfg *schemas.RelayConfig) (*TableRelay, error) {
	plain := cfg.Clone()
	row := &TableRelay{Name: cfg.Name, Description: cfg.Description, ExtraHeaders: plain.ExtraHeaders}
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

// TableVirtualKeyRelayGrant lets a virtual key call one relay; a key with no rows
// can call none. The composite primary key makes a grant
// idempotent, and both foreign keys cascade so deleting either side leaves no dangling grant.
type TableVirtualKeyRelayGrant struct {
	VirtualKeyID string    `gorm:"primaryKey;type:varchar(255);not null" json:"virtual_key_id"`
	RelayName    string    `gorm:"primaryKey;type:varchar(64);not null;index" json:"relay_name"`
	CreatedAt    time.Time `gorm:"not null" json:"created_at"`

	// Endpoint exists to declare the cascade constraint and is never serialized.
	Endpoint *TableRelay `gorm:"foreignKey:RelayName;references:Name;constraint:OnDelete:CASCADE" json:"-"`
}

// TableName namespaces the grant under governance, since it is an authorization record.
func (TableVirtualKeyRelayGrant) TableName() string {
	return "governance_virtual_key_relay_grants"
}
