import { Router } from "lucide-react";
import ContactUsView from "../views/contactUsView";

export default function PromptDeploymentView() {
	return (
		<div className="w-full">
			<ContactUsView
				align="top"
				className="justify-start gap-3 rounded-md border p-4"
				icon={<Router className="h-8 w-8" strokeWidth={1.5} />}
				title="Unlock prompt deployments for better prompt versioning and A/B testing."
				description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
				readmeLink="https://docs.getbifrost.ai/enterprise/prompt-deployments"
			/>
		</div>
	);
}