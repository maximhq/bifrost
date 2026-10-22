import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { buildWindsurfConfig } from "../commandBuilders";
import { HarnessCommandSection } from "../harnessCommandSection";
import type { HarnessInstallProps } from "../types";
import { getRegistrationLabel, getUserHomePrefix } from "../utils";

export function WindsurfHarnessInstall({
	canGenerateCommand,
	clientConfig,
	emptyMessage,
	headers,
	platform,
	selectedServers,
	serverScope,
}: HarnessInstallProps) {
	const { t } = useTranslation("mcp");
	const configPath = `${getUserHomePrefix(platform)}/.codeium/windsurf/mcp_config.json`;

	const config = useMemo(
		() =>
			buildWindsurfConfig({
				clientConfig,
				headers,
				selectedServers: serverScope === "selected" ? selectedServers : undefined,
			}),
		[clientConfig, headers, selectedServers, serverScope],
	);

	return (
		<HarnessCommandSection
			canCopyCommand={canGenerateCommand}
			command={config}
			controls={null}
			copySuccessMessage={t("common.configCopied")}
			emptyMessage={emptyMessage}
			harnessName="Windsurf (Devin)"
			label={t("common.config")}
			logoSrc="/images/harness/windsurf.svg"
			registrationLabel={`${configPath} · ${getRegistrationLabel(serverScope, selectedServers)}`}
		/>
	);
}