import { MCPClientSelector } from "@/components/entitySelectors/mcpClientSelector";
import type { EntitySelectorOption } from "@/components/entitySelectors/entitySelector";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { MultiSelect } from "@/components/ui/multiSelect";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useGetMCPClientsQuery } from "@/lib/store";
import { MCPClient } from "@/lib/types/mcp";
import { Info, Trash2 } from "lucide-react";
import { ReactNode, useCallback, useEffect, useState } from "react";
import { toast } from "sonner";

// One server's tool grant. tools_to_execute: ["*"] = all, [] = none, list = specific.
// Keyed by name (VK shape); mcp_client_id rides along when known, for by-id resolution.
export interface MCPClientConfigEntry {
	id?: number;
	mcp_client_name: string;
	mcp_client_id?: string;
	tools_to_execute?: string[];
}

// Stable key: id when present (vMCP), else the unique name (VK).
function entryKey(entry: MCPClientConfigEntry): string {
	return entry.mcp_client_id || entry.mcp_client_name;
}

interface MCPClientConfigsEditorProps {
	value: MCPClientConfigEntry[];
	onChange: (next: MCPClientConfigEntry[]) => void;
	// VK-only "available by default" note.
	showDefaultsNote?: boolean;
	label?: string;
	tooltip?: ReactNode;
	// Shown when nothing is configured yet.
	emptyState?: ReactNode;
	// Offer every tool the server exposes, ignoring the client's whitelist (vMCP).
	allClientTools?: boolean;
}

const DEFAULT_TOOLTIP = (
	<p>
		Configure which MCP servers this virtual key can use and their allowed tools. Leaving this section empty blocks all MCP tools. After
		adding an MCP server, you must select specific tools or choose <span className="font-medium">Allow All Tools</span> to grant tool
		access.
	</p>
);

// Fetches one configured server (name + tools) by id or name, then renders nothing.
function ConfiguredClientResolver({
	entry,
	onResolved,
}: {
	entry: MCPClientConfigEntry;
	onResolved: (key: string, client: MCPClient) => void;
}) {
	const byId = !!entry.mcp_client_id;
	const { data } = useGetMCPClientsQuery(byId ? { server: entry.mcp_client_id, limit: 1 } : { search: entry.mcp_client_name, limit: 100 });
	const client = byId ? data?.clients?.[0] : data?.clients?.find((c) => c.config.name === entry.mcp_client_name);
	const key = entryKey(entry);
	useEffect(() => {
		if (client) onResolved(key, client);
	}, [client, key, onResolved]);
	return null;
}

