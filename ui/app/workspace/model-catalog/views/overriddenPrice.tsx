import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { formatTokenPriceCompact, formatTokenPriceFull } from "@/lib/utils/numbers";

interface OverriddenPriceProps {
	/** Catalog price from governance_model_pricing. */
	base?: number;
	/** Post-override price. Undefined means this field is not overridden. */
	override?: number;
	variant: "compact" | "full";
	/** Name of the override that produced `override`, shown in the tooltip. */
	overrideName?: string;
	testId?: string;
}

/**
 * Renders a catalog price, striking it through and showing the effective price
 * beside it when a pricing override changes that field. With no override it
 * renders exactly what the plain formatter would, so unaffected rows are
 * visually unchanged.
 */
export default function OverriddenPrice({ base, override, variant, overrideName, testId }: OverriddenPriceProps) {
	const format = variant === "compact" ? formatTokenPriceCompact : formatTokenPriceFull;

	if (override === undefined || override === null) {
		return <span data-testid={testId}>{format(base)}</span>;
	}

	return (
		<TooltipProvider>
			<Tooltip>
				<TooltipTrigger asChild>
					{/* Focusable so keyboard users can reach the tooltip naming the override. */}
					<span
						tabIndex={0}
						className="focus-visible:ring-ring/50 inline-flex flex-wrap items-baseline justify-end gap-x-1.5 rounded-sm focus-visible:ring-[3px] focus-visible:outline-none"
						data-testid={testId}
					>
						{/* The strikethrough is visual only, so name each value for screen readers. */}
						<span className="text-muted-foreground text-xs line-through" data-testid={testId ? `${testId}-original` : undefined}>
							<span className="sr-only">Original price: </span>
							{format(base)}
						</span>
						<span data-testid={testId ? `${testId}-override` : undefined}>
							<span className="sr-only">Effective price: </span>
							{format(override)}
						</span>
					</span>
				</TooltipTrigger>
				<TooltipContent>{overrideName ? `Overridden by "${overrideName}"` : "Overridden by a custom pricing override"}</TooltipContent>
			</Tooltip>
		</TooltipProvider>
	);
}