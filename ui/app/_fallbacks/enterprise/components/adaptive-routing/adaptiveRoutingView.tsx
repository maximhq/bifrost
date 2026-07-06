import { Shuffle } from "lucide-react";
import FeaturePlaceholderView from "../views/featurePlaceholderView";

export default function AdaptiveRoutingView() {
	return (
		<FeaturePlaceholderView
			icon={Shuffle}
			title="Unlock adaptive routing for better performance"
			description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
			readmeLink="https://docs.getbifrost.ai/enterprise/adaptive-load-balancing"
		/>
	);
}
