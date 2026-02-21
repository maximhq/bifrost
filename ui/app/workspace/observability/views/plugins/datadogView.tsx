"use client";

import DatadogConnectorView from "@enterprise/components/data-connectors/datadog/datadogConnectorView";

interface DatadogViewProps {
	onDelete?: () => void;
	isDeleting?: boolean;
}

export default function DatadogView({ onDelete, isDeleting }: DatadogViewProps) {
	return (
		<div className="flex w-full flex-col gap-4">
			<div className="flex w-full flex-col gap-3">
				<DatadogConnectorView onDelete={onDelete} isDeleting={isDeleting} />
			</div>
		</div>
	);
}
