import { Badge } from "@/components/ui/badge";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { TriStateCheckbox } from "@/components/ui/tristateCheckbox";
import type { MCPOpenAPICredential, MCPOpenAPIPreviewResponse, SecretVar } from "@/lib/types/mcp";
import { Info } from "lucide-react";
import { useMemo, useState } from "react";
import {
	mergePreviewRows,
	defaultSchemeHeader,
	securitySchemeToInputs,
	selectableServers,
	supportedToolNames,
	type OpenAPICredentialField,
} from "./mcpClientOpenApiUtils";
import { SectionHeader } from "./sectionHeader";

export type OpenAPIBaseUrlChoice = "server" | "custom";

export interface OpenAPIPreviewPanelProps {
	preview: MCPOpenAPIPreviewResponse;
	baseUrlChoice: OpenAPIBaseUrlChoice;
	onBaseUrlChoiceChange: (choice: OpenAPIBaseUrlChoice) => void;
	baseUrl: string;
	onBaseUrlChange: (url: string) => void;
	baseUrlError?: string;
	credentials: Record<string, MCPOpenAPICredential>;
	onCredentialChange: (scheme: string, field: OpenAPICredentialField, value: SecretVar) => void;
	/** Set when each caller supplies credentials (per-user headers): header-carried schemes name that header instead of storing a value. */
	perUserHeaders?: { typed: Record<string, string>; onChange: (scheme: string, header: string) => void };
	selectedTools: string[];
	onSelectedToolsChange: (names: string[]) => void;
	toolsError?: string;
	disabled?: boolean;
}

const SEARCH_THRESHOLD = 8;

const emptySecretVar: SecretVar = { value: "", ref: "" };

/**
 * What the preview endpoint found in the document: its identity, the servers
 * to pick an upstream from, credential inputs for the security schemes it
 * declares, and the operations that become tools. The ticked operations are
 * the server's tools_to_execute.
 */
