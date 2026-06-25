import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useMemo, useState } from "react";
import { buildClaudeCodeCommand } from "../commandBuilders";
import { HarnessCommandSection } from "../harnessCommandSection";
import type { ClaudeScope, HarnessInstallProps } from "../types";
import { getRegistrationLabel } from "../utils";

export function ClaudeCodeHarnessInstall({
	canGenerateCommand,
	clientConfig,
	selectedServers,
	serverScope,
	virtualKey,
}: HarnessInstallProps) {
	const [scope, setScope] = useState<ClaudeScope>("local");

	const command = useMemo(() => {
		if (!virtualKey) return "";
		return buildClaudeCodeCommand({
			clientConfig,
			scope,
			selectedServers: serverScope === "selected" ? selectedServers : undefined,
			virtualKey,
		});
	}, [clientConfig, scope, selectedServers, serverScope, virtualKey]);

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
						<SelectItem value="local">Local</SelectItem>
						<SelectItem value="project">Project</SelectItem>
						<SelectItem value="user">User</SelectItem>
					</SelectContent>
				</Select>
			}
			emptyMessage={virtualKey ? "Select servers or use Gateway root." : "Select a virtual key to generate the command."}
			harnessName="Claude Code"
			logoSrc="/images/harness/claudecode.svg"
			registrationLabel={getRegistrationLabel(serverScope, selectedServers)}
		/>
	);
}