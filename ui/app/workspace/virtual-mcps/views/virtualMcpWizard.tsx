import { getExternalBaseUrl } from "@/app/workspace/mcp-registry/views/mcpUsageGuide/utils";
import { ExpandableText } from "@/components/expandableText";
import { InfoBox } from "@/components/infoBox";
import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { useToast } from "@/hooks/use-toast";
import {
	getErrorMessage,
	useAttachVirtualMCPVirtualKeyMutation,
	useCreateVirtualMCPMutation,
	useGetCoreConfigQuery,
	useGetMCPClientsQuery,
} from "@/lib/store";
import { VirtualMCPToolSpec } from "@/lib/types/virtualMcps";
import { ArrowLeft, ArrowRight, Check, Copy, ExternalLink, Link2, Loader2 } from "lucide-react";
import { useMemo, useState } from "react";
import VirtualMCPAccessTab from "./virtualMcpAccessTab";
import VirtualMCPGeneralTab, { slugify } from "./virtualMcpGeneralTab";
import VirtualMcpToolsEditor from "./virtualMcpToolsEditor";

const DOCS_URL = "https://docs.getbifrost.ai/mcp/connections";

const STEPS = [
	{ key: "general", title: "General", description: "Name your Virtual MCP and choose the endpoint it is served at." },
	{ key: "tools", title: "Tools", description: "Pick which of your MCP servers' tools this Virtual MCP exposes." },
	{ key: "access", title: "Access", description: "Assign virtual keys that can reach this Virtual MCP." },
	{ key: "review", title: "Review", description: "Confirm the details, then create the Virtual MCP." },
] as const;

interface VirtualMCPWizardProps {
	onCancel: () => void;
	// Called when the wizard is fully done (created + assigned), returning to the list.
	onDone: () => void;
}

