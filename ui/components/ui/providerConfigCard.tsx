import { AsyncMultiSelect } from "@/components/ui/asyncMultiselect";
import { Label } from "@/components/ui/label";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import MultiBudgetLines, { BudgetLineEntry } from "@/components/ui/multibudgets";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { budgetLinesLabel, money, shortPeriod, swatchClass } from "@/lib/budgetOutline";
import { resetDurationOptions } from "@/lib/constants/governance";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { cn } from "@/lib/utils";
import { cleanNumericInput } from "@/lib/utils/strings";
import { ChevronDown, Trash2, X } from "lucide-react";
import { useEffect, useState } from "react";
import { components, MultiValueProps, OptionProps } from "react-select";

// Generic, core-owned shapes so both the Access Profile (enterprise) and Virtual
// Key (core) forms can drive this card. `"*"` in allowedModels/keyIds means "all".
export interface ProviderConfigBudgetLine {
	id?: string;
	max_limit?: number;
	reset_duration?: string;
	/** Fiscal quarter definition; only meaningful when reset_duration is "1Q". */
	reset_config?: { quarter_start_month?: number };
}

export interface ProviderConfigRateLimit {
	token_max_limit?: number;
	token_reset_duration?: string;
	request_max_limit?: number;
	request_reset_duration?: string;
}

export interface ProviderConfigModelBudget {
	model_name: string;
	budgets: ProviderConfigBudgetLine[];
	rate_limit?: ProviderConfigRateLimit;
}

export interface ProviderConfigCardValue {
	providerName: string;
	allowedModels: string[];
	blacklistedModels: string[];
	weight?: number | null;
	keyIds: string[];
	budgets: ProviderConfigBudgetLine[];
	rateLimit?: ProviderConfigRateLimit | null;
	/** Only rendered when `showModelBudgets` is set (Access Profile only). */
	modelBudgets?: ProviderConfigModelBudget[];
}

export interface ProviderKeyInfo {
	key_id: string;
	name: string;
	models?: string[] | null;
}

interface ProviderConfigCardBaseProps {
	value: ProviderConfigCardValue;
	onChange: (next: ProviderConfigCardValue) => void;
	onRemove: () => void;
	providerLabel: string;
	iconProvider: ProviderIconType;
	/** Keys available for this provider. `null` = still loading (keep section visible). */
	providerKeys: ProviderKeyInfo[] | null;
	/** Renders the per-model budgets tree (Access Profile). Off for Virtual Keys. */
	showModelBudgets?: boolean;
	/** Read-only global provider cap shown on the Provider budget row (Access Profile). */
	globalProviderCap?: { max_limit: number; reset_duration?: string };
	/** Prefix for data-testids, e.g. "ap" or "vk". */
	testIdPrefix?: string;
	index: number;
}

// Controlled and uncontrolled expand state are mutually exclusive: a controlled
// `open` must ship its change handler, so a controlled header click can never
// silently no-op. Uncontrolled consumers get `defaultOpen` and internal state.
type ProviderConfigCardOpenProps =
	| { open: boolean; onToggleOpen: () => void; defaultOpen?: never }
	| { open?: never; onToggleOpen?: never; defaultOpen?: boolean };

type ProviderConfigCardProps = ProviderConfigCardBaseProps & ProviderConfigCardOpenProps;

// Clamp the load-balancer weight to the [0, 1] fraction both backends expect.
function clampWeight(n: number | undefined): number | undefined {
	if (n === undefined || Number.isNaN(n)) return undefined;
	return Math.max(0, Math.min(1, n));
}

// Shared wildcard-selection logic for the keys / allowed-models / blocked-models
// pickers: selecting "*" collapses to just ["*"]; adding anything else while "*"
// is present drops the "*"; any other selection passes through unchanged.
function resolveWildcardSelection(current: string[], next: string[]): string[] {
	const hadStar = current.includes("*");
	const hasStar = next.includes("*");
	if (!hadStar && hasStar) return ["*"];
	if (hadStar && hasStar && next.length > 1) return next.filter((v) => v !== "*");
	return next;
}

