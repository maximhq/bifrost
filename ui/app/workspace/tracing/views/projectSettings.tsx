import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, useDeleteTraceProjectMutation, useGetEvaluatorsQuery, useUpdateTraceProjectMutation } from "@/lib/store";
import type { TraceEvaluatorRule, TraceMaskingRule, TraceProject, TraceSource } from "@/lib/types/traces";
import { Plus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { SOURCE_LABELS } from "./format";

/** The value the API returns in place of a stored forward header. Sent back, it keeps that value. */
const SECRET_MASK = "********";

interface Draft {
	name: string;
	retention: string;
	rules: TraceMaskingRule[];
	evaluators: TraceEvaluatorRule[];
	forwardEnabled: boolean;
	vendor: TraceSource;
	baseUrl: string;
	auth: string;
}

function draftOf(project: TraceProject): Draft {
	return {
		name: project.name,
		retention: project.retention_days ? String(project.retention_days) : "",
		rules: (project.masking_rules ?? []).map((rule) => ({ ...rule })),
		evaluators: (project.evaluators ?? []).map((rule) => ({ ...rule })),
		forwardEnabled: Boolean(project.forward?.enabled),
		vendor: project.forward?.vendor ?? "braintrust",
		baseUrl: project.forward?.base_url ?? "",
		auth: project.forward?.headers?.Authorization ?? "",
	};
}

/** Per-project settings: how long traces are kept, what is masked before storing, and where traces are forwarded. */
export function ProjectSettings({
	open,
	onOpenChange,
	projects,
	initialProjectId,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	projects: TraceProject[];
	initialProjectId?: string;
}) {
	const { t } = useTranslation("observability");
	const [updateProject, { isLoading }] = useUpdateTraceProjectMutation();
	const [deleteProject] = useDeleteTraceProjectMutation();
	const { data: evaluatorsData } = useGetEvaluatorsQuery({ limit: 100 }, { skip: !open });
	const evaluatorOptions = evaluatorsData?.evaluators ?? [];
	const [projectId, setProjectId] = useState("");
	const [draft, setDraft] = useState<Draft | null>(null);
	const project = projects.find((candidate) => candidate.id === projectId);

	useEffect(() => {
		if (open) setProjectId(initialProjectId || projects[0]?.id || "");
		// Only when the dialog opens: a refetch while editing must not reset the form.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [open]);
	useEffect(() => {
		setDraft(project ? draftOf(project) : null);
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [projectId, open]);

	const patch = (change: Partial<Draft>) => setDraft((current) => (current ? { ...current, ...change } : current));
	const patchRule = (index: number, change: Partial<TraceMaskingRule>) =>
		setDraft((current) =>
			current ? { ...current, rules: current.rules.map((rule, i) => (i === index ? { ...rule, ...change } : rule)) } : current,
		);

	const patchEvaluator = (index: number, change: Partial<TraceEvaluatorRule>) =>
		setDraft((current) =>
			current ? { ...current, evaluators: current.evaluators.map((rule, i) => (i === index ? { ...rule, ...change } : rule)) } : current,
		);

	const save = async () => {
		if (!draft || !project) return;
		const retention = draft.retention.trim() === "" ? null : Number(draft.retention);
		try {
			await updateProject({
				id: project.id,
				data: {
					name: draft.name.trim(),
					description: project.description,
					retention_days: retention,
					masking_rules: draft.rules.filter((rule) => rule.pattern.trim() !== ""),
					evaluators: draft.evaluators.filter((rule) => rule.evaluator_id !== ""),
					forward: draft.baseUrl.trim()
						? {
								vendor: draft.vendor,
								base_url: draft.baseUrl.trim(),
								enabled: draft.forwardEnabled,
								headers: draft.auth ? { Authorization: draft.auth } : {},
							}
						: null,
				},
			}).unwrap();
			toast.success(t("tracing.settings.saved"));
			onOpenChange(false);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const remove = async () => {
		if (!project || !window.confirm(t("tracing.settings.deleteProjectConfirm"))) return;
		try {
			await deleteProject(project.id).unwrap();
			onOpenChange(false);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="max-h-[88vh] overflow-auto sm:max-w-2xl" data-testid="tracing-project-settings" data-radius="md">
				<DialogHeader>
					<DialogTitle>{t("tracing.settings.title")}</DialogTitle>
					<DialogDescription>{t("tracing.settings.description")}</DialogDescription>
				</DialogHeader>
				{!draft || !project ? (
					<p className="text-muted-foreground text-sm">{t("tracing.settings.noProjects")}</p>
				) : (
					<div className="space-y-5">
						<div className="grid gap-3 md:grid-cols-3">
							<div className="space-y-1">
								<Label>{t("tracing.settings.project")}</Label>
								<Select value={projectId} onValueChange={setProjectId}>
									<SelectTrigger data-testid="tracing-settings-project-select">
										<SelectValue />
									</SelectTrigger>
									<SelectContent data-radius="md">
										{projects.map((candidate) => (
											<SelectItem key={candidate.id} value={candidate.id}>
												{candidate.name}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							</div>
							<div className="space-y-1">
								<Label htmlFor="tracing-settings-name">{t("tracing.settings.name")}</Label>
								<Input
									id="tracing-settings-name"
									data-testid="tracing-settings-name-input"
									value={draft.name}
									onChange={(event) => patch({ name: event.target.value })}
								/>
							</div>
							<div className="space-y-1">
								<Label htmlFor="tracing-settings-retention">{t("tracing.settings.retention")}</Label>
								<Input
									id="tracing-settings-retention"
									data-testid="tracing-settings-retention-input"
									type="number"
									min={1}
									value={draft.retention}
									onChange={(event) => patch({ retention: event.target.value })}
								/>
								<p className="text-muted-foreground text-[11px]">{t("tracing.settings.retentionHint")}</p>
							</div>
						</div>

						<section className="space-y-2">
							<h3 className="text-sm font-medium">{t("tracing.settings.masking")}</h3>
							<p className="text-muted-foreground text-xs">{t("tracing.settings.maskingHint")}</p>
							{draft.rules.map((rule, index) => (
								<div key={index} className="grid gap-2 md:grid-cols-[1fr_2fr_1fr_auto]" data-testid={`tracing-settings-rule-${index}`}>
									<Input
										data-testid={`tracing-settings-rule-name-input-${index}`}
										placeholder={t("tracing.settings.ruleName")}
										value={rule.name}
										onChange={(event) => patchRule(index, { name: event.target.value })}
									/>
									<Input
										data-testid={`tracing-settings-rule-pattern-input-${index}`}
										className="font-mono text-xs"
										placeholder={t("tracing.settings.pattern")}
										value={rule.pattern}
										onChange={(event) => patchRule(index, { pattern: event.target.value })}
									/>
									<Input
										data-testid={`tracing-settings-rule-replacement-input-${index}`}
										placeholder={t("tracing.settings.replacement")}
										value={rule.replacement}
										onChange={(event) => patchRule(index, { replacement: event.target.value })}
									/>
									<Button
										variant="ghost"
										size="icon"
										aria-label={t("tracing.settings.removeRule")}
										data-testid={`tracing-settings-rule-remove-button-${index}`}
										onClick={() => patch({ rules: draft.rules.filter((_, i) => i !== index) })}
									>
										<Trash2 className="h-4 w-4" />
									</Button>
								</div>
							))}
							<Button
								variant="outline"
								size="sm"
								data-testid="tracing-settings-rule-add-button"
								onClick={() => patch({ rules: [...draft.rules, { name: "", pattern: "", replacement: "[redacted]" }] })}
							>
								<Plus className="mr-1 h-3.5 w-3.5" />
								{t("tracing.settings.addRule")}
							</Button>
						</section>

						<section className="space-y-2">
							<h3 className="text-sm font-medium">{t("tracing.evals.evaluators")}</h3>
							<p className="text-muted-foreground text-xs">{t("tracing.evals.evaluatorsHint")}</p>
							{draft.evaluators.map((rule, index) => (
								<div
									key={index}
									className="grid items-center gap-2 md:grid-cols-[2fr_1fr_1fr_auto_auto]"
									data-testid={`tracing-settings-evaluator-${index}`}
								>
									<Select
										value={rule.evaluator_id}
										onValueChange={(id) =>
											patchEvaluator(index, { evaluator_id: id, name: evaluatorOptions.find((option) => option.id === id)?.name ?? "" })
										}
									>
										<SelectTrigger data-testid={`tracing-settings-evaluator-select-${index}`}>
											<SelectValue placeholder={t("tracing.evals.chooseEvaluator")} />
										</SelectTrigger>
										<SelectContent data-radius="md">
											{evaluatorOptions.map((option) => (
												<SelectItem key={option.id} value={option.id}>
													{option.name}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
									<Input
										data-testid={`tracing-settings-evaluator-sample-input-${index}`}
										type="number"
										min={0}
										max={100}
										aria-label={t("tracing.evals.sampleRate")}
										placeholder={t("tracing.evals.sampleRate")}
										value={Math.round(rule.sample_rate * 100)}
										onChange={(event) =>
											patchEvaluator(index, { sample_rate: Math.min(Math.max(Number(event.target.value) || 0, 0), 100) / 100 })
										}
									/>
									<Input
										data-testid={`tracing-settings-evaluator-cap-input-${index}`}
										type="number"
										min={0}
										aria-label={t("tracing.evals.dailyCap")}
										placeholder={t("tracing.evals.dailyCap")}
										value={rule.daily_cap}
										onChange={(event) => patchEvaluator(index, { daily_cap: Math.max(Number(event.target.value) || 0, 0) })}
									/>
									<Switch
										checked={rule.enabled}
										onCheckedChange={(enabled) => patchEvaluator(index, { enabled })}
										aria-label={t("tracing.settings.enabled")}
										data-testid={`tracing-settings-evaluator-enabled-switch-${index}`}
									/>
									<Button
										variant="ghost"
										size="icon"
										aria-label={t("tracing.evals.removeEvaluator")}
										data-testid={`tracing-settings-evaluator-remove-button-${index}`}
										onClick={() => patch({ evaluators: draft.evaluators.filter((_, i) => i !== index) })}
									>
										<Trash2 className="h-4 w-4" />
									</Button>
								</div>
							))}
							<Button
								variant="outline"
								size="sm"
								data-testid="tracing-settings-evaluator-add-button"
								onClick={() =>
									patch({ evaluators: [...draft.evaluators, { evaluator_id: "", sample_rate: 0.1, daily_cap: 500, enabled: true }] })
								}
							>
								<Plus className="mr-1 h-3.5 w-3.5" />
								{t("tracing.evals.addEvaluator")}
							</Button>
						</section>

						<section className="space-y-2">
							<div className="flex items-center justify-between">
								<h3 className="text-sm font-medium">{t("tracing.settings.forward")}</h3>
								<label className="flex items-center gap-2 text-sm">
									<Switch
										checked={draft.forwardEnabled}
										onCheckedChange={(checked) => patch({ forwardEnabled: checked })}
										data-testid="tracing-settings-forward-switch"
									/>
									{t("tracing.settings.enabled")}
								</label>
							</div>
							<p className="text-muted-foreground text-xs">{t("tracing.settings.forwardHint")}</p>
							<div className="grid gap-2 md:grid-cols-[1fr_2fr]">
								<Select value={draft.vendor} onValueChange={(vendor) => patch({ vendor: vendor as TraceSource })}>
									<SelectTrigger data-testid="tracing-settings-forward-vendor-select" aria-label={t("tracing.settings.vendor")}>
										<SelectValue />
									</SelectTrigger>
									<SelectContent data-radius="md">
										{Object.entries(SOURCE_LABELS).map(([value, label]) => (
											<SelectItem key={value} value={value}>
												{label}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
								<Input
									data-testid="tracing-settings-forward-url-input"
									placeholder="https://api.braintrust.dev"
									aria-label={t("tracing.settings.baseUrl")}
									value={draft.baseUrl}
									onChange={(event) => patch({ baseUrl: event.target.value })}
								/>
							</div>
							<Input
								data-testid="tracing-settings-forward-auth-input"
								type="password"
								autoComplete="off"
								placeholder={t("tracing.settings.authHeader")}
								aria-label={t("tracing.settings.authHeader")}
								value={draft.auth}
								onChange={(event) => patch({ auth: event.target.value })}
							/>
							{draft.auth === SECRET_MASK && <p className="text-muted-foreground text-[11px]">{t("tracing.settings.authKept")}</p>}
						</section>
					</div>
				)}
				<DialogFooter className="sm:justify-between">
					<Button variant="destructive" data-testid="tracing-settings-delete-project-button" disabled={!project} onClick={remove}>
						{t("tracing.settings.deleteProject")}
					</Button>
					<Button data-testid="tracing-settings-save-button" disabled={!draft || isLoading || !draft.name.trim()} onClick={save}>
						{t("tracing.settings.save")}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}