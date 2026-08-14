import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Input } from "@/components/ui/input";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
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
					<SelectItem value="inherit">使用密钥设置</SelectItem>
					<SelectItem value="on">On</SelectItem>
					<SelectItem value="off">关闭</SelectItem>
				</SelectContent>
			</Select>
		</div>
	);
}

function AzureSection({ config, onChange, disabled }: ProviderSectionProps) {
	return (
		<div className="space-y-4">
			<SectionHeader
				title="Azure 覆盖"
				description="为此部署覆盖密钥级别的 Azure 默认值。留空使用密钥的设置。"
			/>
			<FieldRow label="API 版本" hint="覆盖 Azure OpenAI 的 api-version 查询参数。">
				<StringField
					value={config.api_version}
					onChange={(v) => onChange({ api_version: v })}
					placeholder="2024-10-21"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label="Anthropic 版本" hint="为 Azure 上的 Claude 部署覆盖 anthropic-version 请求头。">
				<StringField
					value={config.anthropic_version}
					onChange={(v) => onChange({ anthropic_version: v })}
					placeholder="2023-06-01"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label="端点" hint="将此部署指向与密钥默认值不同的 Azure 资源。">
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
	return (
		<div className="space-y-4">
			<SectionHeader
				title="Vertex 覆盖"
				description="为此部署覆盖密钥级别的 Vertex 默认值。留空使用密钥的设置。"
			/>
			<FieldRow label="项目 ID">
				<SecretVarField
					value={config.project_id}
					onChange={(v) => onChange({ project_id: v })}
					placeholder="gcp-project-id 或 env.VERTEX_PROJECT_ID"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label="项目编号" hint="微调模型必填。">
				<SecretVarField
					value={config.project_number}
					onChange={(v) => onChange({ project_number: v })}
					placeholder="123456789 或 env.VERTEX_PROJECT_NUMBER"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label="区域" hint="除非启用强制单区域，否则仅多区域模型会自动路由到多区域端点。">
				<SecretVarField
					value={config.region}
					onChange={(v) => onChange({ region: v })}
					placeholder="us-central1 or env.VERTEX_REGION"
					disabled={disabled}
				/>
			</FieldRow>
			<div className="flex items-start justify-between gap-4 rounded-md border p-3">
				<div className="space-y-0.5">
					<label className="text-sm font-medium">强制单区域</label>
					<p className="text-muted-foreground text-xs">按原样调用上述区域，跳过仅多区域模型的跨区域提升。用于预置吞吐量。</p>
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
	return (
		<div className="space-y-4">
			<SectionHeader
				title="Bedrock 覆盖"
				description="为此部署覆盖密钥级别的 Bedrock 默认值。留空使用密钥的设置。"
			/>
			<FieldRow label="区域">
				<SecretVarField
					value={config.region}
					onChange={(v) => onChange({ region: v })}
					placeholder="us-east-1 或 env.BEDROCK_REGION"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label="推理配置文件 ARN" hint="要调用的跨区域推理配置文件 ARN（代替模型 ID）。">
				<SecretVarField
					value={config.inference_profile_arn}
					onChange={(v) => onChange({ inference_profile_arn: v })}
					placeholder="arn:aws:bedrock:us-east-1:123:inference-profile/... 或 env.BEDROCK_PROFILE_ARN"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow
				label="项目 ID"
				hint="通过 OpenAI-Project 请求头将此部署的 Bedrock Mantle（gpt-*/Gemma）调用限定到特定项目。留空使用密钥的项目。"
			>
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
	return (
		<div className="space-y-4">
			<SectionHeader
				title="Bedrock Mantle 覆盖"
				description="为此部署覆盖密钥级别的 Bedrock Mantle 默认值。留空使用密钥的设置。"
			/>
			<FieldRow label="区域">
				<SecretVarField
					value={config.region}
					onChange={(v) => onChange({ region: v })}
					placeholder="us-east-1 或 env.BEDROCK_REGION"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow
				label="项目 ID"
				hint="通过 OpenAI-Project / anthropic-workspace-id 请求头将此部署限定到特定项目。留空使用密钥的项目。"
			>
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
	return (
		<div className="space-y-4">
			<SectionHeader title="Replicate 覆盖" description="为此部署覆盖密钥级别的 Replicate 默认值。" />
			<TriStateOverrideRow
				label="使用部署端点"
				hint="通过 Replicate 的部署端点路由，而不是模型端点。"
				value={config.use_deployments_endpoint}
				onChange={(next) => onChange({ use_deployments_endpoint: next })}
				disabled={disabled}
				testId="deployment-use-deployments-endpoint"
			/>
		</div>
	);
}

function UseAnthropicEndpointsToggleSection({ config, onChange, disabled, providerName }: ProviderSectionProps & { providerName: string }) {
	return (
		<div className="space-y-4">
			<SectionHeader title={`${providerName} overrides`} description={`Override key-level ${providerName} defaults for this deployment.`} />
			<TriStateOverrideRow
				label="使用 Anthropic 端点"
				hint="通过 Anthropic 兼容端点路由对话补全和响应请求。"
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
	return (
		<div className="space-y-6 border-t p-4">
			<div className="space-y-4">
				<FieldRow label="规范模型名称" hint="用于路由和价格的规范名称。留空默认为模型 ID。">
					<StringField
						value={config.model_name}
						onChange={(v) => onChange({ model_name: v })}
						placeholder="例如 claude-sonnet-4-5"
						disabled={disabled}
					/>
				</FieldRow>
				<FieldRow label="模型系列" hint="强制用于路由决策的模型系列。留空则从模型名称推导。">
					<Select
						value={config.model_family ?? "__none__"}
						onValueChange={(v) => onChange({ model_family: v === "__none__" ? undefined : (v as ModelFamily) })}
						disabled={disabled}
					>
						<SelectTrigger className="w-full">
							<SelectValue placeholder="选择模型系列" />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="__none__">无</SelectItem>
							{ModelFamilyValues.map((f) => (
								<SelectItem key={f} value={f}>
									{f}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				</FieldRow>
				<FieldRow label="描述" hint="给用户的备注。Bifrost 不使用。">
					<Textarea
						value={config.description ?? ""}
						onChange={(e) => {
							const v = e.target.value;
							onChange({ description: v === "" ? undefined : v });
						}}
						placeholder="此部署用于什么？"
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
				<div>部署名称</div>
				<div>模型 ID</div>
				<span className="sr-only">操作</span>
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
											placeholder="请求模型名称"
											disabled={disabled}
											data-testid={`deployment-name-${row.name}`}
										/>
										{pending !== undefined && (
											<p className="text-destructive text-xs">
												{pending.trim() === "" ? "Name cannot be empty." : "A deployment with this name already exists."}
											</p>
										)}
									</div>
									<ModelMultiselect
										isSingleSelect
										provider={providerName}
										value={row.config.model_id}
										onChange={(v) => patchConfig(row.name, { model_id: typeof v === "string" ? v : "" })}
										placeholder="部署 / 配置文件 / 资源 ID"
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
								placeholder="请求模型名称"
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
								placeholder="部署 / 配置文件 / 资源 ID"
								disabled={disabled}
								unfiltered={true}
								data-testid="draft-deployment-model"
							/>
							<div />
						</div>
						{(draftRow.name.trim() !== "" || draftRow.config.model_id.trim() !== "") &&
							!(draftRow.name.trim() && draftRow.config.model_id.trim()) && (
								<p className="text-muted-foreground px-4 pb-2 text-xs">部署名称和模型 ID 均为必填；两者都填写后此行才会保存。</p>
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