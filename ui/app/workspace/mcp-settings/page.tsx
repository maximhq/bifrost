import MCPView from "../config/views/mcpView";
import { WorkspacePageShell } from "@/components/workspacePageShell";

export default function MCPSettingsPage() {
	return (
		<WorkspacePageShell>
			<MCPView />
		</WorkspacePageShell>
	);
}