export default function VirtualMCPWizard({ onCancel, onDone }: VirtualMCPWizardProps) {
	const { toast } = useToast();
	const [createVirtualMCP, { isLoading: creating }] = useCreateVirtualMCPMutation();
	const [attachVk] = useAttachVirtualMCPVirtualKeyMutation();

	const [stepIdx, setStepIdx] = useState(0);
	const [name, setName] = useState("");
	const [endpointSlug, setEndpointSlug] = useState("");
	const [description, setDescription] = useState("");
	const [enabled, setEnabled] = useState(true);
	const [tools, setTools] = useState<VirtualMCPToolSpec[]>([]);
	const [assignedVkIds, setAssignedVkIds] = useState<string[]>([]);
	const [submitting, setSubmitting] = useState(false);
	const [createdId, setCreatedId] = useState<number | null>(null);
	const [attachedVkIds, setAttachedVkIds] = useState<string[]>([]);

	const step = STEPS[stepIdx];
	const isFirst = stepIdx === 0;
	const isLast = stepIdx === STEPS.length - 1;

	const canAdvance = useMemo(() => {
		if (step.key === "general") return name.trim().length > 0;
		return true;
	}, [step.key, name]);

	const goBack = () => setStepIdx((i) => Math.max(0, i - 1));

	const handleCreate = async () => {
		setSubmitting(true);
		try {
			// Create only once; a retry after a partial failure reuses the existing record.
			let id = createdId;
			if (id == null) {
				const record = await createVirtualMCP({
					name: name.trim(),
					endpoint_slug: endpointSlug.trim() || undefined,
					description: description.trim() || undefined,
					enabled,
					tools,
				}).unwrap();
				id = record.id;
				setCreatedId(id);
			}
			// Attach staged virtual keys (attach is a per-key endpoint); skip any already attached.
			const attached = new Set(attachedVkIds);
			for (const vkId of assignedVkIds) {
				if (attached.has(vkId)) continue;
				await attachVk({ id, vkId }).unwrap();
				attached.add(vkId);
				setAttachedVkIds([...attached]);
			}
			toast({ title: "Virtual MCP created" });
			onDone();
		} catch (err) {
			toast({ title: "Failed to create Virtual MCP", description: getErrorMessage(err), variant: "destructive" });
		} finally {
			setSubmitting(false);
		}
	};

	const handleNext = () => {
		if (isLast) {
			void handleCreate();
			return;
		}
		setStepIdx((i) => Math.min(STEPS.length - 1, i + 1));
	};

	return (
		<div className="flex min-h-full min-w-0 flex-col gap-2 overflow-visible md:h-full md:flex-row md:overflow-hidden">
			{/* Keep the topbar title stable, matching the list view (else it falls back to a route-derived label). */}
			<PageTitle title="Virtual MCPs">Bundle tools from your MCP servers into a single endpoint, then assign it to virtual keys.</PageTitle>
			{/* Step rail */}
			<div className="bg-card flex h-auto w-full shrink-0 flex-row gap-2 overflow-x-auto rounded-b-md px-1 py-3 md:h-full md:w-[240px] md:flex-col md:overflow-x-visible md:rounded-r-md md:rounded-b-none">
				{STEPS.map((s, i) => (
					<button
						key={s.key}
						type="button"
						onClick={() => i < stepIdx && setStepIdx(i)}
						disabled={i >= stepIdx}
						aria-current={i === stepIdx ? "step" : undefined}
						data-testid={`virtual-mcp-wizard-step-${s.key}`}
						className={`flex w-auto shrink-0 items-center gap-3 rounded-sm px-3 py-1.5 text-left transition-colors md:w-full ${
							i < stepIdx ? "hover:bg-muted cursor-pointer" : ""
						}`}
					>
						<span
							className={`flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-xs font-medium transition-colors ${
								i === stepIdx
									? "bg-primary text-primary-foreground"
									: i < stepIdx
										? "bg-primary/20 text-primary"
										: "bg-muted text-muted-foreground"
							}`}
						>
							{i < stepIdx ? <Check className="h-4 w-4" /> : i + 1}
						</span>
						<span className={`text-sm font-medium ${i === stepIdx ? "text-foreground" : "text-muted-foreground"}`}>{s.title}</span>
					</button>
				))}
			</div>

			{/* Content */}
			<ScrollArea
				className="bg-card min-w-0 grow overflow-visible rounded-t-md md:h-full md:overflow-hidden md:rounded-l-md md:rounded-tr-none"
				viewportClassName="no-table !h-auto !overflow-visible md:!h-full md:!overflow-x-hidden md:!overflow-y-scroll viewport-table-height-full"
			>
				<div className="mx-auto flex min-h-full w-full flex-col gap-6">
					<div className="bg-card z-10 mx-auto w-full max-w-4xl px-4 pt-4">
						<h2 className="text-lg font-semibold">{step.title}</h2>
						<p className="text-muted-foreground text-sm">{step.description}</p>
					</div>

					<div className="mx-auto w-full max-w-4xl grow px-4">
						{step.key === "general" && (
							<VirtualMCPGeneralTab
								name={name}
								setName={setName}
								endpointSlug={endpointSlug}
								setEndpointSlug={setEndpointSlug}
								description={description}
								setDescription={setDescription}
								enabled={enabled}
								setEnabled={setEnabled}
								isCreate
							/>
						)}
						{step.key === "tools" && <VirtualMcpToolsEditor value={tools} onChange={setTools} />}
						{step.key === "access" && (
							<VirtualMCPAccessTab vmcpId={0} value={assignedVkIds} onChange={setAssignedVkIds} isCreate stagedCreate active />
						)}
						{step.key === "review" && (
							<ReviewStep
								name={name}
								endpointSlug={endpointSlug}
								description={description}
								enabled={enabled}
								tools={tools}
								assignedVkIds={assignedVkIds}
							/>
						)}
					</div>

					{/* Footer */}
					<div className="bg-card sticky bottom-0 z-10 mt-2 border-t">
						<div className="mx-auto flex w-full max-w-4xl flex-wrap items-center justify-between gap-3 px-4 py-3">
							<Button type="button" variant="ghost" onClick={isFirst ? onCancel : goBack} disabled={submitting}>
								<ArrowLeft className="h-4 w-4" />
								Back
							</Button>
							<Button
								type="button"
								onClick={handleNext}
								disabled={!canAdvance || submitting || creating}
								data-testid="virtual-mcp-wizard-next"
							>
								{submitting ? (
									<>
										<Loader2 className="h-4 w-4 animate-spin" />
										Creating...
									</>
								) : isLast ? (
									<>Create Virtual MCP</>
								) : (
									<>
										Next
										<ArrowRight className="h-4 w-4" />
									</>
								)}
							</Button>
						</div>
					</div>
				</div>
			</ScrollArea>
		</div>
	);
}

