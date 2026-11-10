import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Info } from "lucide-react";
import { useTranslation } from "react-i18next";

// The Max Tokens field label with a note that tool calls never count toward it.
export function TokenLimitLabel({ label, "data-testid": testId }: { label: string; "data-testid"?: string }) {
	const { t } = useTranslation("governance");
	return (
		<span className="inline-flex items-center gap-1.5">
			{label}
			<TooltipProvider>
				<Tooltip>
					<TooltipTrigger asChild>
						<span data-testid={testId} tabIndex={0} aria-label={t("requestLimits.tokenInfo")}>
							<Info className="text-muted-foreground h-3 w-3" />
						</span>
					</TooltipTrigger>
					<TooltipContent className="max-w-sm">
						<p>{t("requestLimits.tokenInfo")}</p>
					</TooltipContent>
				</Tooltip>
			</TooltipProvider>
		</span>
	);
}