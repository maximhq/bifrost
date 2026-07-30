import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useCreateMCPLibraryEntryMutation } from "@/lib/store";
import type { CreateMCPLibraryEntryRequest, MCPAuthType, MCPConnectionType } from "@/lib/types/mcp";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";

interface MCPLibraryAddServerFormData {
	name: string;
	description: string;
	category: string;
	connection_type: MCPConnectionType;
	connection_url: string;
	command: string;
	args: string;
	envs: string;
	auth_type: MCPAuthType;
	required_header_keys: string;
	icon_url: string;
	docs_url: string;
	tags: string;
}

interface MCPLibraryAddServerSheetProps {
	open: boolean;
	onClose: () => void;
}

const DEFAULTS: MCPLibraryAddServerFormData = {
	name: "",
	description: "",
	category: "",
	connection_type: "http",
	connection_url: "",
	command: "",
	args: "",
	envs: "",
	auth_type: "none",
	required_header_keys: "",
	icon_url: "",
	docs_url: "",
	tags: "",
};

// Split a comma/newline-separated string into a trimmed, non-empty list.
function parseList(text: string): string[] {
	return text
		.split(/[\n,]/)
		.map((s) => s.trim())
		.filter(Boolean);
}

export function MCPLibraryAddServerSheet({ open, onClose }: MCPLibraryAddServerSheetProps) {
	const [createEntry, { isLoading }] = useCreateMCPLibraryEntryMutation();

	const {
		register,
		handleSubmit,
		watch,
		setValue,
		reset,
		formState: { errors },
	} = useForm<MCPLibraryAddServerFormData>({ defaultValues: DEFAULTS });

	useEffect(() => {
		if (open) reset(DEFAULTS);
	}, [open, reset]);

	const connectionType = watch("connection_type");
	const authType = watch("auth_type");
	const isStdio = connectionType === "stdio";
	const needsHeaderKeys = authType === "headers" || authType === "per_user_headers";

	const onSubmit = async (data: MCPLibraryAddServerFormData) => {
		const tags = parseList(data.tags);
		const payload: CreateMCPLibraryEntryRequest = {
			name: data.name.trim(),
			description: data.description.trim() || undefined,
			category: data.category.trim() || undefined,
			connection_type: data.connection_type,
			auth_type: data.auth_type,
			icon_url: data.icon_url.trim() || undefined,
			docs_url: data.docs_url.trim() || undefined,
			tags: tags.length ? tags : undefined,
		};

		if (isStdio) {
			payload.stdio_config = {
				command: data.command.trim(),
				args: parseList(data.args),
				envs: parseList(data.envs),
			};
		} else {
			payload.connection_url = data.connection_url.trim();
		}

		if (needsHeaderKeys) {
			payload.required_header_keys = parseList(data.required_header_keys);
		}

		try {
			await createEntry(payload).unwrap();
			toast.success("MCP server published to the library.");
			onClose();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<Sheet open={open} onOpenChange={(sheetOpen) => !sheetOpen && onClose()}>
			<SheetContent className="flex w-full flex-col overflow-x-hidden p-0 pt-4">
				<SheetHeader className="flex flex-col items-start px-0 py-4" headerClassName="mb-0 sticky px-8 -top-4 bg-card z-10">
					<SheetTitle>Add MCP Server</SheetTitle>
					<SheetDescription>This MCP server will be available org-wide for members to discover, install, and use.</SheetDescription>
				</SheetHeader>

				<form onSubmit={handleSubmit(onSubmit)} className="flex min-h-0 flex-1 flex-col">
					<div className="flex-1 space-y-4 px-8 py-4">
						{/* Name */}
						<div className="space-y-2">
							<Label htmlFor="mcp-add-name">Name</Label>
							<Input
								id="mcp-add-name"
								placeholder="My Internal Server"
								data-testid="mcp-add-name-input"
								className={errors.name ? "border-destructive" : ""}
								{...register("name", {
									required: "Name is required",
									validate: (v) => v.trim().length > 0 || "Name is required",
								})}
							/>
							{errors.name && <p className="text-destructive text-sm">{errors.name.message}</p>}
						</div>

						{/* Description */}
						<div className="space-y-2">
							<Label htmlFor="mcp-add-description">Description</Label>
							<Textarea
								id="mcp-add-description"
								placeholder="What this server does..."
								data-testid="mcp-add-description-input"
								{...register("description")}
							/>
						</div>

						{/* Connection details */}
						<div className="flex gap-3">
							<div className="w-32 shrink-0 space-y-2">
								<Label>Connection Type</Label>
								<Select value={connectionType} onValueChange={(v) => setValue("connection_type", v as MCPConnectionType)}>
									<SelectTrigger className="w-full" data-testid="mcp-add-connection-type">
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										<SelectItem value="http">HTTP</SelectItem>
										<SelectItem value="sse">SSE</SelectItem>
										<SelectItem value="stdio">STDIO</SelectItem>
									</SelectContent>
								</Select>
							</div>
							{!isStdio && (
								<div className="min-w-0 flex-1 space-y-2">
									<Label htmlFor="mcp-add-url">Connection URL</Label>
									<Input
										id="mcp-add-url"
										placeholder="https://my-server.internal/mcp"
										data-testid="mcp-add-url-input"
										className={errors.connection_url ? "border-destructive" : ""}
										{...register("connection_url", {
											validate: (v) => (!isStdio ? v.trim().length > 0 || "Connection URL is required" : true),
										})}
									/>
									{errors.connection_url && <p className="text-destructive text-sm">{errors.connection_url.message}</p>}
								</div>
							)}
						</div>

						{isStdio && (
							<div className="space-y-4 rounded-sm border p-4">
								<div className="space-y-2">
									<Label htmlFor="mcp-add-command">Command</Label>
									<Input
										id="mcp-add-command"
										placeholder="npx"
										data-testid="mcp-add-command-input"
										className={errors.command ? "border-destructive" : ""}
										{...register("command", {
											validate: (v) => (isStdio ? v.trim().length > 0 || "Command is required for stdio" : true),
										})}
									/>
									{errors.command && <p className="text-destructive text-sm">{errors.command.message}</p>}
								</div>
								<div className="space-y-2">
									<Label htmlFor="mcp-add-args">Arguments</Label>
									<Input
										id="mcp-add-args"
										placeholder="comma separated, e.g. -y, my-package"
										data-testid="mcp-add-args-input"
										{...register("args")}
									/>
								</div>
								<div className="space-y-2">
									<Label htmlFor="mcp-add-envs">Environment Variable Names</Label>
									<Input
										id="mcp-add-envs"
										placeholder="comma separated, e.g. API_KEY, DB_URL"
										data-testid="mcp-add-envs-input"
										{...register("envs")}
									/>
									<p className="text-muted-foreground text-xs">Only names; users supply values at install time.</p>
								</div>
							</div>
						)}

						{/* Auth + category */}
						<div className="grid grid-cols-2 gap-3">
							<div className="w-full space-y-2">
								<Label>Authentication</Label>
								<Select value={authType} onValueChange={(v) => setValue("auth_type", v as MCPAuthType)}>
									<SelectTrigger data-testid="mcp-add-auth-type" className="w-full">
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										<SelectItem value="none">None</SelectItem>
										<SelectItem value="headers">Headers</SelectItem>
										<SelectItem value="oauth">OAuth</SelectItem>
										<SelectItem value="per_user_headers">Per-user Headers</SelectItem>
										<SelectItem value="per_user_oauth">Per-user OAuth</SelectItem>
									</SelectContent>
								</Select>
							</div>
							<div className="space-y-2">
								<Label htmlFor="mcp-add-category">Category</Label>
								<Input id="mcp-add-category" placeholder="e.g. Database" data-testid="mcp-add-category-input" {...register("category")} />
							</div>
						</div>

						{needsHeaderKeys && (
							<div className="space-y-2">
								<Label htmlFor="mcp-add-header-keys">Required Header Names</Label>
								<Input
									id="mcp-add-header-keys"
									placeholder="comma separated, e.g. X-Api-Key"
									data-testid="mcp-add-header-keys-input"
									{...register("required_header_keys")}
								/>
								<p className="text-muted-foreground text-xs">Only names; users supply values at install time.</p>
							</div>
						)}

						{/* Optional metadata */}
						<div className="grid grid-cols-2 gap-3">
							<div className="space-y-2">
								<Label htmlFor="mcp-add-icon">Icon URL</Label>
								<Input id="mcp-add-icon" placeholder="https://..." data-testid="mcp-add-icon-input" {...register("icon_url")} />
							</div>
							<div className="space-y-2">
								<Label htmlFor="mcp-add-docs">Docs URL</Label>
								<Input id="mcp-add-docs" placeholder="https://..." data-testid="mcp-add-docs-input" {...register("docs_url")} />
							</div>
						</div>
						<div className="space-y-2">
							<Label htmlFor="mcp-add-tags">Tags</Label>
							<Input
								id="mcp-add-tags"
								placeholder="comma separated, e.g. internal, database"
								data-testid="mcp-add-tags-input"
								{...register("tags")}
							/>
						</div>
					</div>

					<div className="border-border bg-card sticky bottom-0 z-10 border-t px-8 py-4">
						<div className="flex justify-end gap-2">
							<Button type="button" variant="outline" onClick={onClose} disabled={isLoading} data-testid="mcp-add-cancel-btn">
								Cancel
							</Button>
							<Button type="submit" disabled={isLoading} data-testid="mcp-add-submit-btn">
								{isLoading ? "Publishing..." : "Publish to Library"}
							</Button>
						</div>
					</div>
				</form>
			</SheetContent>
		</Sheet>
	);
}