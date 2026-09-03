import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Input } from "@/components/ui/input";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { useT } from "@/lib/i18n";
import { AliasConfig, ModelFamily, ModelFamilyValues } from "@/lib/types/config";
import { SecretVar } from "@/lib/types/schemas";
import { cn } from "@/lib/utils";
import { ChevronDown, ChevronRight, Trash } from "lucide-react";
import { useId, useMemo, useRef, useState } from "react";

type DeploymentsValue = Record<string, AliasConfig> | undefined | null;

interface Props {
	value: DeploymentsValue;
	onChange: (next: Record<string, AliasConfig>) => void;
	providerName: string;
	disabled?: boolean;
}

interface Row {
	name: string;
	config: AliasConfig;
}

// Normalize legacy shapes (Record<string, string> from older configs or stringified JSON)
// into the rich Record<string, AliasConfig> the component operates on.
function normalize(value: DeploymentsValue): Record<string, AliasConfig> {
	if (value == null) {
		return {};
	}
	if (typeof value === "string") {
		try {
			const parsed = JSON.parse(value);
			return normalize(parsed);
		} catch {
			return {};
		}
	}
	if (typeof value !== "object" || Array.isArray(value)) {
		return {};
	}
	const out: Record<string, AliasConfig> = {};
	for (const [k, v] of Object.entries(value)) {
		if (typeof v === "string") {
			out[k] = { model_id: v };
		} else if (v && typeof v === "object") {
			const cfg = v as Partial<AliasConfig>;
			out[k] = { ...cfg, model_id: typeof cfg.model_id === "string" ? cfg.model_id : "" };
		}
	}
	return out;
}

const emptySecretVar: SecretVar = { value: "", ref: "" };
const isEmptySecretVar = (v: SecretVar | undefined): boolean => !v || (!v.value && !v.ref);