export function OpenAPIPreviewPanel({
	preview,
	baseUrlChoice,
	onBaseUrlChoiceChange,
	baseUrl,
	onBaseUrlChange,
	baseUrlError,
	credentials,
	onCredentialChange,
	perUserHeaders,
	selectedTools,
	onSelectedToolsChange,
	toolsError,
	disabled,
}: OpenAPIPreviewPanelProps) {
	const [search, setSearch] = useState("");
	const rows = useMemo(() => mergePreviewRows(preview.tools, preview.unsupported), [preview]);
	const supported = useMemo(() => supportedToolNames(preview), [preview]);
	const selected = useMemo(() => new Set(selectedTools), [selectedTools]);
	const servers = selectableServers(preview);
	const schemeInputs = useMemo(() => (preview.security_schemes ?? []).map(securitySchemeToInputs), [preview]);

	const visibleRows = useMemo(() => {
		// The search box is only shown above the threshold; a query left over
		// from a larger document must not filter a smaller one with no box to clear it.
		const q = rows.length > SEARCH_THRESHOLD ? search.trim().toLowerCase() : "";
		if (!q) return rows;
		return rows.filter((row) => [row.name, row.method, row.path, row.description ?? ""].some((v) => v.toLowerCase().includes(q)));
	}, [rows, search]);

	const toggleTool = (name: string, checked: boolean) => {
		if (checked) {
			if (selected.has(name)) return;
			onSelectedToolsChange(supported.filter((n) => n === name || selected.has(n)));
		} else {
			onSelectedToolsChange(selectedTools.filter((n) => n !== name));
		}
	};

	const selectedCount = supported.filter((n) => selected.has(n)).length;

	return (
		<div className="space-y-5" data-testid="openapi-preview-panel">
			{/* Summary */}
			<div className="bg-muted/40 rounded-md border px-3 py-2 text-sm" data-testid="openapi-preview-summary">
				<div className="flex flex-wrap items-center gap-2">
					<span className="font-medium" data-testid="openapi-preview-title">
						{preview.title || "Untitled API"}
					</span>
					<span className="text-muted-foreground text-xs" data-testid="openapi-preview-version">
						{preview.version ? `v${preview.version} · ` : ""}OpenAPI {preview.openapi_version}
					</span>
					<Badge variant="outline" className="ml-auto font-mono text-[10px]">
						{preview.tool_count} tool{preview.tool_count === 1 ? "" : "s"}
					</Badge>
				</div>
				{preview.description && <p className="text-muted-foreground mt-1 line-clamp-2 text-xs">{preview.description}</p>}
				{preview.warnings?.length > 0 && (
					<ul className="mt-2 space-y-1" data-testid="openapi-preview-warnings">
						{preview.warnings.map((warning) => (
							<li key={warning} className="flex items-start gap-2 text-xs text-amber-800">
								<Info className="mt-0.5 h-3.5 w-3.5 shrink-0 text-amber-600" />
								<span>{warning}</span>
							</li>
						))}
					</ul>
				)}
			</div>

			{/* Base URL */}
			<div className="space-y-2">
				<Label htmlFor="openapi-base-url">Base URL</Label>
				{servers.length > 0 && (
					<Select
						value={baseUrlChoice === "custom" ? "__custom__" : baseUrl}
						disabled={disabled}
						onValueChange={(value) => {
							if (value === "__custom__") {
								onBaseUrlChoiceChange("custom");
								return;
							}
							onBaseUrlChoiceChange("server");
							onBaseUrlChange(value);
						}}
					>
						<SelectTrigger className="w-full" data-testid="openapi-base-url-select">
							<SelectValue placeholder="Select the upstream server" />
						</SelectTrigger>
						<SelectContent>
							{servers.map((server, index) => (
								<SelectItem key={server} value={server} data-testid={`openapi-base-url-option-${index}`}>
									<span className="font-mono text-xs">{server}</span>
								</SelectItem>
							))}
							<SelectItem value="__custom__" data-testid="openapi-base-url-option-custom">
								Custom…
							</SelectItem>
						</SelectContent>
					</Select>
				)}
				{(baseUrlChoice === "custom" || servers.length === 0) && (
					<Input
						id="openapi-base-url"
						data-testid="openapi-base-url-input"
						placeholder="https://api.example.com/v1"
						value={baseUrl}
						disabled={disabled}
						onChange={(e) => {
							// Typing is an explicit override, so the next parse sends it.
							if (baseUrlChoice !== "custom") onBaseUrlChoiceChange("custom");
							onBaseUrlChange(e.target.value);
						}}
						aria-invalid={!!baseUrlError}
					/>
				)}
				{servers.length === 0 &&
					(preview.operation_servers ? (
						<p className="text-muted-foreground text-xs" data-testid="openapi-operation-servers-note">
							Every operation declares its own server, so calls go there. Set a base URL only to override them all.
						</p>
					) : (
						<p className="text-muted-foreground text-xs">
							The document declares no absolute server URL, so the upstream must be given here.
						</p>
					))}
				{baseUrlError && (
					<p className="text-destructive text-xs" data-testid="openapi-base-url-error">
						{baseUrlError}
					</p>
				)}
			</div>

			{/* Security */}
			<div className="space-y-3">
				<SectionHeader
					title="API authentication"
					description={
						perUserHeaders
							? "Each caller supplies these through the per-user header named for each scheme, forwarded to the API as is. Schemes sent in the query or a cookie keep a stored value."
							: "Credentials for the security schemes the document declares. They are stored encrypted and applied to every call; env.VAR references are supported."
					}
				/>
				{schemeInputs.length === 0 ? (
					<p className="text-muted-foreground text-xs" data-testid="openapi-security-empty">
						This document declares no security schemes. Add static headers below if the API still needs a credential.
					</p>
				) : (
					<div className="space-y-3 rounded-md border p-3">
						{schemeInputs.map(({ scheme, label, fields, helper }) => (
							<div key={scheme.name} className="space-y-2" data-testid={`openapi-security-${scheme.name}`}>
								<div className="flex items-center gap-2">
									<Label className="text-xs font-medium">{label}</Label>
									{!scheme.supported && (
										<Badge variant="outline" className="text-[10px]">
											unsupported
										</Badge>
									)}
								</div>
								{fields.length === 0 ? (
									<p className="text-muted-foreground text-xs">{helper}</p>
								) : perUserHeaders && defaultSchemeHeader(scheme) !== null ? (
									<div className="space-y-1">
										<Input
											aria-label={`${scheme.name} per-user header`}
											placeholder={defaultSchemeHeader(scheme) ?? ""}
											disabled={disabled}
											value={
												Object.hasOwn(perUserHeaders.typed, scheme.name)
													? perUserHeaders.typed[scheme.name]
													: (defaultSchemeHeader(scheme) ?? "")
											}
											onChange={(e) => perUserHeaders.onChange(scheme.name, e.target.value)}
											data-testid={`openapi-security-${scheme.name}-header-input`}
										/>
										<p className="text-muted-foreground text-xs">
											{scheme.type === "http"
												? `Header each caller supplies, forwarded as is; its value is the full credential, e.g. "${scheme.scheme === "basic" ? "Basic <base64>" : "Bearer <token>"}". It is added to Required Headers.`
												: "Header each caller supplies, forwarded as is. It is added to Required Headers."}
										</p>
									</div>
								) : (
									fields.map(({ field, label: fieldLabel, placeholder }) => (
										<SecretVarInput
											key={field}
											aria-label={`${scheme.name} ${fieldLabel}`}
											placeholder={placeholder}
											disabled={disabled}
											value={credentials[scheme.name]?.[field] ?? emptySecretVar}
											onChange={(value) => onCredentialChange(scheme.name, field, value)}
											data-testid={`openapi-security-${scheme.name}-${field === "value" ? "input" : `${field}-input`}`}
										/>
									))
								)}
							</div>
						))}
					</div>
				)}
			</div>

			{/* Operations */}
			<div className="space-y-3">
				<div className="flex items-start justify-between gap-4">
					<SectionHeader
						title={`Operations (${supported.length} of ${rows.length} supported)`}
						description="Ticked operations become tools of this server. Unsupported ones are listed with the reason."
					/>
					{supported.length > 0 && (
						<div className="flex items-center gap-2 pt-1">
							<span className="text-muted-foreground text-xs" data-testid="openapi-tools-selected-count">
								{selectedCount} of {supported.length} selected
							</span>
							<TriStateCheckbox
								allIds={supported}
								selectedIds={selectedTools}
								disabled={disabled}
								ariaLabel="Select all operations"
								data-testid="openapi-tools-select-all"
								onChange={onSelectedToolsChange}
							/>
						</div>
					)}
				</div>
				{rows.length > SEARCH_THRESHOLD && (
					<Input
						placeholder="Search operations…"
						value={search}
						onChange={(e) => setSearch(e.target.value)}
						data-testid="openapi-tools-search"
					/>
				)}
				<div className="divide-y rounded-md border">
					{visibleRows.length === 0 && <p className="text-muted-foreground px-3 py-2 text-xs">No operation matches the search.</p>}
					{visibleRows.map((row) => (
						<div
							key={row.key}
							className={`flex items-start gap-3 px-3 py-2 ${row.supported ? "" : "opacity-60"}`}
							data-testid={`openapi-tool-row-${row.name}`}
						>
							<Checkbox
								id={`openapi-tool-${row.key}`}
								className="mt-0.5"
								checked={row.supported && selected.has(row.name)}
								disabled={disabled || !row.supported}
								aria-label={`${row.supported ? "Include" : "Unsupported"} ${row.name}`}
								onCheckedChange={(checked) => toggleTool(row.name, checked === true)}
								data-testid={`openapi-tool-checkbox-${row.name}`}
							/>
							<div className="min-w-0 flex-1">
								<div className="flex flex-wrap items-center gap-2">
									<Badge variant="outline" className="font-mono text-[10px]" data-testid={`openapi-tool-method-${row.name}`}>
										{row.method}
									</Badge>
									<span className="font-mono text-xs break-all">{row.path}</span>
									<span className="text-sm font-medium">{row.name}</span>
									{row.deprecated && (
										<Badge variant="secondary" className="text-[10px]">
											deprecated
										</Badge>
									)}
									{!row.supported && row.skipReason && (
										<TooltipProvider>
											<Tooltip>
												<TooltipTrigger asChild>
													<Info
														className="text-muted-foreground h-3.5 w-3.5 cursor-help"
														data-testid={`openapi-tool-skip-reason-${row.name}`}
													/>
												</TooltipTrigger>
												<TooltipContent className="max-w-xs">
													<p>{row.skipReason}</p>
												</TooltipContent>
											</Tooltip>
										</TooltipProvider>
									)}
								</div>
								{row.description && <p className="text-muted-foreground mt-0.5 line-clamp-2 text-xs">{row.description}</p>}
								{!row.supported && row.skipReason && <p className="text-muted-foreground mt-0.5 text-xs">{row.skipReason}</p>}
								{row.warnings.map((warning) => (
									<p key={warning} className="mt-0.5 text-xs text-amber-800">
										{warning}
									</p>
								))}
							</div>
						</div>
					))}
				</div>
				{toolsError && (
					<p className="text-destructive text-xs" data-testid="openapi-tools-error">
						{toolsError}
					</p>
				)}
			</div>
		</div>
	);
}