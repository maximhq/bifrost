package tables

const (
	ConfigAdminUsernameKey = "adminUserName"
	ConfigAdminPasswordKey = "adminPassword"
)

// TableGovernanceConfig represents generic configuration key-value pairs
type TableGovernanceConfig struct {
	Key   string `gorm:"primaryKey;type:varchar(255)" json:"key"`
	Value string `gorm:"type:text" json:"value"`
}

// TableName sets the table name for each model
func (TableGovernanceConfig) TableName() string { return "governance_config" }