// Per-server tool grants: a searchable add-dropdown plus a row per server. Server-backed.
export function MCPClientConfigsEditor({
	value,
	onChange,
	showDefaultsNote = false,
	label = "MCP Server Configurations",
	tooltip = DEFAULT_TOOLTIP,
	emptyState,
	allClientTools = false,
}: MCPClientConfigsEditorProps) {
	// Resolved server records, filled in by the resolvers below.
	const [resolved, setResolved] = useState<Record<string, MCPClient>>({});
	const handleResolved = useCallback((key: string, client: MCPClient) => {
		setResolved((prev) => (prev[key] === client ? prev : { ...prev, [key]: client }));
	}, []);

	// allow-by-default servers, for the VK note. Skipped when the note isn't shown.
	const { data: defaultsData } = useGetMCPClientsQuery({ allowed_by_default: true, limit: 100 }, { skip: !showDefaultsNote });

	const handleAddMCPClient = (option: EntitySelectorOption) => {
		if (value.some((config) => (option.value && config.mcp_client_id === option.value) || config.mcp_client_name === option.label)) {
			toast.error("This MCP server is already configured");
			return;
		}
		onChange([...value, { mcp_client_name: option.label, mcp_client_id: option.value, tools_to_execute: ["*"] }]);
	};

	const handleRemoveMCPClient = (index: number) => {
		onChange(value.filter((_, i) => i !== index));
	};

	const handleUpdateMCPConfig = (index: number, field: keyof MCPClientConfigEntry, next: unknown) => {
		const updated = [...value];
		updated[index] = { ...updated[index], [field]: next };
		onChange(updated);
	};

	// Exclude already-configured servers from the picker (by id, where known).
	const excludeIds = Array.from(
		new Set(
			value.flatMap((config) => {
				const id = config.mcp_client_id || resolved[entryKey(config)]?.config.client_id;
				return id ? [id] : [];
			}),
		),
	);

	return (
		<div className="mt-6 space-y-2">
			{value.map((entry) => (
				<ConfiguredClientResolver key={entryKey(entry)} entry={entry} onResolved={handleResolved} />
			))}

			<div className="flex items-center gap-2">
				<Label className="text-sm font-medium">{label}</Label>
				<TooltipProvider>
					<Tooltip>
						<TooltipTrigger asChild>
							<span>
								<Info className="text-muted-foreground h-3 w-3" />
							</span>
						</TooltipTrigger>
						<TooltipContent>{tooltip}</TooltipContent>
					</Tooltip>
				</TooltipProvider>
			</div>

			{/* MCP servers allowed by default, excluding explicitly overridden ones */}
			{showDefaultsNote &&
				(() => {
					const defaultMCPClients = (defaultsData?.clients ?? []).filter(
						(client) => !value.some((config) => config.mcp_client_name === client.config.name),
					);
					return defaultMCPClients.length > 0 ? (
						<div className="text-muted-foreground rounded-md border p-3 text-xs">
							<div className="flex items-start gap-1.5">
								<Info className="mt-0.5 h-3 w-3 shrink-0" />
								<span>
									The following MCP servers are available to this key by default with all tools enabled on that client:{" "}
									<span className="text-foreground font-medium">{defaultMCPClients.map((c) => c.config.name).join(", ")}</span>. Adding an
									explicit config for any of them below will override the all-tools default for this key.
								</span>
							</div>
						</div>
					) : null;
				})()}

			{/* Add MCP Server dropdown. */}
			<MCPClientSelector
				mode="add"
				fullWidth
				placeholder="Select an MCP server to add"
				onSelect={handleAddMCPClient}
				excludeIds={excludeIds}
			/>

			{value.length === 0 && emptyState}

			{/* MCP Configurations Table */}
			{value.length > 0 && (
				<div className="rounded-md border">
					<Table>
						<TableHeader>
							<TableRow>
								<TableHead>MCP Server</TableHead>
								<TableHead>Allowed Tools</TableHead>
								<TableHead className="w-[50px]"></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{value.map((config, index) => {
								const mcpClient = resolved[entryKey(config)];
								const displayName = mcpClient?.config.name || config.mcp_client_name;

								// Handle new wildcard semantics for client-level filtering
								const clientToolsToExecute = mcpClient?.config?.tools_to_execute;
								let availableTools: { name: string; description?: string }[] = [];

								if (allClientTools) {
									// Offer every tool the server exposes, independent of the client's own whitelist.
									availableTools = mcpClient?.tools || [];
								} else if (!clientToolsToExecute || clientToolsToExecute.length === 0) {
									// nil/undefined or empty array - no tools available from client config
									availableTools = [];
								} else if (clientToolsToExecute.includes("*")) {
									// Wildcard - all tools available
									availableTools = mcpClient?.tools || [];
								} else {
									// Specific tools listed
									availableTools = (mcpClient?.tools || []).filter((tool) => clientToolsToExecute.includes(tool.name)) || [];
								}

								const enabledToolsByConfig = (mcpClient?.tools || []).filter((tool) => config.tools_to_execute?.includes(tool.name)) || [];
								const selectedTools = config.tools_to_execute || [];

								return (
									<TableRow key={`${entryKey(config)}-${index}`}>
										<TableCell className="w-[150px]">{displayName}</TableCell>
										<TableCell>
											<MultiSelect
												hideSelectAll
												options={[
													{
														label: "Allow All Tools",
														value: "*",
														description: "Allow all current and future tools",
													},
													...[...availableTools, ...enabledToolsByConfig]
														.filter((tool, index, arr) => arr.findIndex((t) => t.name === tool.name) === index)
														.map((tool) => ({
															label: tool.name,
															value: tool.name,
															description: tool.description,
														})),
												]}
												defaultValue={selectedTools}
												onValueChange={(tools: string[]) => {
													const hadStar = selectedTools.includes("*");
													const hasStar = tools.includes("*");
													if (!hadStar && hasStar) {
														// Just selected "Allow All Tools": set to ["*"] only
														handleUpdateMCPConfig(index, "tools_to_execute", ["*"]);
													} else if (hadStar && hasStar && tools.length > 1) {
														// Had "*", still has "*", but user also selected a specific tool, drop "*"
														handleUpdateMCPConfig(
															index,
															"tools_to_execute",
															tools.filter((t) => t !== "*"),
														);
													} else {
														handleUpdateMCPConfig(index, "tools_to_execute", tools);
													}
												}}
												placeholder={
													selectedTools.length === 0
														? "No tools selected"
														: selectedTools.includes("*")
															? "All tools allowed"
															: "Select tools..."
												}
												variant="inverted"
												className="hover:bg-accent w-full bg-white dark:bg-zinc-800"
												commandClassName="w-full max-w-96"
												modalPopover={true}
												animation={0}
											/>
										</TableCell>
										<TableCell>
											<Button
												type="button"
												variant="ghost"
												size="sm"
												aria-label={`Remove ${displayName}`}
												onClick={() => handleRemoveMCPClient(index)}
												data-testid={`vk-delete-mcp-${index}`}
											>
												<Trash2 className="h-4 w-4" />
											</Button>
										</TableCell>
									</TableRow>
								);
							})}
						</TableBody>
					</Table>
				</div>
			)}
		</div>
	);
}