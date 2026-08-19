package tables

// TableFeatureFlag stores user-toggled overrides for feature flags. Rows
// exist only for flags that have been changed away from their code default;
// flags at default are absent and re-derived at boot time. ID is the flag's
// programmatic identifier (matches featureflags.FlagDef.ID) and is the
// primary key so upserts collapse to a single row per flag. There is no
// stored display_name or description here - those live with the code-side
// registration and can change without a DB migration.
type TableFeatureFlag struct {
	ID        string `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Enabled   bool   `gorm:"not null" json:"enabled"`
	UpdatedAt int64  `gorm:"not null" json:"updated_at"`
}

// TableName sets the table name.
func (TableFeatureFlag) TableName() string { return "feature_flags" }
