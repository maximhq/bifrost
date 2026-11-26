package tables

import "time"

const (
	RSSFeedSourceBuiltin = "builtin"
	RSSFeedSourceCustom  = "custom"
)

// TableRSSFeed is one news source shown in the dashboard notification tray.
// Builtin rows are seeded from configstore.BuiltinRSSFeeds and can only be
// toggled; custom rows are added by admins and are fully editable.
type TableRSSFeed struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Slug      string    `gorm:"type:varchar(64);not null;uniqueIndex:idx_rss_feeds_slug" json:"slug"`
	Name      string    `gorm:"type:varchar(120);not null" json:"name"`
	URL       string    `gorm:"type:varchar(2048);not null" json:"url"`
	Icon      string    `gorm:"type:varchar(64)" json:"icon,omitempty"` // builtin icon key; empty means site favicon
	IconURL   string    `gorm:"type:text" json:"icon_url,omitempty"`    // custom logo: http(s) image URL or data:image URI
	Source    string    `gorm:"type:varchar(16);not null;default:'custom'" json:"source"`
	Enabled   bool      `gorm:"not null;default:false" json:"enabled"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
}

func (TableRSSFeed) TableName() string { return "rss_feeds" }
