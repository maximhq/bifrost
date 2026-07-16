import type { SearchSelectOption } from "@/components/ui/searchSelect";
import type { CoreConfig } from "@/lib/types/config";
import type { VirtualKey } from "@/lib/types/governance";
import type { MCPClient } from "@/lib/types/mcp";
import type { ReactNode } from "react";

export type HarnessID = "claude-code" | "codex" | "cursor" | "windsurf" | "antigravity" | "vscode" | "opencode";
export type ClaudeScope = "local" | "project" | "user";
export type CodexConfigScope = "user" | "project";
export type CursorConfigScope = "global" | "project";
export type HarnessPlatform = "macos" | "windows" | "linux";
export type VSCodeConfigScope = "workspace" | "user";
export type ServerScope = "all" | "selected";

export interface VirtualKeyOption extends SearchSelectOption {
	virtualKey: VirtualKey;
}

export interface HarnessInstallProps {
	canGenerateCommand: boolean;
	clientConfig?: CoreConfig;
	platform: HarnessPlatform;
	selectedServers: MCPClient[];
	serverScope: ServerScope;
	virtualKey?: VirtualKey;
}

export interface HarnessCommandSectionProps {
	canCopyCommand: boolean;
	command: string;
	controls: ReactNode;
	copySuccessMessage?: string;
	deeplink?: string;
	deeplinkLabel?: string;
	emptyMessage: string;
	harnessName: string;
	label?: string;
	logoSrc?: string;
	registrationLabel: string;
}