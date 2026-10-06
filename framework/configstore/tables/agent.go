package tables

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

// TableAgentRegistration stores one durable Agent Gateway registration: the
// upstream agent an administrator registered, how Bifrost discovers and
// authenticates to it, and whether every virtual key may use it. It is the
// authority the runtime is rebuilt from after a restart. Credential secrets are
// stored here encrypted at rest when encryption is enabled and are never returned
// to API readers.
type TableAgentRegistration struct {
	// Name is the primary key and the identifier used in gateway URLs and grants.
	Name         string `gorm:"primaryKey;type:varchar(255)"`
	AgentCardURL string `gorm:"type:text;not null"`
	Tenant       string `gorm:"type:varchar(255)"`
	// Enabled false retains the row while removing the agent from the runtime.
	Enabled bool `gorm:"not null;default:true"`
	// AllowByDefault grants access without any explicit grant row.
	AllowByDefault                         bool                  `gorm:"column:allow_by_default;not null;default:false"`
	ForwardAcceptedCredential              bool                  `gorm:"column:forward_accepted_credential;not null;default:false"`
	ForwardAcceptedCredentialOverridesAuth bool                  `gorm:"column:forward_accepted_credential_overrides_auth;not null;default:false"`
	DiscoveryAuth                          *schemas.UpstreamAuth `gorm:"serializer:json;type:text"`
	ExtensionURIs                          []string              `gorm:"serializer:json;type:text"`
	RuntimeAuth                            *schemas.UpstreamAuth `gorm:"serializer:json;type:text"`
	// EncryptionStatus records whether the persisted secrets are ciphertext, so
	// rows written before encryption was enabled remain readable.
	EncryptionStatus string `gorm:"type:varchar(20);default:'plain_text'"`
	// ConfigHash is used to detect the changes synced from config.json file.
	// Every time we sync the config.json file, we will update the config hash.
	ConfigHash string    `gorm:"type:varchar(255);null"`
	CreatedAt  time.Time `gorm:"index;not null"`
	UpdatedAt  time.Time `gorm:"index;not null"`
}

// TableName pins the physical table so schema changes never depend on GORM's
// pluralization of the Go type name.
func (TableAgentRegistration) TableName() string { return "config_agent_registrations" }

// VaultPathKey scopes this row's secrets in an external vault by agent name, which
// is the registration's stable identity.
func (r *TableAgentRegistration) VaultPathKey() string { return r.Name }

// BeforeSave encrypts literal credential secrets so upstream credentials are never
// stored in plaintext when encryption is enabled. Values sourced from an external
// secret store hold only a reference and are left alone, and the status column
// records the outcome so reads know how to interpret the row.
func (r *TableAgentRegistration) BeforeSave(*gorm.DB) error {
	if !encrypt.IsEnabled() {
		return nil
	}
	for _, auth := range []*schemas.UpstreamAuth{r.DiscoveryAuth, r.RuntimeAuth} {
		if err := transformUpstreamAuthSecrets(auth, encrypt.Encrypt); err != nil {
			return err
		}
	}
	r.EncryptionStatus = EncryptionStatusEncrypted
	return nil
}

// AfterFind decrypts credential secrets only for rows marked encrypted, so rows
// written before encryption was enabled continue to load unchanged.
func (r *TableAgentRegistration) AfterFind(*gorm.DB) error {
	if r.EncryptionStatus != EncryptionStatusEncrypted {
		return nil
	}
	for _, auth := range []*schemas.UpstreamAuth{r.DiscoveryAuth, r.RuntimeAuth} {
		if err := transformUpstreamAuthSecrets(auth, encrypt.Decrypt); err != nil {
			return err
		}
	}
	return nil
}

func transformUpstreamAuthSecrets(auth *schemas.UpstreamAuth, transform func(string) (string, error)) error {
	if auth == nil {
		return nil
	}
	refs := make([]*schemas.SecretVar, 0, len(auth.Headers)+5)
	// Map values are not addressable, so transform copies and write them back after.
	headerRefs := make(map[string]*schemas.SecretVar, len(auth.Headers))
	for name := range auth.Headers {
		value := auth.Headers[name]
		refs = append(refs, &value)
		headerRefs[name] = &value
	}
	if auth.OAuth != nil {
		refs = append(refs, auth.OAuth.ClientID, auth.OAuth.ClientSecret)
	}
	if auth.TLS != nil {
		refs = append(refs, auth.TLS.CACertPEM, auth.TLS.ClientCertPEM, auth.TLS.ClientKeyPEM)
	}
	for _, ref := range refs {
		if ref == nil || ref.IsFromSecret() || ref.GetValue() == "" {
			continue
		}
		value, err := transform(ref.Val)
		if err != nil {
			return err
		}
		ref.Val = value
	}
	for name, value := range headerRefs {
		auth.Headers[name] = *value
	}
	return nil
}

// It is the single join table both the agent APIs and the virtual-key APIs write,
// so grants have one representation regardless of which direction created them.
// The composite primary key makes a grant naturally idempotent, and the foreign key
// cascades so deleting an agent registration cannot leave dangling grants.
type TableVirtualKeyAgentGrant struct {
	VirtualKeyID string    `gorm:"primaryKey;type:varchar(255);not null" json:"virtual_key_id"`
	AgentName    string    `gorm:"primaryKey;type:varchar(255);not null;index" json:"agent_name"`
	CreatedAt    time.Time `gorm:"not null" json:"created_at"`

	// AgentRegistration exists to declare the cascade constraint and is never
	// serialized in API responses.
	AgentRegistration *TableAgentRegistration `gorm:"foreignKey:AgentName;references:Name;constraint:OnDelete:CASCADE" json:"-"`
}

