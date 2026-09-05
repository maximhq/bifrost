package warp

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/framework/logstore"
)

// logsViewPath is the dashboard's Logs page. Links are built here rather than
// left to the model so the URL scheme lives in one place and the model only
// repeats what it was given.
const logsViewPath = "/workspace/logs"

// logDetailLink opens one request's detail sheet. The Logs page fetches the
// row by id when it is outside the current window, so no filters are needed.
func logDetailLink(id string) string {
	if id == "" {
		return ""
	}
	return logsViewPath + "?" + url.Values{"selected_log": {id}}.Encode()
}

// logsViewLink opens the Logs view with the same filters a tool ran, so a
// reader can see the rows behind a number instead of retyping the filters.
// Only filters the page has a URL parameter for are carried; the window is
// sent as unix seconds and only when both ends are set, which is what the
// page needs before it will honour an explicit range.
func logsViewLink(filters *logstore.SearchFilters) string {
	if filters == nil {
		return logsViewPath
	}
	values := url.Values{}
	lists := []struct {
		key    string
		values []string
	}{
		{"providers", filters.Providers},
		{"models", filters.Models},
		{"status", filters.Status},
		{"objects", filters.Objects},
		{"virtual_key_ids", filters.VirtualKeyIDs},
		{"user_ids", filters.UserIDs},
		{"team_ids", filters.TeamIDs},
		{"customer_ids", filters.CustomerIDs},
		{"business_unit_ids", filters.BusinessUnitIDs},
		{"project_ids", filters.ProjectIDs},
		{"apps", filters.Apps},
	}
	for _, list := range lists {
		if len(list.values) > 0 {
			// nuqs reads array parameters as one comma-separated value.
			values.Set(list.key, strings.Join(list.values, ","))
		}
	}
	if filters.StartTime != nil && filters.EndTime != nil {
		values.Set("start_time", strconv.FormatInt(filters.StartTime.Unix(), 10))
		values.Set("end_time", strconv.FormatInt(filters.EndTime.Unix(), 10))
	}
	if len(values) == 0 {
		return logsViewPath
	}
	return logsViewPath + "?" + values.Encode()
}
