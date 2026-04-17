package tables

import (
	"fmt"
	"time"

	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm"
)

type TableCodexAuthSession struct {
	ID               string     `gorm:"type:varchar(255);primaryKey" json:"id"`
	Provider         string     `gorm:"type:varchar(50);index;not null" json:"provider"`
	KeyID            string     `gorm:"type:varchar(255);index;not null" json:"key_id"`
	FlowType         string     `gorm:"type:varchar(32);index;not null" json:"flow_type"`
	Status           string     `gorm:"type:varchar(50);index;not null" json:"status"`
	DeviceAuthID     *string    `gorm:"type:text" json:"-"`
	UserCode         *string    `gorm:"type:varchar(64)" json:"user_code,omitempty"`
	VerificationURI  *string    `gorm:"type:text" json:"verification_uri,omitempty"`
	IntervalSeconds  *int       `gorm:"type:int" json:"interval_seconds,omitempty"`
	NextPollAt       *time.Time `gorm:"index" json:"next_poll_at,omitempty"`
	LastError        *string    `gorm:"type:text" json:"last_error,omitempty"`
	EncryptionStatus string     `gorm:"type:varchar(20);default:'plain_text'" json:"-"`
	CreatedAt        time.Time  `gorm:"index;not null" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"index;not null" json:"updated_at"`
	ExpiresAt        time.Time  `gorm:"index;not null" json:"expires_at"`
	CompletedAt      *time.Time `gorm:"index" json:"completed_at,omitempty"`
}

func (TableCodexAuthSession) TableName() string { return "codex_auth_sessions" }

func (s *TableCodexAuthSession) BeforeSave(tx *gorm.DB) error {
	if s.Status == "" {
		s.Status = "pending"
	}
	if encrypt.IsEnabled() {
		encrypted := false
		if s.DeviceAuthID != nil && *s.DeviceAuthID != "" {
			if err := encryptString(s.DeviceAuthID); err != nil {
				return fmt.Errorf("failed to encrypt codex device auth id: %w", err)
			}
			encrypted = true
		}
		if encrypted {
			s.EncryptionStatus = EncryptionStatusEncrypted
		}
	}
	return nil
}

func (s *TableCodexAuthSession) AfterFind(tx *gorm.DB) error {
	if s.EncryptionStatus == EncryptionStatusEncrypted {
		if s.DeviceAuthID != nil && *s.DeviceAuthID != "" {
			if err := decryptString(s.DeviceAuthID); err != nil {
				return fmt.Errorf("failed to decrypt codex device auth id: %w", err)
			}
		}
	}
	return nil
}
