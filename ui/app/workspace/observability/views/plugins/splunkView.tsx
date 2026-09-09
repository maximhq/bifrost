import SplunkConnectorView from "@enterprise/components/data-connectors/splunk/splunkConnectorView";

export default function SplunkView() {
	return (
		<div className="flex w-full flex-col gap-4">
			<div className="flex w-full flex-col gap-3">
				<SplunkConnectorView />
			</div>
		</div>
	);
}