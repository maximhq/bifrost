import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useMemo, useState } from "react";
import { buildVSCodeConfig, buildVSCodeDeeplink } from "../commandBuilders";
import { HarnessCommandSection } from "../harnessCommandSection";
import type { HarnessInstallProps, VSCodeConfigScope } from "../types";
import { getRegistrationLabel } from "../utils";

export function VSCodeHarnessInstall({
	canGenerateCommand,
	clientConfig,
	platform,
	selectedServers,
	serverScope,
	virtualKey,
}: HarnessInstallProps) {
	const [configScope, setConfigScope] = useState<VSCodeConfigScope>("workspace");

	const serverArgs = useMemo(
		() => ({
			clientConfig,
			selectedServers: serverScope === "selected" ? selectedServers : undefined,
			virtualKey: virtualKey!,
		}),
		[clientConfig, selectedServers, serverScope, virtualKey],
	);

	const config = useMemo(() => {
		if (!virtualKey) return "";
		return buildVSCodeConfig(serverArgs);
	}, [serverArgs, virtualKey]);

	const deeplink = useMemo(() => {
		if (!virtualKey) return "";
		return buildVSCodeDeeplink(serverArgs);
	}, [serverArgs, virtualKey]);

	const userConfigPath = {
		linux: "~/.config/Code/User/mcp.json",
		macos: "~/Library/Application Support/Code/User/mcp.json",
		windows: "%APPDATA%/Code/User/mcp.json",
	}[platform];
	const configPath = configScope === "workspace" ? ".vscode/mcp.json" : userConfigPath;

	return (
		<HarnessCommandSection
			canCopyCommand={canGenerateCommand}
			command={config}
			controls={
				<Select value={configScope} onValueChange={(value) => setConfigScope(value as VSCodeConfigScope)}>
					<SelectTrigger className="w-32" data-testid="mcp-usage-guide-vscode-config-scope" size="sm">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="workspace">Workspace</SelectItem>
						<SelectItem value="user">User</SelectItem>
					</SelectContent>
				</Select>
			}
			copySuccessMessage="Config copied"
			deeplink={deeplink}
			emptyMessage={virtualKey ? "Select servers or use Gateway root." : "Select a virtual key to generate the config."}
			harnessName="VS Code"
			label="Config"
			logoSrc="/images/harness/vscode.svg"
			registrationLabel={`${configPath} · ${getRegistrationLabel(serverScope, selectedServers)}`}
		/>
	);
}