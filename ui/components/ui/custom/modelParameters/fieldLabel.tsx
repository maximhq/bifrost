import { HelpCircle, X } from "lucide-react";
import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Button } from "@/components/ui/button";
import type { ReactNode } from "react";

interface FieldLabelProps {
	label: string;
	helpText?: string;
	htmlFor?: string;
	onClear?: () => void;
	/** Extra content rendered after the label+help group (e.g. NumberInput) */
	children?: ReactNode;
}

export default function FieldLabel({ label, helpText, htmlFor, onClear, children }: FieldLabelProps) {
	return (
		<div className="group/label flex flex-row items-center overflow-hidden">
			<div className="flex h-4 grow flex-row items-center gap-1 pr-1">
				<Label htmlFor={htmlFor} className="truncate">
					{label}
				</Label>
				{helpText && (
					<TooltipProvider delayDuration={200}>
						<Tooltip>
							<TooltipTrigger>
								<HelpCircle className="text-content-disabled h-3.5 w-3.5" />
							</TooltipTrigger>
							<TooltipContent className="max-w-xs">{helpText}</TooltipContent>
						</Tooltip>
					</TooltipProvider>
				)}
				{onClear && (
					<Button
						variant="ghost"
						size="icon"
						onClick={onClear}
						className="text-muted-foreground hover:text-foreground h-4 w-4 opacity-0 transition-opacity group-hover/label:opacity-100"
						title={`Clear ${label}`}
					>
						<X className="h-4 w-4" />
					</Button>
				)}
			</div>
			{children}
		</div>
	);
}