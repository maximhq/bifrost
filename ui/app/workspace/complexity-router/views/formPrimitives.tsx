import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Info } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";

// A tap is neither a hover nor a keyboard focus, so a Radix tooltip never opens
// on a touch device. Coarse pointers get the same copy from a popover instead.
function useCoarsePointer() {
	const [isCoarse, setIsCoarse] = useState(false);
	useEffect(() => {
		const query = window.matchMedia("(pointer: coarse)");
		const sync = () => setIsCoarse(query.matches);
		sync();
		query.addEventListener("change", sync);
		return () => query.removeEventListener("change", sync);
	}, []);
	return isCoarse;
}

// InfoTip carries the explanation that used to sit under a field, so the page
// reads as a form rather than as documentation.
export function InfoTip({ label, children }: { label: string; children: ReactNode }) {
	const isCoarsePointer = useCoarsePointer();
	const trigger = (
		<button type="button" aria-label={label} className="text-muted-foreground/70 hover:text-foreground transition-colors">
			<Info className="size-3.5" />
		</button>
	);

	if (isCoarsePointer) {
		return (
			<Popover>
				<PopoverTrigger asChild>{trigger}</PopoverTrigger>
				<PopoverContent className="w-auto max-w-xs p-3 text-xs leading-relaxed">{children}</PopoverContent>
			</Popover>
		);
	}

	return (
		<Tooltip>
			<TooltipTrigger asChild>{trigger}</TooltipTrigger>
			<TooltipContent className="max-w-xs leading-relaxed">{children}</TooltipContent>
		</Tooltip>
	);
}

export function FieldLabel({ htmlFor, children, tooltip }: { htmlFor?: string; children: ReactNode; tooltip?: ReactNode }) {
	return (
		<div className="flex items-center gap-1.5">
			<Label htmlFor={htmlFor}>{children}</Label>
			{tooltip && <InfoTip label={`About ${typeof children === "string" ? children : "this field"}`}>{tooltip}</InfoTip>}
		</div>
	);
}

export function SectionHeading({ title, description, aside }: { title: string; description: string; aside?: ReactNode }) {
	return (
		<div className="flex flex-wrap items-start justify-between gap-2">
			<div className="space-y-1">
				<h2 className="text-sm font-semibold">{title}</h2>
				<p className="text-muted-foreground max-w-2xl text-xs leading-relaxed">{description}</p>
			</div>
			{aside}
		</div>
	);
}