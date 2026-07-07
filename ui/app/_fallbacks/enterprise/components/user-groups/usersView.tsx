import { Users } from "lucide-react";
import FeaturePlaceholderView from "../views/featurePlaceholderView";

export default function UsersView() {
	return (
		<FeaturePlaceholderView
			icon={Users}
			title="Unlock users & user governance"
			description="Manage users, set per-user budgets and rate limits, and control access with enterprise-grade governance. This feature is part of the Bifrost enterprise license."
			readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
		/>
	);
}
