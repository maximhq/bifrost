import ScopedPricingOverridesView from "@/app/workspace/custom-pricing/overrides/scopedPricingOverridesView";
import { WorkspacePageShell } from "@/components/workspacePageShell";

export default function ScopedPricingOverridesPage() {
	return (
		<WorkspacePageShell className="overflow-hidden">
			<ScopedPricingOverridesView />
		</WorkspacePageShell>
	);
}
