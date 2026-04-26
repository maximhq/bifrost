package tables

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// TableTeam represents a team entity with budget, rate limit and customer association
type TableTeam struct {
	ID          string  `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name        string  `gorm:"type:varchar(255);not null" json:"name"`
	CustomerID  *string `gorm:"type:varchar(255);index" json:"customer_id,omitempty"` // A team can belong to a customer
	RateLimitID *string `gorm:"type:varchar(255);index" json:"rate_limit_id,omitempty"`

	// Relationships
	Customer    *TableCustomer    `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
	Budgets     []TableBudget     `gorm:"foreignKey:TeamID;constraint:OnDelete:CASCADE" json:"budgets,omitempty"` // Multiple budgets with different reset intervals
	RateLimit   *TableRateLimit   `gorm:"foreignKey:RateLimitID" json:"rate_limit,omitempty"`
	VirtualKeys []TableVirtualKey `gorm:"foreignKey:TeamID" json:"virtual_keys,omitempty"`

	// Computed (not a DB column) — populated via correlated subquery in query layer, hence no migration
	VirtualKeyCount int64 `gorm:"->;-:migration" json:"virtual_key_count"`

	Profile       *string        `gorm:"type:text" json:"-"`
	ParsedProfile map[string]any `gorm:"-" json:"profile"`

	Config       *string        `gorm:"type:text" json:"-"`
	ParsedConfig map[string]any `gorm:"-" json:"config"`

	Claims       *string        `gorm:"type:text" json:"-"`
	ParsedClaims map[string]any `gorm:"-" json:"claims"`

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for each model
func (TableTeam) TableName() string { return "governance_teams" }

// BeforeSave hook for TableTeam to serialize JSON fields
func (t *TableTeam) BeforeSave(tx *gorm.DB) error {
	if t.ParsedProfile != nil {
		data, err := json.Marshal(t.ParsedProfile)
		if err != nil {
			return err
		}
		t.Profile = new(string(data))
	} else {
		t.Profile = nil
	}
	if t.ParsedConfig != nil {
		data, err := json.Marshal(t.ParsedConfig)
		if err != nil {
			return err
		}
		t.Config = new(string(data))
	} else {
		t.Config = nil
	}
	if t.ParsedClaims != nil {
		data, err := json.Marshal(t.ParsedClaims)
		if err != nil {
			return err
		}
		t.Claims = new(string(data))
	} else {
		t.Claims = nil
	}
	return nil
}

// AfterFind hook for TableTeam to deserialize JSON fields
func (t *TableTeam) AfterFind(tx *gorm.DB) error {
	if t.Profile != nil {
		if err := json.Unmarshal([]byte(*t.Profile), &t.ParsedProfile); err != nil {
			return err
		}
	}
	if t.Config != nil {
		if err := json.Unmarshal([]byte(*t.Config), &t.ParsedConfig); err != nil {
			return err
		}
	}
	if t.Claims != nil {
		if err := json.Unmarshal([]byte(*t.Claims), &t.ParsedClaims); err != nil {
			return err
		}
	}
	return nil
}
