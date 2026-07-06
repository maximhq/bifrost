import ModelSettingsView from "@/app/workspace/config/views/modelSettingsView";
import { WorkspacePageShell } from "@/components/workspacePageShell";

export default function CustomPricingPage() {
	return (
		<WorkspacePageShell>
			<ModelSettingsView />
		</WorkspacePageShell>
	);
}
