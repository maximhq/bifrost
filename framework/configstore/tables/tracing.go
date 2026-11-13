package tables

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TableTraceProject is one Tracing project: the unit traces are grouped and retained by.
type TableTraceProject struct {
	ID            string    `gorm:"primaryKey;type:varchar(64)" json:"id"`
	Name          string    `gorm:"type:varchar(255);not null;uniqueIndex:idx_trace_projects_name" json:"name"`
	Description   string    `gorm:"type:text" json:"description,omitempty"`
	RetentionDays *int      `json:"retention_days,omitempty"` // nil = the global log retention
	CreatedAt     time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt     time.Time `gorm:"not null" json:"updated_at"`
}

func (TableTraceProject) TableName() string { return "trace_projects" }

func (p *TableTraceProject) BeforeSave(_ *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	return nil
}
