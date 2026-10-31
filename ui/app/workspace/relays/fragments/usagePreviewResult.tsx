import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableRow } from "@/components/ui/table";
import type { Relay, RelayUnitsMeasure, UsagePreview } from "@/lib/types/relays";

/** A USD amount with up to six decimals and no trailing zeros: $0.016, $0.0015. */
export const usd = (v: number) => `$${Number(v.toFixed(6))}`;

const MEASURE: Record<RelayUnitsMeasure, (path: string) => string> = {
	value: (path) => `the number at ${path}`,
	chars: (path) => `the characters of ${path}`,
	body_bytes: () => "the size of the body in bytes",
	body_chars: () => "the characters of the body",
};

function Code({ children }: { children: React.ReactNode }) {
	return <code className="bg-muted rounded px-1 font-mono text-xs">{children}</code>;
}

/**
 * The outcome of a pricing test, laid out like an itemized bill: what was charged, each part of the
 * formula with the relay's settings that produced it, and the formula written out with those numbers.
 */
export function UsagePreviewResult({ result, endpoint }: { result: UsagePreview; endpoint: Relay }) {
	const route = result.route_index === null ? null : endpoint.routes?.[result.route_index];
	// The server notes a request it would refuse with 400 before relaying it; that is not an upstream failure.
	const refusal = result.notes
		.find((n) => n.startsWith("the relay refuses this request with 400"))
		?.replace(/^the relay refuses this request with 400: ?/, "");
	const billedUnits = result.units_billed ?? 0;
	const unitCharge = result.units !== null && result.cost_per_unit > 0 ? billedUnits * result.cost_per_unit : 0;
	const rounding = [
		result.units_block_size > 1 ? `per started block of ${result.units_block_size}` : "",
		result.units_minimum > 0 ? `at least ${result.units_minimum}` : "",
	]
		.filter(Boolean)
		.join(", ");
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
					<div className="text-muted-foreground text-xs">Charged</div>
					<div className="font-mono text-2xl font-semibold" data-testid="relay-preview-cost">
						{usd(result.cost)}
					</div>
				</div>
				<div className="flex flex-wrap items-center gap-2">
					<Badge variant="secondary" className="font-mono">
						{route
							? result.route_allowed
								? `${route.method} ${route.path}`
								: `${route.method} ${route.path} · disabled`
							: result.route_allowed
								? "No route · relay pricing"
								: "No route · refused"}
					</Badge>
					<Badge variant={result.counted ? "default" : "outline"}>{result.counted ? "Billable" : "Not charged"}</Badge>
					{result.streams && <Badge variant="outline">Streamed</Badge>}
				</div>
			</div>

			{!result.route_allowed ? (
				<p className="text-muted-foreground">
					{route
						? "This route is disabled, so the relay answers 404 and nothing is charged."
						: "No route matches and Relay Any Path is off, so the relay answers 404 and nothing is charged."}
				</p>
			) : !result.counted ? (
				<p className="text-muted-foreground">
					{refusal
						? `The relay refuses this request before it is sent (${refusal}), so nothing is relayed or charged.`
						: result.billable
							? "The upstream reply is not a success (2xx), so the call is not counted or charged."
							: "This route is not billable, so the call is relayed without being counted or charged."}
				</p>
			) : (
				<>
					<div className="overflow-hidden rounded-sm border">
						<Table className="table-fixed">
							<TableBody>
								<TableRow>
									<TableCell className="text-muted-foreground w-32 whitespace-nowrap">Fee per call</TableCell>
									<TableCell className="break-words whitespace-normal">
										{route?.usage?.cost_per_request !== undefined ? "From the route override" : "From the relay pricing"}
									</TableCell>
									<TableCell className="w-24 text-right font-mono whitespace-nowrap">{usd(result.cost_per_request)}</TableCell>
								</TableRow>
								{result.cost_per_unit > 0 && (
									<TableRow>
										<TableCell className="text-muted-foreground whitespace-nowrap">Units</TableCell>
										<TableCell className="break-words whitespace-normal">
											{result.units === null ? (
												<>
													Nothing found at <Code>{result.units_path || "the body"}</Code> in the {result.units_source} body, so no units are
													billed
												</>
											) : (
												<>
													{result.units} read from {MEASURE[result.units_measure](result.units_path)} in the {result.units_source} body
													{rounding && <>, billed {rounding}</>} → <span className="font-medium">{billedUnits} billed</span> ×{" "}
													{usd(result.cost_per_unit)}
												</>
											)}
										</TableCell>
										<TableCell className="w-24 text-right font-mono whitespace-nowrap">{usd(unitCharge)}</TableCell>
									</TableRow>
								)}
								{result.extracted_cost !== null && (
									<TableRow>
										<TableCell className="text-muted-foreground whitespace-nowrap">Reported cost</TableCell>
										<TableCell className="break-words whitespace-normal">Cost the upstream reported in its reply</TableCell>
										<TableCell className="w-24 text-right font-mono whitespace-nowrap">{usd(result.extracted_cost)}</TableCell>
									</TableRow>
								)}
								<TableRow>
									<TableCell className="font-medium">Total</TableCell>
									<TableCell />
									<TableCell className="w-24 text-right font-mono font-medium whitespace-nowrap">{usd(result.cost)}</TableCell>
								</TableRow>
							</TableBody>
						</Table>
					</div>
					<div className="bg-muted/50 rounded-sm px-3 py-2 font-mono text-xs" data-testid="relay-preview-formula">
						<span className="text-muted-foreground">
							fee + billed units × price per unit{result.extracted_cost !== null ? " + reported cost" : ""} ={" "}
						</span>
						{formula} = {usd(result.cost)}
					</div>
				</>
			)}

			{(result.tokens || result.model) && (
				<div className="text-muted-foreground flex flex-wrap gap-x-6 gap-y-1 text-xs">
					{result.tokens && (
						<span>
							Tokens {result.tokens.input} in, {result.tokens.output} out ({result.tokens.total} total), logged but not priced
						</span>
					)}
					{result.model && <span>Model {result.model}</span>}
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