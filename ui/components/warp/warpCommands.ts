// One list drives both the menu and matching, so a command cannot show without running or vice versa.

export type WarpCommandID = "clear";

export interface WarpCommand {
	id: WarpCommandID;
	/** What the user types, without the slash. */
	name: string;
	description: string;
}

export const WARP_COMMANDS: WarpCommand[] = [{ id: "clear", name: "clear", description: "Start a new conversation" }];

/** Only a lone leading slash counts, so a question mentioning "/v1/chat" does not pop the menu. */
export function isWarpCommandQuery(value: string): boolean {
	return /^\/[a-z]*$/i.test(value);
}

export function matchWarpCommands(value: string): WarpCommand[] {
	if (!isWarpCommandQuery(value)) return [];
	const typed = value.slice(1).toLowerCase();
	return WARP_COMMANDS.filter((command) => command.name.startsWith(typed));
}

/** Exact match only: "/clear the logs table" is a question, and treating it as a command would drop the text. */
export function resolveWarpCommand(value: string): WarpCommand | null {
	const trimmed = value.trim().toLowerCase();
	if (!trimmed.startsWith("/")) return null;
	return WARP_COMMANDS.find((command) => trimmed === `/${command.name}`) ?? null;
}