function FieldRow({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
	return (
		<div className="space-y-1.5">
			<label className="text-sm font-medium">{label}</label>
			{children}
			{hint && <p className="text-muted-foreground text-xs">{hint}</p>}
		</div>
	);
}

function SectionHeader({ title, description }: { title: string; description?: string }) {
	return (
		<div className="border-b pb-2">
			<h4 className="text-sm font-semibold">{title}</h4>
			{description && <p className="text-muted-foreground mt-0.5 text-xs">{description}</p>}
		</div>
	);
}

function SecretVarField({
	value,
	onChange,
	placeholder,
	disabled,
}: {
	value: SecretVar | undefined;
	onChange: (next: SecretVar | undefined) => void;
	placeholder?: string;
	disabled?: boolean;
}) {
	return (
		<SecretVarInput
			value={value ?? emptySecretVar}
			onChange={(next) => onChange(isEmptySecretVar(next) ? undefined : next)}
			placeholder={placeholder}
			disabled={disabled}
		/>
	);
}

function StringField({
	value,
	onChange,
	placeholder,
	disabled,
}: {
	value: string | undefined;
	onChange: (next: string | undefined) => void;
	placeholder?: string;
	disabled?: boolean;
}) {
	return (
		<Input
			value={value ?? ""}
			onChange={(e) => onChange(e.target.value === "" ? undefined : e.target.value)}
			placeholder={placeholder}
			disabled={disabled}
		/>
	);
}

interface ProviderSectionProps {
	config: AliasConfig;
	onChange: (patch: Partial<AliasConfig>) => void;
	disabled?: boolean;
}

// Three-way control for boolean overrides that inherit a key-level toggle when
// unset: undefined = use the key's setting, true/false = explicit override. A
// plain switch can't express "explicitly off while the key-level toggle is on".
function TriStateOverrideRow({
	label,
	hint,
	value,
	onChange,
	disabled,
	testId,
}: {
	label: string;
	hint: string;
	value: boolean | undefined;
	onChange: (next: boolean | undefined) => void;
	disabled?: boolean;
	testId?: string;
}) {
	const t = useT();
	const id = useId();
	const hintId = `${id}-hint`;
	const selectValue = value === undefined ? "inherit" : value ? "on" : "off";
	return (
		<div className="flex items-start justify-between gap-4 rounded-md border p-3">
			<div className="space-y-0.5">
				<label htmlFor={id} className="text-sm font-medium">
					{label}
				</label>
				<p id={hintId} className="text-muted-foreground text-xs">
					{hint}
				</p>
			</div>
			<Select value={selectValue} onValueChange={(v) => onChange(v === "inherit" ? undefined : v === "on")} disabled={disabled}>
				<SelectTrigger id={id} aria-describedby={hintId} className="w-fit min-w-44 shrink-0" data-testid={testId}>
					<SelectValue />
				</SelectTrigger>
				<SelectContent>
					<SelectItem value="inherit">{t("providers.deployment.inherit")}</SelectItem>
					<SelectItem value="on">{t("providers.deployment.on")}</SelectItem>
					<SelectItem value="off">{t("providers.deployment.off")}</SelectItem>
				</SelectContent>
			</Select>
		</div>
	);
}

function AzureSection({ config, onChange, disabled }: ProviderSectionProps) {
	const t = useT();
	return (
		<div className="space-y-4">
			<SectionHeader title={t("providers.azureOverrides")} description={t("providers.deployment.azureHint")} />
			<FieldRow label={t("providers.deployment.apiVersion")} hint={t("providers.deployment.apiVersionHint")}>
				<StringField
					value={config.api_version}
					onChange={(v) => onChange({ api_version: v })}
					placeholder="2024-10-21"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployment.anthropicVersion")} hint={t("providers.deployment.anthropicVersionHint")}>
				<StringField
					value={config.anthropic_version}
					onChange={(v) => onChange({ anthropic_version: v })}
					placeholder="2023-06-01"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployment.endpoint")} hint={t("providers.deployment.endpointHint")}>
				<SecretVarField
					value={config.endpoint}
					onChange={(v) => onChange({ endpoint: v })}
					placeholder="https://your-resource.openai.azure.com or env.AZURE_ENDPOINT"
					disabled={disabled}
				/>
			</FieldRow>
		</div>
	);
}

function VertexSection({ config, onChange, disabled }: ProviderSectionProps) {
	const t = useT();
	return (
		<div className="space-y-4">
			<SectionHeader title={t("providers.deployment.vertexOverrides")} description={t("providers.deployment.vertexHint")} />
			<FieldRow label={t("providers.deployment.projectId")}>
				<SecretVarField
					value={config.project_id}
					onChange={(v) => onChange({ project_id: v })}
					placeholder="gcp-project-id or env.VERTEX_PROJECT_ID"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployment.projectNumber")} hint={t("providers.deployment.projectNumberHint")}>
				<SecretVarField
					value={config.project_number}
					onChange={(v) => onChange({ project_number: v })}
					placeholder="123456789 or env.VERTEX_PROJECT_NUMBER"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployment.region")} hint={t("providers.deployment.vertexRegionHint")}>
				<SecretVarField
					value={config.region}
					onChange={(v) => onChange({ region: v })}
					placeholder="us-central1 or env.VERTEX_REGION"
					disabled={disabled}
				/>
			</FieldRow>
			<div className="flex items-start justify-between gap-4 rounded-md border p-3">
				<div className="space-y-0.5">
					<label className="text-sm font-medium">{t("providers.deployment.forceSingleRegion")}</label>
					<p className="text-muted-foreground text-xs">{t("providers.deployment.forceSingleRegionHint")}</p>
				</div>
				<Switch
					checked={config.force_single_region ?? false}
					onCheckedChange={(checked) => onChange({ force_single_region: checked })}
					disabled={disabled}
				/>
			</div>
		</div>
	);
}

function BedrockSection({ config, onChange, disabled }: ProviderSectionProps) {
	const t = useT();
	return (
		<div className="space-y-4">
			<SectionHeader title={t("providers.deployment.bedrockOverrides")} description={t("providers.deployment.bedrockHint")} />
			<FieldRow label={t("providers.deployment.region")}>
				<SecretVarField
					value={config.region}
					onChange={(v) => onChange({ region: v })}
					placeholder="us-east-1 or env.BEDROCK_REGION"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployment.inferenceProfileArn")} hint={t("providers.deployment.inferenceProfileArnHint")}>
				<SecretVarField
					value={config.inference_profile_arn}
					onChange={(v) => onChange({ inference_profile_arn: v })}
					placeholder="arn:aws:bedrock:us-east-1:123:inference-profile/... or env.BEDROCK_PROFILE_ARN"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployment.projectId")} hint={t("providers.deployment.bedrockProjectHint")}>
				<SecretVarField
					value={config.project_id}
					onChange={(v) => onChange({ project_id: v })}
					placeholder="proj_xxxxxxxx or env.BEDROCK_PROJECT_ID"
					disabled={disabled}
				/>
			</FieldRow>
		</div>
	);
}

function BedrockMantleSection({ config, onChange, disabled }: ProviderSectionProps) {
	const t = useT();
	return (
		<div className="space-y-4">
			<SectionHeader title={t("providers.deployment.bedrockMantleOverrides")} description={t("providers.deployment.bedrockMantleHint")} />
			<FieldRow label={t("providers.deployment.region")}>
				<SecretVarField
					value={config.region}
					onChange={(v) => onChange({ region: v })}
					placeholder="us-east-1 or env.BEDROCK_REGION"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployment.projectId")} hint={t("providers.deployment.bedrockMantleProjectHint")}>
				<SecretVarField
					value={config.project_id}
					onChange={(v) => onChange({ project_id: v })}
					placeholder="proj_xxxxxxxx or env.BEDROCK_PROJECT_ID"
					disabled={disabled}
				/>
			</FieldRow>
		</div>
	);
}

function ReplicateSection({ config, onChange, disabled }: ProviderSectionProps) {
	const t = useT();
	return (
		<div className="space-y-4">
			<SectionHeader title={t("providers.deployment.replicateOverrides")} description={t("providers.deployment.replicateHint")} />
			<TriStateOverrideRow
				label={t("providers.deployment.useDeploymentsEndpoint")}
				hint={t("providers.deployment.useDeploymentsEndpointHint")}
				value={config.use_deployments_endpoint}
				onChange={(next) => onChange({ use_deployments_endpoint: next })}
				disabled={disabled}
				testId="deployment-use-deployments-endpoint"
			/>
		</div>
	);
}

function UseAnthropicEndpointsToggleSection({ config, onChange, disabled, providerName }: ProviderSectionProps & { providerName: string }) {
	const t = useT();
	return (
		<div className="space-y-4">
			<SectionHeader
				title={t("providers.deployment.providerOverrides", { provider: providerName })}
				description={t("providers.deployment.providerOverridesHint", { provider: providerName })}
			/>
			<TriStateOverrideRow
				label={t("providers.deployment.useAnthropicEndpoints")}
				hint={t("providers.deployment.useAnthropicEndpointsHint")}
				value={config.use_anthropic_endpoints}
				onChange={(next) => onChange({ use_anthropic_endpoints: next })}
				disabled={disabled}
				testId="deployment-use-anthropic-endpoints"
			/>
		</div>
	);
}

function ProviderSection({ providerName, ...props }: ProviderSectionProps & { providerName: string }) {
	switch (providerName) {
		case "azure":
			return <AzureSection {...props} />;
		case "vertex":
			return <VertexSection {...props} />;
		case "bedrock":
			return <BedrockSection {...props} />;
		case "bedrock_mantle":
			return <BedrockMantleSection {...props} />;
		case "replicate":
			return <ReplicateSection {...props} />;
		case "sgl":
			return <UseAnthropicEndpointsToggleSection providerName="SGLang" {...props} />;
		case "deepseek":
			return <UseAnthropicEndpointsToggleSection providerName="Deepseek" {...props} />;
		case "fireworks":
			return <UseAnthropicEndpointsToggleSection providerName="Fireworks" {...props} />;
		case "vllm":
			return <UseAnthropicEndpointsToggleSection providerName="vLLM" {...props} />;
		default:
			return null;
	}
}

function ExpandedConfigPanel({
	config,
	onChange,
	providerName,
	disabled,
}: {
	config: AliasConfig;
	onChange: (patch: Partial<AliasConfig>) => void;
	providerName: string;
	disabled?: boolean;
}) {
	const t = useT();
	return (
		<div className="space-y-6 border-t p-4">
			<div className="space-y-4">
				<FieldRow label={t("providers.deployment.canonicalName")} hint={t("providers.deployment.canonicalNameHint")}>
					<StringField
						value={config.model_name}
						onChange={(v) => onChange({ model_name: v })}
						placeholder={t("providers.deployment.canonicalPlaceholder")}
						disabled={disabled}
					/>
				</FieldRow>
				<FieldRow label={t("providers.deployment.modelFamily")} hint={t("providers.deployment.modelFamilyHint")}>
					<Select
						value={config.model_family ?? "__none__"}
						onValueChange={(v) => onChange({ model_family: v === "__none__" ? undefined : (v as ModelFamily) })}
						disabled={disabled}
					>
						<SelectTrigger className="w-full">
							<SelectValue placeholder={t("providers.selectModelFamily")} />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="__none__">{t("providers.deployment.none")}</SelectItem>
							{ModelFamilyValues.map((f) => (
								<SelectItem key={f} value={f}>
									{f}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				</FieldRow>
				<FieldRow label={t("common.description")} hint={t("providers.deployment.descriptionHint")}>
					<Textarea
						value={config.description ?? ""}
						onChange={(e) => {
							const v = e.target.value;
							onChange({ description: v === "" ? undefined : v });
						}}
						placeholder={t("providers.deployment.descriptionPlaceholder")}
						rows={2}
						disabled={disabled}
					/>
				</FieldRow>
			</div>
			<ProviderSection providerName={providerName} config={config} onChange={onChange} disabled={disabled} />
		</div>
	);
}

export function DeploymentsTable({ value, onChange, providerName, disabled = false }: Props) {
	const t = useT();
	const normalized = useMemo(() => normalize(value), [value]);
	const rows: Row[] = useMemo(() => Object.entries(normalized).map(([name, config]) => ({ name, config })), [normalized]);

	// Stable per-row id keyed by current deployment name. Survives rename so
	// expanded/pendingNames state stays attached to the same row, and gives
	// React a stable list key instead of array index.
	const rowIdsRef = useRef<Map<string, string>>(new Map());
	const ensureRowId = (name: string): string => {
		const existing = rowIdsRef.current.get(name);
		if (existing) return existing;
		const id =
			typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
				? crypto.randomUUID()
				: `row-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
		rowIdsRef.current.set(name, id);
		return id;
	};
	const rowsWithIds = useMemo(() => rows.map((r) => ({ ...r, rowId: ensureRowId(r.name) })), [rows]);

	const [expanded, setExpanded] = useState<Set<string>>(new Set());
	const [draftExpanded, setDraftExpanded] = useState(false);
	const [draftRow, setDraftRow] = useState<Row>({ name: "", config: { model_id: "" } });
	// Per-row pending rename state, keyed by stable rowId. Keeps the input
	// controllable while a typed name collides with another committed row or is
	// empty — without this, we'd either snap the input back (jarring) or emit a
	// duplicate and silently drop a row.
	const [pendingNames, setPendingNames] = useState<Record<string, string>>({});

	const emit = (nextRows: Row[]) => {
		const out: Record<string, AliasConfig> = {};
		for (const r of nextRows) {
			if (!r.name.trim()) continue;
			out[r.name] = r.config;
		}
		onChange(out);
	};

	const updateRowByName = (oldName: string, patch: Partial<Row>) => {
		const next = rows.map((r) => (r.name === oldName ? { name: patch.name ?? r.name, config: patch.config ?? r.config } : r));
		emit(next);
	};

	const patchConfig = (name: string, patch: Partial<AliasConfig>) => {
		const current = rows.find((r) => r.name === name);
		if (!current) return;
		updateRowByName(name, { config: { ...current.config, ...patch } });
	};

	const removeRow = (rowId: string, name: string) => {
		emit(rows.filter((r) => r.name !== name));
		rowIdsRef.current.delete(name);
		setExpanded((prev) => {
			if (!prev.has(rowId)) return prev;
			const next = new Set(prev);
			next.delete(rowId);
			return next;
		});
		setPendingNames((prev) => {
			if (!(rowId in prev)) return prev;
			const { [rowId]: _drop, ...rest } = prev;
			return rest;
		});
	};

	const renameRow = (rowId: string, oldName: string, newName: string) => {
		const trimmed = newName.trim();
		const normalizedName = trimmed.toLowerCase();
		// Backend alias validation is case-insensitive and rejects leading/trailing
		// whitespace, so collision detection mirrors that to avoid UI-passes /
		// server-rejects splits.
		const collides = normalizedName !== "" && rows.some((r) => r.name !== oldName && r.name.trim().toLowerCase() === normalizedName);
		if (collides || trimmed === "") {
			setPendingNames((p) => ({ ...p, [rowId]: newName }));
			return;
		}
		setPendingNames((p) => {
			if (!(rowId in p)) return p;
			const { [rowId]: _drop, ...rest } = p;
			return rest;
		});
		// Transfer the stable rowId from old name → new name so per-row UI state
		// (expanded, pendingNames) survives the rename.
		rowIdsRef.current.delete(oldName);
		rowIdsRef.current.set(trimmed, rowId);
		const next = rows.map((r) => (r.name === oldName ? { name: trimmed, config: r.config } : r));
		emit(next);
	};

	const patchDraftConfig = (patch: Partial<AliasConfig>) => {
		setDraftRow((r) => ({ ...r, config: { ...r.config, ...patch } }));
	};

	// Commit the in-progress draft row into the committed list. Called from
	// blur / Enter / model-selection. No-op when either field is missing — the
	// inline hint below the draft row warns the user before submit that a
	// partial entry will be dropped.
	const commitDraftIfReady = (override?: Row) => {
		const candidate = override ?? draftRow;
		const name = candidate.name.trim();
		const modelId = candidate.config.model_id.trim();
		if (!name || !modelId) return;
		const exists = Object.keys(normalized).some((k) => k.trim().toLowerCase() === name.toLowerCase());
		if (exists) return;
		emit([...rows, { name, config: { ...candidate.config, model_id: modelId } }]);
		setDraftRow({ name: "", config: { model_id: "" } });
		setDraftExpanded(false);
	};

	const toggleExpanded = (rowId: string) => {
		setExpanded((prev) => {
			const next = new Set(prev);
			if (next.has(rowId)) next.delete(rowId);
			else next.add(rowId);
			return next;
		});
	};

	return (
		<div className="overflow-hidden rounded-md border">
			<div className="bg-muted/50 text-foreground grid h-10 grid-cols-[28px_1fr_1fr_28px] items-center gap-2 border-b px-4 text-sm font-medium">
				<div />
				<div>{t("providers.deployment.deploymentName")}</div>
				<div>{t("providers.deployment.modelId")}</div>
				<span className="sr-only">{t("common.actions")}</span>
			</div>
			<div className="divide-y">
				{rowsWithIds.map((row) => {
					const isOpen = expanded.has(row.rowId);
					const pending = pendingNames[row.rowId];
					return (
						<Collapsible key={row.rowId} open={isOpen} onOpenChange={() => toggleExpanded(row.rowId)}>
							<div className={cn(isOpen && "bg-muted/20")}>
								<div className="grid grid-cols-[28px_1fr_1fr_28px] items-center gap-2 px-2 py-1.5">
									<CollapsibleTrigger asChild>
										<Button
											variant="ghost"
											size="icon"
											className="h-7 w-7"
											disabled={disabled}
											data-testid={`deployment-expand-${row.name}`}
										>
											{isOpen ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
										</Button>
									</CollapsibleTrigger>
									<div className="space-y-1">
										<Input
											value={pending ?? row.name}
											onChange={(e) => renameRow(row.rowId, row.name, e.target.value)}
											placeholder={t("providers.deployment.requestModelName")}
											disabled={disabled}
											data-testid={`deployment-name-${row.name}`}
										/>
										{pending !== undefined && (
											<p className="text-destructive text-xs">
												{pending.trim() === "" ? t("providers.deployment.nameEmpty") : t("providers.deployment.nameExists")}
											</p>
										)}
									</div>
									<ModelMultiselect
										isSingleSelect
										provider={providerName}
										value={row.config.model_id}
										onChange={(v) => patchConfig(row.name, { model_id: typeof v === "string" ? v : "" })}
										placeholder={t("providers.deployment.modelIdPlaceholder")}
										disabled={disabled}
										unfiltered={true}
										data-testid={`deployment-model-${row.name}`}
									/>
									<Button
										type="button"
										variant="ghost"
										size="icon"
										className="text-muted-foreground hover:text-destructive h-7 w-7"
										onClick={() => removeRow(row.rowId, row.name)}
										disabled={disabled}
										data-testid={`deployment-delete-${row.name}`}
									>
										<Trash className="h-4 w-4" />
									</Button>
								</div>
								<CollapsibleContent>
									<ExpandedConfigPanel
										config={row.config}
										onChange={(patch) => patchConfig(row.name, patch)}
										providerName={providerName}
										disabled={disabled}
									/>
								</CollapsibleContent>
							</div>
						</Collapsible>
					);
				})}
				<Collapsible open={draftExpanded} onOpenChange={setDraftExpanded}>
					<div className={cn(draftExpanded && "bg-muted/20")}>
						<div className="grid grid-cols-[28px_1fr_1fr_28px] items-center gap-2 px-2 py-1.5">
							<CollapsibleTrigger asChild>
								<Button variant="ghost" size="icon" className="h-7 w-7" disabled={disabled} data-testid="draft-deployment-expand">
									{draftExpanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
								</Button>
							</CollapsibleTrigger>
							<Input
								value={draftRow.name}
								onChange={(e) => setDraftRow((r) => ({ ...r, name: e.target.value }))}
								onBlur={() => commitDraftIfReady()}
								onKeyDown={(e) => {
									if (e.key === "Enter") {
										e.preventDefault();
										commitDraftIfReady();
									}
								}}
								placeholder={t("providers.deployment.requestModelName")}
								disabled={disabled}
								data-testid="draft-deployment-name"
							/>
							<ModelMultiselect
								isSingleSelect
								provider={providerName}
								value={draftRow.config.model_id}
								onChange={(v) => {
									const modelId = typeof v === "string" ? v : "";
									const nextDraft = { ...draftRow, config: { ...draftRow.config, model_id: modelId } };
									setDraftRow(nextDraft);
									commitDraftIfReady(nextDraft);
								}}
								placeholder={t("providers.deployment.modelIdPlaceholder")}
								disabled={disabled}
								unfiltered={true}
								data-testid="draft-deployment-model"
							/>
							<div />
						</div>
						{(draftRow.name.trim() !== "" || draftRow.config.model_id.trim() !== "") &&
							!(draftRow.name.trim() && draftRow.config.model_id.trim()) && (
								<p className="text-muted-foreground px-4 pb-2 text-xs">{t("providers.deployment.draftIncomplete")}</p>
							)}
						<CollapsibleContent>
							<ExpandedConfigPanel config={draftRow.config} onChange={patchDraftConfig} providerName={providerName} disabled={disabled} />
						</CollapsibleContent>
					</div>
				</Collapsible>
			</div>
		</div>
	);
}