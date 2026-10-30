import { cn } from "@/lib/utils";
import { Orbit } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { buildAntigravityConfig } from "../commandBuilders";
import { HarnessCommandSection } from "../harnessCommandSection";
import type { HarnessInstallProps } from "../types";
import { getRegistrationLabel, getUserHomePrefix } from "../utils";

export function AntigravityIcon({ className }: { className?: string }) {
	return <Orbit className={cn("text-muted-foreground", className)} />;
}

export function AntigravityHarnessInstall({
	canGenerateCommand,
	clientConfig,
	emptyMessage,
	headers,
	platform,
	selectedServers,
	serverScope,
}: HarnessInstallProps) {
	const { t } = useTranslation("mcp");
	const configPath = `${getUserHomePrefix(platform)}/.gemini/antigravity/mcp_config.json`;

	const config = useMemo(
		() =>
			buildAntigravityConfig({
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
			harnessName="Antigravity"
			label={t("common.config")}
			logoSrc="/images/harness/antigravity.svg"
			registrationLabel={`${configPath} · ${getRegistrationLabel(serverScope, selectedServers)}`}
		/>
	);
}