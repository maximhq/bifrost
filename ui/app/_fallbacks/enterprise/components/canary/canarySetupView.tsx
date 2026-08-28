import { Split } from "lucide-react";
import ContactUsView from "../views/contactUsView";

export default function CanarySetupView() {
	return (
		<div className="space-y-6">
			{/* Content - OSS: paywall only */}
			<div className="space-y-4">
				<div className="flex w-full flex-col items-center justify-center py-8">
					<ContactUsView
						align="middle"
						className="mx-auto w-full max-w-lg"
						icon={<Split className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
						title="Unlock canary deployments for safe rollouts"
						description="Forward a configurable share of live traffic to a second Bifrost deployment to validate new versions before full rollout. This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
						readmeLink="https://docs.getbifrost.ai/enterprise/canary-deployments"
					/>
				</div>
			</div>
		</div>
	);
}