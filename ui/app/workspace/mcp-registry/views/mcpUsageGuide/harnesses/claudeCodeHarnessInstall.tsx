import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { buildClaudeCodeCommand } from "../commandBuilders";
import { HarnessCommandSection } from "../harnessCommandSection";
import type { ClaudeScope, HarnessInstallProps } from "../types";
import { getRegistrationLabel } from "../utils";
import { withBasePath } from "@/lib/utils/basePath";

/** Install panel for Claude Code: the generated config or command for the selected MCP servers. */
export function ClaudeCodeHarnessInstall({
	canGenerateCommand,
	clientConfig,
	emptyMessage,
	headers,
	selectedServers,
	serverScope,
}: HarnessInstallProps) {
	const { t } = useTranslation("mcp");
	const [scope, setScope] = useState<ClaudeScope>("local");

	const command = useMemo(
		() =>
			buildClaudeCodeCommand({
				clientConfig,
				headers,
				scope,
				selectedServers: serverScope === "selected" ? selectedServers : undefined,
			}),
		[clientConfig, headers, scope, selectedServers, serverScope],
	);

	return (
		<HarnessCommandSection
			canCopyCommand={canGenerateCommand}
			command={command}
			controls={
				<Select value={scope} onValueChange={(value) => setScope(value as ClaudeScope)}>
					<SelectTrigger className="w-32" data-testid="mcp-usage-guide-claude-scope" size="sm">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="local">{t("usageGuide.scopeLocal")}</SelectItem>
						<SelectItem value="project">{t("usageGuide.scopeProject")}</SelectItem>
						<SelectItem value="user">{t("usageGuide.scopeUser")}</SelectItem>
					</SelectContent>
				</Select>
			}
			emptyMessage={emptyMessage}
			harnessName="Claude Code"
			logoSrc={withBasePath("/images/harness/claudecode.svg")}
			registrationLabel={getRegistrationLabel(serverScope, selectedServers)}
		/>
	);
}