interface ReviewStepProps {
	name: string;
	endpointSlug: string;
	description: string;
	enabled: boolean;
	tools: VirtualMCPToolSpec[];
	assignedVkIds: string[];
}

function ReviewStep({ name, endpointSlug, description, enabled, tools, assignedVkIds }: ReviewStepProps) {
	// The endpoint the server will serve; when the slug field is blank it is derived from the name.
	const previewSlug = slugify(endpointSlug.trim() || name);
	const { data: coreConfig } = useGetCoreConfigQuery({ fromDB: true });
	const endpoint = `${getExternalBaseUrl(coreConfig?.client_config)}/mcp/${previewSlug}`;
	const { data: clientsData } = useGetMCPClientsQuery({ limit: 1000 });
	const nameById = new Map((clientsData?.clients ?? []).map((c) => [c.config.client_id, c.config.name]));
	const { copy, copied } = useCopyToClipboard({ successMessage: "Endpoint copied" });
	const rows: { label: string; value: React.ReactNode }[] = [
		{ label: "Name", value: name.trim() || <span className="text-muted-foreground">—</span> },
		{ label: "Endpoint", value: <span className="font-mono text-sm">/mcp/{previewSlug || "—"}</span> },
		{
			label: "Description",
			value: description.trim() ? (
				<ExpandableText text={description.trim()} toggleAlign="right" />
			) : (
				<span className="text-muted-foreground">—</span>
			),
		},
		{ label: "Status", value: enabled ? "Enabled" : "Disabled" },
		{
			label: "Servers",
			value: tools.length === 0 ? <span className="text-muted-foreground">None</span> : `${tools.length} configured`,
		},
		{
			label: "Virtual keys",
			value: assignedVkIds.length === 0 ? <span className="text-muted-foreground">None</span> : `${assignedVkIds.length} assigned`,
		},
	];

	return (
		<div className="flex flex-col gap-4">
			<div className="overflow-hidden rounded-md border">
				{rows.map((r, i) => (
					<div key={r.label} className={`flex items-start gap-4 p-3 ${i > 0 ? "border-t" : ""}`}>
						<span className="text-muted-foreground w-32 shrink-0 text-sm">{r.label}</span>
						<div className="min-w-0 flex-1 text-sm font-medium break-words">{r.value}</div>
					</div>
				))}
			</div>
			{tools.length > 0 && (
				<div className="flex flex-col gap-2">
					<span className="text-muted-foreground text-xs font-medium uppercase">Tools</span>
					<div className="overflow-hidden rounded-md border">
						{tools.map((spec, i) => {
							const all = spec.tool_names.includes("*");
							return (
								<div key={spec.mcp_client_id} className={`flex items-start gap-4 p-3 ${i > 0 ? "border-t" : ""}`}>
									<span className="w-32 shrink-0 truncate text-sm" title={nameById.get(spec.mcp_client_id) ?? spec.mcp_client_id}>
										{nameById.get(spec.mcp_client_id) ?? spec.mcp_client_id}
									</span>
									<span className="text-muted-foreground min-w-0 flex-1 text-sm">
										{all ? "All tools" : `${spec.tool_names.length} tools`}
									</span>
								</div>
							);
						})}
					</div>
				</div>
			)}

			<InfoBox
				className="mt-2"
				icon={<Link2 className="size-4" />}
				title="Endpoint URL"
				subcontent={
					<a
						href={DOCS_URL}
						target="_blank"
						rel="noreferrer"
						className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs"
					>
						<ExternalLink className="size-3.5" />
						Connect Claude Code, Cursor, Codex and more
					</a>
				}
			>
				<div className="flex flex-wrap items-center gap-x-1.5 gap-y-1">
					<span className="text-muted-foreground">Once created, this Virtual MCP will be available at:</span>
					<code className="text-foreground font-mono break-all" data-testid="virtual-mcp-review-endpoint">
						{endpoint}
					</code>
					<Button
						type="button"
						variant="ghost"
						size="icon"
						className="text-muted-foreground h-5 w-5 shrink-0"
						aria-label="Copy endpoint URL"
						onClick={() => copy(endpoint)}
					>
						{copied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
					</Button>
				</div>
			</InfoBox>
		</div>
	);
}