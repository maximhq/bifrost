"use client";

import DatadogConnectorView from "@enterprise/components/data-connectors/datadog/datadogConnectorView";

export default function DatadogView() {
	return (
		<div className="flex w-full flex-col gap-4">
			<div className="flex w-full flex-col gap-3">
				<DatadogConnectorView />
			</div>
		</div>
	);
}
