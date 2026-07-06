import { Building2 } from "lucide-react";
import FeaturePlaceholderView from "../views/featurePlaceholderView";

export function BusinessUnitsView() {
	return (
		<FeaturePlaceholderView
			icon={Building2}
			testIdPrefix="business-units-governance"
			title="Unlock business units & advanced governance"
			description="Manage users, business units with our enterprise-grade governance. This feature is part of the Bifrost enterprise license."
			readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
		/>
	);
}
