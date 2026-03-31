"use client";

import BigQueryConnectorView from "@enterprise/components/data-connectors/bigquery/bigqueryConnectorView";

export default function BigQueryView() {
	return (
		<div className="flex w-full flex-col gap-4">
			<div className="flex w-full flex-col gap-3">
				<BigQueryConnectorView />
			</div>
		</div>
	);
}
