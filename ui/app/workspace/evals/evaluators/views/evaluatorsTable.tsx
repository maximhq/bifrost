import PageTitle from "@/components/pageTitle";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useToast } from "@/hooks/use-toast";
import { getLocale } from "@/lib/i18n";
import { dateFnsLocale } from "@/lib/i18n/dateLocale";
import { getErrorMessage, useDeleteEvaluatorMutation, useDuplicateEvaluatorMutation } from "@/lib/store";
import type { EvalKey, EvaluatorSummary } from "@/lib/types/evals";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link, useNavigate } from "@tanstack/react-router";
import { formatDistanceToNow } from "date-fns";
import {
	ChevronDown,
	ChevronLeft,
	ChevronRight,
	Copy,
	KeyRound,
	LibraryBig,
	MoreHorizontal,
	Pencil,
	Plus,
	Search,
	Trash2,
} from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { EvaluatorTypeBadge, EvaluatorTypeIcon } from "./evaluatorTypeBadge";
import { CREATABLE_EVALUATOR_TYPES, lacksEvalKey, missingEvalKeyProviders } from "./evaluatorUtils";
import { EvaluatorsEmptyState } from "./evaluatorsEmptyState";
import { PassCriterionText } from "./passCriterionText";

interface EvaluatorsTableProps {
	evaluators: EvaluatorSummary[];
	totalCount: number;
	evalKeys: EvalKey[];
	// False until the eval keys have loaded, so nothing is flagged on a guess.
	evalKeysKnown: boolean;
	pageLoading: boolean;
	search: string;
	onSearchChange: (value: string) => void;
	filtersActive: boolean;
	offset: number;
	limit: number;
	onOffsetChange: (offset: number) => void;
}

