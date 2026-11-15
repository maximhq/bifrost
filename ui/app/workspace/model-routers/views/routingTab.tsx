import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { getLocale } from "@/lib/i18n";
import { useGetRoutingRulesQuery } from "@/lib/store/apis/routingRulesApi";
import { ModelRouter } from "@/lib/types/complexityRouter";
import type { RoutingRule } from "@/lib/types/routingRules";
import { getScopeLabel } from "@/lib/utils/labels";
import { getPriorityBadgeClass, truncateCELExpression } from "@/lib/utils/routingRules";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { ArrowRight, Info, Pencil, TriangleAlert } from "lucide-react";
import { useMemo } from "react";
import { Trans, useTranslation } from "react-i18next";
import { RouterEnabledSwitch } from "./routersTable";
import { COMPLEXITY_RULES_SEARCH, complexityRulesFor, namedComplexityRules } from "./routingTab.utils";

const COMPLEXITY_RULES_DOCS_URL = "https://docs.getbifrost.ai/features/complexity-router";
// The most rules the list endpoint returns in one page.
const COMPLEXITY_RULES_LIMIT = 100;

interface RoutingTabProps {
	router: ModelRouter;
	canUpdate: boolean;
	// Opens the router sheet, where the routing is edited.
	onEdit: () => void;
}

// ComplexityRulesNotice warns that enabled routing rules still read
// complexity_tier. The Complexity Router classifies a request for such a rule,
// and this router classifies it again when it takes the request, so the same
// request is classified twice. It names the rules and recommends moving them
// to the router. The names are an element of their own, so a name is never
// parsed as part of the sentence's markup.
export function ComplexityRulesNotice({ rules }: { rules: RoutingRule[] }) {
	const { t } = useTranslation("models");
	const { named, more } = namedComplexityRules(rules);
	const listed = named.map((rule) => rule.name);
	if (more > 0) listed.push(t("routing.modelRouters.routing.complexityRules.more", { count: more }));
	const ruleNames = new Intl.ListFormat(getLocale(), { style: "long", type: "conjunction" }).format(listed);

	return (
		<Alert variant="warning" data-testid="model-router-routing-complexity-rules-notice">
			<TriangleAlert className="h-4 w-4" />
			<AlertTitle>{t("routing.modelRouters.routing.complexityRules.title")}</AlertTitle>
			<AlertDescription className="gap-2">
				<p>
					<Trans
						t={t}
						i18nKey={
							rules.length === 1
								? "routing.modelRouters.routing.complexityRules.bodySingle"
								: "routing.modelRouters.routing.complexityRules.bodyMany"
						}
						components={{
							rules: (
								<span
									className="font-semibold text-amber-900 dark:text-amber-100"
									data-testid="model-router-routing-complexity-rules-names"
								>
									{ruleNames}
								</span>
							),
							code: <code className="font-mono text-xs" />,
						}}
					/>{" "}
					<a
						href={COMPLEXITY_RULES_DOCS_URL}
						target="_blank"
						rel="noopener noreferrer"
						className="font-medium underline underline-offset-2"
						data-testid="model-router-routing-complexity-rules-docs-link"
					>
						{t("routing.modelRouters.routing.complexityRules.readMore")}
					</a>
				</p>
				<Button asChild variant="outline" size="sm" data-testid="model-router-routing-complexity-rules-link">
					<Link to="/workspace/routing-rules">
						{t("routing.modelRouters.routing.complexityRules.openRules")}
						<ArrowRight className="size-3.5" />
					</Link>
				</Button>
			</AlertDescription>
		</Alert>
	);
}

// RoutingTab shows which requests the router takes as one row, laid out like a
// routing rule's in the routing rules table: its scope, its priority among the
// routers there, its condition and whether it is on. The row opens the router
// sheet to edit it, as a routing rule's row opens its sheet.
export function RoutingTab({ router, canUpdate, onEdit }: RoutingTabProps) {
	const { t } = useTranslation("models");
	const { t: tmr } = useTranslation("models", { keyPrefix: "routing.modelRouters" });
	const { t: trt } = useTranslation("models", { keyPrefix: "routing.modelRouters.routing" });
	const { routing } = router;
	// Only the rules that mention complexity_tier are read, and afresh each
	// time the tab opens: a rule turned off on the routing rules page since
	// must not keep the warning up.
	const canViewRules = useRbac(RbacResource.RoutingRules, RbacOperation.View);
	const { data: rulesData } = useGetRoutingRulesQuery(
		{ search: COMPLEXITY_RULES_SEARCH, limit: COMPLEXITY_RULES_LIMIT },
		{ skip: !canViewRules, refetchOnMountOrArgChange: true },
	);
	const complexityRules = useMemo(() => complexityRulesFor(rulesData?.rules, routing), [rulesData?.rules, routing]);

	return (
		<div className="custom-scrollbar flex min-h-0 grow flex-col gap-4 overflow-y-auto" data-testid="model-router-routing">
			<p className="text-muted-foreground text-sm">{trt("description")}</p>
			<Alert variant="info" data-testid="model-router-routing-order-notice">
				<Info className="h-4 w-4" />
				<AlertDescription>{trt("orderNote")}</AlertDescription>
			</Alert>
			{complexityRules.length > 0 && <ComplexityRulesNotice rules={complexityRules} />}

			<div className="overflow-hidden rounded-sm border">
				{/* Scope and priority take the same width, left-aligned, so the columns step evenly; the condition takes the rest. */}
				<Table className="w-full table-fixed">
					<TableHeader className="bg-muted">
						<TableRow className="bg-muted/50">
							<TableHead className="w-[180px] font-semibold">{t("routing.scope")}</TableHead>
							<TableHead className="w-[180px] font-semibold">{t("routing.priority")}</TableHead>
							<TableHead className="font-semibold">{t("routing.expression")}</TableHead>
							<TableHead className="w-[80px] font-semibold">{tmr("columnActive")}</TableHead>
							<TableHead className="w-[56px]" />
						</TableRow>
					</TableHeader>
					<TableBody>
						<TableRow
							className={canUpdate ? "hover:bg-muted/50 cursor-pointer transition-colors" : undefined}
							onClick={() => canUpdate && onEdit()}
							data-testid="model-router-routing-row"
						>
							<TableCell>
								<Badge variant="secondary">{getScopeLabel(routing.scope)}</Badge>
							</TableCell>
							<TableCell>
								<div className={`inline-block rounded px-2.5 py-1 text-xs font-medium ${getPriorityBadgeClass()}`}>{routing.priority}</div>
							</TableCell>
							<TableCell>
								{routing.cel_expression ? (
									<span className="text-muted-foreground block max-w-md truncate font-mono text-xs" title={routing.cel_expression}>
										{truncateCELExpression(routing.cel_expression)}
									</span>
								) : (
									<span className="text-muted-foreground text-xs">{trt("everyRequest")}</span>
								)}
							</TableCell>
							<TableCell onClick={(event) => event.stopPropagation()}>
								<RouterEnabledSwitch router={router} disabled={!canUpdate} />
							</TableCell>
							<TableCell className="text-right" onClick={(event) => event.stopPropagation()}>
								{canUpdate && (
									<Button
										type="button"
										variant="ghost"
										size="icon"
										className="h-8 w-8"
										aria-label={tmr("sheet.title")}
										onClick={onEdit}
										data-testid="model-router-routing-edit-button"
									>
										<Pencil className="h-4 w-4" />
									</Button>
								)}
							</TableCell>
						</TableRow>
					</TableBody>
				</Table>
			</div>
		</div>
	);
}