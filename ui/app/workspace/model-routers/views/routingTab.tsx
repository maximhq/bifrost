import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ModelRouter } from "@/lib/types/complexityRouter";
import { getScopeLabel } from "@/lib/utils/labels";
import { getPriorityBadgeClass, truncateCELExpression } from "@/lib/utils/routingRules";
import { Info, Pencil } from "lucide-react";
import { useTranslation } from "react-i18next";
import { RouterEnabledSwitch } from "./routersTable";

interface RoutingTabProps {
	router: ModelRouter;
	canUpdate: boolean;
	// Opens the router sheet, where the routing is edited.
	onEdit: () => void;
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

	return (
		<div className="flex min-h-0 grow flex-col gap-4" data-testid="model-router-routing">
			<p className="text-muted-foreground text-sm">{trt("description")}</p>
			<Alert variant="info" data-testid="model-router-routing-order-notice">
				<Info className="h-4 w-4" />
				<AlertDescription>{trt("orderNote")}</AlertDescription>
			</Alert>

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