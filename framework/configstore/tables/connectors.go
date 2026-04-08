package tables

import "time"

// TableConnector stores a registered data connector and its (encrypted) configuration.
type TableConnector struct {
	ID           string     `gorm:"primaryKey;type:text"`
	Name         string     `gorm:"uniqueIndex;not null"`
	Type         string     `gorm:"index;type:text"` // ConnectorType constant
	Enabled      bool       `gorm:"default:true"`
	ConfigJSON   string     `gorm:"type:text"` // encrypted connector-specific config
	MCPGroupIDs  string     `gorm:"type:text"` // JSON: []string — which MCP tool groups can use this
	Description  string     `gorm:"type:text"`
	LastTestedAt *time.Time
	LastTestOK   bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (TableConnector) TableName() string { return "data_connectors" }
