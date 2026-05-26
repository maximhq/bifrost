import { Radio } from "lucide-react";
import ContactUsView from "../../views/contactUsView";

interface EnableToggleProps {
	enabled: boolean;
	onToggle: () => void;
	disabled?: boolean;
}

interface PubSubConnectorViewProps {
	onDelete?: () => void;
	isDeleting?: boolean;
	enableToggle?: EnableToggleProps;
}

export default function PubSubConnectorView(_props: PubSubConnectorViewProps) {
	return (
		<div className="space-y-6">
			<div className="space-y-4">
				<div className="flex w-full flex-col items-center justify-center py-8">
					<ContactUsView
						align="middle"
						className="mx-auto w-full max-w-lg"
						icon={<Radio className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
						title="Unlock Google Cloud Pub/Sub trace streaming"
						description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
						readmeLink="https://docs.getbifrost.ai/enterprise/pubsub-connector"
						testIdPrefix="pubsub-connector"
					/>
				</div>
			</div>
		</div>
	);
}
