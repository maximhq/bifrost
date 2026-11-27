package registry

import (
	"context"

	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// CustomerByName names the customer collection's name index, for GetBy.
const CustomerByName = "name"

// Team is one team as request handling reads it: its name, the customer it belongs to, its
// budgets, rate limit and request limits by ID, and its own content-logging decision.
type Team struct {
	ID, Name        string
	CustomerID      string
	RateLimitID     string
	RequestLimitIDs []string // the request limits it owns (governance_rate_limits.team_id)
	BudgetIDs       []string
	CalendarAligned bool
	// DisableContentLogging is the team's own say on content logging: nil inherits, true turns
	// content off, false keeps it on.
	DisableContentLogging *bool
}

// Customer is one customer as request handling reads it: its name, and its budgets, rate limit and
// request limits by ID.
type Customer struct {
	ID, Name        string
	RateLimitID     string
	RequestLimitIDs []string // the request limits it owns (governance_rate_limits.customer_id)
	BudgetIDs       []string
	CalendarAligned bool
}

// orgRow is the columns a team or customer record is read from. Only teams have a
// content-logging column, so only the team select fills DisableContentLogging.
type orgRow struct {
	ID                    string
	Name                  string
	CustomerID            *string
	RateLimitID           *string
	CalendarAligned       bool
	DisableContentLogging *bool
}

// TeamID is a team record's primary key.
func TeamID(t *Team) string { return t.ID }

// TeamCustomerID is the customer a team belongs to, "" for none.
func TeamCustomerID(t *Team) string { return t.CustomerID }

// TeamsPage reads up to limit teams with IDs after the cursor, in ID order, and returns the next
// cursor, "" after the last page.
func TeamsPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*Team, string, error) {
	rows, err := readTeams(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id > ?", after).Order("id").Limit(limit)
	})
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, rows[len(rows)-1].ID, nil
}

// TeamsByID reads the teams with these IDs; an ID with no row is left out.
func TeamsByID(ctx context.Context, db *gorm.DB, ids []string) ([]*Team, error) {
	return readTeams(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "id", ids)) })
}

// TeamsByCustomers reads the teams belonging to these customers.
func TeamsByCustomers(ctx context.Context, db *gorm.DB, customerIDs []string) ([]*Team, error) {
	return readTeams(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "customer_id", customerIDs)) })
}

// readTeams loads the team rows where selects, with the IDs of their budgets.
func readTeams(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*Team, error) {
	var rows []orgRow
	q := db.WithContext(ctx).Table("governance_teams").Select("id, name, customer_id, rate_limit_id, calendar_aligned, disable_content_logging")
	if err := where(q).Scan(&rows).Error; err != nil {
		return nil, err
	}
	budgets, err := budgetIDsBy(ctx, db, "team_id", orgIDs(rows))
	if err != nil {
		return nil, err
	}
	requestLimits, err := requestLimitIDsBy(ctx, db, "team_id", orgIDs(rows))
	if err != nil {
		return nil, err
	}
	out := make([]*Team, 0, len(rows))
	for _, r := range rows {
		out = append(out, &Team{
			ID: r.ID, Name: r.Name, CustomerID: deref(r.CustomerID), RateLimitID: deref(r.RateLimitID),
			RequestLimitIDs: requestLimits[r.ID], BudgetIDs: budgets[r.ID], CalendarAligned: r.CalendarAligned,
			DisableContentLogging: r.DisableContentLogging,
		})
	}
	return out, nil
}

// CustomerID is a customer record's primary key.
func CustomerID(c *Customer) string { return c.ID }

// CustomerName is a customer's key in the name index.
func CustomerName(c *Customer) string { return c.Name }

// CustomersPage reads up to limit customers with IDs after the cursor, in ID order, and returns
// the next cursor, "" after the last page.
func CustomersPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*Customer, string, error) {
	rows, err := readCustomers(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id > ?", after).Order("id").Limit(limit)
	})
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, rows[len(rows)-1].ID, nil
}

// CustomersByID reads the customers with these IDs; an ID with no row is left out.
func CustomersByID(ctx context.Context, db *gorm.DB, ids []string) ([]*Customer, error) {
	return readCustomers(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "id", ids)) })
}

// readCustomers loads the customer rows where selects, with the IDs of their budgets.
func readCustomers(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*Customer, error) {
	var rows []orgRow
	q := db.WithContext(ctx).Table("governance_customers").Select("id, name, rate_limit_id, calendar_aligned")
	if err := where(q).Scan(&rows).Error; err != nil {
		return nil, err
	}
	budgets, err := budgetIDsBy(ctx, db, "customer_id", orgIDs(rows))
	if err != nil {
		return nil, err
	}
	requestLimits, err := requestLimitIDsBy(ctx, db, "customer_id", orgIDs(rows))
	if err != nil {
		return nil, err
	}
	out := make([]*Customer, 0, len(rows))
	for _, r := range rows {
		out = append(out, &Customer{
			ID: r.ID, Name: r.Name, RateLimitID: deref(r.RateLimitID), RequestLimitIDs: requestLimits[r.ID],
			BudgetIDs: budgets[r.ID], CalendarAligned: r.CalendarAligned,
		})
	}
	return out, nil
}

// orgIDs lists the IDs of rows.
func orgIDs(rows []orgRow) []string {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}
