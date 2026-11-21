import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelSelector } from "@/components/ui/modelSelector";
import { MultiSelect } from "@/components/ui/multiSelect";
import { ProviderSelector } from "@/components/ui/providerSelector";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { useToast } from "@/hooks/use-toast";
import { useDebouncedValue } from "@/hooks/useDebounce";
import {
	getErrorMessage,
	useCreateEvalRunMutation,
	useGetEvalDatasetQuery,
	useGetEvalDatasetsQuery,
	useGetEvalDatasetVersionsQuery,
	useGetEvalKeysQuery,
	useGetEvaluatorsQuery,
	useGetPromptsQuery,
	useGetVersionsQuery,
	useValidateEvalRunMutation,
} from "@/lib/store";
import { RUN_TARGET_TYPES, type Run, type RunCheck, type RunInput, type RunTargetType } from "@/lib/types/evalRuns";
import type { EvaluatorType } from "@/lib/types/evals";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { EvaluatorTypeIcon } from "../../evaluators/views/evaluatorTypeBadge";

const CURRENT = "0";
const ALL_ROWS = "__all__";
const METHODS = ["POST", "GET", "PUT"];

// The picker takes an icon as a component, so each evaluator type gets one.
const typeIcon = (type: EvaluatorType) =>
	function TypeIcon({ className }: { className?: string }) {
		return <EvaluatorTypeIcon type={type} className={className} />;
	};
const TYPE_ICONS: Record<EvaluatorType, ReturnType<typeof typeIcon>> = {
	ai: typeIcon("ai"),
	programmatic: typeIcon("programmatic"),
	statistical: typeIcon("statistical"),
	api: typeIcon("api"),
	human: typeIcon("human"),
};

interface NewRunSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	onCreated: (run: Run) => void;
	// Start with this dataset picked, when the sheet is opened from a dataset.
	datasetId?: string;
}

