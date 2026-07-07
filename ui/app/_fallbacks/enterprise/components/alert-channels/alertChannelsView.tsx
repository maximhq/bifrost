import { Siren } from "lucide-react";
import FeaturePlaceholderView from "../views/featurePlaceholderView";

export default function AlertChannelsView() {
	return (
		<FeaturePlaceholderView
			icon={Siren}
			title="Unlock alert channels for better observability"
			description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
			readmeLink="https://docs.getbifrost.ai/enterprise/alert-channels"
		/>
	);
}