// Weight input with an internal string buffer so intermediate values like "0."
// survive keystrokes (a bare controlled number input collapses them). Mirrors
// the display-value pattern in NumberAndSelect; clamps to [0, 1] on commit.
function WeightInput({
	id,
	testId,
	value,
	onChange,
}: {
	id: string;
	testId: string;
	value?: number | null;
	onChange: (n: number | undefined) => void;
}) {
	const [display, setDisplay] = useState(value != null ? String(value) : "");
	useEffect(() => {
		const displayNum = display === "" ? undefined : parseFloat(display);
		if ((value ?? undefined) !== displayNum) setDisplay(value != null ? String(value) : "");
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [value]);
	return (
		<input
			id={id}
			type="text"
			inputMode="decimal"
			data-testid={testId}
			value={display}
			placeholder="Exclude from routing"
			onChange={(e) => {
				const cleaned = cleanNumericInput(e.target.value);
				setDisplay(cleaned);
				if (cleaned === "" || cleaned === ".") {
					onChange(undefined);
				} else {
					const n = Number(cleaned);
					if (!Number.isNaN(n)) onChange(clampWeight(n));
				}
			}}
			onBlur={() => {
				const clamped = clampWeight(display === "" || display === "." ? undefined : Number(display));
				setDisplay(clamped != null ? String(clamped) : "");
				onChange(clamped);
			}}
			className="border-input bg-background focus:border-ring placeholder:text-muted-foreground/60 h-9 w-full rounded-sm border px-2 text-sm outline-none"
		/>
	);
}

export function ProviderConfigCard({
	value,
	onChange,
	onRemove,
	providerLabel,
	iconProvider,
	providerKeys,
	showModelBudgets = false,
	globalProviderCap,
	testIdPrefix = "provider",
	index,
	open: controlledOpen,
	onToggleOpen,
	defaultOpen = false,
}: ProviderConfigCardProps) {
	const [internalOpen, setInternalOpen] = useState(defaultOpen);
	const open = controlledOpen ?? internalOpen;
	const toggleOpen = () => (onToggleOpen ? onToggleOpen() : setInternalOpen((o) => !o));

	// Inner collapsibles are always card-local UI state.
	const [budgetOpen, setBudgetOpen] = useState(false);
	const [modelsOpen, setModelsOpen] = useState(false);
	// Keyed by model_name (stable; duplicate names are prevented when adding) so
	// deleting an earlier row can't leave the editor pointing at the wrong model.
	const [openModelEditor, setOpenModelEditor] = useState<string | null>(null);
	const [accessOpen, setAccessOpen] = useState(false);

	const tid = testIdPrefix;
	const update = (patch: Partial<ProviderConfigCardValue>) => onChange({ ...value, ...patch });

	const modelBudgets = value.modelBudgets || [];
	const capLabel = budgetLinesLabel(value.budgets);
	const ws = globalProviderCap;

	// Key scope handed to ModelMultiselect so model suggestions match the keys
	// actually granted on this provider config.
	const keys = providerKeys ?? [];
	const modelKeyScope = value.keyIds.includes("*")
		? keys.map((k) => k.key_id)
		: keys.filter((k) => value.keyIds.includes(k.key_id)).map((k) => k.key_id);

	// Collapsed access summary.
	const keysSummary = value.keyIds.includes("*")
		? "All keys"
		: value.keyIds.length > 0
			? `${value.keyIds.length} key${value.keyIds.length > 1 ? "s" : ""}`
			: "No keys";
	const modelsSummary = value.allowedModels.includes("*")
		? "All models"
		: value.allowedModels.length > 0
			? `${value.allowedModels.length} models`
			: "Deny all";
	const hasRl = value.rateLimit?.token_max_limit != null || value.rateLimit?.request_max_limit != null;
	const rlSummary = hasRl ? "Rate limits set" : "No rate limits";

	return (
		<div className="bg-card overflow-hidden rounded-md border">
			{/* Provider header — summary (name, cap); click expands the card */}
			<div
				role="button"
				tabIndex={0}
				onClick={toggleOpen}
				onKeyDown={(e) => {
					if (e.key === "Enter" || e.key === " ") {
						e.preventDefault();
						toggleOpen();
					}
				}}
				className="hover:bg-accent/30 flex min-w-0 cursor-pointer items-center gap-3 px-4 py-3"
			>
				<RenderProviderIcon provider={iconProvider} size="sm" className="h-4 w-4 shrink-0" />
				<span className="shrink-0 text-sm font-medium whitespace-nowrap">{providerLabel}</span>
				<span className="min-w-0 flex-1" />
				<span className="text-muted-foreground shrink-0 text-sm whitespace-nowrap">{capLabel}</span>
				<button
					type="button"
					onClick={(e) => {
						e.stopPropagation();
						onRemove();
					}}
					aria-label={`Remove provider ${providerLabel}`}
					data-testid={`${tid}-delete-provider-${index}`}
					className="text-muted-foreground shrink-0 cursor-pointer rounded-sm p-1 hover:text-red-400"
				>
					<Trash2 className="h-3 w-3" />
				</button>
				<ChevronDown
					className={cn("text-muted-foreground/60 h-3.5 w-3.5 shrink-0 transition-transform duration-150", open && "rotate-180")}
				/>
			</div>

			{open && (
				<>
					{/* PROVIDER BUDGET — collapsible row */}
					<div
						role="button"
						tabIndex={0}
						onClick={() => setBudgetOpen((o) => !o)}
						onKeyDown={(e) => {
							if (e.key === "Enter" || e.key === " ") {
								e.preventDefault();
								setBudgetOpen((o) => !o);
							}
						}}
						className={cn(
							"hover:bg-accent/30 flex cursor-pointer items-center gap-2.5 border-t py-2.5 pr-3.5 pl-10",
							budgetOpen && "bg-accent/20",
						)}
					>
						<span className="text-muted-foreground/50 text-sm">└</span>
						<span className="text-muted-foreground text-sm font-medium whitespace-nowrap">Provider budget</span>
						<span className="text-muted-foreground min-w-0 flex-1 truncate text-sm">{capLabel}</span>
						{ws != null && (
							<span className="text-muted-foreground/70 shrink-0 text-sm whitespace-nowrap">
								Global provider cap {money(ws.max_limit)}
								{ws.reset_duration ? `/${shortPeriod(ws.reset_duration)}` : ""}
							</span>
						)}
						<ChevronDown
							className={cn("text-muted-foreground/60 h-3.5 w-3.5 shrink-0 transition-transform duration-150", budgetOpen && "rotate-180")}
						/>
					</div>
					{budgetOpen && (
						<div className="bg-muted/30 border-t py-3 pr-3.5 pl-16">
							<MultiBudgetLines
								data-testid={`${tid}-provider-budget-${index}`}
								label="Provider Budget"
								lines={(value.budgets || []).map((b) => ({
									id: b.id,
									max_limit: b.max_limit,
									reset_duration: b.reset_duration || "1M",
									reset_config: b.reset_config,
								}))}
								onChange={(lines: BudgetLineEntry[]) => {
									update({
										budgets: lines.map((l) => ({
											id: l.id,
											max_limit: l.max_limit,
											reset_duration: l.reset_duration,
											reset_config: l.reset_config,
										})),
									});
								}}
							/>
						</div>
					)}

					{/* MODEL BUDGETS tree section — Access Profile only */}
					{showModelBudgets && (
						<>
							<div
								role="button"
								tabIndex={0}
								onClick={() => setModelsOpen((o) => !o)}
								onKeyDown={(e) => {
									if (e.key === "Enter" || e.key === " ") {
										e.preventDefault();
										setModelsOpen((o) => !o);
									}
								}}
								className="hover:bg-accent/30 flex cursor-pointer items-center gap-2.5 border-t py-2.5 pr-3.5 pl-10"
							>
								<span className="text-muted-foreground/50 text-sm">└</span>
								<span className="text-muted-foreground text-sm font-medium whitespace-nowrap">Model budgets</span>
								<span className="text-muted-foreground/60 min-w-0 flex-1 truncate text-sm">
									{modelsOpen
										? "If you skip this, models share the provider budget freely"
										: `${modelBudgets.length} model budget${modelBudgets.length === 1 ? "" : "s"}`}
								</span>
								<ChevronDown
									className={cn(
										"text-muted-foreground/60 h-3.5 w-3.5 shrink-0 transition-transform duration-150",
										modelsOpen && "rotate-180",
									)}
								/>
							</div>

							{modelsOpen &&
								modelBudgets.map((mb, mbIndex) => {
									const mbEditorOpen = openModelEditor === mb.model_name;
									return (
										<div key={mb.model_name}>
											<div
												role="button"
												tabIndex={0}
												onClick={() => setOpenModelEditor((prev) => (prev === mb.model_name ? null : mb.model_name))}
												onKeyDown={(e) => {
													if (e.key === "Enter" || e.key === " ") {
														e.preventDefault();
														setOpenModelEditor((prev) => (prev === mb.model_name ? null : mb.model_name));
													}
												}}
												className={cn(
													"hover:bg-accent/30 flex cursor-pointer items-center gap-2.5 py-2 pr-3.5 pl-16",
													mbEditorOpen && "bg-accent/20",
												)}
											>
												<span className="text-muted-foreground/50 shrink-0 text-sm">└</span>
												<span className={cn("h-[7px] w-[7px] shrink-0 rounded-[2px]", swatchClass(mbIndex))} />
												<span className="text-foreground/90 min-w-0 flex-1 truncate text-sm font-medium">
													{mb.model_name || "Select a model…"}
												</span>
												<span className="text-muted-foreground shrink-0 text-sm whitespace-nowrap">
													{budgetLinesLabel(mb.budgets, "No cap")}
												</span>
												<button
													type="button"
													onClick={(e) => {
														e.stopPropagation();
														update({ modelBudgets: modelBudgets.filter((_, i) => i !== mbIndex) });
														setOpenModelEditor((prev) => (prev === mb.model_name ? null : prev));
													}}
													aria-label={`Remove model budget ${mb.model_name || ""}`}
													className="text-muted-foreground shrink-0 cursor-pointer rounded-sm p-1 hover:text-red-400"
												>
													<Trash2 className="h-3 w-3" />
												</button>
												<ChevronDown
													className={cn(
														"text-muted-foreground/60 h-3.5 w-3.5 shrink-0 transition-transform duration-150",
														mbEditorOpen && "rotate-180",
													)}
												/>
											</div>
											{mbEditorOpen && (
												<div className="bg-muted/30 space-y-4 border-t py-3 pr-3.5 pl-16">
													<MultiBudgetLines
														data-testid={`${tid}-model-budget-lines-${index}-${mbIndex}`}
														label="Model Budget"
														lines={(mb.budgets || []).map((b) => ({
															max_limit: b.max_limit,
															reset_duration: b.reset_duration || "1d",
															reset_config: b.reset_config,
														}))}
														onChange={(lines: BudgetLineEntry[]) => {
															update({
																modelBudgets: modelBudgets.map((m, i) =>
																	i === mbIndex
																		? {
																				...m,
																				budgets: lines.map((l) => ({
																					max_limit: l.max_limit,
																					reset_duration: l.reset_duration,
																					reset_config: l.reset_config,
																				})),
																			}
																		: m,
																),
															});
														}}
													/>
													<NumberAndSelect
														id={`${tid}-modelTokenLimit-${index}-${mbIndex}`}
														dataTestId={`${tid}-modelTokenLimit-${index}-${mbIndex}`}
														labelClassName="font-medium"
														label="Maximum tokens"
														placeholder="No limit"
														value={mb.rate_limit?.token_max_limit}
														selectValue={mb.rate_limit?.token_reset_duration || "1h"}
														onChangeNumber={(v) => {
															update({
																modelBudgets: modelBudgets.map((m, i) =>
																	i === mbIndex
																		? {
																				...m,
																				rate_limit: {
																					...(m.rate_limit || {}),
																					token_max_limit: v,
																					token_reset_duration: m.rate_limit?.token_reset_duration || "1h",
																				},
																			}
																		: m,
																),
															});
														}}
														onChangeSelect={(v) => {
															update({
																modelBudgets: modelBudgets.map((m, i) =>
																	i === mbIndex ? { ...m, rate_limit: { ...(m.rate_limit || {}), token_reset_duration: v } } : m,
																),
															});
														}}
														options={resetDurationOptions}
													/>
													<NumberAndSelect
														id={`${tid}-modelRequestLimit-${index}-${mbIndex}`}
														dataTestId={`${tid}-modelRequestLimit-${index}-${mbIndex}`}
														labelClassName="font-medium"
														label="Maximum requests"
														placeholder="No limit"
														value={mb.rate_limit?.request_max_limit}
														selectValue={mb.rate_limit?.request_reset_duration || "1h"}
														onChangeNumber={(v) => {
															update({
																modelBudgets: modelBudgets.map((m, i) =>
																	i === mbIndex
																		? {
																				...m,
																				rate_limit: {
																					...(m.rate_limit || {}),
																					request_max_limit: v,
																					request_reset_duration: m.rate_limit?.request_reset_duration || "1h",
																				},
																			}
																		: m,
																),
															});
														}}
														onChangeSelect={(v) => {
															update({
																modelBudgets: modelBudgets.map((m, i) =>
																	i === mbIndex ? { ...m, rate_limit: { ...(m.rate_limit || {}), request_reset_duration: v } } : m,
																),
															});
														}}
														options={resetDurationOptions}
													/>
												</div>
											)}
										</div>
									);
								})}

							{modelsOpen && (
								<div className="pt-1 pr-3.5 pb-2.5 pl-16">
									<div className="w-[200px]">
										<ModelMultiselect
											isSingleSelect
											clearable
											hideSearchIcon
											data-testid={`${tid}-add-model-budget-${index}`}
											provider={value.providerName}
											value=""
											onChange={(model: string) => {
												if (!model) return;
												if (modelBudgets.some((m) => m.model_name === model)) return;
												update({
													modelBudgets: [...modelBudgets, { model_name: model, budgets: [{ max_limit: undefined, reset_duration: "1d" }] }],
												});
												setOpenModelEditor(model);
											}}
											placeholder="Add model…"
										/>
									</div>
								</div>
							)}
						</>
					)}

					{/* ACCESS & RATE LIMITS — collapsed to one row */}
					<div
						role="button"
						tabIndex={0}
						onClick={() => setAccessOpen((o) => !o)}
						onKeyDown={(e) => {
							if (e.key === "Enter" || e.key === " ") {
								e.preventDefault();
								setAccessOpen((o) => !o);
							}
						}}
						className="hover:bg-accent/30 flex cursor-pointer items-center gap-2.5 border-t py-2.5 pr-3.5 pl-10"
					>
						<span className="text-muted-foreground/50 text-sm">└</span>
						<span className="text-muted-foreground text-sm font-medium whitespace-nowrap">Access & rate limits</span>
						<span className="text-muted-foreground/60 min-w-0 flex-1 truncate text-sm">
							{keysSummary} · {modelsSummary} · {rlSummary}
						</span>
						<ChevronDown className={cn("text-muted-foreground/60 h-3.5 w-3.5 shrink-0 transition-transform", accessOpen && "rotate-180")} />
					</div>

					{accessOpen && (
						<div className="bg-muted/20 flex flex-col gap-3 border-t py-3 pr-3.5 pl-10">
							<div className="flex items-start gap-3.5">
								{/* Provider key restriction */}
								{(() => {
									type KeyOption = { label: string; value: string; description: string };
									// providerKeys === null while loading/errored — keep the section
									// visible. Only hide when keys have loaded and there are none.
									if (providerKeys !== null && keys.length === 0) return null;
									const configKeyIds = value.keyIds;
									const hasWildcard = configKeyIds.includes("*");
									const allKeyOptions: KeyOption[] = [
										{ label: "Allow All Keys", value: "*", description: "Allow all current and future keys for this provider" },
										...keys.map((key) => ({
											label: key.name,
											value: key.key_id,
											description:
												key.models == null || key.models.includes("*")
													? "All models"
													: key.models.filter((m) => m !== "*").join(", ") || "No models (deny all)",
										})),
									];
									const selectedProviderKeys = hasWildcard
										? [allKeyOptions[0]]
										: keys
												.filter((key) => configKeyIds.includes(key.key_id))
												.map((key) => ({
													label: key.name,
													value: key.key_id,
													description:
														key.models == null || key.models.includes("*")
															? "All models"
															: key.models.filter((m) => m !== "*").join(", ") || "No models (deny all)",
												}));
									return (
										<div className="w-[260px] shrink-0 space-y-1.5">
											<div className="flex h-5 items-center">
												<Label>Provider keys</Label>
											</div>
											<AsyncMultiSelect
												hideSelectedOptions
												hideSearchIcon
												isNonAsync
												closeMenuOnSelect={false}
												menuPlacement="auto"
												defaultOptions={allKeyOptions}
												value={selectedProviderKeys}
												onChange={(picked) => {
													update({
														keyIds: resolveWildcardSelection(
															value.keyIds,
															picked.map((k) => k.value as string),
														),
													});
												}}
												views={{
													multiValue: (multiValueProps: MultiValueProps<KeyOption>) => (
														<div
															{...multiValueProps.innerProps}
															className="bg-accent dark:!bg-card flex cursor-pointer items-center gap-1 rounded-[3px] border px-1.5 py-0.5 text-sm"
														>
															{multiValueProps.data.label}
															<button
																type="button"
																aria-label={`Remove ${multiValueProps.data.label}`}
																className="hover:text-foreground text-muted-foreground flex cursor-pointer items-center"
																onClick={(e) => {
																	e.stopPropagation();
																	multiValueProps.removeProps.onClick?.(e as any);
																}}
															>
																<X className="h-3 w-3" />
															</button>
														</div>
													),
													option: (optionProps: OptionProps<KeyOption>) => {
														const { Option } = components;
														return (
															<Option
																{...optionProps}
																className={cn(
																	"flex w-full cursor-pointer items-center gap-2 rounded-sm px-2 py-2 text-sm",
																	optionProps.isFocused && "bg-accent dark:!bg-card",
																	"hover:bg-accent",
																	optionProps.isSelected && "bg-accent dark:!bg-card",
																)}
															>
																<span className="text-content-primary grow truncate text-sm">{optionProps.data.label}</span>
																{optionProps.data.description && (
																	<span className="text-content-tertiary max-w-[70%] text-sm">{optionProps.data.description}</span>
																)}
															</Option>
														);
													},
												}}
												placeholder={hasWildcard ? "All keys allowed" : configKeyIds.length === 0 ? "No keys selected" : "Select keys..."}
												className="hover:bg-accent w-full"
												menuClassName="z-[60] max-h-[300px] overflow-y-auto w-full cursor-pointer custom-scrollbar"
											/>
										</div>
									);
								})()}

								{/* ALLOWED MODELS — single textbox with an in-dropdown "All Models" option */}
								{(() => {
									const hasWildcardModels = value.allowedModels.includes("*");
									return (
										<div className="min-w-0 flex-1 space-y-1.5">
											<div className="flex h-5 items-center">
												<Label>Allowed models</Label>
											</div>
											<ModelMultiselect
												allowAllOption
												hideSearchIcon
												data-testid={`${tid}-models-multiselect-${index}`}
												provider={value.providerName}
												keys={modelKeyScope}
												value={hasWildcardModels ? ["*"] : value.allowedModels}
												onChange={(models: string[]) => {
													update({ allowedModels: resolveWildcardSelection(value.allowedModels, models) });
												}}
												placeholder={
													hasWildcardModels
														? "All models allowed"
														: value.allowedModels.length === 0
															? "No models (deny all)"
															: "Add model…"
												}
											/>
										</div>
									);
								})()}
							</div>

							{/* Weight + blocked models */}
							<div className="flex items-start gap-3.5">
								<div className="w-[260px] shrink-0 space-y-1.5">
									<div className="flex h-5 items-center">
										<Label htmlFor={`${tid}-weight-${index}`}>Weight</Label>
									</div>
									<WeightInput
										id={`${tid}-weight-${index}`}
										testId={`${tid}-weight-input-${index}`}
										value={value.weight}
										onChange={(n) => update({ weight: n })}
									/>
								</div>
								<div className="min-w-0 flex-1 space-y-1.5">
									<div className="flex h-5 items-center">
										<Label>Blocked models</Label>
									</div>
									{(() => {
										const hasWildcardBlocked = value.blacklistedModels.includes("*");
										return (
											<ModelMultiselect
												allowAllOption
												hideSearchIcon
												data-testid={`${tid}-blocked-models-multiselect-${index}`}
												provider={value.providerName}
												keys={modelKeyScope}
												value={hasWildcardBlocked ? ["*"] : value.blacklistedModels}
												onChange={(models: string[]) => {
													update({ blacklistedModels: resolveWildcardSelection(value.blacklistedModels, models) });
												}}
												placeholder={
													hasWildcardBlocked
														? "All models blocked"
														: value.blacklistedModels.length === 0
															? "No blocked models"
															: "Search models..."
												}
											/>
										);
									})()}
								</div>
							</div>

							{/* Per-provider rate limits */}
							<NumberAndSelect
								id={`${tid}-providerTokenLimit-${index}`}
								dataTestId={`${tid}-providerTokenLimit-${index}`}
								labelClassName="font-medium"
								label="Maximum tokens"
								placeholder="No limit"
								value={value.rateLimit?.token_max_limit}
								selectValue={value.rateLimit?.token_reset_duration || "1h"}
								onChangeNumber={(v) => {
									const current = value.rateLimit || {};
									update({ rateLimit: { ...current, token_max_limit: v, token_reset_duration: current.token_reset_duration || "1h" } });
								}}
								onChangeSelect={(v) => {
									const current = value.rateLimit || {};
									update({ rateLimit: { ...current, token_reset_duration: v } });
								}}
								options={resetDurationOptions}
							/>
							<NumberAndSelect
								id={`${tid}-providerRequestLimit-${index}`}
								dataTestId={`${tid}-providerRequestLimit-${index}`}
								labelClassName="font-medium"
								label="Maximum requests"
								placeholder="No limit"
								value={value.rateLimit?.request_max_limit}
								selectValue={value.rateLimit?.request_reset_duration || "1h"}
								onChangeNumber={(v) => {
									const current = value.rateLimit || {};
									update({
										rateLimit: { ...current, request_max_limit: v, request_reset_duration: current.request_reset_duration || "1h" },
									});
								}}
								onChangeSelect={(v) => {
									const current = value.rateLimit || {};
									update({ rateLimit: { ...current, request_reset_duration: v } });
								}}
								options={resetDurationOptions}
							/>
						</div>
					)}
				</>
			)}
		</div>
	);
}