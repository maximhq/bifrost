import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { buildCursorConfig, buildCursorDeeplink } from "../commandBuilders";
import { HarnessCommandSection } from "../harnessCommandSection";
import type { CursorConfigScope, HarnessInstallProps } from "../types";
import { getRegistrationLabel, getUserHomePrefix } from "../utils";

export function CursorHarnessInstall({
	canGenerateCommand,
	clientConfig,
	emptyMessage,
	headers,
	platform,
	selectedServers,
	serverScope,
}: HarnessInstallProps) {
	const { t } = useTranslation("mcp");
	const [configScope, setConfigScope] = useState<CursorConfigScope>("global");

	const serverArgs = useMemo(
		() => ({
			clientConfig,
			headers,
			selectedServers: serverScope === "selected" ? selectedServers : undefined,
		}),
		[clientConfig, headers, selectedServers, serverScope],
	);

	const config = useMemo(() => buildCursorConfig(serverArgs), [serverArgs]);

	const deeplink = useMemo(() => buildCursorDeeplink(serverArgs), [serverArgs]);

	const configPath = configScope === "project" ? ".cursor/mcp.json" : `${getUserHomePrefix(platform)}/.cursor/mcp.json`;

	return (
		<HarnessCommandSection
			canCopyCommand={canGenerateCommand}
			command={config}
			controls={
				<Select value={configScope} onValueChange={(value) => setConfigScope(value as CursorConfigScope)}>
					<SelectTrigger className="w-32" data-testid="mcp-usage-guide-cursor-config-scope" size="sm">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="global">{t("usageGuide.scopeGlobal")}</SelectItem>
						<SelectItem value="project">{t("usageGuide.scopeProject")}</SelectItem>
					</SelectContent>
				</Select>
			}
			copySuccessMessage={t("common.configCopied")}
			deeplink={deeplink}
			emptyMessage={emptyMessage}
			harnessName="Cursor"
			label={t("common.config")}
			logoSrc="/images/harness/cursor.svg"
			registrationLabel={`${configPath} · ${getRegistrationLabel(serverScope, selectedServers)}`}
		/>
	);
}