package logstore

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	// aliasedSelectItemRe matches a select item that names its output column,
	// e.g. `COUNT(*) AS count,` -> count.
	aliasedSelectItemRe = regexp.MustCompile(`(?i)\bAS[ \t]+([a-z0-9_]+)$`)
	// bareSelectItemRe matches a select item that is just a column reference,
	// e.g. `provider,` -> provider. Those carry their own name as the output.
	bareSelectItemRe = regexp.MustCompile(`^[a-z0-9_]+$`)
)

// mvLogsHourlyOutputColumns returns the output column of every select item in
// mvLogsHourlyDDL. Each item occupies one line, so the select list is parsed
// line-wise between SELECT and FROM. Anything unrecognized fails the test
// rather than being skipped — a silently-dropped item would weaken the check
// this test exists to enforce.
func mvLogsHourlyOutputColumns(t *testing.T) map[string]struct{} {
	t.Helper()

	_, body, found := strings.Cut(mvLogsHourlyDDL, "SELECT")
	require.True(t, found, "DDL should contain SELECT")
	body, _, found = strings.Cut(body, "FROM logs")
	require.True(t, found, "DDL should contain FROM logs")

	columns := make(map[string]struct{})
	for _, line := range strings.Split(body, "\n") {
		item := strings.TrimSpace(line)
		item = strings.TrimSuffix(item, ",")
		item = strings.TrimSpace(item)
		if item == "" || strings.HasPrefix(item, "--") {
			continue
		}
		if m := aliasedSelectItemRe.FindStringSubmatch(item); m != nil {
			columns[m[1]] = struct{}{}
			continue
		}
		if bareSelectItemRe.MatchString(item) {
			columns[item] = struct{}{}
			continue
		}
		t.Fatalf("could not determine the output column of select item %q — update this parser", item)
	}
	return columns
}

// TestMvLogsHourlyRequiredColumnsMatchDDL pins mvLogsHourlyRequiredColumns to
// the DDL. The list gates two things — whether repairMatViewShapes rebuilds a
// drifted view, and whether matViewShapesReady lets readers onto the matview
// path — so a column in the DDL but not the list means a view lacking it passes
// both checks and readers fail with "column does not exist". Adding a column to
// the view without adding it here should fail loudly.
func TestMvLogsHourlyRequiredColumnsMatchDDL(t *testing.T) {
	inDDL := mvLogsHourlyOutputColumns(t)
	require.NotEmpty(t, inDDL)

	required := make(map[string]struct{}, len(mvLogsHourlyRequiredColumns))
	for _, c := range mvLogsHourlyRequiredColumns {
		required[c] = struct{}{}
	}

	for column := range inDDL {
		assert.Containsf(t, required, column,
			"mv_logs_hourly selects %q but mvLogsHourlyRequiredColumns omits it: a view missing this column would pass the shape gate and readers would fail with \"column does not exist\"", column)
	}
	for column := range required {
		assert.Containsf(t, inDDL, column,
			"mvLogsHourlyRequiredColumns lists %q but mv_logs_hourly does not select it: the shape gate would never be satisfied", column)
	}
}
