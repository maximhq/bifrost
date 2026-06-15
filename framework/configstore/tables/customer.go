package tables

import (
	"time"

	"gorm.io/gorm"
)

// TableCustomer represents a customer entity with budgets, rate limit and team/VK association
type TableCustomer struct {
	ID          string  `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name        string  `gorm:"type:varchar(255);not null;uniqueIndex:idx_governance_customers_name" json:"name"`
	RateLimitID *string `gorm:"type:varchar(255);index" json:"rate_limit_id,omitempty"`

	// BudgetID is a config-file-only field referencing a pre-declared budget (from governance.budgets) to link to this customer. Not persisted; used by the config sync path to set customer_id on the referenced budget row.
	BudgetID *string `gorm:"-" json:"budget_id,omitempty"`

	// Relationships
	Budgets     []TableBudget     `gorm:"foreignKey:CustomerID;constraint:OnDelete:CASCADE" json:"budgets,omitempty"`
	RateLimit   *TableRateLimit   `gorm:"foreignKey:RateLimitID" json:"rate_limit,omitempty"`
	Teams       []TableTeam       `gorm:"foreignKey:CustomerID" json:"teams"`
	VirtualKeys []TableVirtualKey `gorm:"foreignKey:CustomerID" json:"virtual_keys"`

	CalendarAligned bool `gorm:"default:false" json:"calendar_aligned"`

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for each model
func (TableCustomer) TableName() string { return "governance_customers" }

// AfterFind stamps IsCalendarAligned on owned budgets and rate limit so the
// reset path (which reads the derived field off those objects) sees the correct value.
func (c *TableCustomer) AfterFind(tx *gorm.DB) error {
	for i := range c.Budgets {
		c.Budgets[i].IsCalendarAligned = c.CalendarAligned
	}
	if c.RateLimit != nil {
		c.RateLimit.IsCalendarAligned = c.CalendarAligned
	}
	return nil
}
