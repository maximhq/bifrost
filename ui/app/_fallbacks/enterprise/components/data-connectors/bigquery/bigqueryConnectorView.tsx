import { Database } from "lucide-react";
import ContactUsView from "../../views/contactUsView";

interface EnableToggleProps {
	enabled: boolean;
	onToggle: () => void;
	disabled?: boolean;
}

interface BigQueryConnectorViewProps {
	onDelete?: () => void;
	isDeleting?: boolean;
	enableToggle?: EnableToggleProps;
}

export default function BigQueryConnectorView(_props: BigQueryConnectorViewProps) {
	return (
		<div className="space-y-6">
			{/* Content - OSS: paywall only; no delete/save buttons */}
			<div className="space-y-4">
				<div className="flex w-full flex-col items-center justify-center py-8">
					<ContactUsView
						align="middle"
						className="mx-auto w-full max-w-lg"
						icon={<Database className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
						title="Unlock native BigQuery data ingestion for analytics"
						description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
						readmeLink="https://docs.getbifrost.ai/enterprise/bigquery-connector"
					/>
				</div>
			</div>
		</div>
	);
}
