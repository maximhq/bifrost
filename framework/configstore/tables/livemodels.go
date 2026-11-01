package tables

// TableLiveModelListing stores one successful, key-scoped model listing without an expiry.
type TableLiveModelListing struct {
	ID         uint          `gorm:"primaryKey;autoIncrement" json:"-"`
	ProviderID uint          `gorm:"not null;uniqueIndex:idx_live_models_scope" json:"-"`
	KeyID      string        `gorm:"type:varchar(255);not null;uniqueIndex:idx_live_models_scope" json:"-"`
	Unfiltered bool          `gorm:"not null;uniqueIndex:idx_live_models_scope" json:"-"`
	TableKeyID *uint         `gorm:"index" json:"-"`
	SourceHash string        `gorm:"type:varchar(64);not null" json:"-"`
	Data       string        `gorm:"type:text;not null" json:"-"`
	Provider   TableProvider `gorm:"foreignKey:ProviderID;constraint:OnDelete:CASCADE" json:"-"`
	Key        *TableKey     `gorm:"foreignKey:TableKeyID;constraint:OnDelete:CASCADE" json:"-"`
}

// TableName keeps live metadata separate from editable governance configuration.
func (TableLiveModelListing) TableName() string { return "model_catalog_live_models" }
