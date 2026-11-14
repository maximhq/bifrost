package lib

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWarnMultiUnitCalendarConfig covers inline, referenced and inherited windows
// across OSS and enterprise file formats without starting a server.
func TestWarnMultiUnitCalendarConfig(t *testing.T) {
	data := []byte(`{"governance":{
 "budgets":[{"id":"shared","reset_duration":"7d"},{"id":"linked","team_id":"team","reset_duration":"3M"}],
 "rate_limits":[{"id":"rate","request_reset_duration":"2w"}],
 "teams":[{"id":"team","calendar_aligned":true,"budget_id":"shared","rate_limit_id":"rate"}],
 "virtual_keys":[{"calendar_aligned":true,"provider_configs":[{"budgets":[{"reset_duration":"2Q"}],"model_budgets":[{"rate_limit":{"token_reset_duration":"6h"}}]}]}],
 "customers":[{"calendar_aligned":false,"budgets":[{"reset_duration":"7d"}]}],
 "projects":[{"calendar_aligned":true,"budgets":[{"reset_duration":"1d"}]}]},
 "access_profiles":[{"calendar_aligned":true,"budgets":[{"reset_duration":"2Y"}]}]}`)
	var warnings []string
	warnMultiUnitCalendarConfig(data, func(message string, args ...any) { warnings = append(warnings, fmt.Sprintf(message, args...)) })
	require.Len(t, warnings, 6)
	all := strings.Join(warnings, "\n")
	for _, duration := range []string{"7d", "3M", "2w", "2Q", "6h", "2Y"} {
		require.Contains(t, all, fmt.Sprintf("=%q", duration))
	}
	require.Contains(t, all, "governance.teams[0].budget")
	require.Contains(t, all, "governance.budgets[1].reset_duration")
	require.Contains(t, all, "access_profiles[0].budgets[0]")
	require.NotContains(t, all, "customers[0]")
	require.NotContains(t, all, "projects[0]")
}

// TestMultiUnitConfigDuration excludes fractional, compound and single-unit values.
func TestMultiUnitConfigDuration(t *testing.T) {
	for _, duration := range []string{"7d", "2w", "3M", "2Q", "2Y", "30m", "60s", "6h"} {
		require.True(t, isMultiUnitConfigDuration(duration), duration)
	}
	for _, duration := range []string{"", "1d", "1w", "1M", "1Q", "1Y", "0d", "1.5d", "2h30m", "2q", "-2d"} {
		require.False(t, isMultiUnitConfigDuration(duration), duration)
	}
}
