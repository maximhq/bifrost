"use client";

import DatadogConnectorView from "@enterprise/components/data-connectors/datadog/datadogConnectorView";

interface EnableToggleProps {
	enabled: boolean;
	onToggle: () => void;
	disabled?: boolean;
}

interface DatadogViewProps {
	onDelete?: () => void;
	isDeleting?: boolean;
	enableToggle?: EnableToggleProps;
}

export default function DatadogView({ onDelete, isDeleting, enableToggle }: DatadogViewProps) {
	return (
		<div className="flex w-full flex-col gap-4">
			<div className="flex w-full flex-col gap-3">
				<DatadogConnectorView onDelete={onDelete} isDeleting={isDeleting} enableToggle={enableToggle} />
			</div>
		</div>
	);
}
