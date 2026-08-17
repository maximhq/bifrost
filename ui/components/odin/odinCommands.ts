/**
 * Slash commands for the Odin composer.
 *
 * Kept as data rather than a switch in the composer so the menu and the
 * behaviour cannot drift: the list below is both what gets shown and what gets
 * matched, so a command can never appear in the menu without running, or run
 * without appearing.
 */

export type OdinCommandID = "clear";

export interface OdinCommand {
	id: OdinCommandID;
	/** What the user types, without the slash. */
	name: string;
	description: string;
}

export const ODIN_COMMANDS: OdinCommand[] = [{ id: "clear", name: "clear", description: "Start a new conversation" }];

/**
 * Reports whether the current input should open the command menu.
 *
 * Only a lone leading slash counts. Someone writing "what is the p99 for
 * /v1/chat/completions?" is not reaching for a command, and popping a menu over
 * their question mid-sentence would be worse than having no commands at all.
 */
export function isOdinCommandQuery(value: string): boolean {
	return /^\/[a-z]*$/i.test(value);
}

/** Commands matching what has been typed so far, in listed order. */
export function matchOdinCommands(value: string): OdinCommand[] {
	if (!isOdinCommandQuery(value)) return [];
	const typed = value.slice(1).toLowerCase();
	return ODIN_COMMANDS.filter((command) => command.name.startsWith(typed));
}

/**
 * Resolves a submitted line to a command, or null when it is an ordinary
 * question.
 *
 * Matching is exact: "/clear" is a command, "/clear the logs table" is a
 * question that happens to start with a slash. Treating the second as a command
 * would silently discard the rest of what someone wrote.
 */
export function resolveOdinCommand(value: string): OdinCommand | null {
	const trimmed = value.trim().toLowerCase();
	if (!trimmed.startsWith("/")) return null;
	return ODIN_COMMANDS.find((command) => trimmed === `/${command.name}`) ?? null;
}