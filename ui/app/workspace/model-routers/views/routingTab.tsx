import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { getErrorMessage } from "@/lib/store";
import { useSetModelRouterRoutingMutation } from "@/lib/store/apis/governanceApi";
import { useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import { ModelRouter, ModelRouterRouting } from "@/lib/types/complexityRouter";
import { RoutingRuleScope } from "@/lib/types/routingRules";
import { validateRateLimitAndBudgetRules, validateRoutingRules } from "@/lib/utils/celConverterRouting";
import { getScopeLabel } from "@/lib/utils/labels";
import { normalizeRoutingRuleGroupQuery } from "@/lib/utils/routingRuleGroupQuery";
import { getPriorityBadgeClass, truncateCELExpression } from "@/lib/utils/routingRules";
import { Info, Pencil } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { RuleGroupType } from "react-querybuilder";
import { toast } from "sonner";
import { ConditionBuilder, type ConditionMode, initialConditionMode, ScopeEntityField, ScopeSelect, scopeNoun } from "./formParts";

interface RoutingTabProps {
	router: ModelRouter;
	canUpdate: boolean;
}

// RoutingTab shows which requests the router takes as one row, laid out like a
// routing rule's in the routing rules table: its scope, its priority among the
// routers there and its condition. The row opens RoutingSheet to edit it, as a
// routing rule's row opens its sheet. The router is turned on and off from the
// routers list.
export function RoutingTab({ router, canUpdate }: RoutingTabProps) {
	const { t } = useTranslation("models");
	const { t: trt } = useTranslation("models", { keyPrefix: "routing.modelRouters.routing" });
	const [editing, setEditing] = useState(false);
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
							<TableHead className="w-[56px]" />
						</TableRow>
					</TableHeader>
					<TableBody>
						<TableRow
							className={canUpdate ? "hover:bg-muted/50 cursor-pointer transition-colors" : undefined}
							onClick={() => canUpdate && setEditing(true)}
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
							<TableCell className="text-right" onClick={(event) => event.stopPropagation()}>
								{canUpdate && (
									<Button
										type="button"
										variant="ghost"
										size="icon"
										className="h-8 w-8"
										aria-label={trt("sheetTitle")}
										onClick={() => setEditing(true)}
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

			{/* Mounted only while open, so each opening starts from the saved routing. */}
			{editing && <RoutingSheet router={router} onClose={() => setEditing(false)} />}
		</div>
	);
}

// RoutingDraft is the routing being edited, with the priority as typed.
interface RoutingDraft {
	scope: string;
	scopeId: string;
	priority: string;
	celExpression: string;
	query: RuleGroupType;
}

const toDraft = (routing: ModelRouterRouting): RoutingDraft => ({
	scope: routing.scope,
	scopeId: routing.scope_id ?? "",
	priority: String(routing.priority),
	celExpression: routing.cel_expression ?? "",
	query: normalizeRoutingRuleGroupQuery(routing.query),
});

// RoutingSheet edits a router's scope, priority and condition, laid out like
// the routing rule sheet. Whether the router is on is switched from the row,
// so a save sends the saved on/off state back unchanged. It saves on its own
// endpoint, apart from the classifier and the models.
function RoutingSheet({ router, onClose }: { router: ModelRouter; onClose: () => void }) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const { t: trt } = useTranslation("models", { keyPrefix: "routing.modelRouters.routing" });
	const [setRouting, { isLoading: isSaving }] = useSetModelRouterRoutingMutation();
	const { data: providersData = [] } = useGetProvidersQuery();
	const availableProviders = useMemo(() => providersData.map((provider) => provider.name), [providersData]);

	const [draft, setDraft] = useState<RoutingDraft>(() => toDraft(router.routing));
	const [conditionMode, setConditionMode] = useState<ConditionMode>(() => initialConditionMode(router.routing));
	const [scopeError, setScopeError] = useState<string | null>(null);
	const [priorityError, setPriorityError] = useState<string | null>(null);
	const [celError, setCelError] = useState<string | null>(null);

	const handleQueryChange = useCallback((expression: string, query: RuleGroupType) => {
		setDraft((current) => ({ ...current, celExpression: expression, query }));
		setCelError(null);
	}, []);

	const save = (event: React.FormEvent) => {
		event.preventDefault();
		const priority = Number(draft.priority);
		const missingScope = draft.scope !== RoutingRuleScope.Global && !draft.scopeId.trim();
		setScopeError(missingScope ? t("routing.scopeRequired", { scope: scopeNoun(draft.scope, t) }) : null);
		const badPriority = draft.priority.trim() === "" || !Number.isInteger(priority) || priority < 0 || priority > 1000;
		setPriorityError(badPriority ? trt("priorityInvalid") : null);
		if (missingScope || badPriority) return;
		// Builder-only checks: they read the visual query, which CEL mode does
		// not have. A typed CEL expression is checked by the gateway on save. The
		// CEL field's error only shows in CEL mode, so these are toasts, as in the
		// routing rule sheet.
		if (conditionMode === "builder") {
			const regexErrors = validateRoutingRules(draft.query);
			if (regexErrors.length > 0) {
				toast.error(t("routing.invalidRegex", { errors: regexErrors.join("\n") }));
				return;
			}
			const ruleErrors = validateRateLimitAndBudgetRules(draft.query);
			if (ruleErrors.length > 0) {
				toast.error(t("routing.invalidRuleConfig", { errors: ruleErrors.join("\n") }));
				return;
			}
		}
		// The switch owns enabled, so the sheet leaves it out rather than send
		// back a value read when it opened.
		const routing: Omit<ModelRouterRouting, "enabled"> = {
			scope: draft.scope as ModelRouterRouting["scope"],
			scope_id: draft.scope === RoutingRuleScope.Global ? undefined : draft.scopeId.trim(),
			cel_expression: draft.celExpression.trim(),
			query: conditionMode === "builder" ? draft.query : undefined,
			priority,
		};
		setRouting({ id: router.id, routing })
			.unwrap()
			.then(() => {
				toast.success(trt("saved"), { position: "top-right" });
				onClose();
			})
			.catch((err) => {
				const message = getErrorMessage(err);
				// A malformed CEL expression is shown under the CEL editor, only in
				// CEL mode, as the routing rule sheet does; a taken priority under
				// the priority field; anything else is a toast.
				if (conditionMode === "cel" && /cel expression/i.test(message)) setCelError(message);
				else if (/priority/i.test(message)) setPriorityError(message);
				else toast.error(trt("saveFailed", { error: message }));
			});
	};

	return (
		<Sheet open onOpenChange={(open) => !open && onClose()}>
			<SheetContent className="flex w-full min-w-1/2 flex-col gap-4 overflow-x-hidden p-0 pt-4" data-testid="model-router-routing-sheet">
				<SheetHeader className="flex flex-col items-start py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>{trt("sheetTitle")}</SheetTitle>
					<SheetDescription>{trt("description")}</SheetDescription>
				</SheetHeader>

				<form onSubmit={save} className="flex grow flex-col" noValidate>
					<div className="flex grow flex-col gap-6 px-4 pb-6 md:px-8">
						<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
							<ScopeSelect
								id="model-router-routing-scope"
								value={draft.scope}
								onChange={(scope) => {
									setDraft((current) => ({ ...current, scope, scopeId: "" }));
									setScopeError(null);
								}}
								testId="model-router-routing-scope-select"
							/>
							<div className="space-y-3">
								<Label htmlFor="model-router-routing-priority">{t("routing.priority")}</Label>
								<Input
									id="model-router-routing-priority"
									type="number"
									min={0}
									max={1000}
									value={draft.priority}
									onChange={(event) => {
										setDraft((current) => ({ ...current, priority: event.target.value }));
										setPriorityError(null);
									}}
									data-testid="model-router-routing-priority-input"
								/>
								<p className="text-muted-foreground text-xs">{trt("priorityHelp")}</p>
								{priorityError && (
									<p className="text-destructive text-sm" data-testid="model-router-routing-priority-error">
										{priorityError}
									</p>
								)}
							</div>
						</div>

						{draft.scope !== RoutingRuleScope.Global && (
							<ScopeEntityField
								scope={draft.scope}
								value={draft.scopeId}
								onChange={(scopeId) => {
									setDraft((current) => ({ ...current, scopeId }));
									setScopeError(null);
								}}
								error={scopeError ?? undefined}
								userInputTestId="model-router-routing-scope-user-input"
							/>
						)}

						<Separator />

						<div className="space-y-3">
							<Label>{trt("conditionLabel")}</Label>
							<p className="text-muted-foreground text-sm">{trt("conditionHelp")}</p>
							<ConditionBuilder
								initialQuery={draft.query}
								onChange={handleQueryChange}
								providers={availableProviders}
								models={[]}
								allowCustomModels
								allowCelMode
								initialMode={conditionMode}
								initialCel={router.routing.cel_expression ?? ""}
								onModeChange={(mode) => {
									setConditionMode(mode);
									setCelError(null);
								}}
								celError={celError}
							/>
						</div>
					</div>

					<div className="bg-card sticky bottom-0 flex justify-end gap-2 border-t px-4 py-3 md:px-8">
						<Button type="button" variant="outline" onClick={onClose} disabled={isSaving} data-testid="model-router-routing-cancel-button">
							{tc("cancel")}
						</Button>
						<Button type="submit" disabled={isSaving} data-testid="model-router-routing-save-button">
							{tc("save")}
						</Button>
					</div>
				</form>
			</SheetContent>
		</Sheet>
	);
}