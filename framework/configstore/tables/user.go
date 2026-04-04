package tables

import "time"

// TableUser represents a user in the database for SSO and user management.
type TableUser struct {
	ID        string    `gorm:"primaryKey;type:varchar(36)" json:"id"`
	Email     string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"email"`
	Name      string    `gorm:"type:varchar(255)" json:"name"`
	Role      string    `gorm:"type:varchar(50);not null;default:'viewer'" json:"role"` // "admin" or "viewer"
	Provider  string    `gorm:"type:varchar(50)" json:"provider"`                      // "password", "google_sso", "saml"
	AvatarURL string    `gorm:"type:text" json:"avatar_url,omitempty"`
	LastLogin *time.Time `gorm:"index" json:"last_login,omitempty"`
	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for the user model.
func (TableUser) TableName() string { return "users" }
