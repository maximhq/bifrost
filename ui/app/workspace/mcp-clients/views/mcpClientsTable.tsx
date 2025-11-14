"use client";

import ClientForm from "@/app/workspace/mcp-clients/views/mcpClientForm";
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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useToast } from "@/hooks/use-toast";
import { MCP_STATUS_COLORS } from "@/lib/constants/config";
import { getErrorMessage, useDeleteMCPClientMutation, useGetMCPClientsQuery, useReconnectMCPClientMutation } from "@/lib/store";
import { MCPClient } from "@/lib/types/mcp";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Loader2, Plus, RefreshCcw, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import MCPClientSheet from "./mcpClientSheet";

interface MCPClientsTableProps {
	mcpClients: MCPClient[];
}

export default function MCPClientsTable({ mcpClients }: MCPClientsTableProps) {
	const [formOpen, setFormOpen] = useState(false);
	const hasCreateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Create);
	const hasUpdateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Update);
	const hasDeleteMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Delete);
	const [selectedMCPClient, setSelectedMCPClient] = useState<MCPClient | null>(null);
	const [showDetailSheet, setShowDetailSheet] = useState(false);
	const { toast } = useToast();

	const [reconnectingClients, setReconnectingClients] = useState<string[]>([]);

	// RTK Query hooks
	const { data: clientsData, refetch } = useGetMCPClientsQuery();
	const [reconnectMCPClient] = useReconnectMCPClientMutation();
	const [deleteMCPClient] = useDeleteMCPClientMutation();

	const clients = clientsData || mcpClients;

	const loadClients = async () => {
		refetch();
	};

	useEffect(() => {
		loadClients();
	}, []);

	const handleCreate = () => {
		setFormOpen(true);
	};

	const handleReconnect = async (client: MCPClient) => {
		try {
			setReconnectingClients((prev) => [...prev, client.config.id]);
			await reconnectMCPClient(client.config.id).unwrap();
			setReconnectingClients((prev) => prev.filter((id) => id !== client.config.id));
			toast({ title: "Reconnected", description: `Client ${client.config.name} reconnected successfully.` });
			loadClients();
		} catch (error) {
			setReconnectingClients((prev) => prev.filter((id) => id !== client.config.id));
			toast({ title: "Error", description: getErrorMessage(error), variant: "destructive" });
		}
	};

	const handleDelete = async (client: MCPClient) => {
		try {
			await deleteMCPClient(client.config.id).unwrap();
			toast({ title: "Deleted", description: `Client ${client.config.name} removed successfully.` });
			loadClients();
		} catch (error) {
			toast({ title: "Error", description: getErrorMessage(error), variant: "destructive" });
		}
	};

	const handleSaved = () => {
		setFormOpen(false);
		loadClients();
	};

	const getConnectionDisplay = (client: MCPClient) => {
		if (client.config.connection_type === "stdio") {
			return `${client.config.stdio_config?.command} ${client.config.stdio_config?.args.join(" ")}` || "STDIO";
		}
		return client.config.connection_string || `${client.config.connection_type.toUpperCase()}`;
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

	const handleEditTools = () => {
		setShowDetailSheet(false);
		setSelectedMCPClient(null);
		loadClients();
	};

	return (
		<div className="space-y-4">
			{showDetailSheet && selectedMCPClient && (
				<MCPClientSheet mcpClient={selectedMCPClient} onClose={handleDetailSheetClose} onSubmitSuccess={handleEditTools} />
			)}

			<CardHeader className="mb-4 px-0">
				<CardTitle className="flex items-center justify-between">
					<div className="flex items-center gap-2">Registered MCP Clients</div>
					<Button onClick={handleCreate} disabled={!hasCreateMCPClientAccess}>
						<Plus className="h-4 w-4" /> New MCP Client
					</Button>
				</CardTitle>
				<CardDescription>Manage clients that can connect to the MCP Tools endpoint.</CardDescription>
			</CardHeader>
			<div className="rounded-sm border">
				<Table>
					<TableHeader>
						<TableRow>
							<TableHead>Name</TableHead>
							<TableHead>Connection Type</TableHead>
							<TableHead>Connection Info</TableHead>
							<TableHead>State</TableHead>
							<TableHead className="w-20 text-right"></TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{clients.length === 0 && (
							<TableRow>
								<TableCell colSpan={5} className="py-6 text-center">
									No clients found.
								</TableCell>
							</TableRow>
						)}
						{clients.map((c: MCPClient) => (
							<TableRow key={c.config.id} className="hover:bg-muted/50 cursor-pointer transition-colors" onClick={() => handleRowClick(c)}>
								<TableCell className="font-medium">{c.config.name}</TableCell>
								<TableCell>{getConnectionTypeDisplay(c.config.connection_type)}</TableCell>
								<TableCell className="max-w-72 overflow-hidden text-ellipsis whitespace-nowrap">{getConnectionDisplay(c)}</TableCell>
								<TableCell>
									<Badge className={MCP_STATUS_COLORS[c.state]}>{c.state}</Badge>
								</TableCell>
								<TableCell className="space-x-2 text-right" onClick={(e) => e.stopPropagation()}>
									<Button
										variant="ghost"
										size="icon"
										onClick={() => handleReconnect(c)}
										disabled={reconnectingClients.includes(c.config.id) || !hasUpdateMCPClientAccess}
									>
										{reconnectingClients.includes(c.config.id) ? (
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
												<AlertDialogTitle>Remove MCP Client</AlertDialogTitle>
												<AlertDialogDescription>
													Are you sure you want to remove MCP client {c.config.name}? You will need to reconnect the client to continue
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
						))}
					</TableBody>
				</Table>
			</div>
			{formOpen && <ClientForm open={formOpen} onClose={() => setFormOpen(false)} onSaved={handleSaved} />}
		</div>
	);
}
