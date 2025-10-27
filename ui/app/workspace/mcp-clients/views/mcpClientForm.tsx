"use client";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useCreateMCPClientMutation } from "@/lib/store";
import { CreateMCPClientRequest, MCPConnectionType, MCPStdioConfig } from "@/lib/types/mcp";
import { parseArrayFromText } from "@/lib/utils/array";
import { Validator } from "@/lib/utils/validation";
import { Info } from "lucide-react";
import React, { useEffect, useState } from "react";

interface ClientFormProps {
	open: boolean;
	onClose: () => void;
	onSaved: () => void;
}

const emptyStdioConfig: MCPStdioConfig = {
	command: "",
	args: [],
	envs: [],
};

const emptyForm: CreateMCPClientRequest = {
	name: "",
	connection_type: "http",
	connection_string: "",
	stdio_config: emptyStdioConfig,
};

const ClientForm: React.FC<ClientFormProps> = ({ open, onClose, onSaved }) => {
	const [form, setForm] = useState<CreateMCPClientRequest>(emptyForm);
	const [isLoading, setIsLoading] = useState(false);
	const [argsText, setArgsText] = useState("");
	const [envsText, setEnvsText] = useState("");
	const { toast } = useToast();

	// RTK Query mutations
	const [createMCPClient] = useCreateMCPClientMutation();

	// Reset form state when dialog opens
	useEffect(() => {
		if (open) {
			setForm(emptyForm);
			setArgsText("");
			setEnvsText("");
			setIsLoading(false);
		}
	}, [open]);

	const handleChange = (field: keyof CreateMCPClientRequest, value: string | string[] | MCPConnectionType | MCPStdioConfig | undefined) => {
		setForm((prev) => ({ ...prev, [field]: value }));
	};

	const handleStdioConfigChange = (field: keyof MCPStdioConfig, value: string | string[]) => {
		setForm((prev) => ({
			...prev,
			stdio_config: {
				command: "",
				args: [],
				envs: [],
				...(prev.stdio_config || {}),
				[field]: value,
			},
		}));
	};

	const validator = new Validator([
		// Name validation
		Validator.required(form.name?.trim(), "Client name is required"),
		Validator.pattern(form.name || "", /^[a-zA-Z0-9-_]+$/, "Client name can only contain letters, numbers, hyphens and underscores"),
		Validator.minLength(form.name || "", 3, "Client name must be at least 3 characters"),
		Validator.maxLength(form.name || "", 50, "Client name cannot exceed 50 characters"),

		// Connection type specific validation
		...(form.connection_type === "http" || form.connection_type === "sse"
			? [
					Validator.required(form.connection_string?.trim(), "Connection URL is required"),
					Validator.pattern(
						form.connection_string || "",
						/^((https?:\/\/.+)|(env\.[A-Z_]+))$/,
						"Connection URL must start with http://, https://, or be an environment variable (env.VAR_NAME)",
					),
				]
			: []),

		// STDIO validation
		...(form.connection_type === "stdio"
			? [
					Validator.required(form.stdio_config?.command?.trim(), "Command is required for STDIO connections"),
					Validator.pattern(form.stdio_config?.command || "", /^[^<>|&;]+$/, "Command cannot contain special shell characters"),
				]
			: []),
	]);

	const handleSubmit = async () => {
		setIsLoading(true);
		let error: string | null = null;

		// Prepare the payload
		const payload: CreateMCPClientRequest = {
			...form,
			stdio_config:
				form.connection_type === "stdio"
					? {
							command: form.stdio_config?.command || "",
							args: parseArrayFromText(argsText),
							envs: parseArrayFromText(envsText),
						}
					: undefined,
			tools_to_execute: ["*"],
		};

		try {
			await createMCPClient(payload).unwrap();

			setIsLoading(false);
			toast({
				title: "Success",
				description: "Client created",
			});
			onSaved();
			onClose();
		} catch (error) {
			setIsLoading(false);
			toast({ title: "Error", description: getErrorMessage(error), variant: "destructive" });
		}
	};

	return (
		<Dialog open={open} onOpenChange={onClose}>
			<DialogContent className="max-h-[90vh] max-w-2xl overflow-y-auto">
				<DialogHeader>
					<DialogTitle>New MCP Client</DialogTitle>
				</DialogHeader>
				<div className="space-y-4">
					<div className="space-y-2">
						<Label>Name</Label>
						<Input
							value={form.name}
							onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleChange("name", e.target.value)}
							placeholder="Client name"
							maxLength={50}
						/>
					</div>

					<div className="w-full space-y-2">
						<Label>Connection Type</Label>
						<Select value={form.connection_type} onValueChange={(value: MCPConnectionType) => handleChange("connection_type", value)}>
							<SelectTrigger className="w-full">
								<SelectValue placeholder="Select connection type" />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="http">HTTP (Streamable)</SelectItem>
								<SelectItem value="sse">Server-Sent Events (SSE)</SelectItem>
								<SelectItem value="stdio">STDIO</SelectItem>
							</SelectContent>
						</Select>
					</div>

					{(form.connection_type === "http" || form.connection_type === "sse") && (
						<div className="space-y-2">
							<div className="flex w-fit items-center gap-1">
								<Label>Connection URL</Label>
								<TooltipProvider>
									<Tooltip>
										<TooltipTrigger asChild>
											<span>
												<Info className="text-muted-foreground ml-1 h-3 w-3" />
											</span>
										</TooltipTrigger>
										<TooltipContent className="max-w-fit">
											<p>
												Use <code className="rounded bg-neutral-100 px-1 py-0.5 text-neutral-800">env.&lt;VAR&gt;</code> to read the value
												from an environment variable.
											</p>
										</TooltipContent>
									</Tooltip>
								</TooltipProvider>
							</div>

							<Input
								value={form.connection_string || ""}
								onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleChange("connection_string", e.target.value)}
								placeholder="http://your-mcp-server:3000 or env.MCP_SERVER_URL"
							/>
						</div>
					)}

					{form.connection_type === "stdio" && (
						<>
							<div className="space-y-2">
								<Label>Command</Label>
								<Input
									value={form.stdio_config?.command || ""}
									onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleStdioConfigChange("command", e.target.value)}
									placeholder="node, python, /path/to/executable"
								/>
							</div>
							<div className="space-y-2">
								<Label>Arguments (comma-separated)</Label>
								<Input
									value={argsText}
									onChange={(e: React.ChangeEvent<HTMLInputElement>) => setArgsText(e.target.value)}
									placeholder="--port, 3000, --config, config.json"
								/>
							</div>
							<div className="space-y-2">
								<Label>Environment Variables (comma-separated)</Label>
								<Input
									value={envsText}
									onChange={(e: React.ChangeEvent<HTMLInputElement>) => setEnvsText(e.target.value)}
									placeholder="API_KEY, DATABASE_URL"
								/>
							</div>
						</>
					)}
				</div>
				<DialogFooter>
					<Button variant="outline" onClick={onClose} disabled={isLoading}>
						Cancel
					</Button>
					<TooltipProvider>
						<Tooltip>
							<TooltipTrigger asChild>
								<span>
									<Button onClick={handleSubmit} disabled={!validator.isValid() || isLoading} isLoading={isLoading}>
										Create
									</Button>
								</span>
							</TooltipTrigger>
							{!validator.isValid() && <TooltipContent>{validator.getFirstError() || "Please fix validation errors"}</TooltipContent>}
						</Tooltip>
					</TooltipProvider>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
};

export default ClientForm;
