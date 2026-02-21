"use client"

import DatadogView from "@/app/workspace/observability/views/plugins/datadogView"
import MaximView from "@/app/workspace/observability/views/plugins/maximView"
import NewrelicView from "@/app/workspace/observability/views/plugins/newRelicView"
import OtelView from "@/app/workspace/observability/views/plugins/otelView"
import PrometheusView from "@/app/workspace/observability/views/plugins/prometheusView"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"

const CONNECTOR_TITLES: Record<string, string> = {
	prometheus: "Prometheus",
	otel: "Open Telemetry",
	maxim: "Maxim",
	datadog: "Datadog",
	newrelic: "New Relic",
}

interface ConnectorConfigSheetProps {
	open: boolean
	onOpenChange: (open: boolean) => void
	connectorId: string | null
}

export function ConnectorConfigSheet({ open, onOpenChange, connectorId }: ConnectorConfigSheetProps) {
	const title = connectorId ? CONNECTOR_TITLES[connectorId] ?? connectorId : "Configuration"

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent
				side="right"
				className="dark:bg-card flex w-full flex-col gap-4 overflow-x-hidden bg-white p-8 sm:max-w-[60%]"
			>
				<SheetHeader className="shrink-0 border-b px-6 py-4">
					<SheetTitle>{title}</SheetTitle>
				</SheetHeader>
				<div className="custom-scrollbar min-h-0 flex-1 overflow-y-auto px-6 py-4">
					{connectorId === "prometheus" && <PrometheusView />}
					{connectorId === "otel" && <OtelView />}
					{connectorId === "maxim" && <MaximView />}
					{connectorId === "datadog" && <DatadogView />}
					{connectorId === "newrelic" && <NewrelicView />}
				</div>
			</SheetContent>
		</Sheet>
	)
}
