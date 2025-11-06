"use client";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useCreateMCPClientMutation } from "@/lib/store";
import { CreateMCPClientRequest, MCPConnectionType, MCPStdioConfig } from "@/lib/types/mcp";
import { parseArrayFromText } from "@/lib/utils/array";
import { Validator } from "@/lib/utils/validation";
import { Info } from "lucide-react";
import React, { useEffect, useState } from "react";
import { cn } from "@/lib/utils";

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
	headers: {},
};

const ClientForm: React.FC<ClientFormProps> = ({ open, onClose, onSaved }) => {
	const [form, setForm] = useState<CreateMCPClientRequest>(emptyForm);
	const [isLoading, setIsLoading] = useState(false);
	const [argsText, setArgsText] = useState("");
	const [envsText, setEnvsText] = useState("");
	const [headersText, setHeadersText] = useState("");
	const { toast } = useToast();

	// RTK Query mutations
	const [createMCPClient] = useCreateMCPClientMutation();

	// Reset form state when dialog opens
	useEffect(() => {
		if (open) {
			setForm(emptyForm);
			setArgsText("");
			setEnvsText("");
			setHeadersText(JSON.stringify(emptyForm.headers || {}, null, 2));
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

	const handleHeadersChange = (value: string) => {
		setHeadersText(value);
		// Try to parse JSON on change, but allow intermediate invalid states
		const trimmed = value.trim();
		if (trimmed) {
			try {
				const parsed = JSON.parse(trimmed);
				if (typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)) {
					setForm((prev) => ({ ...prev, headers: parsed as Record<string, string> }));
				}
			} catch {
				// Keep as string during editing to allow intermediate invalid states
			}
		} else {
			setForm((prev) => ({ ...prev, headers: {} }));
		}
	};

	const handleHeadersBlur = () => {
		// Final parse on blur
		const trimmed = headersText.trim();
		if (trimmed) {
			try {
				const parsed = JSON.parse(trimmed);
				if (typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)) {
					setForm((prev) => ({ ...prev, headers: parsed as Record<string, string> }));
					setHeadersText(JSON.stringify(parsed, null, 2));
				}
			} catch {
				// Keep current value if invalid JSON
			}
		} else {
			setForm((prev) => ({ ...prev, headers: {} }));
			setHeadersText("");
		}
	};

	// Validate headers JSON format
	const validateHeadersJSON = (): string | null => {
		if ((form.connection_type === "http" || form.connection_type === "sse") && headersText.trim()) {
			try {
				const parsed = JSON.parse(headersText.trim());
				if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
					return "Headers must be a valid JSON object";
				}
				// Ensure all values are strings
				for (const [key, value] of Object.entries(parsed)) {
					if (typeof value !== "string") {
						return `Header "${key}" must have a string value`;
					}
				}
			} catch {
				return "Headers must be valid JSON format";
			}
		}
		return null;
	};

	const headersValidationError = validateHeadersJSON();

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
		// Parse headers from text if needed
		let headers: Record<string, string> | undefined = form.headers;
		if (headersText.trim()) {
			try {
				const parsed = JSON.parse(headersText.trim());
				if (typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)) {
					headers = parsed as Record<string, string>;
				}
			} catch {
				setIsLoading(false);
				toast({ title: "Error", description: "Invalid JSON format for headers", variant: "destructive" });
				return;
			}
		}

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
			headers: headers && Object.keys(headers).length > 0 ? headers : undefined,
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

								<Input
									value={form.connection_string || ""}
									onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleChange("connection_string", e.target.value)}
									placeholder="http://your-mcp-server:3000 or env.MCP_SERVER_URL"
								/>
							</div>
							<div className="space-y-2">
								<Label>Headers (Optional)</Label>
								<Textarea
									value={headersText}
									onChange={(e) => handleHeadersChange(e.target.value)}
									onBlur={handleHeadersBlur}
									placeholder='{"Authorization": "Bearer token", "X-Custom-Header": "value"}'
									rows={3}
									className={cn("font-mono text-sm", headersValidationError && "border-destructive focus-visible:ring-destructive")}
								/>
								{headersValidationError ? (
									<p className="text-destructive text-xs">{headersValidationError}</p>
								) : (
									<p className="text-muted-foreground text-xs">Enter headers as a JSON object with key-value pairs.</p>
								)}
							</div>
						</>
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