// TableName pins the physical grant table name, which is namespaced under
// governance because it is an authorization record rather than agent configuration.
func (TableVirtualKeyAgentGrant) TableName() string { return "governance_virtual_key_agent_grants" }

// TableAgentPushConfig stores one downstream push callback registration and the
// hash of the independent gateway-minted credential the upstream agent presents
// on Bifrost's push ingress. Downstream secrets are encrypted at rest when
// encryption is enabled and are never returned by any read API.
type TableAgentPushConfig struct {
	AgentName string `gorm:"primaryKey;type:varchar(255);not null"`
	TaskID    string `gorm:"primaryKey;type:varchar(255);not null"`
	ConfigID  string `gorm:"primaryKey;type:varchar(255);not null"`

	URL             string             `gorm:"type:text;not null"`
	Token           *schemas.SecretVar `gorm:"type:text"`
	AuthScheme      string             `gorm:"type:varchar(64)"`
	AuthCredentials *schemas.SecretVar `gorm:"type:text"`
	// IngressTokenHash is a SHA-256 hex digest; the raw ingress token is never
	// persisted, so a database read cannot forge an upstream push.
	IngressTokenHash string `gorm:"type:varchar(64);not null;index:idx_agent_push_configs_ingress_hash"`
	// EncryptionStatus records whether the persisted secrets are ciphertext, so
	// rows written before encryption was enabled remain readable.
	EncryptionStatus string    `gorm:"type:varchar(20);default:'plain_text'"`
	CreatedAt        time.Time `gorm:"not null"`
	UpdatedAt        time.Time `gorm:"not null"`

	// AgentRegistration declares the cascade so deleting an agent removes its
	// push configurations; it is never serialized.
	AgentRegistration *TableAgentRegistration `gorm:"foreignKey:AgentName;references:Name;constraint:OnDelete:CASCADE" json:"-"`
}

// TableName pins the physical table name.
func (TableAgentPushConfig) TableName() string { return "config_agent_push_configs" }

// VaultPathKey scopes this row's secrets in an external vault by its owning agent.
func (c *TableAgentPushConfig) VaultPathKey() string { return c.AgentName }

// BeforeSave encrypts literal downstream callback secrets, mirroring
// TableAgentRegistration's handling of upstream credentials.
func (c *TableAgentPushConfig) BeforeSave(*gorm.DB) error {
	if !encrypt.IsEnabled() {
		return nil
	}
	for _, ref := range []*schemas.SecretVar{c.Token, c.AuthCredentials} {
		if ref != nil && !ref.IsFromSecret() && ref.GetValue() != "" {
			value, err := encrypt.Encrypt(ref.Val)
			if err != nil {
				return err
			}
			ref.Val = value
		}
	}
	c.EncryptionStatus = EncryptionStatusEncrypted
	return nil
}

// AfterFind decrypts callback secrets only for rows marked encrypted.
func (c *TableAgentPushConfig) AfterFind(*gorm.DB) error {
	if c.EncryptionStatus != EncryptionStatusEncrypted {
		return nil
	}
	for _, ref := range []*schemas.SecretVar{c.Token, c.AuthCredentials} {
		if ref != nil && !ref.IsFromSecret() && ref.GetValue() != "" {
			value, err := encrypt.Decrypt(ref.Val)
			if err != nil {
				return err
			}
			ref.Val = value
		}
	}
	return nil
}

// TableAgentPushDelivery is the durable push outbox. A row is created when a
// verified upstream push is accepted at the ingress and survives restarts until
// it is delivered downstream or dead-lettered after retry exhaustion. The ID is
// a content digest, so idempotent creation deduplicates upstream retransmissions.
// Rows carry no secrets.
type TableAgentPushDelivery struct {
	ID        string `gorm:"primaryKey;type:varchar(64)"`
	AgentName string `gorm:"type:varchar(255);not null;index:idx_agent_push_deliveries_agent"`
	TaskID    string `gorm:"type:varchar(255);not null"`
	ConfigID  string `gorm:"type:varchar(255);not null"`

	Payload       string     `gorm:"type:text;not null"`
	Status        string     `gorm:"type:varchar(16);not null;index:idx_agent_push_deliveries_status"`
	Attempts      int        `gorm:"not null;default:0"`
	NextAttemptAt time.Time  `gorm:"not null;index:idx_agent_push_deliveries_next_attempt"`
	LastError     string     `gorm:"type:text"`
	ClaimedBy     string     `gorm:"type:varchar(255)"`
	ClaimedUntil  *time.Time `gorm:"index:idx_agent_push_deliveries_claimed_until"`
	CreatedAt     time.Time  `gorm:"not null"`
	UpdatedAt     time.Time  `gorm:"not null"`

	// AgentRegistration declares the cascade so deleting an agent removes its
	// queued deliveries; it is never serialized.
	AgentRegistration *TableAgentRegistration `gorm:"foreignKey:AgentName;references:Name;constraint:OnDelete:CASCADE" json:"-"`
}

// TableName pins the physical table name.
func (TableAgentPushDelivery) TableName() string { return "config_agent_push_deliveries" }
