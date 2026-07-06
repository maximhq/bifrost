import { BookUser } from "lucide-react";
import FeaturePlaceholderView from "../views/featurePlaceholderView";

export default function SCIMView() {
	return (
		<FeaturePlaceholderView
			icon={BookUser}
			title="Unlock SCIM based access management for user provisioning"
			description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
			readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
		/>
	);
}
