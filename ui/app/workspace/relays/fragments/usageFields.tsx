import { FormControl, FormField, FormItem, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import {
	RelayUnitsMeasureLabels,
	RelayUnitsSourceLabels,
	type RelayUnitsMeasure,
	type RelayUnitsSource,
	type RelayUsageConfig,
} from "@/lib/types/relays";
import type { RelayUsageForm } from "@/lib/types/schemas";
import { describePricing, mergeUsage } from "@/lib/utils/relayForm";
import type { Control, FieldValues } from "react-hook-form";
import { DecimalInput } from "./decimalInput";
import { InfoLabel } from "./infoLabel";

const INHERIT = "__inherit__";

interface UsageFieldsProps {
	control: Control<FieldValues>;
	/** Form path of the usage mapping: "usage" on the endpoint, "usage" on a route form. */
	name: string;
	value: RelayUsageForm | undefined;
	/** Set on a route: the endpoint mapping its empty fields inherit, shown as placeholders. */
	inheritFrom?: RelayUsageConfig;
	/** The relay's ignore_upstream_cost: the formula line then leaves out the reported cost. */
	ignoreUpstreamCost?: boolean;
	disabled?: boolean;
	testIdPrefix: string;
}

/**
 * The pricing formula for one mapping: a flat fee per call plus a per-unit charge on a quantity read
 * from the request or response. On a route every empty field inherits the endpoint's value, and 0
 * waives it; the effective formula is shown underneath.
 */
export function UsageFields({ control, name, value, inheritFrom, ignoreUpstreamCost, disabled, testIdPrefix }: UsageFieldsProps) {
	const isRoute = inheritFrom !== undefined;
	const effective = mergeUsage(inheritFrom, value);
	const measure = effective?.units_measure ?? "value";
	const wholeBody = measure === "body_bytes" || measure === "body_chars";
	const placeholder = (field: keyof RelayUsageConfig, fallback: string) => {
		if (!isRoute) return fallback;
		const inherited = inheritFrom?.[field];
		return inherited !== undefined && inherited !== "" ? `${inherited} (relay default)` : "Inherits relay";
	};

	const numberInput = (field: keyof RelayUsageConfig, label: string, description: string, fallback: string) => (
		<FormField
			control={control}
			name={`${name}.${field}`}
			render={({ field: f }) => (
				<FormItem className="flex-1">
					<InfoLabel label={label} info={description} />
					<FormControl>
						<DecimalInput
							placeholder={placeholder(field, fallback)}
							disabled={disabled}
							data-testid={`${testIdPrefix}-${field.replaceAll("_", "-")}`}
							value={f.value as number | null | undefined}
							onChange={f.onChange}
						/>
					</FormControl>
					<FormMessage />
				</FormItem>
			)}
		/>
	);

	return (
		<div className="space-y-4">
			<div className="flex w-full flex-row items-start gap-4">
				{numberInput(
					"cost_per_request",
					"Fee per Call (USD)",
					isRoute ? "0 waives the relay fee on this route." : "Added to every counted call.",
					"0",
				)}
				{numberInput("cost_per_unit", "Price per Unit (USD)", "Charged per billed unit.", "0")}
			</div>
			<div className="flex w-full flex-row items-start gap-4">
				<FormField
					control={control}
					name={`${name}.units_source`}
					render={({ field }) => (
						<FormItem className="flex-1">
							<InfoLabel
								label="Units Read From"
								info="Whether the billed quantity comes from the request the caller sent or the response the upstream returned."
							/>
							<Select
								value={(field.value as string) ?? (isRoute ? INHERIT : "response")}
								onValueChange={(v) => field.onChange(v === INHERIT ? null : (v as RelayUnitsSource))}
								disabled={disabled}
							>
								<FormControl>
									<SelectTrigger className="w-full" data-testid={`${testIdPrefix}-units-source`}>
										<SelectValue />
									</SelectTrigger>
								</FormControl>
								<SelectContent>
									{isRoute && <SelectItem value={INHERIT}>Inherit from relay</SelectItem>}
									{(Object.keys(RelayUnitsSourceLabels) as RelayUnitsSource[]).map((k) => (
										<SelectItem key={k} value={k}>
											{RelayUnitsSourceLabels[k]}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name={`${name}.units_measure`}
					render={({ field }) => (
						<FormItem className="flex-1">
							<InfoLabel
								label="Measure"
								info="Number at path reads a number; Characters counts the text at the path; Body size uses the whole body in bytes or characters."
							/>
							<Select
								value={(field.value as string) ?? (isRoute ? INHERIT : "value")}
								onValueChange={(v) => field.onChange(v === INHERIT ? null : (v as RelayUnitsMeasure))}
								disabled={disabled}
							>
								<FormControl>
									<SelectTrigger className="w-full" data-testid={`${testIdPrefix}-units-measure`}>
										<SelectValue />
									</SelectTrigger>
								</FormControl>
								<SelectContent>
									{isRoute && <SelectItem value={INHERIT}>Inherit from relay</SelectItem>}
									{(Object.keys(RelayUnitsMeasureLabels) as RelayUnitsMeasure[]).map((k) => (
										<SelectItem key={k} value={k}>
											{RelayUnitsMeasureLabels[k]}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							<FormMessage />
						</FormItem>
					)}
				/>
			</div>
			{!wholeBody && (
				<FormField
					control={control}
					name={`${name}.units_path`}
					render={({ field }) => (
						<FormItem>
							<InfoLabel label="Units Path" info="A gjson path into the JSON body, such as usage.credits. Arrays are summed." />
							<FormControl>
								<Input
									placeholder={placeholder("units_path", "usage.credits")}
									disabled={disabled}
									data-testid={`${testIdPrefix}-units-path`}
									value={(field.value as string) ?? ""}
									onChange={(e) => field.onChange(e.target.value)}
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			)}
			<div className="flex w-full flex-row items-start gap-4">
				{numberInput("units_block_size", "Block Size (Optional)", "Bill per started block of this many units.", "1")}
				{numberInput("units_minimum", "Minimum Units (Optional)", "Least units billed once a quantity is read.", "0")}
			</div>
			<p className="text-muted-foreground text-sm" data-testid={`${testIdPrefix}-formula`}>
				Each counted call costs <span className="text-foreground font-medium">{describePricing(effective, ignoreUpstreamCost)}</span>
			</p>
		</div>
	);
}