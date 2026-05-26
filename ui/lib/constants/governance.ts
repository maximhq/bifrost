// Governance-related constants

export const resetDurationOptions = [
	{ label: "Every Minute", value: "1m" },
	{ label: "Every 5 Minutes", value: "5m" },
	{ label: "Every 15 Minutes", value: "15m" },
	{ label: "Every 30 Minutes", value: "30m" },
	{ label: "Hourly", value: "1h" },
	{ label: "Every 6 Hours", value: "6h" },
	{ label: "Daily", value: "1d" },
	{ label: "Weekly", value: "1w" },
	{ label: "Monthly", value: "1M" },
];

export const budgetDurationOptions = [
	{ label: "Hourly", value: "1h" },
	{ label: "Daily", value: "1d" },
	{ label: "Weekly", value: "1w" },
	{ label: "Monthly", value: "1M" },
];

// Durations that support calendar-aligned resets (snap to day/week/month/year boundaries).
// Must stay in sync with IsCalendarAlignableDuration in framework/configstore/tables/utils.go.
export const supportsCalendarAlignment = (duration: string): boolean => duration.length > 0 && /[dwMY]$/.test(duration);

// Map of duration values to short labels for display
export const resetDurationLabels: Record<string, string> = {
	"1m": "Every Minute",
	"5m": "Every 5 Minutes",
	"15m": "Every 15 Minutes",
	"30m": "Every 30 Minutes",
	"1h": "Hourly",
	"6h": "Every 6 Hours",
	"1d": "Daily",
	"1w": "Weekly",
	"1M": "Monthly",
};

// Providers that expose a passthrough API endpoint in Bifrost.
// Used to conditionally show the "Allow Passthrough API" toggle in the virtual key UI.
export const PASSTHROUGH_ENABLED_PROVIDERS = new Set([
	"openai",
	"anthropic",
	"gemini",
	"vertex",
	"azure",
]);