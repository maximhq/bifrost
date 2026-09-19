import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Input } from "@/components/ui/input";
import { ModelSelector } from "@/components/ui/modelSelector";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { AliasConfig, ModelFamily, ModelFamilyValues } from "@/lib/types/config";
import { SecretVar } from "@/lib/types/schemas";
import { cn } from "@/lib/utils";
import { ChevronDown, ChevronRight, Trash } from "lucide-react";
import { useId, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

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
	const { t } = useTranslation("models");
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
					<SelectItem value="inherit">{t("providers.deployments.useKeySetting")}</SelectItem>
					<SelectItem value="on">{t("providers.deployments.on")}</SelectItem>
					<SelectItem value="off">{t("providers.deployments.off")}</SelectItem>
				</SelectContent>
			</Select>
		</div>
	);
}

function AzureSection({ config, onChange, disabled }: ProviderSectionProps) {
	const { t } = useTranslation("models");
	return (
		<div className="space-y-4">
			<SectionHeader
				title={t("providers.deployments.azureOverrides")}
				description={t("providers.deployments.overrideDefaults", { provider: "Azure" })}
			/>
			<FieldRow label={t("providers.deployments.apiVersion")} hint={t("providers.deployments.apiVersionHint")}>
				<StringField
					value={config.api_version}
					onChange={(v) => onChange({ api_version: v })}
					placeholder="2024-10-21"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployments.anthropicVersion")} hint={t("providers.deployments.anthropicVersionHint")}>
				<StringField
					value={config.anthropic_version}
					onChange={(v) => onChange({ anthropic_version: v })}
					placeholder="2023-06-01"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployments.endpoint")} hint={t("providers.deployments.endpointHint")}>
				<SecretVarField
					value={config.endpoint}
					onChange={(v) => onChange({ endpoint: v })}
					placeholder={t("providers.deployments.azureEndpointPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
		</div>
	);
}

function VertexSection({ config, onChange, disabled }: ProviderSectionProps) {
	const { t } = useTranslation("models");
	return (
		<div className="space-y-4">
			<SectionHeader
				title={t("providers.deployments.vertexOverrides")}
				description={t("providers.deployments.overrideDefaults", { provider: "Vertex" })}
			/>
			<FieldRow label={t("providers.deployments.projectId")}>
				<SecretVarField
					value={config.project_id}
					onChange={(v) => onChange({ project_id: v })}
					placeholder={t("providers.deployments.vertexProjectIdPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployments.projectNumber")} hint={t("providers.deployments.projectNumberHint")}>
				<SecretVarField
					value={config.project_number}
					onChange={(v) => onChange({ project_number: v })}
					placeholder={t("providers.deployments.vertexProjectNumberPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployments.region")} hint={t("providers.deployments.regionHint")}>
				<SecretVarField
					value={config.region}
					onChange={(v) => onChange({ region: v })}
					placeholder={t("providers.deployments.vertexRegionPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
			<div className="flex items-start justify-between gap-4 rounded-md border p-3">
				<div className="space-y-0.5">
					<label className="text-sm font-medium">{t("providers.keys.forceSingleRegion")}</label>
					<p className="text-muted-foreground text-xs">{t("providers.deployments.forceSingleRegionHint")}</p>
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
	const { t } = useTranslation("models");
	return (
		<div className="space-y-4">
			<SectionHeader
				title={t("providers.deployments.bedrockOverrides")}
				description={t("providers.deployments.overrideDefaults", { provider: "Bedrock" })}
			/>
			<FieldRow label={t("providers.deployments.region")}>
				<SecretVarField
					value={config.region}
					onChange={(v) => onChange({ region: v })}
					placeholder={t("providers.deployments.bedrockRegionPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployments.inferenceProfileArn")} hint={t("providers.deployments.inferenceProfileArnHint")}>
				<SecretVarField
					value={config.inference_profile_arn}
					onChange={(v) => onChange({ inference_profile_arn: v })}
					placeholder={t("providers.deployments.profileArnPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployments.projectId")} hint={t("providers.deployments.bedrockProjectHint")}>
				<SecretVarField
					value={config.project_id}
					onChange={(v) => onChange({ project_id: v })}
					placeholder={t("providers.deployments.projectIdPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
		</div>
	);
}

function BedrockMantleSection({ config, onChange, disabled }: ProviderSectionProps) {
	const { t } = useTranslation("models");
	return (
		<div className="space-y-4">
			<SectionHeader
				title={t("providers.deployments.bedrockMantleOverrides")}
				description={t("providers.deployments.overrideDefaults", { provider: "Bedrock Mantle" })}
			/>
			<FieldRow label={t("providers.deployments.region")}>
				<SecretVarField
					value={config.region}
					onChange={(v) => onChange({ region: v })}
					placeholder={t("providers.deployments.bedrockRegionPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label={t("providers.deployments.projectId")} hint={t("providers.deployments.mantleProjectHint")}>
				<SecretVarField
					value={config.project_id}
					onChange={(v) => onChange({ project_id: v })}
					placeholder={t("providers.deployments.projectIdPlaceholder")}
					disabled={disabled}
				/>
			</FieldRow>
		</div>
	);
}

function ReplicateSection({ config, onChange, disabled }: ProviderSectionProps) {
	const { t } = useTranslation("models");
	return (
		<div className="space-y-4">
			<SectionHeader
				title={t("providers.deployments.replicateOverrides")}
				description={t("providers.deployments.overrideDefaultsShort", { provider: "Replicate" })}
			/>
			<TriStateOverrideRow
				label={t("providers.deployments.useDeploymentsEndpoint")}
				hint={t("providers.deployments.useDeploymentsEndpointHint")}
				value={config.use_deployments_endpoint}
				onChange={(next) => onChange({ use_deployments_endpoint: next })}
				disabled={disabled}
				testId="deployment-use-deployments-endpoint"
			/>
		</div>
	);
}

function UseAnthropicEndpointsToggleSection({ config, onChange, disabled, providerName }: ProviderSectionProps & { providerName: string }) {
	const { t } = useTranslation("models");
	return (
		<div className="space-y-4">
			<SectionHeader
				title={t("providers.deployments.providerOverrides", { provider: providerName })}
				description={t("providers.deployments.overrideDefaultsShort", { provider: providerName })}
			/>
			<TriStateOverrideRow
				label={t("providers.deployments.useAnthropicEndpoints")}
				hint={t("providers.deployments.useAnthropicEndpointsHint")}
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
	const { t } = useTranslation("models");
	return (
		<div className="space-y-6 border-t p-4">
			<div className="space-y-4">
				<FieldRow label={t("providers.deployments.canonicalModelName")} hint={t("providers.deployments.canonicalModelNameHint")}>
					<StringField
						value={config.model_name}
						onChange={(v) => onChange({ model_name: v })}
						placeholder={t("providers.deployments.canonicalPlaceholder")}
						disabled={disabled}
					/>
				</FieldRow>
				<FieldRow label={t("providers.deployments.modelFamily")} hint={t("providers.deployments.modelFamilyHint")}>
					<Select
						value={config.model_family ?? "__none__"}
						onValueChange={(v) => onChange({ model_family: v === "__none__" ? undefined : (v as ModelFamily) })}
						disabled={disabled}
					>
						<SelectTrigger className="w-full">
							<SelectValue placeholder={t("providers.deployments.selectModelFamily")} />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="__none__">{t("virtualKeys.none")}</SelectItem>
							{ModelFamilyValues.map((f) => (
								<SelectItem key={f} value={f}>
									{f}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				</FieldRow>
				<FieldRow label={t("modelCatalog.description")} hint={t("providers.deployments.descriptionHint")}>
					<Textarea
						value={config.description ?? ""}
						onChange={(e) => {
							const v = e.target.value;
							onChange({ description: v === "" ? undefined : v });
						}}
						placeholder={t("providers.deployments.descriptionPlaceholder")}
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
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
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
			<div className="bg-muted/50 text-foreground grid h-10 grid-cols-[28px_minmax(0,1fr)_minmax(0,1fr)_28px] items-center gap-2 border-b px-4 text-sm font-medium">
				<div />
				<div>{t("providers.deployments.deploymentName")}</div>
				<div>{t("providers.deployments.modelId")}</div>
				<span className="sr-only">{tc("actions")}</span>
			</div>
			<div className="divide-y">
				{rowsWithIds.map((row) => {
					const isOpen = expanded.has(row.rowId);
					const pending = pendingNames[row.rowId];
					return (
						<Collapsible key={row.rowId} open={isOpen} onOpenChange={() => toggleExpanded(row.rowId)}>
							<div className={cn(isOpen && "bg-muted/20")}>
								<div className="grid grid-cols-[28px_minmax(0,1fr)_minmax(0,1fr)_28px] items-center gap-2 px-2 py-1.5">
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
											placeholder={t("providers.deployments.requestModelName")}
											disabled={disabled}
											data-testid={`deployment-name-${row.name}`}
										/>
										{pending !== undefined && (
											<p className="text-destructive text-xs">
												{pending.trim() === "" ? t("providers.deployments.nameEmpty") : t("providers.deployments.nameExists")}
											</p>
										)}
									</div>
									<ModelSelector
										provider={providerName}
										value={row.config.model_id}
										onChange={(v) => patchConfig(row.name, { model_id: v })}
										placeholder={t("providers.deployments.deploymentProfileId")}
										disabled={disabled}
										unfiltered
										allowCustomModel
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
						<div className="grid grid-cols-[28px_minmax(0,1fr)_minmax(0,1fr)_28px] items-center gap-2 px-2 py-1.5">
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
								placeholder={t("providers.deployments.requestModelName")}
								disabled={disabled}
								data-testid="draft-deployment-name"
							/>
							<ModelSelector
								provider={providerName}
								value={draftRow.config.model_id}
								onChange={(modelId) => {
									const nextDraft = { ...draftRow, config: { ...draftRow.config, model_id: modelId } };
									setDraftRow(nextDraft);
									commitDraftIfReady(nextDraft);
								}}
								placeholder={t("providers.deployments.deploymentProfileId")}
								disabled={disabled}
								unfiltered
								allowCustomModel
								data-testid="draft-deployment-model"
							/>
							<div />
						</div>
						{(draftRow.name.trim() !== "" || draftRow.config.model_id.trim() !== "") &&
							!(draftRow.name.trim() && draftRow.config.model_id.trim()) && (
								<p className="text-muted-foreground px-4 pb-2 text-xs">
									{t("providers.deployments.draftIncomplete")}
								</p>
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