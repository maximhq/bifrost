package registry

import (
	"context"

	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// Agent is one agent-gateway registration as governance reads it: whether the agent is enabled,
// and whether it is open to every identified caller without a grant. A disabled registration keeps
// its record, so a refresh that reads it disabled replaces the enabled one.
type Agent struct {
	Name           string
	Enabled        bool
	AllowByDefault bool
}

// AgentName is an agent record's primary key.
func AgentName(a *Agent) string { return a.Name }

// AgentsAllowedByDefault is the index of the enabled agents allowed by default, all under
// AllowedByDefaultKey, so a request can ask whether any agent is open to every identified caller
// without ranging over the registrations.
const AgentsAllowedByDefault = "allowed_by_default"

// AllowedByDefaultKey is the one key AgentsAllowedByDefault holds.
const AllowedByDefaultKey = "true"

// AgentAllowedByDefault indexes an enabled agent allowed by default under AllowedByDefaultKey, and
// any other agent under no key.
func AgentAllowedByDefault(a *Agent) []string {
	if a.Enabled && a.AllowByDefault {
		return []string{AllowedByDefaultKey}
	}
	return nil
}

// AgentsPage reads up to limit registrations named after the cursor, in name order, and returns the
// next cursor, "" after the last page.
func AgentsPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*Agent, string, error) {
	rows, err := readAgents(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("name > ?", after).Order("name").Limit(limit)
	})
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, rows[len(rows)-1].Name, nil
}

// AgentsByName reads the named registrations; a name with no row is left out.
func AgentsByName(ctx context.Context, db *gorm.DB, names []string) ([]*Agent, error) {
	return readAgents(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "name", names)) })
}

// readAgents loads the registration rows where selects. Only the policy columns are read, so the
// credentials a registration holds are neither loaded nor decrypted.
func readAgents(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*Agent, error) {
	var rows []*Agent
	q := db.WithContext(ctx).Table("config_agent_registrations").Select("name, enabled, allow_by_default")
	if err := where(q).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
