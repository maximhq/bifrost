package command

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/maximhq/bifrost/cli/internal/agentapi"
	"github.com/maximhq/bifrost/cli/internal/output"
)

// runUsage prints the Enterprise user's budgets, rate limits, and usage rankings.
func (r *Runner) runUsage(ctx context.Context, env *environment, args []string) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(r.Out, "Usage: bifrost usage [summary|budgets|rate-limits|models|apps]")
		return err
	}
	action := "summary"
	if len(args) > 0 {
		action = strings.ToLower(strings.TrimSpace(args[0]))
	}
	if len(args) > 1 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(args[1:], " "))
	}
	if !isValidUsageView(action) {
		return fmt.Errorf("unknown usage view %q; use summary, budgets, rate-limits, models, or apps", action)
	}
	if env == nil || env.Client == nil || strings.TrimSpace(env.Client.CredentialsSnapshot().AgentToken) == "" {
		return fmt.Errorf("enterprise SSO is required; run 'bifrost auth login'")
	}
	if env.AgentAPI == nil {
		return fmt.Errorf("agent API client is unavailable")
	}
	summary, err := env.AgentAPI.GetUsageSummary(ctx)
	if err != nil {
		return err
	}
	if env.Quiet {
		return nil
	}
	switch action {
	case "summary":
		if env.Output == output.Table {
			return printUsageSummary(r.Out, summary)
		}
		return printStructured(r.Out, env.Output, summary)
	case "budgets":
		return printStructured(r.Out, env.Output, effectiveUsageBudgets(summary))
	case "rate-limits", "rates":
		return printStructured(r.Out, env.Output, summary.RateLimits)
	case "models":
		return printStructured(r.Out, env.Output, summary.TopModels)
	case "apps":
		return printStructured(r.Out, env.Output, summary.TopApps)
	default:
		return fmt.Errorf("unknown usage view %q; use summary, budgets, rate-limits, models, or apps", action)
	}
}

// isValidUsageView reports whether action names a supported usage view.
func isValidUsageView(action string) bool {
	switch action {
	case "summary", "budgets", "rate-limits", "rates", "models", "apps":
		return true
	default:
		return false
	}
}

// effectiveUsageBudgets preserves compatibility with gateways that return only the legacy headline budget.
func effectiveUsageBudgets(summary agentapi.UsageSummaryResponse) []agentapi.UsageBudget {
	if summary.Budgets != nil {
		return summary.Budgets
	}
	if summary.Budget.ID != "" || summary.Budget.Scope != "" || summary.Budget.Limit > 0 {
		return []agentapi.UsageBudget{summary.Budget}
	}
	return []agentapi.UsageBudget{}
}

// printUsageSummary renders a terminal-oriented equivalent of the Edge tray usage panel.
func printUsageSummary(writer io.Writer, summary agentapi.UsageSummaryResponse) error {
	if _, err := fmt.Fprintln(writer, "Budgets"); err != nil {
		return err
	}
	budgets := effectiveUsageBudgets(summary)
	if len(budgets) == 0 {
		if _, err := fmt.Fprintln(writer, "  No budgets assigned."); err != nil {
			return err
		}
	} else {
		tw := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "SCOPE\tPERIOD\tUSED\tLIMIT\tAVAILABLE\tRESETS")
		for _, budget := range budgets {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				usageScopeLabel(budget.UsageLimitSource), usagePeriodLabel(budget.ResetDuration),
				formatUsageMoney(budget.Used), formatUsageMoney(budget.Limit),
				formatUsageMoney(budget.Available), formatUsageReset(budget.ResetAt))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	if err := printRateLimits(writer, summary.RateLimits); err != nil {
		return err
	}
	if err := printUsageRankings(writer, "Top models", summary.TopModels); err != nil {
		return err
	}
	if err := printUsageRankings(writer, "Top apps", summary.TopApps); err != nil {
		return err
	}
	if !summary.GeneratedAt.IsZero() {
		_, err := fmt.Fprintf(writer, "\nGenerated: %s\n", summary.GeneratedAt.Local().Format(time.RFC3339))
		return err
	}
	return nil
}

// printRateLimits renders token and request counters without converting int64 values through floats.
func printRateLimits(writer io.Writer, limits []agentapi.UsageRateLimit) error {
	if _, err := fmt.Fprintln(writer, "\nRate limits"); err != nil {
		return err
	}
	if len(limits) == 0 {
		_, err := fmt.Fprintln(writer, "  No rate limits assigned.")
		return err
	}
	tw := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SCOPE\tTYPE\tPERIOD\tUSED\tLIMIT\tAVAILABLE\tRESETS")
	for _, limit := range limits {
		for _, counter := range []struct {
			name  string
			value *agentapi.UsageRateCounter
		}{{"tokens", limit.Tokens}, {"requests", limit.Requests}} {
			if counter.value == nil {
				continue
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
				usageScopeLabel(limit.UsageLimitSource), counter.name,
				usagePeriodLabel(counter.value.ResetDuration), counter.value.Used,
				counter.value.Limit, counter.value.Available, formatUsageReset(counter.value.ResetAt))
		}
	}
	return tw.Flush()
}

// printUsageRankings renders up to the rows returned by the user-scoped endpoint.
func printUsageRankings(writer io.Writer, title string, rows []agentapi.UsageRow) error {
	if _, err := fmt.Fprintf(writer, "\n%s\n", title); err != nil {
		return err
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(writer, "  No usage in this window.")
		return err
	}
	tw := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tPROVIDER\tREQUESTS\tTOKENS\tCOST")
	for _, row := range rows {
		label := strings.TrimSpace(row.Label)
		if label == "" {
			label = "Unknown"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\n", label, row.Provider, row.TotalRequests, row.TotalTokens, formatUsageMoney(row.TotalCost))
	}
	return tw.Flush()
}

// usageScopeLabel returns a compact label for an access-profile limit scope.
func usageScopeLabel(source agentapi.UsageLimitSource) string {
	profile := strings.TrimSpace(source.AccessProfileName)
	if profile == "" && source.AccessProfileID != 0 {
		profile = fmt.Sprintf("Access profile %d", source.AccessProfileID)
	}
	if profile == "" {
		profile = "Assigned"
	}
	parts := []string{profile}
	if source.Provider != "" {
		parts = append(parts, source.Provider)
	}
	if source.Scope == "model" && source.Model != "" {
		parts = append(parts, source.Model)
	} else if source.Scope == "profile" {
		parts = append(parts, "overall")
	}
	return strings.Join(parts, " / ")
}

// usagePeriodLabel gives common reset durations readable names.
func usagePeriodLabel(duration string) string {
	switch duration {
	case "1d", "24h":
		return "Daily"
	case "1w", "7d", "168h":
		return "Weekly"
	case "1M":
		return "Monthly"
	case "1Q":
		return "Quarterly"
	case "1Y":
		return "Yearly"
	case "":
		return "--"
	default:
		return duration
	}
}

// formatUsageMoney renders gateway monetary values without changing precision in structured output.
func formatUsageMoney(value float64) string {
	return fmt.Sprintf("$%.2f", value)
}

// formatUsageReset renders a reset timestamp in the user's local timezone.
func formatUsageReset(value *time.Time) string {
	if value == nil || value.IsZero() {
		return "--"
	}
	return value.Local().Format("Jan 2, 3:04 PM")
}
