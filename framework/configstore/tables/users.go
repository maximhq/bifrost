package tables

import "time"

// TableUser represents a user account in the database.
type TableUser struct {
	ID           string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Email        string    `gorm:"type:varchar(255);uniqueIndex" json:"email"`
	Name         string    `gorm:"type:varchar(255)" json:"name"`
	Role         string    `gorm:"type:varchar(50);default:'viewer'" json:"role"`
	AuthProvider string    `gorm:"type:varchar(50)" json:"auth_provider"` // e.g. "google", "saml", "password"
	CreatedAt    time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt    time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for TableUser.
func (TableUser) TableName() string { return "users" }
