"use client";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { EnvVarInput } from "@/components/ui/envVarInput";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useCreateMCPClientMutation } from "@/lib/store";
import { CreateMCPClientRequest, EnvVar, MCPConnectionType, MCPStdioConfig } from "@/lib/types/mcp";
import { parseArrayFromText } from "@/lib/utils/array";
import { Validator } from "@/lib/utils/validation";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
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

const emptyEnvVar: EnvVar = { value: "", env_var: "", from_env: false };

const emptyForm: CreateMCPClientRequest = {
	name: "",
	is_code_mode_client: false,
	is_ping_available: true,
	connection_type: "http",
	connection_string: emptyEnvVar,
	stdio_config: emptyStdioConfig,
	headers: {},
};

const ClientForm: React.FC<ClientFormProps> = ({ open, onClose, onSaved }) => {
	const hasCreateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Create);
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

	const handleChange = (
		field: keyof CreateMCPClientRequest,
		value: string | string[] | boolean | MCPConnectionType | MCPStdioConfig | undefined,
	) => {
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

	const handleHeadersChange = (value: Record<string, EnvVar>) => {
		setForm((prev) => ({ ...prev, headers: value }));
	};

	const handleConnectionStringChange = (value: EnvVar) => {
		setForm((prev) => ({
			...prev,
			connection_string: value,
		}));
	};

	// Validate headers format
	const validateHeaders = (): string | null => {
		if ((form.connection_type === "http" || form.connection_type === "sse") && form.headers) {
			// Ensure all EnvVar values have either a value or env_var
			for (const [key, envVar] of Object.entries(form.headers)) {
				if (!envVar.value && !envVar.env_var) {
					return `Header "${key}" must have a value`;
				}
			}
		}
		return null;
	};

	const headersValidationError = validateHeaders();

	// Get the connection string value for validation
	const connectionStringValue = form.connection_string?.value || "";

	const validator = new Validator([
		// Name validation
		Validator.required(form.name?.trim(), "Server name is required"),
		Validator.pattern(form.name || "", /^[a-zA-Z0-9_]+$/, "Server name can only contain letters, numbers, and underscores"),
		Validator.custom(!(form.name || "").includes("-"), "Server name cannot contain hyphens"),
		Validator.custom(!(form.name || "").includes(" "), "Server name cannot contain spaces"),
		Validator.custom((form.name || "").length === 0 || !/^[0-9]/.test(form.name || ""), "Server name cannot start with a number"),
		Validator.minLength(form.name || "", 3, "Server name must be at least 3 characters"),
		Validator.maxLength(form.name || "", 50, "Server name cannot exceed 50 characters"),

		// Connection type specific validation
		...(form.connection_type === "http" || form.connection_type === "sse"
			? [
					Validator.required(connectionStringValue?.trim(), "Connection URL is required"),
					Validator.pattern(
						connectionStringValue,
						/^((https?:\/\/.+)|(env\.[A-Z_]+))$/,
						"Connection URL must start with http://, https://, or be an environment variable (env.VAR_NAME)",
					),
					...(headersValidationError ? [Validator.custom(false, headersValidationError)] : []),
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
		// Validate before submitting
		if (!validator.isValid()) {
			toast({
				title: "Validation Error",
				description: validator.getFirstError() || "Please fix validation errors",
				variant: "destructive",
			});
			return;
		}

		setIsLoading(true);

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
			headers: form.headers && Object.keys(form.headers).length > 0 ? form.headers : undefined,
			tools_to_execute: ["*"],
		};

		try {
			await createMCPClient(payload).unwrap();

			setIsLoading(false);
			toast({
				title: "Success",
				description: "Server created",
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
					<DialogTitle>New MCP Server</DialogTitle>
				</DialogHeader>
				<div className="space-y-4">
					<div className="space-y-2">
						<Label>Name</Label>
						<Input
							value={form.name}
							onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleChange("name", e.target.value)}
							placeholder="Server name"
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

					<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
						<Label htmlFor="code-mode">Code Mode Server</Label>
						<Switch
							id="code-mode"
							checked={form.is_code_mode_client || false}
							onCheckedChange={(checked) => handleChange("is_code_mode_client", checked)}
						/>
					</div>

					<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
						<div className="flex items-center gap-2">
							<Label htmlFor="ping-available">Ping Available for Health Check</Label>
							<TooltipProvider>
								<Tooltip>
									<TooltipTrigger asChild>
										<Info className="text-muted-foreground h-4 w-4 cursor-help" />
									</TooltipTrigger>
									<TooltipContent className="max-w-xs">
										<p>
											Enable to use lightweight ping method for health checks. Disable if your MCP server doesn't support ping - will use listTools instead.
										</p>
									</TooltipContent>
								</Tooltip>
							</TooltipProvider>
						</div>
						<Switch
							id="ping-available"
							checked={form.is_ping_available === true}
							onCheckedChange={(checked) => handleChange("is_ping_available", checked)}
						/>
					</div>

					{(form.connection_type === "http" || form.connection_type === "sse") && (
						<>
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

								<EnvVarInput
									value={form.connection_string}
									onChange={handleConnectionStringChange}
									placeholder="http://your-mcp-server:3000 or env.MCP_SERVER_URL"
								/>
							</div>
							<div className="space-y-2">
								<HeadersTable
									value={form.headers || {}}
									onChange={handleHeadersChange}
									keyPlaceholder="Header name"
									valuePlaceholder="Header value"
									label="Headers (Optional)"
									useEnvVarInput
								/>
								{headersValidationError && <p className="text-destructive text-xs">{headersValidationError}</p>}
							</div>
						</>
					)}

					{form.connection_type === "stdio" && (
						<>
							<div className="rounded-lg border border-amber-200 bg-amber-50 p-3">
								<div className="flex items-start gap-2">
									<Info className="text-amber-700 mt-0.5 h-4 w-4 flex-shrink-0" />
									<div className="flex-1">
										<p className="text-xs font-medium text-amber-900">Docker Notice</p>
										<p className="text-xs text-amber-800 mt-0.5">
											If not using the official Bifrost Docker image, STDIO connections may not work if required commands (npx, python, etc.) aren't installed. You can safely ignore this if running locally or using a custom image with the necessary dependencies.
										</p>
									</div>
								</div>
							</div>
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
									<Button
										onClick={handleSubmit}
										disabled={!validator.isValid() || isLoading || !hasCreateMCPClientAccess}
										isLoading={isLoading}
									>
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
