"use client";

import ClientForm from "@/app/workspace/mcp-registry/views/mcpClientForm";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogTrigger,
} from "@/components/ui/alertDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useToast } from "@/hooks/use-toast";
import { MCP_STATUS_COLORS } from "@/lib/constants/config";
import { getErrorMessage, useDeleteMCPClientMutation, useReconnectMCPClientMutation } from "@/lib/store";
import { MCPClient } from "@/lib/types/mcp";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { ChevronLeft, ChevronRight, Loader2, Plus, RefreshCcw, Search, Trash2 } from "lucide-react";
import { useState } from "react";
import { MCPServersEmptyState } from "./mcpServersEmptyState";
import MCPClientSheet from "./mcpClientSheet";

interface MCPClientsTableProps {
	mcpClients: MCPClient[];
	totalCount: number;
	refetch?: () => void;
	search: string;
	debouncedSearch: string;
	onSearchChange: (value: string) => void;
	offset: number;
	limit: number;
	onOffsetChange: (offset: number) => void;
}

export default function MCPClientsTable({ mcpClients, totalCount, refetch, search, debouncedSearch, onSearchChange, offset, limit, onOffsetChange }: MCPClientsTableProps) {
	const [formOpen, setFormOpen] = useState(false);
	const hasCreateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Create);
	const hasUpdateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Update);
	const hasDeleteMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Delete);
	const [selectedMCPClient, setSelectedMCPClient] = useState<MCPClient | null>(null);
	const [showDetailSheet, setShowDetailSheet] = useState(false);
	const { toast } = useToast();

	const [reconnectingClients, setReconnectingClients] = useState<string[]>([]);

	// RTK Query mutations
	const [reconnectMCPClient] = useReconnectMCPClientMutation();
	const [deleteMCPClient] = useDeleteMCPClientMutation();

	const handleCreate = () => {
		setFormOpen(true);
	};

	const handleReconnect = async (client: MCPClient) => {
		try {
			setReconnectingClients((prev) => [...prev, client.config.client_id]);
			await reconnectMCPClient(client.config.client_id).unwrap();
			setReconnectingClients((prev) => prev.filter((id) => id !== client.config.client_id));
			toast({ title: "Reconnected", description: `Client ${client.config.name} reconnected successfully.` });
			if (refetch) {
				await refetch();
			}
		} catch (error) {
			setReconnectingClients((prev) => prev.filter((id) => id !== client.config.client_id));
			toast({ title: "Error", description: getErrorMessage(error), variant: "destructive" });
		}
	};

	const handleDelete = async (client: MCPClient) => {
		try {
			await deleteMCPClient(client.config.client_id).unwrap();
			toast({ title: "Deleted", description: `Client ${client.config.name} removed successfully.` });
			if (refetch) {
				await refetch();
			}
		} catch (error) {
			toast({ title: "Error", description: getErrorMessage(error), variant: "destructive" });
		}
	};

	const handleSaved = async () => {
		setFormOpen(false);
		if (refetch) {
			await refetch();
		}
	};

	const getConnectionDisplay = (client: MCPClient) => {
		if (client.config.connection_type === "stdio") {
			return `${client.config.stdio_config?.command} ${client.config.stdio_config?.args.join(" ")}` || "STDIO";
		}
		// connection_string is now an EnvVar, display the value or env_var reference
		const connStr = client.config.connection_string;
		if (connStr) {
			return connStr.from_env ? connStr.env_var : connStr.value || `${client.config.connection_type.toUpperCase()}`;
		}
		return `${client.config.connection_type.toUpperCase()}`;
	};

	const getConnectionTypeDisplay = (type: string) => {
		switch (type) {
			case "http":
				return "HTTP";
			case "sse":
				return "SSE";
			case "stdio":
				return "STDIO";
			default:
				return type.toUpperCase();
		}
	};

	const handleRowClick = (mcpClient: MCPClient) => {
		setSelectedMCPClient(mcpClient);
		setShowDetailSheet(true);
	};

	const handleDetailSheetClose = () => {
		setShowDetailSheet(false);
		setSelectedMCPClient(null);
	};

	const handleEditTools = async () => {
		setShowDetailSheet(false);
		setSelectedMCPClient(null);
		if (refetch) {
			await refetch();
		}
	};

	const hasActiveFilters = debouncedSearch;

	// True empty state: no servers at all (not just filtered to zero)
	if (totalCount === 0 && !hasActiveFilters) {
		return (
			<>
				{formOpen && <ClientForm open={formOpen} onClose={() => setFormOpen(false)} onSaved={handleSaved} />}
				<MCPServersEmptyState onAddClick={handleCreate} canCreate={hasCreateMCPClientAccess} />
			</>
		);
	}

	return (
		<div className="space-y-4">
			{showDetailSheet && selectedMCPClient && (
				<MCPClientSheet mcpClient={selectedMCPClient} onClose={handleDetailSheetClose} onSubmitSuccess={handleEditTools} />
			)}

			<CardHeader className="mb-4 px-0">
				<CardTitle className="flex items-center justify-between">
					<h1 className="text-foreground text-lg font-semibold">MCP server catalog</h1>
					<Button onClick={handleCreate} disabled={!hasCreateMCPClientAccess} data-testid="create-mcp-client-btn">
						<Plus className="h-4 w-4" /> New MCP Server
					</Button>
				</CardTitle>
				<CardDescription>Manage servers that can connect to the MCP Tools endpoint.</CardDescription>
			</CardHeader>

			{/* Toolbar: Search */}
			<div className="flex items-center gap-3">
				<div className="relative max-w-sm flex-1">
					<Search className="text-muted-foreground absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2" />
					<Input
						aria-label="Search MCP servers by name"
						placeholder="Search by name..."
						value={search}
						onChange={(e) => onSearchChange(e.target.value)}
						className="pl-9"
						data-testid="mcp-clients-search-input"
					/>
				</div>
			</div>

			<div className="rounded-sm border overflow-hidden">
				<Table data-testid="mcp-clients-table">
					<TableHeader>
						<TableRow className="bg-muted/50">
							<TableHead className="font-semibold">Name</TableHead>
							<TableHead className="font-semibold">Connection Type</TableHead>
							<TableHead className="font-semibold">Code Mode</TableHead>
							<TableHead className="font-semibold">Connection Info</TableHead>
							<TableHead className="font-semibold">Enabled Tools</TableHead>
							<TableHead className="font-semibold">Auto-execute Tools</TableHead>
							<TableHead className="font-semibold">State</TableHead>
							<TableHead className="w-20 text-right"></TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{mcpClients.length === 0 ? (
							<TableRow>
								<TableCell colSpan={8} className="h-24 text-center">
									<span className="text-muted-foreground text-sm">No matching MCP servers found.</span>
								</TableCell>
							</TableRow>
						) : (
						mcpClients.map((c: MCPClient) => {
							const enabledToolsCount =
								c.state == "connected"
									? c.config.tools_to_execute?.includes("*")
										? c.tools?.length
										: (c.config.tools_to_execute?.length ?? 0)
									: 0;
							const autoExecuteToolsCount =
								c.state == "connected"
									? c.config.tools_to_auto_execute?.includes("*")
										? c.tools?.length
										: (c.config.tools_to_auto_execute?.length ?? 0)
									: 0;
							return (
								<TableRow
									key={c.config.client_id}
									className="hover:bg-muted/50 cursor-pointer transition-colors"
									onClick={() => handleRowClick(c)}
								>
									<TableCell className="font-medium">{c.config.name}</TableCell>
									<TableCell data-testid="mcp-client-connection-type">{getConnectionTypeDisplay(c.config.connection_type)}</TableCell>
									<TableCell>
										<Badge
											className={
												c.state == "connected" ? MCP_STATUS_COLORS[c.config.is_code_mode_client ? "connected" : "disconnected"] : ""
											}
										>
											{c.state == "connected" ? <>{c.config.is_code_mode_client ? "Enabled" : "Disabled"}</> : "-"}
										</Badge>
									</TableCell>
									<TableCell className="max-w-72 overflow-hidden text-ellipsis whitespace-nowrap">{getConnectionDisplay(c)}</TableCell>
									<TableCell>
										{c.state == "connected" ? (
											<>
												{enabledToolsCount}/{c.tools?.length}
											</>
										) : (
											"-"
										)}
									</TableCell>
									<TableCell>
										{c.state == "connected" ? (
											<>
												{autoExecuteToolsCount}/{c.tools?.length}
											</>
										) : (
											"-"
										)}
									</TableCell>
									<TableCell>
										<Badge className={MCP_STATUS_COLORS[c.state]}>{c.state}</Badge>
									</TableCell>
									<TableCell className="space-x-2 text-right" onClick={(e) => e.stopPropagation()}>
										<Button
											variant="ghost"
											size="icon"
											onClick={() => handleReconnect(c)}
											disabled={reconnectingClients.includes(c.config.client_id) || !hasUpdateMCPClientAccess}
											title="Reconnect"
										>
											{reconnectingClients.includes(c.config.client_id) ? (
												<Loader2 className="h-4 w-4 animate-spin" />
											) : (
												<RefreshCcw className="h-4 w-4" />
											)}
										</Button>

										<AlertDialog>
											<AlertDialogTrigger asChild>
												<Button variant="ghost" size="icon" disabled={!hasDeleteMCPClientAccess}>
													<Trash2 className="h-4 w-4" />
												</Button>
											</AlertDialogTrigger>
											<AlertDialogContent>
												<AlertDialogHeader>
													<AlertDialogTitle>Remove MCP Server</AlertDialogTitle>
													<AlertDialogDescription>
														Are you sure you want to remove MCP server {c.config.name}? You will need to reconnect the server to continue
														using it.
													</AlertDialogDescription>
												</AlertDialogHeader>
												<AlertDialogFooter>
													<AlertDialogCancel>Cancel</AlertDialogCancel>
													<AlertDialogAction onClick={() => handleDelete(c)}>Delete</AlertDialogAction>
												</AlertDialogFooter>
											</AlertDialogContent>
										</AlertDialog>
									</TableCell>
								</TableRow>
							);
						})
						)}
					</TableBody>
				</Table>
			</div>

			{/* Pagination */}
			{totalCount > 0 && (
				<div className="flex items-center justify-between px-2">
					<p className="text-muted-foreground text-sm">
						Showing {offset + 1}-{Math.min(offset + limit, totalCount)} of {totalCount}
					</p>
					<div className="flex gap-2">
						<Button
							variant="outline"
							size="sm"
							disabled={offset === 0}
							onClick={() => onOffsetChange(Math.max(0, offset - limit))}
							data-testid="mcp-clients-pagination-prev-btn"
						>
							<ChevronLeft className="mr-1 h-4 w-4" />
							Previous
						</Button>
						<Button
							variant="outline"
							size="sm"
							disabled={offset + limit >= totalCount}
							onClick={() => onOffsetChange(offset + limit)}
							data-testid="mcp-clients-pagination-next-btn"
						>
							Next
							<ChevronRight className="ml-1 h-4 w-4" />
						</Button>
					</div>
				</div>
			)}

			{formOpen && <ClientForm open={formOpen} onClose={() => setFormOpen(false)} onSaved={handleSaved} />}
		</div>
	);
}
