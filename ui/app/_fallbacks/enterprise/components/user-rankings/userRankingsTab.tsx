import { Users } from "lucide-react";
import FeaturePlaceholderView from "../views/featurePlaceholderView";

export default function UserRankingsTab() {
	return (
		<FeaturePlaceholderView
			icon={Users}
			title="Unlock user rankings for better visibility"
			description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
			readmeLink="https://docs.getbifrost.ai/enterprise/user-rankings"
			testIdPrefix="user-rankings"
		/>
	);
}
