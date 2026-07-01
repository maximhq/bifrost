import type { ComponentType, ReactNode } from "react";
import type { HarnessID, HarnessInstallProps } from "../types";
import { AntigravityHarnessInstall } from "./antigravityHarnessInstall";
import { ClaudeCodeHarnessInstall } from "./claudeCodeHarnessInstall";
import { CodexHarnessInstall } from "./codexHarnessInstall";
import { CursorHarnessInstall } from "./cursorHarnessInstall";
import { OpenCodeHarnessInstall } from "./opencodeHarnessInstall";
import { VSCodeHarnessInstall } from "./vscodeHarnessInstall";
import { WindsurfHarnessInstall } from "./windsurfHarnessInstall";

export { AntigravityHarnessInstall, AntigravityIcon } from "./antigravityHarnessInstall";
export { ClaudeCodeHarnessInstall } from "./claudeCodeHarnessInstall";
export { CodexHarnessInstall } from "./codexHarnessInstall";
export { CursorHarnessInstall } from "./cursorHarnessInstall";
export { OpenCodeHarnessInstall } from "./opencodeHarnessInstall";
export { VSCodeHarnessInstall } from "./vscodeHarnessInstall";
export { WindsurfHarnessInstall } from "./windsurfHarnessInstall";

export interface HarnessDefinition {
	id: HarnessID;
	label: string;
	/** Tab icon. A logo path renders an <img>; otherwise a custom node. */
	icon: ReactNode;
	usesPlatform?: boolean;
	Install: ComponentType<HarnessInstallProps>;
}

function logoIcon(src: string): ReactNode {
	return <img src={src} alt="" aria-hidden="true" className="size-4 rounded-[2px]" />;
}

/** Ordered list of supported harnesses, driving both the tabs and the install panels. */
export const HARNESSES: HarnessDefinition[] = [
	{
		id: "claude-code",
		label: "Claude Code",
		icon: logoIcon("/images/harness/claudecode.svg"),
		Install: ClaudeCodeHarnessInstall,
	},
	{
		id: "cursor",
		label: "Cursor",
		icon: logoIcon("/images/harness/cursor.svg"),
		usesPlatform: true,
		Install: CursorHarnessInstall,
	},
	{
		id: "codex",
		label: "Codex",
		icon: logoIcon("/images/harness/codex.svg"),
		usesPlatform: true,
		Install: CodexHarnessInstall,
	},
	{
		id: "vscode",
		label: "VS Code",
		icon: logoIcon("/images/harness/vscode.svg"),
		usesPlatform: true,
		Install: VSCodeHarnessInstall,
	},
	{
		id: "opencode",
		label: "OpenCode",
		icon: logoIcon("/images/harness/opencode.svg"),
		usesPlatform: true,
		Install: OpenCodeHarnessInstall,
	},
	{
		id: "windsurf",
		label: "Windsurf (Devin)",
		icon: logoIcon("/images/harness/windsurf.svg"),
		usesPlatform: true,
		Install: WindsurfHarnessInstall,
	},
	{
		id: "antigravity",
		label: "Antigravity",
		icon: logoIcon("/images/harness/antigravity.svg"),
		usesPlatform: true,
		Install: AntigravityHarnessInstall,
	},
];