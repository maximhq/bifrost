import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { buildVSCodeConfig, buildVSCodeDeeplink } from "../commandBuilders";
import { HarnessCommandSection } from "../harnessCommandSection";
import type { HarnessInstallProps, VSCodeConfigScope } from "../types";
import { getRegistrationLabel } from "../utils";

export function VSCodeHarnessInstall({
	canGenerateCommand,
	clientConfig,
	emptyMessage,
	headers,
	platform,
	selectedServers,
	serverScope,
}: HarnessInstallProps) {
	const { t } = useTranslation("mcp");
	const [configScope, setConfigScope] = useState<VSCodeConfigScope>("workspace");

	const serverArgs = useMemo(
		() => ({
			clientConfig,
			headers,
			selectedServers: serverScope === "selected" ? selectedServers : undefined,
		}),
		[clientConfig, headers, selectedServers, serverScope],
	);

	const config = useMemo(() => buildVSCodeConfig(serverArgs), [serverArgs]);

	const deeplink = useMemo(() => buildVSCodeDeeplink(serverArgs), [serverArgs]);

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
						<SelectItem value="workspace">{t("usageGuide.scopeWorkspace")}</SelectItem>
						<SelectItem value="user">{t("usageGuide.scopeUser")}</SelectItem>
					</SelectContent>
				</Select>
			}
			copySuccessMessage={t("common.configCopied")}
			deeplink={deeplink}
			emptyMessage={emptyMessage}
			harnessName="VS Code"
			label={t("common.config")}
			logoSrc="/images/harness/vscode.svg"
			registrationLabel={`${configPath} · ${getRegistrationLabel(serverScope, selectedServers)}`}
		/>
	);
}