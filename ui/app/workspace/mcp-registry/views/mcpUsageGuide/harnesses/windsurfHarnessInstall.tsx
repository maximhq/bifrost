import { useMemo } from "react";
import { buildWindsurfConfig } from "../commandBuilders";
import { HarnessCommandSection } from "../harnessCommandSection";
import type { HarnessInstallProps } from "../types";
import { getRegistrationLabel, getUserHomePrefix } from "../utils";

export function WindsurfHarnessInstall({
	canGenerateCommand,
	clientConfig,
	platform,
	selectedServers,
	serverScope,
	virtualKey,
}: HarnessInstallProps) {
	const configPath = `${getUserHomePrefix(platform)}/.codeium/windsurf/mcp_config.json`;

	const config = useMemo(() => {
		if (!virtualKey) return "";
		return buildWindsurfConfig({
			clientConfig,
			selectedServers: serverScope === "selected" ? selectedServers : undefined,
			virtualKey,
		});
	}, [clientConfig, selectedServers, serverScope, virtualKey]);

	return (
		<HarnessCommandSection
			canCopyCommand={canGenerateCommand}
			command={config}
			controls={null}
			copySuccessMessage="Config copied"
			emptyMessage={virtualKey ? "Select servers or use Gateway root." : "Select a virtual key to generate the config."}
			harnessName="Windsurf (Devin)"
			label="Config"
			logoSrc="/images/harness/windsurf.svg"
			registrationLabel={`${configPath} · ${getRegistrationLabel(serverScope, selectedServers)}`}
		/>
	);
}