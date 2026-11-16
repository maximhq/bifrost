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

/**
 * How the generated client config authenticates to Bifrost's /mcp endpoint.
 * Mirrors the inbound credentials the gateway accepts (see mcp_server_auth_mode):
 *  - virtual_key: x-bf-vk header
 *  - oauth: no credential in the config; the client runs the browser consent flow
 *  - idp_token: the caller's own identity-provider bearer, enterprise + SSO only
 */
export type AuthMethod = "virtual_key" | "oauth" | "idp_token";

export interface VirtualKeyOption extends SearchSelectOption {
	virtualKey: VirtualKey;
}

export interface HarnessInstallProps {
	canGenerateCommand: boolean;
	clientConfig?: CoreConfig;
	/** Shown in place of the code block while the selections are incomplete. */
	emptyMessage: string;
	/** Request headers the generated config must carry; empty for the OAuth flow. */
	headers: Record<string, string>;
	platform: HarnessPlatform;
	selectedServers: MCPClient[];
	serverScope: ServerScope;
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