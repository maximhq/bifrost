import { FormLabel } from "@/components/ui/form";
import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Info } from "lucide-react";
import type { ReactNode } from "react";

/** A field label with the info tooltip the provider key forms use, so help text stays out of the layout. */
export function InfoLabel({ label, info, inForm = true }: { label: ReactNode; info?: ReactNode; inForm?: boolean }) {
	const Text = inForm ? FormLabel : Label;
	return (
		<div className="flex items-center gap-2">
			<Text>{label}</Text>
			{info && (
				<TooltipProvider>
					<Tooltip>
						<TooltipTrigger asChild>
							<span>
								<Info className="text-muted-foreground h-3 w-3" />
							</span>
						</TooltipTrigger>
						<TooltipContent className="max-w-sm">{info}</TooltipContent>
					</Tooltip>
				</TooltipProvider>
			)}
		</div>
	);
}