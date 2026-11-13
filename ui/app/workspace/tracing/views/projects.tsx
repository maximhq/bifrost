import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import {
	getErrorMessage,
	useCreateTraceProjectMutation,
	useDeleteTraceProjectMutation,
	useGetTraceHistogramQuery,
	useGetTraceStatsQuery,
	useUpdateTraceProjectMutation,
} from "@/lib/store";
import type { TraceProject } from "@/lib/types/traces";
import { ChevronRight, FolderPlus, Search } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { formatCost, formatTokens } from "./format";
import { TraceMetricStrip } from "./traceOverview";
import TracingProjectPolicies from "@enterprise/components/tracing/tracingProjectPolicies";

/** The window project metrics cover: the last 30 days, fixed when the view opens. */
const PROJECT_METRICS_DAYS = 30;

function sinceDays(days: number): string {
	return new Date(Date.now() - days * 86_400_000).toISOString();
}

/**
 * Every tracing project, with its traffic over the last 30 days, and a field to create one. Opened
 * from the Projects section of the filter sidebar. Choosing a project opens its sheet.
 */
export function ProjectsDialog({
	open,
	onOpenChange,
	projects,
	onOpenProject,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	projects: TraceProject[];
	onOpenProject: (projectId: string) => void;
}) {
	const { t } = useTranslation("observability");
	const [query, setQuery] = useState("");
	const [name, setName] = useState("");
	const [since] = useState(() => sinceDays(PROJECT_METRICS_DAYS));
	const [createProject, { isLoading: creating }] = useCreateTraceProjectMutation();
	const { data: stats, isLoading } = useGetTraceStatsQuery({ start_time: since }, { skip: !open });
	const traffic = useMemo(() => new Map((stats?.by_project ?? []).map((row) => [row.key, row])), [stats]);
	const visible = projects.filter((project) => project.name.toLowerCase().includes(query.trim().toLowerCase()));

	const create = async (event: React.FormEvent) => {
		event.preventDefault();
		const trimmed = name.trim();
		if (!trimmed) return;
		try {
			const { project } = await createProject({ name: trimmed }).unwrap();
			setName("");
			toast.success(t("tracing.projects.created", { name: project.name }));
			onOpenProject(project.id);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="flex max-h-[80vh] flex-col gap-4 sm:max-w-xl" data-testid="tracing-projects-dialog" data-radius="md">
				<DialogHeader>
					<DialogTitle>{t("tracing.projects.title")}</DialogTitle>
					<DialogDescription>{t("tracing.projects.description")}</DialogDescription>
				</DialogHeader>
				<form className="flex gap-2" onSubmit={create}>
					<Input
						value={name}
						onChange={(event) => setName(event.target.value)}
						placeholder={t("tracing.projects.newPlaceholder")}
						aria-label={t("tracing.projects.newPlaceholder")}
						data-testid="tracing-projects-new-name"
					/>
					<Button
						type="submit"
						className="h-9 shrink-0 gap-1.5 px-3"
						disabled={creating || !name.trim()}
						data-testid="tracing-projects-create"
					>
						<FolderPlus className="size-4" />
						{t("tracing.projects.create")}
					</Button>
				</form>
				<label className="relative block">
					<Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
					<Input
						value={query}
						onChange={(event) => setQuery(event.target.value)}
						placeholder={t("tracing.filters.searchProjects")}
						className="pl-8"
						data-testid="tracing-projects-search"
					/>
				</label>
				<div className="min-h-0 flex-1 divide-y overflow-y-auto rounded-md border" data-testid="tracing-projects-list">
					{visible.length === 0 && <p className="text-muted-foreground p-4 text-sm">{t("tracing.filters.noProjects")}</p>}
					{visible.map((project) => {
						const row = traffic.get(project.id);
						return (
							<button
								key={project.id}
								type="button"
								onClick={() => onOpenProject(project.id)}
								className="hover:bg-muted/60 flex w-full items-center gap-3 px-3 py-2.5 text-left"
								data-testid={`tracing-projects-row-${project.id}`}
							>
								<span className="min-w-0 flex-1">
									<span className="block truncate text-sm font-medium">{project.name}</span>
									<span className="text-muted-foreground block truncate text-xs tabular-nums">
										{isLoading
											? "…"
											: t("tracing.projects.traffic", {
													traces: row?.trace_count ?? 0,
													tokens: formatTokens(row?.total_tokens ?? 0),
													cost: formatCost(row?.total_cost ?? 0),
												})}
									</span>
								</span>
								<ChevronRight className="text-muted-foreground size-4 shrink-0" />
							</button>
						);
					})}
				</div>
			</DialogContent>
		</Dialog>
	);
}

/**
 * One project: its metrics over the last 30 days, then its details, which can be edited.
 */
export function ProjectSheet({
	project,
	onClose,
	onViewTraces,
}: {
	project: TraceProject | undefined;
	onClose: () => void;
	onViewTraces: (projectId: string) => void;
}) {
	const { t } = useTranslation("observability");
	const [updateProject, { isLoading: saving }] = useUpdateTraceProjectMutation();
	const [deleteProject] = useDeleteTraceProjectMutation();
	const [since] = useState(() => sinceDays(PROJECT_METRICS_DAYS));
	const [name, setName] = useState("");
	const [description, setDescription] = useState("");
	const [retention, setRetention] = useState("");
	const open = Boolean(project);
	const filters = { project_ids: project ? [project.id] : [], start_time: since };
	const stats = useGetTraceStatsQuery(filters, { skip: !open });
	const histogram = useGetTraceHistogramQuery({ filters, bucketSizeSeconds: 86_400 }, { skip: !open });

	// The form follows the project it is opened on, not a refetch while it is being edited.
	useEffect(() => {
		if (!project) return;
		setName(project.name);
		setDescription(project.description ?? "");
		setRetention(project.retention_days ? String(project.retention_days) : "");
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [project?.id]);

	const retentionValue = retention.trim() === "" ? null : Number(retention);
	const retentionValid = retentionValue === null || (Number.isInteger(retentionValue) && retentionValue > 0);
	const changed =
		!!project &&
		(name.trim() !== project.name ||
			description.trim() !== (project.description ?? "") ||
			retentionValue !== (project.retention_days ?? null));

	const save = async (event: React.FormEvent) => {
		event.preventDefault();
		if (!project || !name.trim() || !retentionValid) return;
		try {
			await updateProject({
				id: project.id,
				data: {
					name: name.trim(),
					description: description.trim(),
					retention_days: retentionValue,
				},
			}).unwrap();
			toast.success(t("tracing.settings.saved"));
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const remove = async () => {
		if (!project || !window.confirm(t("tracing.settings.deleteProjectConfirm"))) return;
		try {
			await deleteProject(project.id).unwrap();
			onClose();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<Sheet open={open} onOpenChange={(next) => !next && onClose()}>
			<SheetContent
				className="flex w-full flex-col gap-0 overflow-y-auto p-0 sm:max-w-2xl"
				data-testid="tracing-project-sheet"
				data-radius="md"
			>
				<SheetHeader className="border-b p-5">
					<div className="flex min-w-0 flex-col gap-1">
						<SheetTitle>{project?.name}</SheetTitle>
						<SheetDescription>{t("tracing.projects.metricsWindow", { days: PROJECT_METRICS_DAYS })}</SheetDescription>
					</div>
				</SheetHeader>
				<div className="flex flex-col gap-6 p-5">
					<div className="overflow-hidden rounded-md border">
						<TraceMetricStrip data={stats.data} histogram={histogram.data} loading={stats.isFetching} error={stats.error} />
					</div>
					<Button
						variant="outline"
						className="h-9 self-start px-3"
						onClick={() => project && onViewTraces(project.id)}
						data-testid="tracing-project-view-traces"
					>
						{t("tracing.projects.viewTraces")}
					</Button>

					<form className="flex flex-col gap-4" onSubmit={save} data-testid="tracing-project-form">
						<h3 className="text-muted-foreground text-xs font-medium tracking-wide uppercase">{t("tracing.projects.details")}</h3>
						<div className="space-y-1.5">
							<Label htmlFor="tracing-project-name">{t("tracing.settings.name")}</Label>
							<Input
								id="tracing-project-name"
								value={name}
								onChange={(event) => setName(event.target.value)}
								data-testid="tracing-project-name"
							/>
						</div>
						<div className="space-y-1.5">
							<Label htmlFor="tracing-project-description">{t("tracing.projects.descriptionLabel")}</Label>
							<Textarea
								id="tracing-project-description"
								value={description}
								onChange={(event) => setDescription(event.target.value)}
								rows={3}
								data-testid="tracing-project-description"
							/>
						</div>
						<div className="space-y-1.5">
							<Label htmlFor="tracing-project-retention">{t("tracing.settings.retention")}</Label>
							<Input
								id="tracing-project-retention"
								inputMode="numeric"
								value={retention}
								onChange={(event) => setRetention(event.target.value)}
								aria-invalid={!retentionValid}
								className="max-w-40"
								data-testid="tracing-project-retention"
							/>
							<p className="text-muted-foreground text-xs">{t("tracing.settings.retentionHint")}</p>
						</div>
						<div className="flex items-center justify-between gap-3 border-t pt-4">
							<Button type="button" variant="destructive" className="h-9 px-3" onClick={remove} data-testid="tracing-project-delete">
								{t("tracing.settings.deleteProject")}
							</Button>
							<Button
								type="submit"
								className="h-9 px-3"
								disabled={saving || !changed || !name.trim() || !retentionValid}
								data-testid="tracing-project-save"
							>
								{t("tracing.settings.save")}
							</Button>
						</div>
					</form>
					{project && <TracingProjectPolicies projectId={project.id} />}
				</div>
			</SheetContent>
		</Sheet>
	);
}