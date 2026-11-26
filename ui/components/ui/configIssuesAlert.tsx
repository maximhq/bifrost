import { AlertTriangle } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

// Config issues arrive as one newline-separated string: each line is one part of a config.json
// declaration that could not be applied.
function splitConfigIssues(issues?: string | null): string[] {
	return (issues ?? "")
		.split("\n")
		.map((line) => line.trim())
		.filter(Boolean);
}

interface ConfigIssuesProps {
	issues?: string | null;
	className?: string;
}

// Lists what config.json reconciliation left out of an entity. Renders nothing when there are no issues.
export function ConfigIssuesAlert({ issues, className }: ConfigIssuesProps) {
	const { t } = useTranslation("common");
	const lines = splitConfigIssues(issues);
	if (lines.length === 0) return null;
	return (
		<Alert variant="warning" className={className} data-testid="config-issues-alert">
			<AlertTriangle className="h-4 w-4" />
			<AlertTitle>{t("configIssuesAlert.title")}</AlertTitle>
			<AlertDescription>
				<ul className="list-disc space-y-1 pl-4">
					{lines.map((line) => (
						<li key={line}>{line}</li>
					))}
				</ul>
				<p className="mt-2">{t("configIssuesAlert.hint")}</p>
			</AlertDescription>
		</Alert>
	);
}

// Compact table-row marker for an entity with config issues; the tooltip lists them.
export function ConfigIssuesBadge({ issues, className }: ConfigIssuesProps) {
	const { t } = useTranslation("common");
	const lines = splitConfigIssues(issues);
	if (lines.length === 0) return null;
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Badge
					tabIndex={0}
					variant="outline"
					className={`cursor-help gap-1 border-amber-500/50 font-normal text-amber-700 dark:text-amber-400 ${className ?? ""}`}
					data-testid="config-issues-badge"
				>
					<AlertTriangle className="size-3" />
					{t("configIssuesAlert.badge")}
				</Badge>
			</TooltipTrigger>
			<TooltipContent className="max-w-sm">
				<ul className="list-disc space-y-1 pl-4">
					{lines.map((line) => (
						<li key={line}>{line}</li>
					))}
				</ul>
			</TooltipContent>
		</Tooltip>
	);
}