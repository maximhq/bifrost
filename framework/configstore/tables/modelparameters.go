package tables

// TableModelParameters stores model parameters and capabilities data
// synced from the external datasheet API. Each row holds one model's
// full parameter/capability JSON blob.
type TableModelParameters struct {
	ID    uint   `gorm:"primaryKey;autoIncrement" json:"id"`
	Model string `gorm:"type:varchar(255);not null;uniqueIndex:idx_model_params_model" json:"model"`
	Data  string `gorm:"type:text;not null" json:"data"` // Raw JSON blob
}

// TableName sets the table name
func (TableModelParameters) TableName() string { return "governance_model_parameters" }
