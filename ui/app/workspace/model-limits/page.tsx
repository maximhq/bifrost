import { WorkspacePageShell } from "@/components/workspacePageShell";
import ModelLimitsView from "./views/modelLimitsView";

export default function ModelLimitsPage() {
	return (
		<WorkspacePageShell className="overflow-hidden">
			<ModelLimitsView />
		</WorkspacePageShell>
	);
}
