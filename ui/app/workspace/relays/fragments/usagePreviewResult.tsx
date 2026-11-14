import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableRow } from "@/components/ui/table";
import type { Relay, RelayUnitsMeasure, UsagePreview } from "@/lib/types/relays";
import type { TFunction } from "i18next";
import { Trans, useTranslation } from "react-i18next";

/** A USD amount with up to six decimals and no trailing zeros: $0.016, $0.0015. */
export const usd = (v: number) => `$${Number(v.toFixed(6))}`;

const describeMeasure = (t: TFunction<"config">, measure: RelayUnitsMeasure, path: string) => {
	switch (measure) {
		case "value":
			return t("relays.preview.measure.value", { path });
		case "chars":
			return t("relays.preview.measure.chars", { path });
		case "body_bytes":
			return t("relays.preview.measure.bodyBytes");
		case "body_chars":
			return t("relays.preview.measure.bodyChars");
	}
};

function Code({ children }: { children?: React.ReactNode }) {
	return <code className="bg-muted rounded px-1 font-mono text-xs">{children}</code>;
}

/**
 * The outcome of a pricing test, laid out like an itemized bill: what was charged, each part of the
 * formula with the relay's settings that produced it, and the formula written out with those numbers.
 */
export function UsagePreviewResult({ result, endpoint }: { result: UsagePreview; endpoint: Relay }) {
	const { t } = useTranslation("config");

	const route = result.route_index === null ? null : endpoint.routes?.[result.route_index];
	// The server notes a request it would refuse with 400 before relaying it; that is not an upstream failure.
	const refusal = result.notes
		.find((n) => n.startsWith("the relay refuses this request with 400"))
		?.replace(/^the relay refuses this request with 400: ?/, "");
	const billedUnits = result.units_billed ?? 0;
	const unitCharge = result.units !== null && result.cost_per_unit > 0 ? billedUnits * result.cost_per_unit : 0;
	const rounding = [
		result.units_block_size > 1 ? t("relays.preview.perBlock", { size: result.units_block_size }) : "",
		result.units_minimum > 0 ? t("relays.preview.atLeast", { min: result.units_minimum }) : "",
	]
		.filter(Boolean)
		.join(", ");
	const unitsSource = result.units_source === "request" ? t("relays.preview.sourceRequest") : t("relays.preview.sourceResponse");
	const formula = [
		usd(result.cost_per_request),
		result.cost_per_unit > 0 ? `${billedUnits} × ${usd(result.cost_per_unit)}` : "",
		result.extracted_cost !== null ? usd(result.extracted_cost) : "",
	]
		.filter(Boolean)
		.join(" + ");

	return (
		<div className="space-y-4 rounded-sm border p-4 text-sm" data-testid="relay-preview-result">
			<div className="flex flex-wrap items-start justify-between gap-3">
				<div>
					<div className="text-muted-foreground text-xs">{t("relays.preview.charged")}</div>
					<div className="font-mono text-2xl font-semibold" data-testid="relay-preview-cost">
						{usd(result.cost)}
					</div>
				</div>
				<div className="flex flex-wrap items-center gap-2">
					<Badge variant="secondary" className="font-mono">
						{route
							? result.route_allowed
								? `${route.method} ${route.path}`
								: t("relays.preview.routeDisabled", { method: route.method, path: route.path })
							: result.route_allowed
								? t("relays.preview.noRouteRelayPricing")
								: t("relays.preview.noRouteRefused")}
					</Badge>
					<Badge variant={result.counted ? "default" : "outline"}>{result.counted ? t("relays.preview.billable") : t("relays.preview.notCharged")}</Badge>
					{result.streams && <Badge variant="outline">{t("relays.preview.streamed")}</Badge>}
				</div>
			</div>

			{!result.route_allowed ? (
				<p className="text-muted-foreground">
					{route
						? t("relays.preview.disabledRoute")
						: t("relays.preview.noRouteMatch")}
				</p>
			) : !result.counted ? (
				<p className="text-muted-foreground">
					{refusal
						? t("relays.preview.refused", { reason: refusal })
						: result.billable
							? t("relays.preview.upstreamFailed")
							: t("relays.preview.notBillable")}
				</p>
			) : (
				<>
					<div className="overflow-hidden rounded-sm border">
						<Table className="table-fixed">
							<TableBody>
								<TableRow>
									<TableCell className="text-muted-foreground w-32 whitespace-nowrap">{t("relays.preview.feePerCall")}</TableCell>
									<TableCell className="break-words whitespace-normal">
										{route?.usage?.cost_per_request !== undefined ? t("relays.preview.fromRoute") : t("relays.preview.fromRelay")}
									</TableCell>
									<TableCell className="w-24 text-right font-mono whitespace-nowrap">{usd(result.cost_per_request)}</TableCell>
								</TableRow>
								{result.cost_per_unit > 0 && (
									<TableRow>
										<TableCell className="text-muted-foreground whitespace-nowrap">{t("relays.preview.units")}</TableCell>
										<TableCell className="break-words whitespace-normal">
											{result.units === null ? (
												<Trans
																						t={t}
																						i18nKey="relays.preview.unitsNone"
																						values={{ path: result.units_path || t("relays.preview.theBody"), source: unitsSource }}
																						components={{ code: <Code /> }}
																					/>
											) : (
												<Trans
																						t={t}
																						i18nKey="relays.preview.unitsRead"
																						values={{
																							units: result.units,
																							measure: describeMeasure(t, result.units_measure, result.units_path),
																							source: unitsSource,
																							rounding: rounding ? t("relays.preview.billedRounding", { rounding }) : "",
																							billed: billedUnits,
																							price: usd(result.cost_per_unit),
																						}}
																						components={{ strong: <span className="font-medium" /> }}
																					/>
											)}
										</TableCell>
										<TableCell className="w-24 text-right font-mono whitespace-nowrap">{usd(unitCharge)}</TableCell>
									</TableRow>
								)}
								{result.extracted_cost !== null && (
									<TableRow>
										<TableCell className="text-muted-foreground whitespace-nowrap">{t("relays.preview.reportedCost")}</TableCell>
										<TableCell className="break-words whitespace-normal">{t("relays.preview.reportedCostInfo")}</TableCell>
										<TableCell className="w-24 text-right font-mono whitespace-nowrap">{usd(result.extracted_cost)}</TableCell>
									</TableRow>
								)}
								<TableRow>
									<TableCell className="font-medium">{t("relays.preview.total")}</TableCell>
									<TableCell />
									<TableCell className="w-24 text-right font-mono font-medium whitespace-nowrap">{usd(result.cost)}</TableCell>
								</TableRow>
							</TableBody>
						</Table>
					</div>
					<div className="bg-muted/50 rounded-sm px-3 py-2 font-mono text-xs" data-testid="relay-preview-formula">
						<span className="text-muted-foreground">
							{t("relays.preview.formulaLabel")}{result.extracted_cost !== null ? t("relays.preview.formulaReported") : ""} ={" "}
						</span>
						{formula} = {usd(result.cost)}
					</div>
				</>
			)}

			{(result.tokens || result.model) && (
				<div className="text-muted-foreground flex flex-wrap gap-x-6 gap-y-1 text-xs">
					{result.tokens && (
						<span>
							{t("relays.preview.tokens", { input: result.tokens.input, output: result.tokens.output, total: result.tokens.total })}
						</span>
					)}
					{result.model && <span>{t("relays.preview.model", { model: result.model })}</span>}
				</div>
			)}
			{result.notes.length > 0 && (
				<ul className="text-muted-foreground list-disc space-y-0.5 pl-5 text-xs">
					{result.notes.map((n) => (
						<li key={n}>{n}</li>
					))}
				</ul>
			)}
		</div>
	);
}