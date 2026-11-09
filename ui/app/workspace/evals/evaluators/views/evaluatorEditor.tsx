import PageTitle from "@/components/pageTitle";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { TagInput } from "@/components/ui/tagInput";
import { useToast } from "@/hooks/use-toast";
import { useDebouncedValue } from "@/hooks/useDebounce";
import {
	getErrorMessage,
	useCreateEvaluatorMutation,
	useGetEvalKeysQuery,
	useUpdateEvaluatorMutation,
	useValidateEvaluatorMutation,
} from "@/lib/store";
import type { EvaluatorConfig, EvaluatorType, EvaluatorWithConfig, ValidateEvaluatorResponse } from "@/lib/types/evals";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link, useNavigate } from "@tanstack/react-router";
import { Save, Undo2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { EvaluatorConfigFields } from "./evaluatorConfigFields";
import { EvaluatorTestPanel } from "./evaluatorTestPanel";
import { EvaluatorTypeBadge } from "./evaluatorTypeBadge";
import { defaultConfig, evalKeyFor } from "./evaluatorUtils";

interface EvaluatorEditorProps {
	// The evaluator being edited, or undefined for a new one.
	evaluator?: EvaluatorWithConfig;
	// The type of a new evaluator. Ignored when editing.
	newType: EvaluatorType;
}

interface MetadataForm {
	name: string;
	description: string;
	tags: string[];
}

const VALIDATE_DEBOUNCE_MS = 400;

/** The provider an evaluator's model calls go to, if it makes any. */
function modelProviderOf(type: EvaluatorType, config: EvaluatorConfig): string | undefined {
	const fields = config as { provider?: string; embedding_provider?: string };
	if (type === "ai") return fields.provider || undefined;
	if (type === "statistical") return fields.embedding_provider || undefined;
	return undefined;
}

/** Create or edit one evaluator, with a panel beside the form that runs it. */
export function EvaluatorEditor({ evaluator, newType }: EvaluatorEditorProps) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const navigate = useNavigate();
	const isNew = !evaluator;
	const type = evaluator?.type ?? newType;
	const canWrite = useRbac(RbacResource.Evaluators, isNew ? RbacOperation.Create : RbacOperation.Update);

	const initialConfig = useMemo(() => evaluator?.config ?? defaultConfig(newType), [evaluator, newType]);
	const [config, setConfig] = useState<EvaluatorConfig>(initialConfig);
	const form = useForm<MetadataForm>({
		defaultValues: { name: evaluator?.name ?? "", description: evaluator?.description ?? "", tags: evaluator?.tags ?? [] },
	});

	// A save returns the stored evaluator: start again from it.
	useEffect(() => {
		setConfig(initialConfig);
		form.reset({ name: evaluator?.name ?? "", description: evaluator?.description ?? "", tags: evaluator?.tags ?? [] });
	}, [initialConfig, evaluator, form]);

	const { data: evalKeysData } = useGetEvalKeysQuery(undefined, { refetchOnMountOrArgChange: true });
	const evalKeys = evalKeysData?.keys ?? [];
	const evalKeysKnown = Boolean(evalKeysData);

	const [createEvaluator, { isLoading: isCreating }] = useCreateEvaluatorMutation();
	const [updateEvaluator, { isLoading: isUpdating }] = useUpdateEvaluatorMutation();
	const isSaving = isCreating || isUpdating;

	// The gateway is the one place that knows what a config reads and whether it is
	// valid, so the editor asks it instead of re-implementing the rules.
	const [validate] = useValidateEvaluatorMutation();
	const [validation, setValidation] = useState<ValidateEvaluatorResponse | null>(null);
	const serializedConfig = useMemo(() => JSON.stringify(config), [config]);
	const debouncedConfig: string = useDebouncedValue(serializedConfig, VALIDATE_DEBOUNCE_MS);
	const validateSeq = useRef(0);
	useEffect(() => {
		const seq = ++validateSeq.current;
		validate({ type, builtin_key: evaluator?.builtin_key, config: JSON.parse(debouncedConfig) })
			.unwrap()
			.then((response) => {
				if (seq === validateSeq.current) setValidation(response);
			})
			.catch(() => {
				// A failed check is not a verdict: keep the last one.
			});
	}, [debouncedConfig, type, evaluator?.builtin_key, validate]);

	const configDirty = serializedConfig !== JSON.stringify(initialConfig);
	const dirty = configDirty || form.formState.isDirty;
	const variables = validation?.variables ?? evaluator?.variables ?? [];
	const optionalVariables = validation?.optional_variables ?? evaluator?.optional_variables ?? [];
	const configError = validation && !validation.valid ? validation.error : undefined;
	// Shown only once the check has caught up with what is on screen.
	const checkedCurrent = debouncedConfig === serializedConfig;

	const provider = modelProviderOf(type, config);
	let blockedReason: string | undefined;
	if (type === "human") blockedReason = t("evals.test.blockedHuman");
	else if (configError) blockedReason = t("evals.test.blockedInvalid");
	else if (provider && evalKeysKnown && !evalKeyFor(evalKeys, provider)) blockedReason = t("evals.model.noEvalKey", { provider });

	const handleDiscard = () => {
		setConfig(initialConfig);
		form.reset();
	};

	const onSubmit = async (metadata: MetadataForm) => {
		const data = { name: metadata.name.trim(), description: metadata.description.trim(), tags: metadata.tags, config };
		try {
			if (evaluator) {
				const saved = await updateEvaluator({ id: evaluator.id, data }).unwrap();
				toast({
					title:
						saved.evaluator.version > evaluator.version
							? t("evals.editor.savedVersion", { version: saved.evaluator.version })
							: t("evals.editor.saved"),
				});
			} else {
				const created = await createEvaluator({ ...data, type }).unwrap();
				toast({ title: t("evals.editor.created", { name: created.evaluator.name }) });
				void navigate({ to: "/workspace/evals/evaluators/editor", search: { id: created.evaluator.id }, replace: true });
			}
		} catch (error) {
			toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	const saveLabel = isNew
		? t("evals.editor.create")
		: configDirty
			? t("evals.editor.saveAsVersion", { version: evaluator.current_version + 1 })
			: t("evals.editor.save");

	return (
		<Form {...form}>
			<form onSubmit={(e) => void form.handleSubmit(onSubmit)(e)} className="flex h-full flex-col" data-testid="evaluator-editor-form">
				<PageTitle
					breadcrumbs={[
						{ label: t("evals.list.title"), to: "/workspace/evals/evaluators" },
						{ label: evaluator?.name ?? t("evals.editor.newTitle") },
					]}
				/>
				{/* Two panels side by side: the evaluator on the left, its test bench on the right. */}
				<div className="bg-background flex h-full flex-col gap-3 overflow-y-auto lg:flex-row lg:overflow-hidden">
					<div
						className="bg-card flex min-w-0 flex-1 flex-col rounded-md border lg:h-full lg:overflow-hidden"
						data-testid="evaluator-edit-panel"
					>
						<div className="flex shrink-0 flex-wrap items-center gap-2 border-b px-4 py-3">
							<h1 className="truncate text-base font-semibold" data-testid="evaluator-editor-title">
								{evaluator?.name ?? t("evals.editor.newTitle")}
							</h1>
							<EvaluatorTypeBadge type={type} />
							{evaluator?.builtin_key && (
								<Badge variant="secondary" className="font-normal">
									{t("evals.sources.builtin")}
								</Badge>
							)}
							{evaluator && (
								<span className="text-muted-foreground text-xs" data-testid="evaluator-editor-version">
									{t("evals.editor.version", { version: evaluator.version })}
								</span>
							)}
							{dirty && (
								<Badge variant="warning" className="font-normal" data-testid="evaluator-editor-unsaved">
									{t("evals.editor.unsaved")}
								</Badge>
							)}
						</div>

						{/* relative: Radix selects render a hidden, absolutely positioned native select. Without a
						    positioned scroller it escapes this box and makes the whole page scroll. */}
						<div className="relative flex min-h-0 grow flex-col gap-6 p-4 lg:overflow-y-auto">
							<div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
								<FormField
									control={form.control}
									name="name"
									rules={{
										validate: (value) => value.trim() !== "" || t("evals.editor.nameRequired"),
										maxLength: { value: 255, message: t("evals.editor.nameTooLong") },
									}}
									render={({ field }) => (
										<FormItem>
											<FormLabel>{t("evals.fields.name")}</FormLabel>
											<FormControl>
												<Input {...field} disabled={!canWrite} data-testid="evaluator-name-input" />
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>
								<FormField
									control={form.control}
									name="tags"
									render={({ field }) => (
										<FormItem>
											<FormLabel>{t("evals.fields.tags")}</FormLabel>
											<FormControl>
												<TagInput
													value={field.value}
													onValueChange={field.onChange}
													readOnly={!canWrite}
													placeholder={t("evals.fields.tagsPlaceholder")}
													data-testid="evaluator-tags-input"
												/>
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>
								<FormField
									control={form.control}
									name="description"
									render={({ field }) => (
										<FormItem className="sm:col-span-2">
											<FormLabel>{t("evals.fields.description")}</FormLabel>
											<FormControl>
												<Input
													{...field}
													disabled={!canWrite}
													placeholder={t("evals.fields.descriptionPlaceholder")}
													data-testid="evaluator-description-input"
												/>
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>
							</div>

							<EvaluatorConfigFields
								type={type}
								builtinKey={evaluator?.builtin_key}
								config={config}
								onChange={setConfig}
								evalKeys={evalKeys}
								evalKeysKnown={evalKeysKnown}
								disabled={!canWrite}
							/>

							{type !== "human" && (
								<div className="flex flex-wrap items-center gap-1.5 text-xs" data-testid="evaluator-variables">
									<span className="text-muted-foreground">{t("evals.editor.variablesFound")}</span>
									{variables.length === 0 && optionalVariables.length === 0 && (
										<span className="text-muted-foreground">{t("evals.editor.noVariables")}</span>
									)}
									{variables.map((name) => (
										<Badge key={name} variant="outline" className="font-mono font-normal">
											{name}
										</Badge>
									))}
									{optionalVariables.map((name) => (
										<Badge key={name} variant="outline" className="text-muted-foreground font-mono font-normal">
											{t("evals.editor.optionalVariable", { name })}
										</Badge>
									))}
								</div>
							)}

							{configError && checkedCurrent && (dirty || !isNew) && (
								<div
									role="alert"
									className="rounded-sm border border-amber-200/40 bg-amber-50/40 p-3 text-sm text-amber-900 dark:border-amber-800/40 dark:bg-amber-950/40 dark:text-amber-100"
									data-testid="evaluator-config-error"
								>
									{configError}
								</div>
							)}
						</div>
						{/* Actions sit at the bottom of the panel, as on every other form. */}
						<div className="flex shrink-0 justify-end gap-2 border-t px-4 py-3" data-testid="evaluator-editor-footer">
							{isNew && (
								<Button type="button" variant="outline" className="h-8" asChild>
									<Link to="/workspace/evals/evaluators" data-testid="evaluator-editor-cancel">
										{tCommon("cancel")}
									</Link>
								</Button>
							)}
							{!isNew && (
								<Button
									type="button"
									variant="outline"
									className="h-8"
									onClick={handleDiscard}
									disabled={!dirty || isSaving}
									data-testid="evaluator-editor-discard"
								>
									<Undo2 className="size-4" />
									{t("evals.editor.discard")}
								</Button>
							)}
							<Button
								type="submit"
								className="h-8"
								disabled={!canWrite || isSaving || (!isNew && !dirty) || Boolean(configError)}
								data-testid="evaluator-editor-save"
							>
								<Save className="size-4" />
								{isSaving ? t("evals.editor.saving") : saveLabel}
							</Button>
						</div>
					</div>

					<div className="bg-card flex w-full shrink-0 flex-col rounded-md border lg:h-full lg:w-[24rem] lg:overflow-hidden xl:w-[28rem]">
						<EvaluatorTestPanel
							draft={{ type, builtin_key: evaluator?.builtin_key, config, evaluator_id: evaluator?.id }}
							variables={variables}
							optionalVariables={optionalVariables}
							blockedReason={blockedReason}
							evalKeys={evalKeys}
						/>
					</div>
				</div>
			</form>
		</Form>
	);
}