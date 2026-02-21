import { Dog } from "lucide-react";
import ContactUsView from "../../views/contactUsView";

interface DatadogConnectorViewProps {
	onDelete?: () => void;
	isDeleting?: boolean;
}

export default function DatadogConnectorView(_props: DatadogConnectorViewProps) {
	return (
		<div className="space-y-6">
			{/* Content - OSS: paywall only; no delete/save buttons */}
			<div className="space-y-4">
				<div className="flex w-full flex-col items-center justify-center py-8">
					<ContactUsView
						align="middle"
						className="mx-auto w-full max-w-lg"
						icon={<Dog className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
						title="Unlock native Datadog data ingestion for better observability"
						description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
						readmeLink="https://docs.getbifrost.ai/enterprise/datadog-connector"
					/>
				</div>
			</div>
		</div>
	);
}