export default function EvaluatorsTable({
	evaluators,
	totalCount,
	evalKeys,
	evalKeysKnown,
	pageLoading,
	search,
	onSearchChange,
	filtersActive,
	offset,
	limit,
	onOffsetChange,
}: EvaluatorsTableProps) {
	const { t, i18n } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { t: tMcp } = useTranslation("mcp");
	const { toast } = useToast();
	const navigate = useNavigate();
	const canCreate = useRbac(RbacResource.Evaluators, RbacOperation.Create);
	const canUpdate = useRbac(RbacResource.Evaluators, RbacOperation.Update);
	const canDelete = useRbac(RbacResource.Evaluators, RbacOperation.Delete);
	const [deleteEvaluator, { isLoading: isDeleting }] = useDeleteEvaluatorMutation();
	const [duplicateEvaluator] = useDuplicateEvaluatorMutation();
	const [pendingDelete, setPendingDelete] = useState<EvaluatorSummary | null>(null);

	const pageTitle = <PageTitle title={t("evals.list.title")}>{t("evals.list.description")}</PageTitle>;

	if (totalCount === 0 && !filtersActive && !search) {
		return (
			<>
				{pageTitle}
				<EvaluatorsEmptyState canCreate={canCreate} />
			</>
		);
	}

	const blocked = evalKeysKnown ? missingEvalKeyProviders(evaluators, evalKeys) : [];

	const openEditor = (id: string) => void navigate({ to: "/workspace/evals/evaluators/editor", search: { id } });

	const handleDuplicate = async (evaluator: EvaluatorSummary) => {
		try {
			const copy = await duplicateEvaluator({ id: evaluator.id }).unwrap();
			toast({ title: t("evals.list.duplicated", { name: copy.evaluator.name }) });
		} catch (error) {
			toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	const handleDelete = async () => {
		if (!pendingDelete) return;
		try {
			await deleteEvaluator(pendingDelete.id).unwrap();
			toast({ title: t("evals.list.deleted", { name: pendingDelete.name }) });
			setPendingDelete(null);
		} catch (error) {
			toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	return (
		<>
			<div className="mb-4 flex flex-wrap items-center gap-3">
				{pageTitle}
				<div className="relative max-w-sm flex-1">
					<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
					<Input
						aria-label={t("evals.list.searchPlaceholder")}
						placeholder={t("evals.list.searchPlaceholder")}
						value={search}
						onChange={(e) => onSearchChange(e.target.value)}
						className="pl-9"
						data-testid="evaluators-search-input"
					/>
				</div>
				<div className="flex gap-2 sm:ml-auto">
					<Button asChild variant="outline" className="h-8" data-testid="evaluators-catalog-link">
						<Link to="/workspace/evals/evaluators/catalog" aria-label={t("evals.list.browseCatalog")}>
							<LibraryBig />
							<span className="hidden sm:inline">{t("evals.list.browseCatalog")}</span>
						</Link>
					</Button>
					<DropdownMenu>
						<DropdownMenuTrigger asChild>
							<Button
								className="h-8 gap-2"
								disabled={!canCreate}
								aria-label={t("evals.list.newEvaluator")}
								data-testid="evaluators-create-button"
							>
								<Plus />
								<span className="hidden sm:inline">{t("evals.list.newEvaluator")}</span>
								<ChevronDown className="size-3" />
							</Button>
						</DropdownMenuTrigger>
						<DropdownMenuContent align="end" className="w-72">
							{CREATABLE_EVALUATOR_TYPES.map((type) => (
								<DropdownMenuItem
									key={type}
									className="items-start gap-2.5"
									onSelect={() => void navigate({ to: "/workspace/evals/evaluators/editor", search: { type } })}
									data-testid={`evaluators-create-${type}`}
								>
									<EvaluatorTypeIcon type={type} className="mt-0.5 size-4 shrink-0" />
									<div className="flex flex-col">
										<span className="text-sm font-medium">{t(`evals.types.${type}`)}</span>
										<span className="text-muted-foreground text-xs">{t(`evals.typeHints.${type}`)}</span>
									</div>
								</DropdownMenuItem>
							))}
						</DropdownMenuContent>
					</DropdownMenu>
				</div>
			</div>

			{blocked.map(({ provider, count }) => (
				<Alert
					key={provider}
					variant="warning"
					className="mb-3 flex items-center gap-3"
					data-testid={`evaluators-no-eval-key-alert-${provider}`}
				>
					<KeyRound className="size-4 shrink-0" />
					<AlertDescription className="flex-1">{t("evals.list.noEvalKeyAlert", { provider, count })}</AlertDescription>
					<Button asChild variant="outline" size="sm" className="h-7 shrink-0">
						<Link to="/workspace/providers" search={{ provider }} data-testid={`evaluators-mark-key-link-${provider}`}>
							{t("evals.list.markKey")}
						</Link>
					</Button>
				</Alert>
			))}

			<div className="flex grow flex-col overflow-hidden">
				<div className="mb-2 grow overflow-hidden rounded-sm border">
					<Table data-testid="evaluators-table" containerClassName="h-full overflow-auto">
						<TableHeader className="bg-muted sticky top-0 z-20">
							<TableRow>
								<TableHead className="min-w-[260px] font-semibold">{t("evals.list.columns.name")}</TableHead>
								<TableHead className="w-[130px] font-semibold">{t("evals.list.columns.type")}</TableHead>
								<TableHead className="w-[200px] font-semibold">{t("evals.list.columns.reads")}</TableHead>
								<TableHead className="w-[150px] font-semibold">{t("evals.list.columns.passesWhen")}</TableHead>
								<TableHead className="w-[230px] font-semibold">{t("evals.list.columns.runsOn")}</TableHead>
								<TableHead className="w-[80px] font-semibold">{t("evals.list.columns.version")}</TableHead>
								<TableHead className="w-[140px] font-semibold">{t("evals.list.columns.updated")}</TableHead>
								<TableHead className="w-[60px] text-right font-semibold">
									<span className="sr-only">{tCommon("actions")}</span>
								</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody className={pageLoading ? "opacity-60" : undefined}>
							{evaluators.length === 0 && (
								<TableRow>
									<TableCell colSpan={8} className="text-muted-foreground h-24 text-center text-sm" data-testid="evaluators-no-results">
										{t("evals.list.noResults")}
									</TableCell>
								</TableRow>
							)}
							{evaluators.map((evaluator) => {
								const noKey = evalKeysKnown && lacksEvalKey(evaluator, evalKeys);
								return (
									<TableRow
										key={evaluator.id}
										className="hover:bg-muted/40 cursor-pointer"
										onClick={() => openEditor(evaluator.id)}
										data-testid={`evaluators-row-${evaluator.id}`}
									>
										<TableCell className="max-w-[360px]">
											<div className="flex items-center gap-2">
												<span className="truncate font-medium" data-testid={`evaluators-name-${evaluator.id}`}>
													{evaluator.name}
												</span>
												{evaluator.builtin_key && (
													<Badge variant="secondary" className="shrink-0 font-normal">
														{t("evals.sources.builtin")}
													</Badge>
												)}
											</div>
											{evaluator.description && <div className="text-muted-foreground truncate text-xs">{evaluator.description}</div>}
										</TableCell>
										<TableCell>
											<EvaluatorTypeBadge type={evaluator.type} />
										</TableCell>
										<TableCell className="text-muted-foreground max-w-[200px] truncate font-mono text-xs">
											{evaluator.variables.length > 0 ? evaluator.variables.join(", ") : "-"}
										</TableCell>
										<TableCell>
											<PassCriterionText criterion={evaluator.pass_criterion} />
										</TableCell>
										<TableCell>
											<div className="flex flex-wrap items-center gap-1.5">
												<RunsOn evaluator={evaluator} />
												{noKey && (
													<Badge variant="warning" className="shrink-0 font-normal" data-testid={`evaluators-no-eval-key-${evaluator.id}`}>
														{t("evals.list.noEvalKey")}
													</Badge>
												)}
											</div>
										</TableCell>
										<TableCell className="text-muted-foreground font-mono text-xs">
											{t("evals.list.versionShort", { version: evaluator.current_version })}
										</TableCell>
										<TableCell className="text-muted-foreground text-xs whitespace-nowrap">
											{formatDistanceToNow(new Date(evaluator.updated_at), { addSuffix: true, locale: dateFnsLocale(i18n.language) })}
										</TableCell>
										<TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
											<DropdownMenu>
												<DropdownMenuTrigger asChild>
													<Button
														variant="ghost"
														size="icon"
														className="size-7"
														aria-label={t("evals.list.actionsFor", { name: evaluator.name })}
														data-testid={`evaluators-actions-${evaluator.id}`}
													>
														<MoreHorizontal className="size-4" />
													</Button>
												</DropdownMenuTrigger>
												<DropdownMenuContent align="end">
													<DropdownMenuItem onSelect={() => openEditor(evaluator.id)} data-testid={`evaluators-edit-${evaluator.id}`}>
														<Pencil className="size-3.5" />
														{canUpdate ? tCommon("edit") : tMcp("common.open")}
													</DropdownMenuItem>
													<DropdownMenuItem
														disabled={!canCreate}
														onSelect={() => void handleDuplicate(evaluator)}
														data-testid={`evaluators-duplicate-${evaluator.id}`}
													>
														<Copy className="size-3.5" />
														{t("evals.list.duplicate")}
													</DropdownMenuItem>
													<DropdownMenuItem
														variant="destructive"
														disabled={!canDelete}
														onSelect={() => setPendingDelete(evaluator)}
														data-testid={`evaluators-delete-${evaluator.id}`}
													>
														<Trash2 className="size-3.5" />
														{tCommon("delete")}
													</DropdownMenuItem>
												</DropdownMenuContent>
											</DropdownMenu>
										</TableCell>
									</TableRow>
								);
							})}
						</TableBody>
					</Table>
				</div>

				{totalCount > 0 && (
					<div className="flex shrink-0 items-center justify-between text-xs" data-testid="evaluators-pagination">
						<div className="text-muted-foreground flex items-center gap-2">
							{tMcp("common.ofEntries", {
								from: (offset + 1).toLocaleString(getLocale()),
								to: Math.min(offset + limit, totalCount).toLocaleString(getLocale()),
								total: totalCount.toLocaleString(getLocale()),
							})}
						</div>
						<div className="flex items-center gap-2">
							<Button
								variant="ghost"
								size="sm"
								onClick={() => onOffsetChange(Math.max(0, offset - limit))}
								disabled={offset === 0}
								data-testid="evaluators-pagination-prev"
								aria-label={tMcp("common.previousPage")}
							>
								<ChevronLeft className="size-3" />
							</Button>
							<div className="flex items-center gap-1">
								<span>{tMcp("common.page")}</span>
								<span>{Math.floor(offset / limit) + 1}</span>
								<span>{tMcp("common.ofPages", { total: Math.ceil(totalCount / limit) })}</span>
							</div>
							<Button
								variant="ghost"
								size="sm"
								onClick={() => onOffsetChange(offset + limit)}
								disabled={offset + limit >= totalCount}
								data-testid="evaluators-pagination-next"
								aria-label={tMcp("common.nextPage")}
							>
								<ChevronRight className="size-3" />
							</Button>
						</div>
					</div>
				)}
			</div>

			<AlertDialog open={pendingDelete !== null} onOpenChange={(open) => !open && setPendingDelete(null)}>
				<AlertDialogContent data-testid="evaluators-delete-dialog">
					<AlertDialogHeader>
						<AlertDialogTitle>{t("evals.list.deleteTitle", { name: pendingDelete?.name ?? "" })}</AlertDialogTitle>
						<AlertDialogDescription>{t("evals.list.deleteDescription")}</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel disabled={isDeleting} data-testid="evaluators-delete-cancel">
							{tCommon("cancel")}
						</AlertDialogCancel>
						<AlertDialogAction
							disabled={isDeleting}
							onClick={(e) => {
								// Stay open until the request settles, so a failure is not lost.
								e.preventDefault();
								void handleDelete();
							}}
							data-testid="evaluators-delete-confirm"
						>
							{tCommon("delete")}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</>
	);
}

/** Where an evaluator runs: a model, an HTTP endpoint, the sandbox or the gateway itself. */
function RunsOn({ evaluator }: { evaluator: EvaluatorSummary }) {
	const { t } = useTranslation("config");
	if (evaluator.provider) {
		return (
			<span className="truncate font-mono text-xs">
				{evaluator.provider}
				{evaluator.model ? ` / ${evaluator.model}` : ""}
			</span>
		);
	}
	if (evaluator.endpoint) return <span className="truncate font-mono text-xs">{evaluator.endpoint}</span>;
	const where =
		evaluator.type === "programmatic"
			? t("evals.list.runsOnSandbox")
			: evaluator.type === "human"
				? t("evals.list.runsOnReviewer")
				: t("evals.list.runsOnGateway");
	return <span className="text-muted-foreground text-xs">{where}</span>;
}