import { Network } from "lucide-react";
import FeaturePlaceholderView from "../views/featurePlaceholderView";

export default function ClusterPage() {
	return (
		<FeaturePlaceholderView
			icon={Network}
			title="Unlock cluster mode to scale reliably"
			description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
			readmeLink="https://docs.getbifrost.ai/enterprise/clustering"
		/>
	);
}
