import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { EnvVarInput } from "@/components/ui/envVarInput";
import { Input } from "@/components/ui/input";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { AliasConfig, ModelFamily, ModelFamilyValues } from "@/lib/types/config";
import { EnvVar } from "@/lib/types/schemas";
import { cn } from "@/lib/utils";
import { ChevronDown, ChevronRight, Trash } from "lucide-react";
import { useMemo, useState } from "react";

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

const emptyEnvVar: EnvVar = { value: "", env_var: "", from_env: false };
const isEmptyEnvVar = (v: EnvVar | undefined): boolean => !v || (!v.value && !v.env_var);

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

function EnvVarField({
	value,
	onChange,
	placeholder,
	disabled,
}: {
	value: EnvVar | undefined;
	onChange: (next: EnvVar | undefined) => void;
	placeholder?: string;
	disabled?: boolean;
}) {
	return (
		<EnvVarInput
			value={value ?? emptyEnvVar}
			onChange={(next) => onChange(isEmptyEnvVar(next) ? undefined : next)}
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

function AzureSection({ config, onChange, disabled }: ProviderSectionProps) {
	return (
		<div className="space-y-4">
			<SectionHeader
				title="Azure overrides"
				description="Override key-level Azure defaults for this deployment. Leave blank to use the key's settings."
			/>
			<FieldRow label="API version" hint="Override the Azure OpenAI api-version query parameter.">
				<StringField
					value={config.api_version}
					onChange={(v) => onChange({ api_version: v })}
					placeholder="2024-10-21"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label="Anthropic version" hint="Override the anthropic-version header for Claude-on-Azure deployments.">
				<StringField
					value={config.anthropic_version}
					onChange={(v) => onChange({ anthropic_version: v })}
					placeholder="2023-06-01"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label="Endpoint" hint="Point this deployment at a different Azure resource than the key default.">
				<EnvVarField
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
				title="Vertex overrides"
				description="Override key-level Vertex defaults for this deployment. Leave blank to use the key's settings."
			/>
			<FieldRow label="Project ID">
				<EnvVarField
					value={config.project_id}
					onChange={(v) => onChange({ project_id: v })}
					placeholder="gcp-project-id or env.VERTEX_PROJECT_ID"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label="Project number" hint="Required for fine-tuned models.">
				<EnvVarField
					value={config.project_number}
					onChange={(v) => onChange({ project_number: v })}
					placeholder="123456789 or env.VERTEX_PROJECT_NUMBER"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label="Region">
				<EnvVarField
					value={config.region}
					onChange={(v) => onChange({ region: v })}
					placeholder="us-central1 or env.VERTEX_REGION"
					disabled={disabled}
				/>
			</FieldRow>
		</div>
	);
}

function BedrockSection({ config, onChange, disabled }: ProviderSectionProps) {
	return (
		<div className="space-y-4">
			<SectionHeader
				title="Bedrock overrides"
				description="Override key-level Bedrock defaults for this deployment. Leave blank to use the key's settings."
			/>
			<FieldRow label="Region">
				<EnvVarField
					value={config.region}
					onChange={(v) => onChange({ region: v })}
					placeholder="us-east-1 or env.BEDROCK_REGION"
					disabled={disabled}
				/>
			</FieldRow>
			<FieldRow label="Inference profile ARN" hint="Cross-region inference profile ARN to invoke instead of the model ID.">
				<EnvVarField
					value={config.inference_profile_arn}
					onChange={(v) => onChange({ inference_profile_arn: v })}
					placeholder="arn:aws:bedrock:us-east-1:123:inference-profile/... or env.BEDROCK_PROFILE_ARN"
					disabled={disabled}
				/>
			</FieldRow>
		</div>
	);
}

function ReplicateSection({ config, onChange, disabled }: ProviderSectionProps) {
	return (
		<div className="space-y-4">
			<SectionHeader
				title="Replicate overrides"
				description="Override key-level Replicate defaults for this deployment."
			/>
			<div className="flex items-start justify-between gap-4 rounded-md border p-3">
				<div className="space-y-0.5">
					<label className="text-sm font-medium">Use deployments endpoint</label>
					<p className="text-muted-foreground text-xs">
						Route through Replicate&apos;s deployments endpoint instead of the models endpoint.
					</p>
				</div>
				<Switch
					checked={config.use_deployments_endpoint ?? false}
					onCheckedChange={(checked) => onChange({ use_deployments_endpoint: checked ? true : undefined })}
					disabled={disabled}
				/>
			</div>
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
		case "replicate":
			return <ReplicateSection {...props} />;
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
				<FieldRow
					label="Canonical model name"
					hint="The canonical name used for routing and pricing. Defaults to the model ID when blank."
				>
					<StringField
						value={config.model_name}
						onChange={(v) => onChange({ model_name: v })}
						placeholder="e.g. claude-sonnet-4-5"
						disabled={disabled}
					/>
				</FieldRow>
				<FieldRow label="Model family" hint="Forces the family used for routing decisions. Derived from model name when left blank.">
					<Select
						value={config.model_family ?? ""}
						onValueChange={(v) => onChange({ model_family: v as ModelFamily })}
						disabled={disabled}
					>
						<SelectTrigger className="w-full">
							<SelectValue placeholder="Select a model family" />
						</SelectTrigger>
						<SelectContent>
							{ModelFamilyValues.map((f) => (
								<SelectItem key={f} value={f}>
									{f}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				</FieldRow>
				<FieldRow label="Description" hint="Note for users. Not used by Bifrost.">
					<Textarea
						value={config.description ?? ""}
						onChange={(e) => {
							const v = e.target.value;
							onChange({ description: v === "" ? undefined : v });
						}}
						placeholder="What is this deployment used for?"
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

	const [expanded, setExpanded] = useState<Set<number>>(new Set());
	const [draftExpanded, setDraftExpanded] = useState(false);
	const [draftRow, setDraftRow] = useState<Row>({ name: "", config: { model_id: "" } });

	const emit = (nextRows: Row[]) => {
		const out: Record<string, AliasConfig> = {};
		for (const r of nextRows) {
			if (!r.name.trim()) continue;
			out[r.name] = r.config;
		}
		onChange(out);
	};

	const updateRow = (idx: number, patch: Partial<Row>) => {
		const next = rows.map((r, i) => (i === idx ? { name: patch.name ?? r.name, config: patch.config ?? r.config } : r));
		emit(next);
	};

	const patchConfig = (idx: number, patch: Partial<AliasConfig>) => {
		updateRow(idx, { config: { ...rows[idx].config, ...patch } });
	};

	const removeRow = (idx: number) => {
		emit(rows.filter((_, i) => i !== idx));
		setExpanded((prev) => {
			const out = new Set<number>();
			for (const v of prev) {
				if (v < idx) out.add(v);
				else if (v > idx) out.add(v - 1);
			}
			return out;
		});
	};

	const renameRow = (idx: number, newName: string) => {
		const next = rows.map((r, i) => (i === idx ? { name: newName, config: r.config } : r));
		emit(next);
	};

	const patchDraftConfig = (patch: Partial<AliasConfig>) => {
		setDraftRow((r) => ({ ...r, config: { ...r.config, ...patch } }));
	};

	// Commit the in-progress draft row into the committed list. Called from
	// blur / Enter / model-selection. No-op when either field is missing; the
	// schema's submit-time validation surfaces a partial draft as an error.
	const commitDraftIfReady = (override?: Row) => {
		const candidate = override ?? draftRow;
		const name = candidate.name.trim();
		const modelId = candidate.config.model_id.trim();
		if (!name || !modelId) return;
		if (normalized[name] !== undefined) return;
		emit([...rows, { name, config: candidate.config }]);
		setDraftRow({ name: "", config: { model_id: "" } });
		setDraftExpanded(false);
	};

	const toggleExpanded = (idx: number) => {
		setExpanded((prev) => {
			const next = new Set(prev);
			if (next.has(idx)) next.delete(idx);
			else next.add(idx);
			return next;
		});
	};

	return (
		<div className="overflow-hidden rounded-md border">
			<div className="bg-muted/50 text-foreground grid h-10 grid-cols-[28px_1fr_1fr_28px] items-center gap-2 border-b px-4 text-sm font-medium">
				<div />
				<div>Deployment name</div>
				<div>Model ID</div>
				<span className="sr-only">Actions</span>
			</div>
			<div className="divide-y">
				{rows.map((row, idx) => {
					const isOpen = expanded.has(idx);
					return (
						<Collapsible key={idx} open={isOpen} onOpenChange={() => toggleExpanded(idx)}>
							<div className={cn(isOpen && "bg-muted/20")}>
								<div className="grid grid-cols-[28px_1fr_1fr_28px] items-center gap-2 px-2 py-1.5">
									<CollapsibleTrigger asChild>
										<Button variant="ghost" size="icon" className="h-7 w-7" disabled={disabled}>
											{isOpen ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
										</Button>
									</CollapsibleTrigger>
									<Input
										value={row.name}
										onChange={(e) => renameRow(idx, e.target.value)}
										placeholder="Request model name"
										disabled={disabled}
									/>
									<ModelMultiselect
										isSingleSelect
										provider={providerName}
										value={row.config.model_id}
										onChange={(v) => patchConfig(idx, { model_id: typeof v === "string" ? v : "" })}
										placeholder="Deployment / profile / resource ID"
										disabled={disabled}
										unfiltered={true}
									/>
									<Button
										type="button"
										variant="ghost"
										size="icon"
										className="text-muted-foreground hover:text-destructive h-7 w-7"
										onClick={() => removeRow(idx)}
										disabled={disabled}
									>
										<Trash className="h-4 w-4" />
									</Button>
								</div>
								<CollapsibleContent>
									<ExpandedConfigPanel
										config={row.config}
										onChange={(patch) => patchConfig(idx, patch)}
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
								<Button variant="ghost" size="icon" className="h-7 w-7" disabled={disabled}>
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
								placeholder="Request model name"
								disabled={disabled}
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
								placeholder="Deployment / profile / resource ID"
								disabled={disabled}
								unfiltered={true}
							/>
							<div />
						</div>
						<CollapsibleContent>
							<ExpandedConfigPanel
								config={draftRow.config}
								onChange={patchDraftConfig}
								providerName={providerName}
								disabled={disabled}
							/>
						</CollapsibleContent>
					</div>
				</Collapsible>
			</div>
		</div>
	);
}
