import { Badge } from "@/components/ui/badge";
import type { EvaluatorType } from "@/lib/types/evals";
import { cn } from "@/lib/utils";
import { Braces, Globe, type LucideIcon, Sigma, Sparkles, UserRound } from "lucide-react";
import { useTranslation } from "react-i18next";

const TYPE_ICONS: Record<EvaluatorType, LucideIcon> = {
	ai: Sparkles,
	programmatic: Braces,
	statistical: Sigma,
	api: Globe,
	human: UserRound,
};

export function EvaluatorTypeIcon({ type, className }: { type: EvaluatorType; className?: string }) {
	const Icon = TYPE_ICONS[type] ?? Sparkles;
	return <Icon className={cn("size-3.5", className)} aria-hidden />;
}

export function EvaluatorTypeBadge({ type, className }: { type: EvaluatorType; className?: string }) {
	const { t } = useTranslation("config");
	return (
		<Badge variant="outline" className={cn("gap-1 font-normal whitespace-nowrap", className)}>
			<EvaluatorTypeIcon type={type} className="size-3" />
			{t(`evals.types.${type}`)}
		</Badge>
	);
}