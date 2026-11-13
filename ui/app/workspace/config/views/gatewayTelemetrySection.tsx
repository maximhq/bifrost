import { Button } from "@/components/ui/button";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Plus, Trash2 } from "lucide-react";
import type { Dispatch, SetStateAction } from "react";
import { Trans, useTranslation } from "react-i18next";
import { newDestinationId, type TelemetryDestinationDraft, type TelemetryDraft } from "./gatewayTelemetrySection.utils";

interface GatewayTelemetrySectionProps {
	value: TelemetryDraft;
	onChange: Dispatch<SetStateAction<TelemetryDraft>>;
	disabled: boolean;
}

// GatewayTelemetrySection edits where a client gateway forwards its clients'
// OTLP telemetry. It is controlled: the gateways view owns the draft and saves
// it with the rest of the gateway settings.
export default function GatewayTelemetrySection({ value, onChange, disabled }: GatewayTelemetrySectionProps) {
	const { t } = useTranslation("config");
	// Functional updates: the headers table can report a conflict and an edit in one event.
	const update = (patch: Partial<TelemetryDraft>) => onChange((prev) => ({ ...prev, ...patch }));
	const updateRow = (id: string, patch: Partial<TelemetryDestinationDraft>) =>
		onChange((prev) => ({ ...prev, destinations: prev.destinations.map((d) => (d.id === id ? { ...d, ...patch } : d)) }));

	return (
		<div className="space-y-3 rounded-sm border p-4" data-testid="claude-code-telemetry">
			<div className="flex items-center justify-between space-x-2">
				<div className="space-y-0.5">
					<label htmlFor="claude-code-telemetry-enabled" className="text-sm font-medium">
						{t("claudeCode.telemetryLabel")}
					</label>
					<p className="text-muted-foreground text-sm">{t("claudeCode.telemetryDescription")}</p>
				</div>
				<Switch
					id="claude-code-telemetry-enabled"
					data-testid="claude-code-telemetry-switch"
					size="md"
					checked={value.enabled}
					onCheckedChange={(enabled) => update({ enabled })}
					disabled={disabled}
				/>
			</div>

			<p className="text-sm font-medium">{t("claudeCode.collectorsLabel")}</p>
			{value.destinations.map((d, i) => (
				<div key={d.id} className="space-y-2 rounded-sm border p-3" data-testid="claude-code-telemetry-destination">
					<div className="flex items-center gap-2">
						<Input
							value={d.endpoint}
							// i18n-ignore: a URL example, not prose
							placeholder="https://otel-collector.example.com"
							aria-label={t("claudeCode.collectorEndpointLabel")}
							data-testid="claude-code-telemetry-endpoint-input"
							onChange={(e) => updateRow(d.id, { endpoint: e.target.value })}
							disabled={disabled}
						/>
						<Button
							type="button"
							variant="ghost"
							size="icon"
							aria-label={t("claudeCode.removeCollector")}
							data-testid="claude-code-telemetry-remove-btn"
							onClick={() => onChange((prev) => ({ ...prev, destinations: prev.destinations.filter((row) => row.id !== d.id) }))}
							disabled={disabled}
						>
							<Trash2 className="h-4 w-4" />
						</Button>
					</div>
					<HeadersTable
						label={t("claudeCode.collectorHeadersLabel")}
						value={d.headers}
						onChange={(headers) => updateRow(d.id, { headers })}
						// A conflict on the empty add row has nothing to save, so only held rows count.
						onConflictsChange={(conflicts, headers) =>
							updateRow(d.id, { headerConflict: [...conflicts.keys()].some((row) => row < Object.keys(headers).length) })
						}
						// i18n-ignore: a header name example, not prose
						keyPlaceholder="authorization"
						// i18n-ignore: a reference example, not prose
						valuePlaceholder="env.OTEL_COLLECTOR_TOKEN"
						testIdPrefix={`claude-code-telemetry-header-${i}`}
						keepClearedNames
						useSecretVarInput
						disabled={disabled}
					/>
				</div>
			))}
			<p className="text-muted-foreground text-xs">{t("claudeCode.collectorHeadersHelp")}</p>
			<Button
				type="button"
				variant="outline"
				size="sm"
				data-testid="claude-code-telemetry-add-btn"
				onClick={() =>
					onChange((prev) => ({ ...prev, destinations: [...prev.destinations, { id: newDestinationId(), endpoint: "", headers: {} }] }))
				}
				disabled={disabled}
			>
				<Plus className="h-4 w-4" />
				{t("claudeCode.addCollector")}
			</Button>

			<div className="space-y-1.5">
				<label htmlFor="claude-code-telemetry-drop" className="text-sm font-medium">
					{t("claudeCode.dropAttributesLabel")}
				</label>
				<Input
					id="claude-code-telemetry-drop"
					data-testid="claude-code-telemetry-drop-input"
					value={value.dropAttributes}
					// i18n-ignore: an attribute key example, not prose
					placeholder="prompt"
					onChange={(e) => update({ dropAttributes: e.target.value })}
					disabled={disabled}
				/>
				<p className="text-muted-foreground text-xs">
					<Trans t={t} i18nKey="claudeCode.dropAttributesHelp" components={{ code0: <code className="text-xs" /> }} />
				</p>
			</div>
		</div>
	);
}