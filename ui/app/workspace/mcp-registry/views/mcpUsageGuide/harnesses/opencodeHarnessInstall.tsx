import { useMemo } from "react";
import { buildOpenCodeConfig } from "../commandBuilders";
import { HarnessCommandSection } from "../harnessCommandSection";
import type { HarnessInstallProps } from "../types";
import { getRegistrationLabel } from "../utils";

export function OpenCodeHarnessInstall({
	canGenerateCommand,
	clientConfig,
	emptyMessage,
	headers,
	platform,
	selectedServers,
	serverScope,
}: HarnessInstallProps) {
	const configPath = {
		linux: "~/.config/opencode/opencode.json",
		macos: "~/.config/opencode/opencode.json",
		windows: "%APPDATA%/opencode/opencode.json",
	}[platform];

	const config = useMemo(
		() =>
			buildOpenCodeConfig({
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
			copySuccessMessage="Config copied"
			emptyMessage={emptyMessage}
			harnessName="OpenCode"
			label="Config"
			logoSrc="/images/harness/opencode.svg"
			registrationLabel={`${configPath} · ${getRegistrationLabel(serverScope, selectedServers)}`}
		/>
	);
}