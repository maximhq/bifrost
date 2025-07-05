"use client";

import React, { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { useToast } from "@/hooks/use-toast";
import { apiService } from "@/lib/api";
import { MCPClient } from "@/lib/types/mcp";
import ClientForm from "@/components/config/mcp-client-form";
import { Trash2, Pencil, Plus, RefreshCcw } from "lucide-react";
import {
	AlertDialog,
	AlertDialogDescription,
	AlertDialogHeader,
	AlertDialogCancel,
	AlertDialogAction,
	AlertDialogContent,
	AlertDialogFooter,
	AlertDialogTitle,
	AlertDialogTrigger,
} from "../ui/alert-dialog";
import { MCP_STATUS_COLORS } from "@/lib/constants/config";

export default function MCPClientsTab() {
	const [clients, setClients] = useState<MCPClient[]>([]);
	const [selected, setSelected] = useState<MCPClient | null>(null);
	const [formOpen, setFormOpen] = useState(false);
	const { toast } = useToast();

	const loadClients = async () => {
		const [data, error] = await apiService.getMCPClients();
		if (error) {
			toast({ title: "Error", description: error, variant: "destructive" });
		} else {
			setClients(data || []);
		}
	};

	useEffect(() => {
		loadClients();
	}, []);

	const handleCreate = () => {
		setSelected(null);
		setFormOpen(true);
	};

	const handleEdit = (client: MCPClient) => {
		setSelected(client);
		setFormOpen(true);
	};

	const handleReconnect = async (client: MCPClient) => {
		const [, error] = await apiService.reconnectMCPClient(client.name);
		if (error) {
			toast({ title: "Error", description: error, variant: "destructive" });
		} else {
			toast({ title: "Reconnected", description: "Client reconnected." });
			loadClients();
		}
	};

	const handleDelete = async (client: MCPClient) => {
		const [, error] = await apiService.deleteMCPClient(client.name);
		if (error) {
			toast({ title: "Error", description: error, variant: "destructive" });
		} else {
			toast({ title: "Deleted", description: "Client removed." });
			loadClients();
		}
	};

	const handleSaved = () => {
		setFormOpen(false);
		loadClients();
	};

	const getConnectionDisplay = (client: MCPClient) => {
		if (client.config.connection_type === "stdio") {
			return client.config.stdio_config?.command + " " + client.config.stdio_config?.args.join(" ") || "STDIO";
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

	return (
		<div className="space-y-4">
			<CardHeader className="mb-4 px-0">
				<CardTitle className="flex items-center justify-between">
					<div className="flex items-center gap-2">Registered Clients</div>
					<Button onClick={handleCreate}>
						<Plus className="h-4 w-4" /> New Client
					</Button>
				</CardTitle>
				<CardDescription>Manage clients that can connect to the MCP Tools endpoint.</CardDescription>
			</CardHeader>
			<div className="rounded-md border">
				<Table>
					<TableHeader>
						<TableRow>
							<TableHead>Name</TableHead>
							<TableHead>Connection Type</TableHead>
							<TableHead>Connection Info</TableHead>
							<TableHead>State</TableHead>
							<TableHead className="text-right">Actions</TableHead>
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
							<TableRow key={c.name}>
								<TableCell className="font-medium">{c.name}</TableCell>
								<TableCell>{getConnectionTypeDisplay(c.config.connection_type)}</TableCell>
								<TableCell className="max-w-72 overflow-hidden text-ellipsis whitespace-nowrap">{getConnectionDisplay(c)}</TableCell>
								<TableCell>
									<Badge className={MCP_STATUS_COLORS[c.state]}>{c.state}</Badge>
								</TableCell>
								<TableCell className="space-x-2 text-right">
									{c.state === "disconnected" ? (
										<Button variant="ghost" size="icon" onClick={() => handleReconnect(c)}>
											<RefreshCcw className="h-4 w-4" />
										</Button>
									) : (
										c.state === "connected" && (
											<Button variant="ghost" size="icon" onClick={() => handleEdit(c)}>
												<Pencil className="h-4 w-4" />
											</Button>
										)
									)}

									<AlertDialog>
										<AlertDialogTrigger asChild>
											<Button variant="ghost" size="icon" disabled={c.state === "error"}>
												<Trash2 className="h-4 w-4" />
											</Button>
										</AlertDialogTrigger>
										<AlertDialogContent>
											<AlertDialogHeader>
												<AlertDialogTitle>Remove MCP Client</AlertDialogTitle>
												<AlertDialogDescription>
													Are you sure you want to remove MCP client {c.name}? You will need to reconnect the client to continue using it.
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
			{formOpen && <ClientForm open={formOpen} client={selected} onClose={() => setFormOpen(false)} onSaved={handleSaved} />}
		</div>
	);
}
