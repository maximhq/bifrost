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

	// VirtualKeyCount is the number of virtual keys owned by this customer. Not
	// persisted; populated by the read paths so list responses can report the
	// count without carrying (or even loading) the full VirtualKeys relation.
	VirtualKeyCount int `gorm:"-" json:"virtual_key_count"`

	// TeamCount is the number of teams attached to this customer. Not persisted;
	// the list read path sets it so the table can show a count without teams.
	TeamCount int `gorm:"-" json:"team_count"`

	CalendarAligned bool `gorm:"default:false" json:"calendar_aligned"`

	// AccessProfile is a config-file-only field naming the enterprise access profile the customer holds
	// in place of budgets and a rate limit of its own. Not persisted: the enterprise build attaches the
	// profile when this entry is written from config.json (see Config.GovernanceFileSync). Part of the
	// config hash, so changing it in the file is a change to the customer's declaration.
	AccessProfile string `gorm:"-" json:"access_profile,omitempty"`

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
	StampCalendarAlignment(c.CalendarAligned, c.Budgets, c.RateLimit)
	return nil
}
