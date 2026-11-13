import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import {
	getErrorMessage,
	useCreateTraceProjectMutation,
	useCreateTracingKeyMutation,
	useDeleteTracingKeyMutation,
	useGetTraceConnectInfoQuery,
	useGetTraceStatsQuery,
	useGetTracingKeysQuery,
	useUpdateTracingKeyMutation,
} from "@/lib/store";
import type { TraceProject, TraceSource, TracingKey } from "@/lib/types/traces";
import { CheckCircle2, Copy, Loader2, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { SOURCE_LABELS } from "./format";

const KEY_PLACEHOLDER = "<your tracing key>";

/** What to paste to point each SDK at Bifrost, and the one line that links gateway calls to the trace. */
function snippets(endpoint: string, gateway: string, key: string): Record<TraceSource, { setup: string; link: string }> {
	return {
		braintrust: {
			setup: `export BRAINTRUST_APP_URL="${endpoint}"\nexport BRAINTRUST_API_KEY="${key}"\n# BRAINTRUST_ORG_NAME must not be set`,
			link: `# Pass the current span on each call you send through Bifrost\nclient.chat.completions.create(\n    ...,\n    extra_headers={"x-bt-parent": braintrust.current_span().export()},\n)`,
		},
		langfuse: {
			setup: `export LANGFUSE_BASE_URL="${endpoint}"   # LANGFUSE_HOST on older SDKs\nexport LANGFUSE_SECRET_KEY="${key}"\nexport LANGFUSE_PUBLIC_KEY="pk-lf-bifrost"`,
			link: `client.chat.completions.create(\n    ...,\n    extra_headers={\n        "x-bf-trace-id": langfuse.get_current_trace_id(),\n        "x-bf-parent-span-id": langfuse.get_current_observation_id(),\n    },\n)`,
		},
		arize: {
			setup: `# Phoenix\nfrom phoenix.otel import register\nregister(endpoint="${endpoint}/v1/traces", headers={"authorization": "${key}"},\n         project_name="my-app", batch=True)\n\n# Arize AX: HTTP transport, any non-empty space id\nfrom arize.otel import register, Transport\nregister(space_id="bifrost", api_key="${key}", project_name="my-app",\n         endpoint="${endpoint}/v1/traces", transport=Transport.HTTP)`,
			link: `# OpenInference does not propagate trace context to the LLM endpoint.\n# Add the ids of the current span to calls you send through Bifrost:\nctx = trace.get_current_span().get_span_context()\nextra_headers={\n    "x-bf-trace-id": format(ctx.trace_id, "032x"),\n    "x-bf-parent-span-id": format(ctx.span_id, "016x"),\n}`,
		},
		otel: {
			setup: `export OTEL_EXPORTER_OTLP_ENDPOINT="${endpoint}"\nexport OTEL_EXPORTER_OTLP_HEADERS="authorization=${key}"\nexport OTEL_EXPORTER_OTLP_PROTOCOL="http/protobuf"`,
			link: `# With an instrumented HTTP client, traceparent reaches Bifrost on its own.\n# Send your LLM calls to ${gateway} and the link is made.`,
		},
	};
}

function Snippet({ code, testId }: { code: string; testId: string }) {
	const { copy } = useCopyToClipboard();
	return (
		<div className="relative">
			<pre className="bg-muted/50 overflow-auto rounded-md border p-3 pr-10 text-xs" data-testid={testId}>
				{code}
			</pre>
			<Button
				variant="ghost"
				size="icon"
				className="absolute top-1 right-1"
				aria-label="Copy"
				data-testid={`${testId}-copy-button`}
				onClick={() => copy(code)}
			>
				<Copy className="h-3.5 w-3.5" />
			</Button>
		</div>
	);
}

/**
 * The Connect panel: create a tracing key, see the URL each SDK is pointed at, and watch for the
 * first trace to arrive.
 */
export function ConnectPanel({
	open,
	onOpenChange,
	projects,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	projects: TraceProject[];
}) {
	const { t } = useTranslation("observability");
	const { data: connect } = useGetTraceConnectInfoQuery(undefined, { skip: !open });
	const { data: keysData } = useGetTracingKeysQuery(undefined, { skip: !open });
	// Polled while the panel is open: the count turning non-zero is the "first trace arrived" signal.
	const { data: stats } = useGetTraceStatsQuery({}, { skip: !open, pollingInterval: open ? 3000 : 0 });
	const [createKey, { isLoading: creatingKey }] = useCreateTracingKeyMutation();
	const [updateKey] = useUpdateTracingKeyMutation();
	const [deleteKey] = useDeleteTracingKeyMutation();
	const [createProject] = useCreateTraceProjectMutation();
	const { copy } = useCopyToClipboard();

	const [keyName, setKeyName] = useState("");
	const [projectName, setProjectName] = useState("");
	const [canCreateProjects, setCanCreateProjects] = useState(true);
	const [logContent, setLogContent] = useState(true);
	const [newKeyValue, setNewKeyValue] = useState<string | null>(null);

	const keys = keysData?.keys ?? [];
	const gateway = connect?.base_url ?? "";
	const all = useMemo(() => {
		const sources: TraceSource[] = ["braintrust", "langfuse", "arize", "otel"];
		return sources.map((source) => ({
			source,
			...snippets(connect?.endpoints?.[source] ?? "", gateway, newKeyValue ?? KEY_PLACEHOLDER)[source],
		}));
	}, [connect, gateway, newKeyValue]);

	const handleCreateKey = async () => {
		try {
			let projectIds = projects.map((project) => project.id);
			let defaultProjectId: string | undefined;
			if (projectName.trim()) {
				const created = await createProject({ name: projectName.trim() }).unwrap();
				projectIds = [created.project.id];
				defaultProjectId = created.project.id;
			} else if (projectIds.length === 1) {
				defaultProjectId = projectIds[0];
			}
			const result = await createKey({
				name: keyName.trim() || "default",
				project_ids: projectIds,
				default_project_id: defaultProjectId,
				can_create_projects: canCreateProjects,
				log_content: logContent,
			}).unwrap();
			setNewKeyValue(result.value);
			setKeyName("");
			setProjectName("");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const toggleKey = async (key: TracingKey) => {
		try {
			await updateKey({
				id: key.id,
				data: {
					name: key.name,
					project_ids: key.project_ids ?? [],
					default_project_id: key.default_project_id,
					virtual_key_ids: key.virtual_key_ids,
					log_content: key.log_content,
					can_create_projects: key.can_create_projects,
					enabled: !key.enabled,
				},
			}).unwrap();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const received = (stats?.stats.trace_count ?? 0) > 0;
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="max-h-[88vh] overflow-auto sm:max-w-3xl" data-testid="tracing-connect-panel">
				<DialogHeader>
					<DialogTitle>{t("tracing.connect.title")}</DialogTitle>
					<DialogDescription>{t("tracing.connect.description")}</DialogDescription>
				</DialogHeader>

				<section className="space-y-3">
					<h3 className="text-sm font-medium">{t("tracing.connect.keys")}</h3>
					<p className="text-muted-foreground text-xs">{t("tracing.connect.keysHint")}</p>
					{keys.length > 0 && (
						<div className="divide-y rounded-md border">
							{keys.map((key) => (
								<div key={key.id} className="flex items-center gap-3 px-3 py-2 text-sm" data-testid={`tracing-key-row-${key.id}`}>
									<span className="font-medium">{key.name}</span>
									<code className="text-muted-foreground text-xs">{key.key_prefix}…</code>
									{!key.log_content && <Badge variant="secondary">{t("tracing.connect.noContent")}</Badge>}
									<span className="text-muted-foreground ml-auto text-xs">
										{key.last_used_at
											? t("tracing.connect.lastUsed", { when: new Date(key.last_used_at).toLocaleString() })
											: t("tracing.connect.neverUsed")}
									</span>
									<Switch
										checked={key.enabled}
										onCheckedChange={() => toggleKey(key)}
										aria-label={t("tracing.connect.enabled")}
										data-testid={`tracing-key-enabled-switch-${key.id}`}
									/>
									<Button
										variant="ghost"
										size="icon"
										aria-label={t("tracing.connect.deleteKey")}
										data-testid={`tracing-key-delete-button-${key.id}`}
										onClick={() =>
											deleteKey(key.id)
												.unwrap()
												.catch((error) => toast.error(getErrorMessage(error)))
										}
									>
										<Trash2 className="h-4 w-4" />
									</Button>
								</div>
							))}
						</div>
					)}
					{newKeyValue ? (
						<div className="space-y-2 rounded-md border border-amber-500/50 bg-amber-500/10 p-3" data-testid="tracing-new-key">
							<p className="text-xs font-medium">{t("tracing.connect.copyNow")}</p>
							<div className="flex items-center gap-2">
								<code
									className="bg-background min-w-0 flex-1 truncate rounded border px-2 py-1 text-xs"
									data-testid="tracing-new-key-value"
								>
									{newKeyValue}
								</code>
								<Button size="sm" variant="outline" data-testid="tracing-new-key-copy-button" onClick={() => copy(newKeyValue)}>
									<Copy className="mr-1 h-3.5 w-3.5" />
									{t("tracing.connect.copy")}
								</Button>
							</div>
						</div>
					) : (
						<div className="grid gap-3 rounded-md border p-3 md:grid-cols-2">
							<div className="space-y-1">
								<Label htmlFor="tracing-key-name">{t("tracing.connect.keyName")}</Label>
								<Input
									id="tracing-key-name"
									data-testid="tracing-key-name-input"
									value={keyName}
									placeholder="production"
									onChange={(event) => setKeyName(event.target.value)}
								/>
							</div>
							<div className="space-y-1">
								<Label htmlFor="tracing-key-project">{t("tracing.connect.newProject")}</Label>
								<Input
									id="tracing-key-project"
									data-testid="tracing-key-project-input"
									value={projectName}
									placeholder={projects.length > 0 ? t("tracing.connect.allProjects") : "my-app"}
									onChange={(event) => setProjectName(event.target.value)}
								/>
							</div>
							<label className="flex items-center gap-2 text-sm">
								<Switch
									checked={canCreateProjects}
									onCheckedChange={setCanCreateProjects}
									data-testid="tracing-key-create-projects-switch"
								/>
								{t("tracing.connect.canCreateProjects")}
							</label>
							<label className="flex items-center gap-2 text-sm">
								<Switch checked={logContent} onCheckedChange={setLogContent} data-testid="tracing-key-log-content-switch" />
								{t("tracing.connect.logContent")}
							</label>
							<div className="md:col-span-2">
								<Button
									data-testid="tracing-key-create-button"
									disabled={creatingKey || (!canCreateProjects && projects.length === 0 && !projectName.trim())}
									onClick={handleCreateKey}
								>
									{creatingKey && <Loader2 className="mr-1 h-4 w-4 animate-spin" />}
									{t("tracing.connect.createKey")}
								</Button>
							</div>
						</div>
					)}
				</section>

				<section className="space-y-3">
					<h3 className="text-sm font-medium">{t("tracing.connect.pointSDK")}</h3>
					<Tabs defaultValue="braintrust">
						<TabsList>
							{all.map(({ source }) => (
								<TabsTrigger key={source} value={source} data-testid={`tracing-connect-${source}-tab`}>
									{SOURCE_LABELS[source]}
								</TabsTrigger>
							))}
						</TabsList>
						{all.map(({ source, setup, link }) => (
							<TabsContent key={source} value={source} className="space-y-3">
								<Snippet code={setup} testId={`tracing-connect-${source}-setup`} />
								<p className="text-muted-foreground text-xs">{t("tracing.connect.linkHint")}</p>
								<Snippet code={link} testId={`tracing-connect-${source}-link`} />
							</TabsContent>
						))}
					</Tabs>
					{connect && !connect.uploads_available && <p className="text-muted-foreground text-xs">{t("tracing.connect.noUploads")}</p>}
				</section>

				<div className="flex items-center gap-2 rounded-md border p-3 text-sm" data-testid="tracing-connect-status">
					{received ? (
						<>
							<CheckCircle2 className="h-4 w-4 text-emerald-600" />
							{t("tracing.connect.received", { count: stats?.stats.trace_count ?? 0 })}
						</>
					) : (
						<>
							<Loader2 className="text-muted-foreground h-4 w-4 animate-spin" />
							{t("tracing.connect.waiting")}
						</>
					)}
				</div>
			</DialogContent>
		</Dialog>
	);
}