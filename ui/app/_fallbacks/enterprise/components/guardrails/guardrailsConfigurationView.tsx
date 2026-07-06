import { Construction } from "lucide-react";
import FeaturePlaceholderView from "../views/featurePlaceholderView";

export default function GuardrailsConfigurationView() {
	return (
		<FeaturePlaceholderView
			icon={Construction}
			title="Unlock guardrails for better security"
			description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
			readmeLink="https://docs.getbifrost.ai/enterprise/guardrails"
		/>
	);
}
