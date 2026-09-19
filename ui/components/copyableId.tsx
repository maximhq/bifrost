import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { cn } from "@/lib/utils";
import { Check, Copy } from "lucide-react";
import { useTranslation } from "react-i18next";

interface CopyableIdProps {
	id: string | number;
	/** Entity name used in the toast, tooltip, and aria-label, e.g. "Team" -> "Copy team ID". */
	entityLabel?: string;
	className?: string;
	testId?: string;
}

const ENTITY_NOUN_KEYS: Record<string, string> = {
	Team: "entities.team",
	Customer: "entities.customer",
	"Virtual key": "entities.virtualKey",
	Video: "entities.video",
};

/**
 * Icon-only click-to-copy control for an entity id, sized to sit inline beside a
 * sheet title. The full id lives in the tooltip so it never competes with the name.
 */
export function CopyableId({ id, entityLabel, className, testId }: CopyableIdProps) {
	const { t } = useTranslation("common");
	const value = String(id ?? "");
	const nounKey = entityLabel ? ENTITY_NOUN_KEYS[entityLabel] : undefined;
	const entityNoun = nounKey ? t(nounKey) : (entityLabel ?? "");
	const { copy, copied } = useCopyToClipboard({
		successMessage: entityNoun ? t("idCopied", { entity: entityNoun }) : t("idCopiedGeneric"),
	});

	if (!value) return null;

	return (
		<Tooltip delayDuration={0}>
			<TooltipTrigger asChild>
				<button
					type="button"
					onClick={(e) => {
						e.stopPropagation();
						copy(value);
					}}
					aria-label={entityNoun ? t("copyId", { entity: entityNoun }) : t("copyIdGeneric")}
					data-testid={testId}
					className={cn(
						"text-muted-foreground hover:text-foreground hover:bg-muted inline-flex size-6 shrink-0 cursor-pointer items-center justify-center rounded transition-colors",
						className,
					)}
				>
					{copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
				</button>
			</TooltipTrigger>
			<TooltipContent className="flex flex-col items-start gap-0.5 px-2 py-1">
				<span className="text-xs">{entityNoun ? t("copyId", { entity: entityNoun }) : t("copyIdGeneric")}</span>
				<span className="font-mono text-xs opacity-80">{value}</span>
			</TooltipContent>
		</Tooltip>
	);
}
