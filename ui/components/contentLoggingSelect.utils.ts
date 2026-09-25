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

// contentLoggingOptions labels the choices for one kind of entity ("key", "team", ...).
export function contentLoggingOptions(entityLabel: string): { value: ContentLoggingChoice; label: string }[] {
	return [
		{ value: "inherit", label: "Inherit" },
		{ value: "disabled", label: `Off for this ${entityLabel}` },
		{ value: "enabled", label: `On for this ${entityLabel}` },
	];
}