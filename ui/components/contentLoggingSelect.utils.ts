// The disable_content_logging wire field is tri-state on every entity that carries it (virtual key,
// team, provider key, and the enterprise levels): absent or null inherits, true forces content off,
// false forces it on. Forms show it as three named choices.
export type ContentLoggingChoice = "inherit" | "disabled" | "enabled";

export function contentLoggingChoice(disableContentLogging: boolean | null | undefined): ContentLoggingChoice {
	if (disableContentLogging === true) return "disabled";
	if (disableContentLogging === false) return "enabled";
	return "inherit";
}

export function contentLoggingValue(choice: ContentLoggingChoice): boolean | null {
	if (choice === "disabled") return true;
	if (choice === "enabled") return false;
	return null;
}

// ContentLoggingLabels are the translated names of the three choices for one kind of entity. Each
// editor passes whole phrases ("Off for this team") rather than a noun, so translations never have
// to build a sentence around an entity name.
export interface ContentLoggingLabels {
	inherit: string;
	off: string;
	on: string;
}

// contentLoggingOptions pairs each choice with its label.
export function contentLoggingOptions(labels: ContentLoggingLabels): { value: ContentLoggingChoice; label: string }[] {
	return [
		{ value: "inherit", label: labels.inherit },
		{ value: "disabled", label: labels.off },
		{ value: "enabled", label: labels.on },
	];
}