// NewRunSheet starts a test run: what to test, the dataset to test it on, and the
// evaluators to score it with. It checks the settings with the server as they change
// and lists what would stop the run, so "Start run" is only offered when it can start.
export function NewRunSheet({ open, onOpenChange, onCreated, datasetId: initialDataset }: NewRunSheetProps) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();

	const [name, setName] = useState("");
	const [targetType, setTargetType] = useState<RunTargetType>("prompt");
	const [promptId, setPromptId] = useState("");
	const [promptVersion, setPromptVersion] = useState(CURRENT);
	const [provider, setProvider] = useState("");
	const [model, setModel] = useState("");
	const [system, setSystem] = useState("");
	const [method, setMethod] = useState("POST");
	const [url, setUrl] = useState("");
	const [payload, setPayload] = useState('{"input": "{{input}}"}');
	const [outputPath, setOutputPath] = useState("");
	const [headerName, setHeaderName] = useState("");
	const [headerValue, setHeaderValue] = useState("");
	const [datasetId, setDatasetId] = useState("");
	const [datasetVersion, setDatasetVersion] = useState(CURRENT);
	const [splitId, setSplitId] = useState(ALL_ROWS);
	const [evaluatorIds, setEvaluatorIds] = useState<string[]>([]);
	const [check, setCheck] = useState<RunCheck | null>(null);

	const { data: promptsData, refetch: refetchPrompts } = useGetPromptsQuery(undefined, { skip: !open });
	const { data: versionsData } = useGetVersionsQuery(promptId, { skip: !open || !promptId });
	const { data: keysData } = useGetEvalKeysQuery(undefined, { skip: !open });
	const { data: datasetsData, refetch: refetchDatasets } = useGetEvalDatasetsQuery({ limit: 200 }, { skip: !open });
	const { data: datasetData } = useGetEvalDatasetQuery(datasetId, { skip: !open || !datasetId });
	const { data: datasetVersionsData } = useGetEvalDatasetVersionsQuery(datasetId, { skip: !open || !datasetId });
	const { data: evaluatorsData, refetch: refetchEvaluators } = useGetEvaluatorsQuery({ limit: 200 }, { skip: !open });
	const [validate] = useValidateEvalRunMutation();
	const [createRun, { isLoading: creating }] = useCreateEvalRunMutation();

	const providers = useMemo(() => [...new Set((keysData?.keys ?? []).map((key) => key.provider))].sort(), [keysData]);
	const evaluatorOptions = useMemo(
		() =>
			(evaluatorsData?.evaluators ?? []).map((evaluator) => ({
				value: evaluator.id,
				label: evaluator.name,
				description: evaluator.variables.join(", "),
				icon: TYPE_ICONS[evaluator.type],
			})),
		[evaluatorsData],
	);

	// A prompt, dataset or evaluator made in another tab shows up on coming back.
	useEffect(() => {
		if (!open) return;
		const refresh = () => {
			void refetchPrompts();
			void refetchDatasets();
			void refetchEvaluators();
		};
		window.addEventListener("focus", refresh);
		return () => window.removeEventListener("focus", refresh);
	}, [open, refetchPrompts, refetchDatasets, refetchEvaluators]);

	useEffect(() => {
		if (!open) return;
		setName("");
		setCheck(null);
		setEvaluatorIds([]);
		setDatasetId(initialDataset ?? "");
		setDatasetVersion(CURRENT);
		setSplitId(ALL_ROWS);
	}, [open, initialDataset]);
	useEffect(() => setPromptVersion(CURRENT), [promptId]);
	useEffect(() => {
		setDatasetVersion(CURRENT);
		setSplitId(ALL_ROWS);
	}, [datasetId]);

	const input: RunInput = useMemo(() => {
		const target: RunInput["target"] = { type: targetType };
		if (targetType === "prompt") {
			target.prompt_id = promptId;
			target.prompt_version = Number(promptVersion);
		}
		if (targetType === "model") {
			target.provider = provider;
			target.model = model.trim();
			if (system.trim()) target.system = system;
		}
		if (targetType === "http") {
			target.http = {
				method,
				url: url.trim(),
				payload: method === "GET" ? undefined : payload,
				output_mapping: { score: outputPath.trim() },
				headers: headerName.trim() ? [{ key: headerName.trim(), value: headerValue }] : undefined,
			};
		}
		return {
			name: name.trim(),
			target,
			dataset_id: datasetId,
			dataset_version: Number(datasetVersion),
			split_id: splitId === ALL_ROWS ? undefined : splitId,
			evaluators: evaluatorIds.map((id) => ({ id })),
		};
	}, [
		name,
		targetType,
		promptId,
		promptVersion,
		provider,
		model,
		system,
		method,
		url,
		payload,
		outputPath,
		headerName,
		headerValue,
		datasetId,
		datasetVersion,
		splitId,
		evaluatorIds,
	]);

	// The server says what would stop the run. It is asked once the form has settled.
	const settled = useDebouncedValue(input, 400);
	const ready = open && settled.dataset_id !== "" && settled.evaluators.length > 0;
	useEffect(() => {
		if (!ready) {
			setCheck(null);
			return;
		}
		let stale = false;
		validate(settled)
			.unwrap()
			.then((result) => !stale && setCheck(result))
			.catch(() => !stale && setCheck(null));
		return () => {
			stale = true;
		};
	}, [ready, settled, validate]);

	const submit = async () => {
		try {
			const created = await createRun(input).unwrap();
			toast({ title: t("evals.runs.form.started", { name: created.run.name }) });
			onOpenChange(false);
			onCreated(created.run);
		} catch (error) {
			toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	const field = "flex flex-col gap-2";
	// A link beside a label, to make the thing the field picks. It opens in a new tab,
	// so what is already filled in here is kept.
	const createLink = "text-primary text-xs hover:underline";
	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="p-0 pt-4 sm:max-w-2xl" data-testid="run-form-sheet">
				<SheetHeader className="flex flex-col items-start px-0 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>{t("evals.runs.form.title")}</SheetTitle>
					<SheetDescription>{t("evals.runs.form.description")}</SheetDescription>
				</SheetHeader>
				<div className="@container flex flex-1 flex-col gap-6 overflow-y-auto px-4 py-4 md:px-8">
					<div className={field}>
						<Label htmlFor="run-name">{t("evals.datasets.form.name")}</Label>
						<Input id="run-name" value={name} onChange={(event) => setName(event.target.value)} data-testid="run-form-name-input" />
					</div>

					<fieldset className="flex flex-col gap-3">
						<legend className="mb-2 text-sm font-medium">{t("evals.runs.form.target")}</legend>
						<div className="grid grid-cols-1 gap-2 @md:grid-cols-2">
							{RUN_TARGET_TYPES.map((type) => (
								<label
									key={type}
									className={cn(
										"flex cursor-pointer flex-col gap-1 rounded-sm border px-3 py-2.5 transition-colors",
										targetType === type ? "border-primary ring-primary bg-muted/40 ring-1" : "border-border hover:bg-muted/40",
									)}
								>
									<span className="flex items-center gap-2 text-sm font-medium">
										<input
											type="radio"
											name="run-target"
											className="accent-primary"
											checked={targetType === type}
											onChange={() => setTargetType(type)}
											data-testid={`run-form-target-${type}`}
										/>
										{t(`evals.runs.targets.${type}`)}
									</span>
									<span className="text-muted-foreground pl-5 text-xs">{t(`evals.runs.targetHints.${type}`)}</span>
								</label>
							))}
						</div>

						<div className="flex flex-col gap-4 rounded-sm border p-4" data-testid="run-form-target-settings">
							{targetType === "prompt" && (
								<div className="grid grid-cols-1 gap-4 @md:grid-cols-2">
									<div className={field}>
										<div className="flex items-center justify-between gap-2">
											<Label htmlFor="run-prompt">{t("evals.runs.form.prompt")}</Label>
											<Link to="/workspace/prompt-repo" target="_blank" className={createLink} data-testid="run-form-create-prompt-link">
												{t("promptRepo.createPrompt")}
											</Link>
										</div>
										<Select value={promptId} onValueChange={setPromptId}>
											<SelectTrigger className="w-full" id="run-prompt" data-testid="run-form-prompt-select">
												<SelectValue placeholder={t("evals.runs.form.pickPrompt")} />
											</SelectTrigger>
											<SelectContent>
												{(promptsData?.prompts ?? []).map((prompt) => (
													<SelectItem key={prompt.id} value={prompt.id}>
														{prompt.name}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
									</div>
									<div className={field}>
										<Label htmlFor="run-prompt-version">{t("evals.runs.form.version")}</Label>
										<Select value={promptVersion} onValueChange={setPromptVersion} disabled={!promptId}>
											<SelectTrigger className="w-full" id="run-prompt-version" data-testid="run-form-prompt-version-select">
												<SelectValue />
											</SelectTrigger>
											<SelectContent>
												<SelectItem value={CURRENT}>{t("evals.runs.form.latest")}</SelectItem>
												{(versionsData?.versions ?? []).map((version) => (
													<SelectItem key={version.id} value={String(version.version_number)}>
														v{version.version_number} · {version.provider} / {version.model}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
									</div>
								</div>
							)}

							{targetType === "model" && (
								<>
									<div className="grid grid-cols-1 gap-4 @md:grid-cols-2">
										<div className={field}>
											<Label id="run-provider-label">{t("evals.runs.form.provider")}</Label>
											{/* Only providers with an eval key are offered: a run's model calls use no other key. */}
											<ProviderSelector
												source="configured"
												filter={(candidate) => providers.includes(candidate.name)}
												value={provider}
												onChange={(next: string) => {
													if (next !== provider) setModel("");
													setProvider(next);
												}}
												placeholder={t("evals.runs.form.pickProvider")}
												noPortal
												ariaLabelledBy="run-provider-label"
												data-testid="run-form-provider-select"
											/>
										</div>
										<div className={field}>
											<Label id="run-model-label">{t("evals.runs.form.model")}</Label>
											<ModelSelector
												provider={provider || undefined}
												value={model}
												onChange={setModel}
												disabled={!provider}
												allowCustomModel
												ariaLabelledBy="run-model-label"
												data-testid="run-form-model-select"
											/>
										</div>
									</div>
									<div className={field}>
										<Label htmlFor="run-system">{t("evals.runs.form.system")}</Label>
										<Textarea
											id="run-system"
											rows={2}
											value={system}
											onChange={(event) => setSystem(event.target.value)}
											data-testid="run-form-system-input"
										/>
									</div>
								</>
							)}

							{targetType === "http" && (
								<>
									<div className="grid grid-cols-[7rem_1fr] gap-4">
										<div className={field}>
											<Label htmlFor="run-method">{t("evals.runs.form.method")}</Label>
											<Select value={method} onValueChange={setMethod}>
												<SelectTrigger className="w-full" id="run-method" data-testid="run-form-method-select">
													<SelectValue />
												</SelectTrigger>
												<SelectContent>
													{METHODS.map((item) => (
														<SelectItem key={item} value={item}>
															{item}
														</SelectItem>
													))}
												</SelectContent>
											</Select>
										</div>
										<div className={field}>
											<Label htmlFor="run-url">{t("evals.runs.form.url")}</Label>
											<Input
												id="run-url"
												value={url}
												placeholder="https://"
												onChange={(event) => setUrl(event.target.value)}
												data-testid="run-form-url-input"
											/>
										</div>
									</div>
									{method !== "GET" && (
										<div className={field}>
											<Label htmlFor="run-payload">{t("evals.runs.form.body")}</Label>
											<Textarea
												id="run-payload"
												rows={3}
												className="font-mono text-xs"
												value={payload}
												onChange={(event) => setPayload(event.target.value)}
												data-testid="run-form-payload-input"
											/>
										</div>
									)}
									<div className="grid grid-cols-1 gap-4 @md:grid-cols-2">
										<div className={cn(field, "@md:col-span-2")}>
											<Label htmlFor="run-output-path">{t("evals.runs.form.outputPath")}</Label>
											<Input
												id="run-output-path"
												className="font-mono text-xs"
												value={outputPath}
												placeholder="choices.0.message.content"
												onChange={(event) => setOutputPath(event.target.value)}
												data-testid="run-form-output-path-input"
											/>
										</div>
										<div className={field}>
											<Label htmlFor="run-header-name">{t("evals.runs.form.headerName")}</Label>
											<Input
												id="run-header-name"
												value={headerName}
												onChange={(event) => setHeaderName(event.target.value)}
												data-testid="run-form-header-name-input"
											/>
										</div>
										<div className={field}>
											<Label htmlFor="run-header-value">{t("evals.runs.form.headerValue")}</Label>
											<Input
												id="run-header-value"
												type="password"
												autoComplete="off"
												value={headerValue}
												onChange={(event) => setHeaderValue(event.target.value)}
												data-testid="run-form-header-value-input"
											/>
										</div>
									</div>
								</>
							)}
							<p className="text-muted-foreground text-xs">{t(`evals.runs.targetNotes.${targetType}`)}</p>
						</div>
					</fieldset>

					<div className="grid grid-cols-1 gap-4 @md:grid-cols-2">
						<div className={cn(field, "@md:col-span-2")}>
							<div className="flex items-center justify-between gap-2">
								<Label htmlFor="run-dataset">{t("evals.runs.list.columns.dataset")}</Label>
								<Link to="/workspace/evals/datasets" target="_blank" className={createLink} data-testid="run-form-create-dataset-link">
									{t("evals.datasets.form.create")}
								</Link>
							</div>
							<Select value={datasetId} onValueChange={setDatasetId}>
								<SelectTrigger className="w-full" id="run-dataset" data-testid="run-form-dataset-select">
									<SelectValue placeholder={t("evals.datasets.addTo.pickDataset")} />
								</SelectTrigger>
								<SelectContent>
									{(datasetsData?.datasets ?? []).map((dataset) => (
										<SelectItem key={dataset.id} value={dataset.id}>
											{dataset.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
						<div className={field}>
							<Label htmlFor="run-dataset-version">{t("evals.runs.form.version")}</Label>
							<Select value={datasetVersion} onValueChange={setDatasetVersion} disabled={!datasetId}>
								<SelectTrigger className="w-full" id="run-dataset-version" data-testid="run-form-dataset-version-select">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value={CURRENT}>{t("evals.runs.form.currentVersion")}</SelectItem>
									{(datasetVersionsData?.versions ?? []).map((version) => (
										<SelectItem key={version.number} value={String(version.number)}>
											v{version.number}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
						<div className={field}>
							<Label htmlFor="run-split">{t("evals.datasets.addTo.split")}</Label>
							<Select value={splitId} onValueChange={setSplitId} disabled={!datasetId}>
								<SelectTrigger className="w-full" id="run-split" data-testid="run-form-split-select">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value={ALL_ROWS}>{t("evals.datasets.splits.all")}</SelectItem>
									{(datasetData?.dataset.splits ?? []).map((split) => (
										<SelectItem key={split.id} value={split.id}>
											{split.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
					</div>

					<div className={field}>
						<div className="flex items-center justify-between gap-2">
							<Label htmlFor="run-evaluators">{t("evals.runs.form.evaluators")}</Label>
							<Link to="/workspace/evals/evaluators" target="_blank" className={createLink} data-testid="run-form-create-evaluator-link">
								{t("evals.editor.create")}
							</Link>
						</div>
						<MultiSelect
							id="run-evaluators"
							data-testid="run-form-evaluators-select"
							options={evaluatorOptions}
							defaultValue={evaluatorIds}
							resetOnDefaultValueChange
							onValueChange={setEvaluatorIds}
							placeholder={t("evals.runs.form.pickEvaluators")}
							emptyIndicator={t("evals.runs.form.noEvaluators")}
							hideSelectAll
							modalPopover
							maxCount={8}
							className="border-input text-foreground hover:bg-accent hover:text-accent-foreground min-h-9 w-full rounded-sm bg-transparent font-normal"
						/>
					</div>

					{check && !check.valid && (
						<Alert data-testid="run-form-problems">
							<AlertDescription>
								<p className="font-medium">{t("evals.runs.form.problems", { count: check.problems.length })}</p>
								<ul className="mt-1 list-disc pl-4 text-xs">
									{check.problems.map((problem) => (
										<li key={problem}>{problem}</li>
									))}
								</ul>
								{check.variables.length > 0 && (
									<p className="mt-2 text-xs">{t("evals.runs.form.provides", { variables: check.variables.join(", ") })}</p>
								)}
							</AlertDescription>
						</Alert>
					)}
				</div>
				<SheetFooter className="bg-card sticky bottom-0 flex-row flex-wrap items-center justify-between gap-2 border-t px-4 py-4 md:px-8">
					<span className="text-muted-foreground text-xs">{t("evals.runs.form.keysNote")}</span>
					<span className="flex gap-2">
						<Button variant="outline" onClick={() => onOpenChange(false)} data-testid="run-form-cancel-btn">
							{tCommon("cancel")}
						</Button>
						<Button onClick={() => void submit()} disabled={!check?.valid || creating} data-testid="run-form-submit-btn">
							{t("evals.runs.form.start")}
						</Button>
					</span>
				</SheetFooter>
			</SheetContent>
		</Sheet>
	);
}