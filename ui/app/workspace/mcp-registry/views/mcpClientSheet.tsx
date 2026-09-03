import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Fragment } from "react";

import { SheetNavigationButtons } from "@/components/sheetNavigationButtons";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { MultiSelect } from "@/components/ui/multiSelect";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { TriStateCheckbox } from "@/components/ui/tristateCheckbox";
import { useToast } from "@/hooks/use-toast";
import { useSheetNavigation } from "@/hooks/useSheetNavigation";
import { useT } from "@/lib/i18n";
import { IS_ENTERPRISE, MCP_STATUS_COLORS } from "@/lib/constants/config";
import { VirtualKeySelector } from "@/components/entitySelectors/virtualKeySelector";
import { getErrorMessage, useGetCoreConfigQuery, useGetVirtualKeysQuery, useUpdateMCPClientMutation } from "@/lib/store";
import { MCPClient, MCPVKConfig } from "@/lib/types/mcp";
import { mcpClientUpdateSchema, type MCPClientUpdateSchema } from "@/lib/types/schemas";
import { parseArrayFromText } from "@/lib/utils/array";
import { titleCaseFromSnakeCase } from "@/lib/utils/strings";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useGetSCIMProvidersQuery } from "@enterprise/lib/store/apis/scimApi";
import { zodResolver } from "@hookform/resolvers/zod";
import { ChevronDown, ChevronRight, Info, Plus, Trash2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { OAuthAdvancedFields } from "./oauthAdvancedFields";
import { OAuth2Authorizer } from "./oauth2Authorizer";
import { SectionHeader } from "./sectionHeader";
import { TLSConfigFields } from "./tlsConfigFields";
import { TokenExchangeFields } from "./tokenExchangeFields";

interface MCPClientSheetProps {
	mcpClient: MCPClient;
	onClose: () => void;
	onSubmitSuccess: () => void;
	onNavigate?: (direction: "prev" | "next") => void;
	hasPrev?: boolean;
	hasNext?: boolean;
}

/** API sends tool_sync_interval as nanoseconds (Go time.Duration). Normalize to minutes for form/store. */
function toolSyncIntervalToMinutes(v: number | undefined | null): number {
	if (v === undefined || v === null) return 0;
	const n = Number(v);
	if (Number.isNaN(n)) return 0;
	if (Math.abs(n) >= 1e9) return Math.round(n / 6e10);
	return n;
}

/** API sends tool_execution_timeout as a Go duration string e.g. "30s". Normalize to whole seconds for form. */
function toolExecutionTimeoutToSeconds(v: string | number | undefined | null): number {
	if (v === undefined || v === null || v === "") return 0;
	if (typeof v === "number") return v;
	// Parse Go duration string: "30s", "1m30s", "2h", etc.
	let total = 0;
	const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g;
	let match;
	while ((match = re.exec(v)) !== null) {
		const n = parseFloat(match[1]);
		switch (match[2]) {
			case "ns":
				total += n / 1e9;
				break;
			case "us":
			case "µs":
				total += n / 1e6;
				break;
			case "ms":
				total += n / 1e3;
				break;
			case "s":
				total += n;
				break;
			case "m":
				total += n * 60;
				break;
			case "h":
				total += n * 3600;
				break;
		}
	}
	return Math.ceil(total);
}

export default function MCPClientSheet({
	mcpClient,
	onClose,
	onSubmitSuccess,
	onNavigate,
	hasPrev = false,
	hasNext = false,
}: MCPClientSheetProps) {
	const t = useT();
	const hasUpdateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Update);
	const isPerUserAuth =
		mcpClient.config.auth_type === "per_user_oauth" ||
		mcpClient.config.auth_type === "per_user_headers" ||
		mcpClient.config.auth_type === "token_exchange";
	const [updateMCPClient, { isLoading: isUpdating }] = useUpdateMCPClientMutation();

	const { toast } = useToast();

	const [pendingNavDirection, setPendingNavDirection] = useState<"prev" | "next" | null>(null);
	const [selectedTab, setSelectedTab] = useState("general");

	// Land back on the General tab whenever a different client is opened (e.g. via prev/next navigation).
	useEffect(() => {
		setSelectedTab("general");
	}, [mcpClient.config.client_id]);

	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const globalToolSyncInterval = bifrostConfig?.client_config?.mcp_tool_sync_interval ?? 10;
	const globalToolExecutionTimeout = bifrostConfig?.client_config?.mcp_tool_execution_timeout ?? 30;
	const [expandedTools, setExpandedTools] = useState<Set<string>>(new Set());

	const allToolNames = useMemo(() => mcpClient.tools?.map((t) => t.name) ?? [], [mcpClient.tools]);

	// Initial VK configs come directly from the MCP client response — always complete, no pagination issue.
	const initialVKConfigs = useMemo<MCPVKConfig[]>(
		() => (mcpClient.vk_configs ?? []).map((vc) => ({ virtual_key_id: vc.virtual_key_id, tools_to_execute: vc.tools_to_execute })),
		[mcpClient.vk_configs],
	);

	const [vkConfigs, setVKConfigs] = useState<MCPVKConfig[]>([]);
	const [vkConfigsDirty, setVKConfigsDirty] = useState(false);
	const [allowedExtraHeadersRaw, setAllowedExtraHeadersRaw] = useState<string>((mcpClient.config.allowed_extra_headers || []).join(", "));
	const [perUserHeaderKeysRaw, setPerUserHeaderKeysRaw] = useState<string>((mcpClient.config.per_user_header_keys || []).join(", "));
	const [oauthScopesRaw, setOauthScopesRaw] = useState<string>((mcpClient.config.oauth_scopes || []).join(", "));
	const [tokenExchangeScopesRaw, setTokenExchangeScopesRaw] = useState<string>((mcpClient.config.token_exchange?.scopes || []).join(", "));
	// Persists names for newly added VKs so they survive search result changes
	const [localVKNames, setLocalVKNames] = useState<Record<string, string>>({});

	// Sync vkConfigs when mcpClient changes
	useEffect(() => {
		setVKConfigs(initialVKConfigs);
		setVKConfigsDirty(false);
		setLocalVKNames({});
	}, [initialVKConfigs]);

	// Sync allowedExtraHeadersRaw when mcpClient changes
	useEffect(() => {
		setAllowedExtraHeadersRaw((mcpClient.config.allowed_extra_headers || []).join(", "));
	}, [mcpClient.config.allowed_extra_headers]);

	useEffect(() => {
		setPerUserHeaderKeysRaw((mcpClient.config.per_user_header_keys || []).join(", "));
	}, [mcpClient.config.per_user_header_keys]);

	useEffect(() => {
		setOauthScopesRaw((mcpClient.config.oauth_scopes || []).join(", "));
	}, [mcpClient.config.oauth_scopes]);

	useEffect(() => {
		setTokenExchangeScopesRaw((mcpClient.config.token_exchange?.scopes || []).join(", "));
	}, [mcpClient.config.token_exchange?.scopes]);

	// Name lookup: server response names → names captured when a key was picked.
	// Every row is one or the other, so the selector's results aren't needed here.
	const vkNameByID = useMemo<Record<string, string>>(() => {
		const m: Record<string, string> = {};
		for (const vc of mcpClient.vk_configs ?? []) m[vc.virtual_key_id] = vc.virtual_key_name;
		Object.assign(m, localVKNames);
		return m;
	}, [mcpClient.vk_configs, localVKNames]);

	const configuredVKIDs = useMemo(() => vkConfigs.map((vc) => vc.virtual_key_id), [vkConfigs]);

	const toolOptions = useMemo(
		() => [
			{ value: "*", label: t("mcp.sheet.allowAllTools"), description: t("mcp.sheet.allowAllToolsDesc") },
			...allToolNames.map((n) => ({ value: n, label: n })),
		],
		[allToolNames, t],
	);
	const supportsOAuthCredentialUpdate = mcpClient.config.auth_type === "oauth" || mcpClient.config.auth_type === "per_user_oauth";
	const supportsTokenExchangeCredentialUpdate = mcpClient.config.auth_type === "token_exchange";
	// Entra's on-behalf-of grant requires use_idp_credentials — see the
	// Prerequisites warning in docs/mcp/auth/token-exchange.mdx for why a
	// dedicated exchange app structurally can't work there.
	const { data: scimProviders } = useGetSCIMProvidersQuery(undefined, { skip: !IS_ENTERPRISE || !supportsTokenExchangeCredentialUpdate });
	const enabledScimProvider = scimProviders?.find((p) => (p as { enabled?: boolean }).enabled) as { name?: string } | undefined;
	// Matches the create form's idpConfigured gate: without an enabled
	// provider there's nothing for use_idp_credentials to resolve against at
	// runtime, so the picker shouldn't offer that option at all here either.
	const idpConfigured = !!enabledScimProvider;
	const isEntraIdp = ["entra", "azure", "azuread"].includes((enabledScimProvider?.name ?? "").toLowerCase());

	const addVKConfig = ({ value: vkId, label }: { value: string; label: string }) => {
		setLocalVKNames((prev) => ({ ...prev, [vkId]: label }));
		setVKConfigs((prev) => [...prev, { virtual_key_id: vkId, tools_to_execute: ["*"] }]);
		setVKConfigsDirty(true);
	};

	const removeVKConfig = (vkId: string) => {
		setVKConfigs((prev) => prev.filter((vc) => vc.virtual_key_id !== vkId));
		setVKConfigsDirty(true);
	};

	const updateVKConfigTools = (vkId: string, tools: string[]) => {
		setVKConfigs((prev) => prev.map((vc) => (vc.virtual_key_id === vkId ? { ...vc, tools_to_execute: tools } : vc)));
		setVKConfigsDirty(true);
	};

	const toggleToolExpanded = (toolName: string) => {
		setExpandedTools((prev) => {
			const next = new Set(prev);
			if (next.has(toolName)) {
				next.delete(toolName);
			} else {
				next.add(toolName);
			}
			return next;
		});
	};

	const form = useForm<MCPClientUpdateSchema>({
		resolver: zodResolver(mcpClientUpdateSchema),
		mode: "onBlur",
		defaultValues: {
			name: mcpClient.config.name,
			is_code_mode_client: mcpClient.config.is_code_mode_client || false,
			is_ping_available: mcpClient.config.is_ping_available === true || mcpClient.config.is_ping_available === undefined,
			needs_session_stickiness: mcpClient.config.needs_session_stickiness === true,
			allow_by_default: mcpClient.config.allow_by_default || false,
			disabled: mcpClient.config.disabled || false,
			headers: mcpClient.config.headers,
			per_user_header_keys: mcpClient.config.auth_type === "per_user_headers" ? mcpClient.config.per_user_header_keys || [] : undefined,
			tools_to_execute: mcpClient.config.tools_to_execute || [],
			tools_to_auto_execute: mcpClient.config.tools_to_auto_execute || [],
			tool_pricing: mcpClient.config.tool_pricing || {},
			tool_sync_interval: toolSyncIntervalToMinutes(mcpClient.config.tool_sync_interval),
			tool_execution_timeout: toolExecutionTimeoutToSeconds(mcpClient.config.tool_execution_timeout),
			allowed_extra_headers: mcpClient.config.allowed_extra_headers || [],
			oauth_config: supportsOAuthCredentialUpdate
				? {
						client_id: mcpClient.config.oauth_client_id,
						client_secret: mcpClient.config.oauth_client_secret,
						authorize_url: mcpClient.config.oauth_authorize_url,
						token_url: mcpClient.config.oauth_token_url,
						registration_url: mcpClient.config.oauth_registration_url,
						resource: mcpClient.config.oauth_resource,
					}
				: undefined,
			// Unlike oauth_config, token_exchange is replaced wholesale server-side
			// (only client_id/client_secret get redacted-value preservation) — so
			// every field, not just the ones the user edits, must be pre-populated
			// with its current stored value rather than left blank.
			token_exchange: supportsTokenExchangeCredentialUpdate
				? {
						audience: mcpClient.config.token_exchange?.audience,
						use_idp_credentials: mcpClient.config.token_exchange?.use_idp_credentials,
						client_id: mcpClient.config.token_exchange?.client_id,
						client_secret: mcpClient.config.token_exchange?.client_secret,
						authorization_server_url: mcpClient.config.token_exchange?.authorization_server_url,
					}
				: undefined,
			tls_config: mcpClient.config.tls_config
				? {
						insecure_skip_verify: mcpClient.config.tls_config.insecure_skip_verify,
						ca_cert_pem: mcpClient.config.tls_config.ca_cert_pem,
					}
				: undefined,
		},
	});
	const isDisabled = form.watch("disabled");
	const needsSessionStickiness = form.watch("needs_session_stickiness");

	// Reset form when mcpClient changes
	useEffect(() => {
		form.reset({
			name: mcpClient.config.name,
			is_code_mode_client: mcpClient.config.is_code_mode_client || false,
			is_ping_available: mcpClient.config.is_ping_available === true || mcpClient.config.is_ping_available === undefined,
			needs_session_stickiness: mcpClient.config.needs_session_stickiness === true,
			allow_by_default: mcpClient.config.allow_by_default || false,
			disabled: mcpClient.config.disabled || false,
			headers: mcpClient.config.headers,
			per_user_header_keys: mcpClient.config.auth_type === "per_user_headers" ? mcpClient.config.per_user_header_keys || [] : undefined,
			tools_to_execute: mcpClient.config.tools_to_execute || [],
			tools_to_auto_execute: mcpClient.config.tools_to_auto_execute || [],
			tool_pricing: mcpClient.config.tool_pricing || {},
			tool_sync_interval: toolSyncIntervalToMinutes(mcpClient.config.tool_sync_interval),
			tool_execution_timeout: toolExecutionTimeoutToSeconds(mcpClient.config.tool_execution_timeout),
			allowed_extra_headers: mcpClient.config.allowed_extra_headers || [],
			oauth_config: supportsOAuthCredentialUpdate
				? {
						client_id: mcpClient.config.oauth_client_id,
						client_secret: mcpClient.config.oauth_client_secret,
						authorize_url: mcpClient.config.oauth_authorize_url,
						token_url: mcpClient.config.oauth_token_url,
						registration_url: mcpClient.config.oauth_registration_url,
						resource: mcpClient.config.oauth_resource,
					}
				: undefined,
			// Unlike oauth_config, token_exchange is replaced wholesale server-side
			// (only client_id/client_secret get redacted-value preservation) — so
			// every field, not just the ones the user edits, must be pre-populated
			// with its current stored value rather than left blank.
			token_exchange: supportsTokenExchangeCredentialUpdate
				? {
						audience: mcpClient.config.token_exchange?.audience,
						use_idp_credentials: mcpClient.config.token_exchange?.use_idp_credentials,
						client_id: mcpClient.config.token_exchange?.client_id,
						client_secret: mcpClient.config.token_exchange?.client_secret,
						authorization_server_url: mcpClient.config.token_exchange?.authorization_server_url,
					}
				: undefined,
			tls_config: mcpClient.config.tls_config
				? {
						insecure_skip_verify: mcpClient.config.tls_config.insecure_skip_verify,
						ca_cert_pem: mcpClient.config.tls_config.ca_cert_pem,
					}
				: undefined,
		});
	}, [form, mcpClient, supportsOAuthCredentialUpdate, supportsTokenExchangeCredentialUpdate]);

	const initialOauthScopesRaw = (mcpClient.config.oauth_scopes || []).join(", ");
	const oauthScopesDirty = oauthScopesRaw !== initialOauthScopesRaw;
	const initialTokenExchangeScopesRaw = (mcpClient.config.token_exchange?.scopes || []).join(", ");
	const tokenExchangeScopesDirty = tokenExchangeScopesRaw !== initialTokenExchangeScopesRaw;
	const isDirty = form.formState.isDirty || vkConfigsDirty || oauthScopesDirty || tokenExchangeScopesDirty;
	// dirtyFields tracks deep changes vs. the pre-populated default values —
	// used both to gate the rotation warning below and, in onSubmit, to only
	// rotate when the user actually changed a field. Every oauth_config field
	// triggers the same rotation and reauth cascade server-side (not just
	// client_id/client_secret), so any of them counts as "dirty" here.
	const oauthCredentialsDirty = !!(
		form.formState.dirtyFields.oauth_config?.client_id ||
		form.formState.dirtyFields.oauth_config?.client_secret ||
		form.formState.dirtyFields.oauth_config?.authorize_url ||
		form.formState.dirtyFields.oauth_config?.token_url ||
		form.formState.dirtyFields.oauth_config?.registration_url ||
		form.formState.dirtyFields.oauth_config?.resource ||
		oauthScopesDirty
	);
	// Same rationale as oauthCredentialsDirty: any touched token_exchange
	// field gates both the cache-eviction warning below and, in onSubmit,
	// whether the block is sent at all (see the comment there on why it must
	// be sent whole, not just the changed field).
	const tokenExchangeCredentialsDirty = !!(
		form.formState.dirtyFields.token_exchange?.audience ||
		form.formState.dirtyFields.token_exchange?.use_idp_credentials ||
		form.formState.dirtyFields.token_exchange?.client_id ||
		form.formState.dirtyFields.token_exchange?.client_secret ||
		form.formState.dirtyFields.token_exchange?.authorization_server_url ||
		tokenExchangeScopesDirty
	);
	// Gates the "takes effect immediately" warning below the toggle — the
	// backend applies a stickiness change live (opens or closes the shared
	// connection) as part of the same update, not on some later reconnect.
	const needsSessionStickinessDirty = !!form.formState.dirtyFields.needs_session_stickiness;

	const handleNavigate = useCallback(
		(direction: "prev" | "next") => {
			if (isDirty) {
				setPendingNavDirection(direction);
			} else {
				onNavigate?.(direction);
			}
		},
		[isDirty, onNavigate],
	);

	const { prev: prevKeys, next: nextKeys } = useSheetNavigation({
		enabled: !pendingNavDirection,
		hasPrev,
		hasNext,
		onNavigate: handleNavigate,
	});

	const onSubmit = async (data: MCPClientUpdateSchema) => {
		try {
			if (mcpClient.config.auth_type === "per_user_headers" && (!data.per_user_header_keys || data.per_user_header_keys.length === 0)) {
				toast({
					title: t("mcp.validation.headerKeysRequiredTitle"),
					description: t("mcp.validation.headerKeysRequiredDesc"),
					variant: "destructive",
				});
				return;
			}
			const oauthClientID = data.oauth_config?.client_id;
			const oauthClientSecret = data.oauth_config?.client_secret;
			// Omitted (undefined) preserves the stored scopes; an explicit
			// empty array clears them. Only send either when the user
			// actually touched this field — otherwise an untouched-but-empty
			// initial value would clear scopes nobody asked to change.
			const oauthScopes = !oauthScopesDirty
				? undefined
				: oauthScopesRaw.trim()
					? oauthScopesRaw
							.split(",")
							.map((s) => s.trim())
							.filter(Boolean)
					: [];
			// Only rotate when the user actually changed a field, and never
			// alongside a disable (the backend rejects that combination
			// outright — disabling and rotating credentials must be two
			// separate requests). The oauth_config draft itself is left in
			// form state either way, so re-enabling before a later save still
			// submits the edited values.
			const shouldRotateOAuthCredentials = supportsOAuthCredentialUpdate && !data.disabled && oauthCredentialsDirty;
			// Unlike oauth_config, the backend replaces token_exchange wholesale
			// (it only preserves client_id/client_secret when they round-trip the
			// redacted sentinel) — so whenever anything in this block changed, the
			// full current state (including untouched fields) must be resent, not
			// just the edited field, or untouched fields would be cleared.
			const shouldUpdateTokenExchange = supportsTokenExchangeCredentialUpdate && tokenExchangeCredentialsDirty;
			const tokenExchangeScopes = tokenExchangeScopesRaw.trim()
				? tokenExchangeScopesRaw
						.split(",")
						.map((s) => s.trim())
						.filter(Boolean)
				: [];
			await updateMCPClient({
				id: mcpClient.config.client_id,
				data: {
					name: data.name,
					is_code_mode_client: data.is_code_mode_client,
					is_ping_available: data.is_ping_available,
					// Only meaningful (and only accepted) for http clients — an
					// explicit false is rejected for sse/stdio, which always keep
					// a persistent connection regardless of this field.
					needs_session_stickiness: mcpClient.config.connection_type === "http" ? data.needs_session_stickiness : undefined,
					allow_by_default: data.allow_by_default,
					disabled: data.disabled,
					headers: data.headers ?? {},
					per_user_header_keys: mcpClient.config.auth_type === "per_user_headers" ? data.per_user_header_keys : undefined,
					tools_to_execute: data.tools_to_execute,
					tools_to_auto_execute: data.tools_to_auto_execute,
					tool_pricing: data.tool_pricing,
					// Sent only when edited: PUT has PATCH semantics, and the
					// nanoseconds-to-minutes normalization is lossy for a sub-minute
					// interval set in config.json, so echoing the field back on an
					// unrelated edit would silently rewrite it.
					tool_sync_interval: form.formState.dirtyFields.tool_sync_interval ? (data.tool_sync_interval ?? 0) : undefined,
					tool_execution_timeout: data.tool_execution_timeout ?? 0,
					allowed_extra_headers: data.allowed_extra_headers,
					oauth_config: shouldRotateOAuthCredentials
						? {
								client_id: oauthClientID,
								client_secret: oauthClientSecret,
								authorize_url: data.oauth_config?.authorize_url || undefined,
								token_url: data.oauth_config?.token_url || undefined,
								registration_url: data.oauth_config?.registration_url || undefined,
								scopes: oauthScopes,
								resource: data.oauth_config?.resource || undefined,
							}
						: undefined,
					token_exchange: shouldUpdateTokenExchange
						? {
								audience: data.token_exchange?.audience?.trim() || "",
								use_idp_credentials: data.token_exchange?.use_idp_credentials ?? false,
								client_id: data.token_exchange?.use_idp_credentials
									? undefined
									: (data.token_exchange?.client_id ?? { value: "", ref: "" }),
								client_secret: data.token_exchange?.use_idp_credentials ? undefined : data.token_exchange?.client_secret,
								authorization_server_url: data.token_exchange?.authorization_server_url?.trim() || undefined,
								scopes: tokenExchangeScopes,
							}
						: undefined,
					tls_config:
						data.tls_config !== undefined
							? {
									insecure_skip_verify: data.tls_config.insecure_skip_verify ?? false,
									ca_cert_pem: data.tls_config.ca_cert_pem,
								}
							: undefined,
					vk_configs: vkConfigsDirty ? vkConfigs : undefined,
				},
			}).unwrap();

			toast({
				title: t("mcp.success"),
				description: t("mcp.sheet.updated"),
			});
			onSubmitSuccess();
		} catch (error) {
			toast({
				title: t("mcp.error"),
				description: getErrorMessage(error),
				variant: "destructive",
			});
		}
	};

	const handleToolToggle = (toolName: string, checked: boolean) => {
		const currentTools = form.getValues("tools_to_execute") || [];
		let newTools: string[];
		const allToolNames = mcpClient.tools?.map((tool) => tool.name) || [];

		// Check if we're in "all tools" mode (wildcard)
		const isAllToolsMode = currentTools.includes("*");

		if (isAllToolsMode) {
			if (checked) {
				// Already all selected, keep wildcard
				newTools = ["*"];
			} else {
				// Unchecking a tool when all are selected - switch to explicit list without this tool
				newTools = allToolNames.filter((name) => name !== toolName);
			}
		} else {
			// We're in explicit tool selection mode
			if (checked) {
				// Add tool to selection
				newTools = currentTools.includes(toolName) ? currentTools : [...currentTools, toolName];

				// If we now have all tools selected, switch to wildcard mode
				if (newTools.length === allToolNames.length) {
					newTools = ["*"];
				}
			} else {
				// Remove tool from selection
				newTools = currentTools.filter((tool) => tool !== toolName);
			}
		}

		form.setValue("tools_to_execute", newTools, { shouldDirty: true });

		// If tool is being removed from tools_to_execute, also remove it from tools_to_auto_execute
		if (!checked) {
			const currentAutoExecute = form.getValues("tools_to_auto_execute") || [];
			if (currentAutoExecute.includes(toolName) || currentAutoExecute.includes("*")) {
				const newAutoExecute = currentAutoExecute.filter((tool) => tool !== toolName);
				// If we had "*" and removed a tool, we need to recalculate
				if (currentAutoExecute.includes("*")) {
					// If all tools mode, keep "*" only if tool is still in tools_to_execute
					if (newTools.includes("*")) {
						form.setValue("tools_to_auto_execute", ["*"], { shouldDirty: true });
					} else {
						// Switch to explicit list - when in wildcard mode, all remaining tools should be auto-execute
						form.setValue("tools_to_auto_execute", newTools, { shouldDirty: true });
					}
				} else {
					form.setValue("tools_to_auto_execute", newAutoExecute, { shouldDirty: true });
				}
			}
		}
	};

	const handleAutoExecuteToggle = (toolName: string, checked: boolean) => {
		const currentAutoExecute = form.getValues("tools_to_auto_execute") || [];
		const currentTools = form.getValues("tools_to_execute") || [];
		const allToolNames = mcpClient.tools?.map((tool) => tool.name) || [];

		// Check if we're in "all tools" mode (wildcard)
		const isAllToolsMode = currentTools.includes("*");
		const isAllAutoExecuteMode = currentAutoExecute.includes("*");

		let newAutoExecute: string[];

		if (isAllAutoExecuteMode) {
			if (checked) {
				// Already all selected, keep wildcard
				newAutoExecute = ["*"];
			} else {
				// Unchecking a tool when all are selected - switch to explicit list without this tool
				if (isAllToolsMode) {
					newAutoExecute = allToolNames.filter((name) => name !== toolName);
				} else {
					newAutoExecute = currentTools.filter((name) => name !== toolName);
				}
			}
		} else {
			// We're in explicit tool selection mode
			if (checked) {
				// Add tool to selection
				newAutoExecute = currentAutoExecute.includes(toolName) ? currentAutoExecute : [...currentAutoExecute, toolName];

				// Only switch to wildcard if ALL tools are enabled (tools_to_execute is "*")
				// and all of those tools are now auto-executed. When specific tools are
				// explicitly listed, keep the explicit list to avoid sending "*" when only
				// a subset of tools is enabled.
				if (
					isAllToolsMode &&
					newAutoExecute.length === allToolNames.length &&
					allToolNames.every((tool) => newAutoExecute.includes(tool))
				) {
					newAutoExecute = ["*"];
				}
			} else {
				// Remove tool from selection
				newAutoExecute = currentAutoExecute.filter((tool) => tool !== toolName);
			}
		}

		form.setValue("tools_to_auto_execute", newAutoExecute, { shouldDirty: true });
	};

	return (
		<>
			<Sheet open onOpenChange={(open) => !open && onClose()}>
				<SheetContent className="flex w-full flex-col overflow-hidden! pt-4 sm:max-w-[60%]">
					<SheetHeader className="w-full p-0 px-4 py-4 md:px-8" showCloseButton={false} headerClassName="mb-0 sticky -top-4 bg-card z-10">
						<div className="flex w-full items-center justify-between">
							<div className="space-y-2">
								<SheetTitle className="flex w-fit items-center gap-2 font-medium">
									{mcpClient.config.name}
									<Badge className={MCP_STATUS_COLORS[mcpClient.state]}>{titleCaseFromSnakeCase(mcpClient.state)}</Badge>
								</SheetTitle>
								<SheetDescription>
									{mcpClient.state === "pending_verification"
										? mcpClient.config.auth_type === "token_exchange"
											? t("mcp.sheet.descPendingExchange")
											: mcpClient.config.auth_type === "per_user_oauth"
												? t("mcp.sheet.descPendingPerUserOauth")
												: t("mcp.sheet.descPendingOauth")
										: mcpClient.state === "needs_reauth"
											? isPerUserAuth
												? t("mcp.sheet.descNeedsReauthPerUser")
												: t("mcp.sheet.descNeedsReauth")
											: t("mcp.sheet.descDefault")}
								</SheetDescription>
							</div>
							<SheetNavigationButtons
								hasPrev={hasPrev}
								hasNext={hasNext}
								onNavigate={handleNavigate}
								prevKeys={prevKeys}
								nextKeys={nextKeys}
								entityLabel={t("mcp.sheet.entityLabel")}
							/>
						</div>
					</SheetHeader>
					<Form {...form}>
						<form onSubmit={form.handleSubmit(onSubmit)} className="flex min-h-0 flex-1 flex-col">
							<div className="min-h-0 flex-1 overflow-y-auto px-4 md:px-8">
								<Tabs value={selectedTab} onValueChange={setSelectedTab} className="w-full">
									<div className="bg-card sticky top-0 z-10 pb-4">
										<TabsList>
											<TabsTrigger value="general" data-testid="mcpclient-tab-general">
												{t("mcp.sheet.tabGeneral")}
											</TabsTrigger>
											<TabsTrigger value="authentication" data-testid="mcpclient-tab-authentication">
												{t("mcp.sheet.tabAuthentication")}
											</TabsTrigger>
											<TabsTrigger value="tools" data-testid="mcpclient-tab-tools">
												{t("mcp.sheet.tabTools")}
											</TabsTrigger>
											<TabsTrigger value="access" data-testid="mcpclient-tab-access">
												{t("mcp.sheet.tabAccess")}
											</TabsTrigger>
										</TabsList>
									</div>

									<TabsContent value="general" className="space-y-6 pb-10">
										<div className="space-y-4">
											<SectionHeader title={t("mcp.basicInfo")} description={t("mcp.sheet.basicInfoDesc")} />
											<FormField
												control={form.control}
												name="name"
												render={({ field }) => (
													<FormItem className="flex flex-col gap-3">
														<div className="flex items-center gap-2">
															<FormLabel>{t("common.name")}</FormLabel>
															<TooltipProvider>
																<Tooltip>
																	<TooltipTrigger asChild>
																		<Info className="text-muted-foreground h-4 w-4 cursor-help" />
																	</TooltipTrigger>
																	<TooltipContent className="max-w-xs">
																		<p>{t("mcp.form.nameTooltip")}</p>
																	</TooltipContent>
																</Tooltip>
															</TooltipProvider>
														</div>
														<div>
															<FormControl>
																<Input placeholder={t("mcp.form.clientNamePlaceholder")} {...field} value={field.value || ""} />
															</FormControl>
															<FormMessage />
														</div>
													</FormItem>
												)}
											/>
											{/* Read-only connection summary. Connection type and target
										    can't be changed after create — surface them here for
										    visibility without exposing edit controls. */}
											<div className="flex flex-col gap-2">
												<div className="text-sm font-medium">{t("mcp.sheet.connection")}</div>
												<div className="bg-muted/40 text-muted-foreground rounded-md border px-3 py-2 text-sm">
													<span className="text-foreground font-mono text-xs uppercase">
														{mcpClient.config.connection_type === "stdio"
															? "STDIO"
															: mcpClient.config.connection_type === "sse"
																? "SSE"
																: "HTTP"}
													</span>
													<span className="mx-2">·</span>
													<span className="font-mono break-all">
														{mcpClient.config.connection_type === "stdio"
															? `${mcpClient.config.stdio_config?.command ?? ""} ${(mcpClient.config.stdio_config?.args ?? []).join(" ")}`.trim() ||
																"-"
															: mcpClient.config.connection_string?.type === "env" || mcpClient.config.connection_string?.type === "vault"
																? mcpClient.config.connection_string.ref
																: mcpClient.config.connection_string?.value || "-"}
													</span>
												</div>
											</div>
											{mcpClient.config.connection_type === "stdio" &&
												mcpClient.config.stdio_config?.envs &&
												mcpClient.config.stdio_config.envs.length > 0 && (
													<div className="space-y-2">
														<div className="text-sm font-medium">{t("mcp.form.envVars")}</div>
														<HeadersTable
															value={Object.fromEntries(
																mcpClient.config.stdio_config.envs.map((env) => {
																	const [name, ...valueParts] = env.split("=");
																	return [name, valueParts.join("=")];
																}),
															)}
															onChange={() => {}}
															fixedKeys={mcpClient.config.stdio_config.envs.map((env) => env.split("=")[0])}
															valuePlaceholder="—"
															label=""
															disabled
														/>
													</div>
												)}
										</div>

										<DottedSeparator />

										<div className="space-y-4">
											<SectionHeader title={t("mcp.serverBehavior")} description={t("mcp.form.serverBehaviorDesc")} />
											<div className="divide-y rounded-md border">
												<FormField
													control={form.control}
													name="is_code_mode_client"
													render={({ field }) => (
														<FormItem className="flex flex-row items-center justify-between gap-4 px-4 py-3">
															<div className="flex items-center gap-2">
																<FormLabel>{t("mcp.form.codeMode")}</FormLabel>
																<TooltipProvider>
																	<Tooltip>
																		<TooltipTrigger asChild>
																			<a
																				href="https://docs.getbifrost.ai/mcp/code-mode"
																				target="_blank"
																				rel="noopener noreferrer"
																				data-testid="code-mode-link-help"
																				className="text-muted-foreground hover:text-foreground focus-visible:ring-ring rounded focus-visible:ring-2 focus-visible:outline-none"
																				aria-label={t("mcp.learnMoreCodeMode")}
																			>
																				<Info className="h-4 w-4 cursor-help" />
																			</a>
																		</TooltipTrigger>
																		<TooltipContent>
																			<p>{t("mcp.clickLearnCodeMode")}</p>
																		</TooltipContent>
																	</Tooltip>
																</TooltipProvider>
															</div>
															<FormControl>
																<Switch checked={field.value || false} onCheckedChange={field.onChange} />
															</FormControl>
														</FormItem>
													)}
												/>
												<FormField
													control={form.control}
													name="is_ping_available"
													render={({ field }) => (
														<FormItem className="flex flex-row items-center justify-between gap-4 px-4 py-3">
															<div className="flex items-center gap-2">
																<FormLabel>{t("mcp.form.pingAvailable")}</FormLabel>
																<TooltipProvider>
																	<Tooltip>
																		<TooltipTrigger asChild>
																			<Info className="text-muted-foreground h-4 w-4 cursor-help" />
																		</TooltipTrigger>
																		<TooltipContent className="max-w-xs">
																			<p>{t("mcp.form.pingTooltip")}</p>
																		</TooltipContent>
																	</Tooltip>
																</TooltipProvider>
															</div>
															<FormControl>
																<Switch checked={field.value === true} onCheckedChange={field.onChange} />
															</FormControl>
														</FormItem>
													)}
												/>
												{mcpClient.config.connection_type === "http" &&
													mcpClient.config.auth_type !== "per_user_oauth" &&
													mcpClient.config.auth_type !== "per_user_headers" &&
													mcpClient.config.auth_type !== "token_exchange" && (
														<>
															<FormField
																control={form.control}
																name="needs_session_stickiness"
																render={({ field }) => (
																	<FormItem className="flex flex-row items-center justify-between gap-4 px-4 py-3">
																		<div className="flex items-center gap-2">
																			<FormLabel>{t("mcp.form.persistentConnection")}</FormLabel>
																			<TooltipProvider>
																				<Tooltip>
																					<TooltipTrigger asChild>
																						<Info className="text-muted-foreground h-4 w-4 cursor-help" />
																					</TooltipTrigger>
																					<TooltipContent className="max-w-xs">
																						<p>{t("mcp.form.persistentTooltipEdit")}</p>
																					</TooltipContent>
																				</Tooltip>
																			</TooltipProvider>
																		</div>
																		<FormControl>
																			<Switch
																				checked={field.value === true}
																				onCheckedChange={field.onChange}
																				data-testid="mcpclient-session-stickiness-switch"
																			/>
																		</FormControl>
																	</FormItem>
																)}
															/>
														</>
													)}
											</div>
											{/* Sits outside the bordered row group: it's a consequence of the
											    toggle above, not another setting row. */}
											{needsSessionStickinessDirty && (
												<div className="flex items-start gap-2 rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800">
													<Info className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
													<p>{needsSessionStickiness ? t("mcp.sheet.stickinessOpen") : t("mcp.sheet.stickinessClose")}</p>
												</div>
											)}
										</div>

										<DottedSeparator />

										<div className="space-y-4">
											<SectionHeader title={t("mcp.sheet.syncTimeouts")} description={t("mcp.sheet.syncTimeoutsDesc")} />
											<div className="divide-y rounded-md border">
												<FormField
													control={form.control}
													name="tool_sync_interval"
													render={({ field }) => {
														const isUsingGlobal = field.value === undefined || field.value === null || field.value === 0;
														return (
															<FormItem className="flex flex-row items-center justify-between gap-4 px-4 py-3">
																<div className="flex flex-col items-start gap-0.5">
																	<div className="flex items-start gap-2">
																		<div>
																			<FormLabel>{t("mcp.sheet.toolSyncInterval")}</FormLabel>
																		</div>
																		<TooltipProvider>
																			<Tooltip>
																				<TooltipTrigger asChild>
																					<Info className="text-muted-foreground h-4 w-4 cursor-help" />
																				</TooltipTrigger>
																				<TooltipContent className="max-w-xs">
																					<p>{t("mcp.sheet.toolSyncTooltip")}</p>
																				</TooltipContent>
																			</Tooltip>
																		</TooltipProvider>
																	</div>
																	<div>
																		{isUsingGlobal && <p className="text-muted-foreground text-xs">{t("mcp.usingGlobalSetting")}</p>}
																	</div>
																</div>
																<FormControl>
																	<Input
																		type="number"
																		className={`w-24 ${isUsingGlobal ? "text-muted-foreground" : ""}`}
																		placeholder={String(globalToolSyncInterval)}
																		value={field.value === 0 || field.value === undefined ? "" : String(field.value)}
																		onChange={(e) => {
																			const val = e.target.value === "" ? undefined : parseInt(e.target.value);
																			field.onChange(val);
																		}}
																		min="0"
																	/>
																</FormControl>
															</FormItem>
														);
													}}
												/>
												<FormField
													control={form.control}
													name="tool_execution_timeout"
													render={({ field }) => {
														const isUsingGlobal = field.value === undefined || field.value === null || field.value === 0;
														return (
															<FormItem className="flex flex-row items-center justify-between gap-4 px-4 py-3">
																<div className="flex flex-col items-start gap-0.5">
																	<div className="flex items-start gap-2">
																		<div>
																			<FormLabel>{t("mcp.sheet.toolExecTimeout")}</FormLabel>
																		</div>
																		<TooltipProvider>
																			<Tooltip>
																				<TooltipTrigger asChild>
																					<Info className="text-muted-foreground h-4 w-4 cursor-help" />
																				</TooltipTrigger>
																				<TooltipContent className="max-w-xs">
																					<p>{t("mcp.sheet.toolExecTooltip")}</p>
																				</TooltipContent>
																			</Tooltip>
																		</TooltipProvider>
																	</div>
																	<div>
																		{isUsingGlobal && <p className="text-muted-foreground text-xs">{t("mcp.usingGlobalSetting")}</p>}
																	</div>
																</div>
																<FormControl>
																	<Input
																		type="number"
																		className={`w-24 ${isUsingGlobal ? "text-muted-foreground" : ""}`}
																		placeholder={String(globalToolExecutionTimeout)}
																		value={field.value === 0 || field.value === undefined ? "" : String(field.value)}
																		onChange={(e) => {
																			if (e.target.value === "") {
																				field.onChange(undefined);
																				return;
																			}
																			const n = Number(e.target.value);
																			if (!Number.isInteger(n)) return;
																			field.onChange(n);
																		}}
																		min="0"
																		step="1"
																		data-testid="mcp-tool-execution-timeout"
																	/>
																</FormControl>
															</FormItem>
														);
													}}
												/>
											</div>
										</div>

										<FormField
											control={form.control}
											name="disabled"
											render={({ field }) => (
												<FormItem className="border-destructive/30 bg-destructive/5 flex flex-row items-center justify-between gap-4 rounded-md border p-4">
													<div className="flex items-center gap-2">
														<FormLabel>{t("mcp.sheet.disableClient")}</FormLabel>
														<TooltipProvider>
															<Tooltip>
																<TooltipTrigger asChild>
																	<Info className="text-muted-foreground h-4 w-4 cursor-help" />
																</TooltipTrigger>
																<TooltipContent className="max-w-xs">
																	<p>{t("mcp.sheet.disableClientTooltip")}</p>
																</TooltipContent>
															</Tooltip>
														</TooltipProvider>
													</div>
													<FormControl>
														<Switch
															checked={field.value === true}
															onCheckedChange={(checked) => {
																field.onChange(checked);
															}}
															data-testid="mcpclient-disabled-switch"
														/>
													</FormControl>
												</FormItem>
											)}
										/>
									</TabsContent>

									<TabsContent value="authentication" className="space-y-6 pb-10">
										<div className="space-y-4">
											<SectionHeader title={t("mcp.form.headers")} description={t("mcp.sheet.headersDesc")} testId="headers-heading" />
											<FormField
												control={form.control}
												name="headers"
												render={({ field }) => (
													<FormItem className="flex flex-col gap-3">
														<FormControl>
															<HeadersTable
																value={field.value || {}}
																onChange={field.onChange}
																keyPlaceholder={t("mcp.headerName")}
																valuePlaceholder={t("mcp.headerValue")}
																label=""
																useSecretVarInput
															/>
														</FormControl>
														<FormMessage />
													</FormItem>
												)}
											/>
										</div>

										{mcpClient.config.auth_type === "per_user_headers" && (
											<>
												<DottedSeparator />
												<div className="space-y-4">
													<SectionHeader
														title={t("mcp.requiredHeaders")}
														description={t("mcp.form.requiredHeadersDesc")}
														testId="required-headers-heading"
														tooltip={t("mcp.sheet.requiredHeadersTooltip")}
													/>
													<div className="rounded-md border p-4">
														<FormField
															control={form.control}
															name="per_user_header_keys"
															render={({ field }) => (
																<FormItem>
																	<FormControl>
																		<Textarea
																			id="mcpclient-per-user-header-keys"
																			data-testid="mcpclient-per-user-header-keys-textarea"
																			className="h-24"
																			placeholder={t("mcp.form.requiredHeadersPlaceholder")}
																			name={field.name}
																			ref={field.ref}
																			value={perUserHeaderKeysRaw}
																			onChange={(e) => {
																				const value = e.target.value;
																				setPerUserHeaderKeysRaw(value);
																				form.setValue("per_user_header_keys", parseArrayFromText(value), {
																					shouldDirty: true,
																					shouldValidate: true,
																				});
																			}}
																			onBlur={field.onBlur}
																		/>
																	</FormControl>
																	<FormMessage />
																</FormItem>
															)}
														/>
													</div>
												</div>
											</>
										)}

										<DottedSeparator />
										<div className="space-y-4">
											<SectionHeader title={t("mcp.sheet.allowedExtraHeaders")} description={t("mcp.sheet.allowedExtraHeadersDesc")} />
											<FormField
												control={form.control}
												name="allowed_extra_headers"
												render={({ field }) => (
													<FormItem className="flex flex-col gap-2">
														<FormControl>
															<Input
																data-testid="mcpclient-input-allowed-extra-headers"
																placeholder={t("mcp.sheet.allowedExtraHeadersPlaceholder")}
																name={field.name}
																ref={field.ref}
																value={allowedExtraHeadersRaw}
																onChange={(e) => {
																	setAllowedExtraHeadersRaw(e.target.value);
																}}
																onBlur={() => {
																	const parsed = allowedExtraHeadersRaw.trim()
																		? allowedExtraHeadersRaw
																				.split(",")
																				.map((h) => h.trim())
																				.filter(Boolean)
																		: [];
																	field.onChange(parsed);
																	field.onBlur();
																}}
															/>
														</FormControl>
														<FormMessage />
													</FormItem>
												)}
											/>
										</div>

										{(() => {
											const showTLS = mcpClient.config.connection_type === "http" || mcpClient.config.connection_type === "sse";
											const showOAuth = supportsOAuthCredentialUpdate;
											const showTokenExchange = supportsTokenExchangeCredentialUpdate;
											if (!showTLS && !showOAuth && !showTokenExchange) return null;

											return (
												<>
													{showTLS && (
														<>
															<DottedSeparator />
															<div className="space-y-4">
																<SectionHeader
																	title={t("mcp.form.tlsTitle")}
																	description={t("mcp.form.tlsDesc")}
																	testId="tls-config-heading"
																/>
																<div className="space-y-4 rounded-md border p-4">
																	<TLSConfigFields control={form.control} disabled={!hasUpdateMCPClientAccess} />
																</div>
															</div>
														</>
													)}

													{showOAuth && (
														<>
															<DottedSeparator />
															<div className="space-y-4">
																<SectionHeader
																	title={t("mcp.form.oauthTitle")}
																	description={t("mcp.form.oauthDesc")}
																	testId="oauth-advanced-heading"
																/>
																<div className="space-y-4 rounded-md border p-4">
																	<OAuthAdvancedFields
																		control={form.control}
																		disabled={isDisabled || !hasUpdateMCPClientAccess}
																		beforeFields={
																			isDisabled ? (
																				<div className="flex items-start gap-2 rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800">
																					<Info className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
																					<p>{t("mcp.sheet.oauthDisabledWarning")}</p>
																				</div>
																			) : (
																				oauthCredentialsDirty && (
																					<div className="flex items-start gap-2 rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800">
																						<Info className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
																						<p>{t("mcp.sheet.oauthDirtyWarning")}</p>
																					</div>
																				)
																			)
																		}
																		scopesRaw={oauthScopesRaw}
																		onScopesRawChange={setOauthScopesRaw}
																		scopesLabel={t("mcp.scopes")}
																		scopesTestId="mcpclient-input-oauth-scopes"
																		resource={{ mode: "field" }}
																		resourceLabel={t("mcp.resource")}
																		resourceTestId="mcpclient-input-oauth-resource"
																		clientIdLabel={t("mcp.oauthFields.clientId")}
																		clientIdPlaceholder={t("mcp.oauthFields.clientIdPlaceholderNew")}
																		clientIdHelperText={!isDisabled ? t("mcp.oauthFields.clientIdHelperKeep") : undefined}
																		clientIdTestId="mcpclient-input-oauth-client-id"
																		clientSecretLabel={t("mcp.oauthFields.clientSecret")}
																		clientSecretPlaceholder={t("mcp.oauthFields.clientSecretPlaceholderNew")}
																		clientSecretTestId="mcpclient-input-oauth-client-secret"
																		authorizeUrlLabel={t("mcp.oauthFields.authorizeUrl")}
																		authorizeUrlTestId="mcpclient-input-oauth-authorize-url"
																		tokenUrlLabel={t("mcp.oauthFields.tokenUrl")}
																		tokenUrlTestId="mcpclient-input-oauth-token-url"
																		registrationUrlLabel={t("mcp.oauthFields.registrationUrl")}
																		registrationUrlTestId="mcpclient-input-oauth-registration-url"
																	/>
																</div>
															</div>
														</>
													)}

													{showTokenExchange && (
														<>
															<DottedSeparator />
															<div className="space-y-4">
																<SectionHeader
																	title={t("mcp.form.tokenExchangeTitle")}
																	description={t("mcp.form.tokenExchangeDesc")}
																	testId="token-exchange-advanced-heading"
																/>
																<div className="space-y-4 rounded-md border p-4">
																	<TokenExchangeFields
																		control={form.control}
																		disabled={!hasUpdateMCPClientAccess}
																		beforeFields={
																			tokenExchangeCredentialsDirty && (
																				<div className="flex items-start gap-2 rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800">
																					<Info className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
																					<p>{t("mcp.sheet.tokenExchangeDirtyWarning")}</p>
																				</div>
																			)
																		}
																		audienceLabel={t("mcp.tokenExchange.audience")}
																		audienceTooltip={
																			isEntraIdp ? t("mcp.tokenExchange.audienceTooltipEntra") : t("mcp.tokenExchange.audienceTooltip")
																		}
																		audienceTestId="mcpclient-input-token-exchange-audience"
																		useIdPCredentialsLabel={idpConfigured ? t("mcp.tokenExchange.exchangeApp") : undefined}
																		useIdPCredentialsDedicatedDescription={t("mcp.tokenExchange.dedicatedDesc")}
																		useIdPCredentialsIdPDescription={t("mcp.tokenExchange.idpDesc")}
																		useIdPCredentialsRequiredWarning={isEntraIdp && t("mcp.tokenExchange.entraWarningEdit")}
																		useIdPCredentialsTestId="mcpclient-input-token-exchange-use-idp-credentials"
																		clientIdLabel={t("mcp.tokenExchange.clientId")}
																		clientIdPlaceholder={t("mcp.tokenExchange.clientIdPlaceholder")}
																		clientIdTestId="mcpclient-input-token-exchange-client-id"
																		clientIdRedactNonEnvValue={false}
																		clientSecretLabel={t("mcp.tokenExchange.clientSecret")}
																		clientSecretPlaceholder={t("mcp.tokenExchange.clientSecretPlaceholder")}
																		clientSecretHelperText={t("mcp.tokenExchange.clientSecretHelper")}
																		clientSecretTestId="mcpclient-input-token-exchange-client-secret"
																		clientSecretMaskNonEnvValue={true}
																		clientSecretRedactNonEnvValue={false}
																		authServerUrlLabel={t("mcp.tokenExchange.authServerUrl")}
																		authServerUrlHelperText={t("mcp.tokenExchange.authServerUrlHelper")}
																		authServerUrlTestId="mcpclient-input-token-exchange-authorization-server-url"
																		scopes={{
																			variant: "input",
																			value: tokenExchangeScopesRaw,
																			onChange: setTokenExchangeScopesRaw,
																			label: t("mcp.scopes"),
																			helperText: isEntraIdp ? t("mcp.tokenExchange.scopesHelperEntra") : t("mcp.commaSeparated"),
																			testId: "mcpclient-input-token-exchange-scopes",
																			disabled: !hasUpdateMCPClientAccess,
																		}}
																	/>
																</div>
															</div>
														</>
													)}
												</>
											);
										})()}
									</TabsContent>

									<TabsContent value="tools" className="space-y-6 pb-10">
										<div className="space-y-4">
											<div className="flex items-start justify-between gap-4">
												<SectionHeader
													title={t("mcp.sheet.availableTools", { count: mcpClient.tools?.length || 0 })}
													description={t("mcp.sheet.availableToolsDesc")}
												/>
												{mcpClient.tools && mcpClient.tools.length > 0 && (
													<div className="flex items-center gap-4">
														{/* Enable All */}
														<FormField
															control={form.control}
															name="tools_to_execute"
															render={() => {
																const currentTools = form.watch("tools_to_execute") || [];
																const allToolNames = mcpClient.tools?.map((tool) => tool.name) || [];
																const isAllEnabled = currentTools.includes("*");
																const isNoneEnabled = currentTools.length === 0;
																const selectedIds = isAllEnabled ? allToolNames : currentTools;
																const statusLabel = isAllEnabled
																	? t("mcp.sheet.allEnabled")
																	: isNoneEnabled
																		? t("mcp.sheet.noneEnabled")
																		: t("mcp.sheet.nEnabled", { count: currentTools.length });

																return (
																	<FormItem>
																		<FormControl>
																			<div className="flex items-center gap-2">
																				<span id="mcpclient-tools-enable-status" className="text-muted-foreground text-sm">
																					{statusLabel}
																				</span>
																				<TriStateCheckbox
																					allIds={allToolNames}
																					selectedIds={selectedIds}
																					ariaLabel={t("mcp.sheet.enableAllAria", { status: statusLabel })}
																					data-testid="mcpclient-tools-enable-all"
																					onChange={(nextSelectedIds) => {
																						if (nextSelectedIds.length === 0) {
																							form.setValue("tools_to_execute", [], { shouldDirty: true });
																							// Also clear auto-execute when disabling all
																							form.setValue("tools_to_auto_execute", [], { shouldDirty: true });
																						} else if (nextSelectedIds.length === allToolNames.length) {
																							form.setValue("tools_to_execute", ["*"], { shouldDirty: true });
																						} else {
																							form.setValue("tools_to_execute", nextSelectedIds, { shouldDirty: true });
																						}
																					}}
																				/>
																			</div>
																		</FormControl>
																	</FormItem>
																);
															}}
														/>
														{/* Auto-execute All */}
														<FormField
															control={form.control}
															name="tools_to_auto_execute"
															render={() => {
																const currentTools = form.watch("tools_to_execute") || [];
																const currentAutoExecute = form.watch("tools_to_auto_execute") || [];
																const allToolNames = mcpClient.tools?.map((tool) => tool.name) || [];

																// Get the list of enabled tools
																const enabledToolNames = currentTools.includes("*") ? allToolNames : currentTools;
																const isAllAutoExecute = currentAutoExecute.includes("*");
																const isNoneAutoExecute = currentAutoExecute.length === 0;

																// For TriStateCheckbox, we need the selected auto-execute tools that are also enabled
																const selectedAutoExecuteIds = isAllAutoExecute
																	? enabledToolNames
																	: currentAutoExecute.filter((t) => enabledToolNames.includes(t));

																const autoExecuteCount = isAllAutoExecute ? enabledToolNames.length : selectedAutoExecuteIds.length;
																const statusLabel = isAllAutoExecute
																	? t("mcp.sheet.allAutoExecute")
																	: isNoneAutoExecute
																		? t("mcp.sheet.noneAutoExecute")
																		: t("mcp.sheet.nAutoExecute", { count: autoExecuteCount });

																return (
																	<FormItem>
																		<FormControl>
																			<div className="flex items-center gap-2">
																				<span id="mcpclient-tools-autoexecute-status" className="text-muted-foreground text-sm">
																					{statusLabel}
																				</span>
																				<TriStateCheckbox
																					allIds={enabledToolNames}
																					selectedIds={selectedAutoExecuteIds}
																					disabled={enabledToolNames.length === 0}
																					ariaLabel={t("mcp.sheet.autoExecuteAllAria", { status: statusLabel })}
																					data-testid="mcpclient-tools-autoexecute-all"
																					onChange={(nextSelectedIds) => {
																						if (nextSelectedIds.length === 0) {
																							form.setValue("tools_to_auto_execute", [], { shouldDirty: true });
																						} else if (nextSelectedIds.length === enabledToolNames.length) {
																							form.setValue("tools_to_auto_execute", ["*"], { shouldDirty: true });
																						} else {
																							form.setValue("tools_to_auto_execute", nextSelectedIds, { shouldDirty: true });
																						}
																					}}
																				/>
																			</div>
																		</FormControl>
																	</FormItem>
																);
															}}
														/>
													</div>
												)}
											</div>

											{mcpClient.tools && mcpClient.tools.length > 0 ? (
												<div className="rounded-md border">
													<Table>
														<TableHeader>
															<TableRow>
																<TableHead className="w-10"></TableHead>
																<TableHead className="max-w-[300px]">{t("mcp.sheet.toolName")}</TableHead>
																<TableHead className="w-24 text-center">{t("common.enabled")}</TableHead>
																<TableHead className="w-28 text-center">
																	<div className="flex items-center justify-center gap-1.5">
																		<span>{t("mcp.sheet.autoExecute")}</span>
																		<TooltipProvider>
																			<Tooltip>
																				<TooltipTrigger asChild>
																					<a
																						href="https://docs.getbifrost.ai/mcp/agent-mode"
																						target="_blank"
																						rel="noopener noreferrer"
																						aria-label={t("mcp.learnMoreAutoExecute")}
																						className="text-muted-foreground hover:text-foreground focus-visible:ring-ring inline-flex rounded focus-visible:ring-2 focus-visible:outline-none"
																					>
																						<Info className="h-3.5 w-3.5 cursor-help" />
																					</a>
																				</TooltipTrigger>
																				<TooltipContent className="max-w-xs">
																					<p>{t("mcp.sheet.autoExecuteTooltip")}</p>
																				</TooltipContent>
																			</Tooltip>
																		</TooltipProvider>
																	</div>
																</TableHead>
																<TableHead className="w-32 text-center">{t("mcp.sheet.costUsd")}</TableHead>
															</TableRow>
														</TableHeader>
														<TableBody>
															{mcpClient.tools.map((tool, index) => {
																const currentTools = form.watch("tools_to_execute") || [];
																const currentAutoExecute = form.watch("tools_to_auto_execute") || [];
																const isToolEnabled = currentTools?.includes("*") || currentTools?.includes(tool.name);
																const isAutoExecuteEnabled =
																	(currentAutoExecute?.includes("*") && isToolEnabled) ||
																	(currentAutoExecute?.includes(tool.name) && isToolEnabled);
																const isExpanded = expandedTools.has(tool.name);

																return (
																	<Fragment key={index}>
																		<TableRow className="group">
																			<TableCell className="p-2">
																				<button
																					type="button"
																					className="hover:bg-muted flex h-8 w-8 items-center justify-center rounded-md transition-colors"
																					onClick={() => toggleToolExpanded(tool.name)}
																				>
																					{isExpanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
																				</button>
																			</TableCell>
																			<TableCell className="max-w-[300px]">
																				<div className="min-w-0">
																					<div className="text-foreground truncate text-sm font-medium">{tool.name}</div>
																					{tool.description && (
																						<p className="text-muted-foreground mt-0.5 truncate text-xs">{tool.description}</p>
																					)}
																				</div>
																			</TableCell>
																			<TableCell className="text-center">
																				<FormField
																					control={form.control}
																					name="tools_to_execute"
																					render={() => (
																						<FormItem>
																							<FormControl>
																								<div className="flex h-11 w-11 items-center justify-center">
																									<Switch
																										size="md"
																										checked={isToolEnabled}
																										aria-label={
																											isToolEnabled
																												? t("mcp.sheet.disableToolAria", { name: tool.name })
																												: t("mcp.sheet.enableToolAria", { name: tool.name })
																										}
																										onCheckedChange={(checked) => handleToolToggle(tool.name, checked)}
																									/>
																								</div>
																							</FormControl>
																						</FormItem>
																					)}
																				/>
																			</TableCell>
																			<TableCell className="text-center">
																				<FormField
																					control={form.control}
																					name="tools_to_auto_execute"
																					render={() => (
																						<FormItem>
																							<FormControl>
																								<div className="flex h-11 w-11 items-center justify-center">
																									<Switch
																										size="md"
																										checked={isAutoExecuteEnabled}
																										disabled={!isToolEnabled}
																										aria-label={
																											isAutoExecuteEnabled
																												? t("mcp.sheet.disableAutoAria", { name: tool.name })
																												: t("mcp.sheet.enableAutoAria", { name: tool.name })
																										}
																										onCheckedChange={(checked) => handleAutoExecuteToggle(tool.name, checked)}
																									/>
																								</div>
																							</FormControl>
																						</FormItem>
																					)}
																				/>
																			</TableCell>
																			<TableCell className="text-center">
																				<FormField
																					control={form.control}
																					name="tool_pricing"
																					render={({ field }) => (
																						<FormItem>
																							<FormControl>
																								<Input
																									type="number"
																									step="0.000001"
																									min="0"
																									placeholder="0.00"
																									className="h-8 w-24"
																									disabled={!isToolEnabled}
																									value={field.value?.[tool.name] ?? ""}
																									onChange={(e) => {
																										const value = e.target.value === "" ? undefined : parseFloat(e.target.value);
																										const newPricing = { ...field.value };
																										if (value === undefined || isNaN(value)) {
																											delete newPricing[tool.name];
																										} else {
																											newPricing[tool.name] = value;
																										}
																										field.onChange(newPricing);
																									}}
																								/>
																							</FormControl>
																						</FormItem>
																					)}
																				/>
																			</TableCell>
																		</TableRow>
																		{isExpanded && (
																			<tr>
																				<td colSpan={5} className="p-0">
																					<div className="bg-muted/30 border-b px-4 py-3">
																						<div className="text-muted-foreground mb-2 text-xs font-medium">
																							{t("mcp.sheet.parametersSchema")}
																						</div>
																						{tool.parameters ? (
																							<CodeEditor
																								className="z-0 w-full rounded-sm border"
																								shouldAdjustInitialHeight={true}
																								maxHeight={300}
																								wrap={true}
																								code={JSON.stringify(tool.parameters, null, 2)}
																								lang="json"
																								readonly={true}
																								options={{
																									scrollBeyondLastLine: false,
																									collapsibleBlocks: true,
																									lineNumbers: "off",
																									alwaysConsumeMouseWheel: false,
																								}}
																							/>
																						) : (
																							<div className="text-muted-foreground text-sm">{t("mcp.sheet.noParameters")}</div>
																						)}
																					</div>
																				</td>
																			</tr>
																		)}
																	</Fragment>
																);
															})}
														</TableBody>
													</Table>
												</div>
											) : (
												<div className="text-muted-foreground rounded-sm border p-6 text-center">
													<p className="text-sm">{t("mcp.sheet.noTools")}</p>
												</div>
											)}
										</div>
									</TabsContent>

									<TabsContent value="access" className="space-y-6 pb-10">
										<div className="space-y-4">
											<SectionHeader title={t("mcp.sheet.accessControl")} description={t("mcp.sheet.accessControlDesc")} />
											<FormField
												control={form.control}
												name="allow_by_default"
												render={({ field }) => (
													<FormItem className="flex flex-row items-center justify-between gap-4 rounded-md border p-4">
														<div className="flex items-center gap-2">
															<FormLabel>{t("mcp.sheet.allowByDefault")}</FormLabel>
															<TooltipProvider>
																<Tooltip>
																	<TooltipTrigger asChild>
																		<Info className="text-muted-foreground h-4 w-4 cursor-help" />
																	</TooltipTrigger>
																	<TooltipContent className="max-w-xs">
																		<p>{t("mcp.sheet.allowByDefaultTooltip")}</p>
																	</TooltipContent>
																</Tooltip>
															</TooltipProvider>
														</div>
														<FormControl>
															<Switch
																checked={field.value === true}
																onCheckedChange={field.onChange}
																data-testid="mcpclient-allow-on-all-virtual-keys-switch"
															/>
														</FormControl>
													</FormItem>
												)}
											/>
										</div>

										<DottedSeparator />

										<div className="space-y-4">
											<SectionHeader
												title={t("mcp.sheet.vkAssignments")}
												description={t("mcp.sheet.vkAssignmentsDesc")}
												action={
													<VirtualKeySelector
														mode="add"
														onSelect={addVKConfig}
														excludeIds={configuredVKIDs}
														trigger={
															<Button
																type="button"
																variant="outline"
																size="sm"
																className="h-7.5 gap-1.5 px-2 py-1 text-sm font-medium"
																data-testid="mcpclient-virtualkey-add-trigger"
															>
																<Plus className="h-4 w-4" />
																{t("mcp.sheet.addVirtualKey")}
															</Button>
														}
													/>
												}
											/>
											<div className="flex flex-col gap-2">
												{form.watch("allow_by_default") && (
													<p className="text-muted-foreground flex items-center gap-1 text-xs">
														<Info className="h-3 w-3 shrink-0" />
														{t("mcp.sheet.allowByDefaultOverride")}
													</p>
												)}
											</div>

											{vkConfigs.length > 0 ? (
												<div className="rounded-md border">
													<Table>
														<TableHeader>
															<TableRow>
																<TableHead>{t("mcp.sheet.virtualKey")}</TableHead>
																<TableHead>{t("mcp.sheet.allowedTools")}</TableHead>
																<TableHead className="w-12"></TableHead>
															</TableRow>
														</TableHeader>
														<TableBody>
															{vkConfigs.map((vc) => (
																<TableRow key={vc.virtual_key_id}>
																	<TableCell className="font-medium">{vkNameByID[vc.virtual_key_id] ?? vc.virtual_key_id}</TableCell>
																	<TableCell>
																		<MultiSelect
																			data-testid={`mcpclient-virtualkey-tool-selector-${vc.virtual_key_id}`}
																			options={toolOptions}
																			defaultValue={vc.tools_to_execute}
																			resetOnDefaultValueChange
																			onValueChange={(tools) => {
																				const hadStar = vc.tools_to_execute.includes("*");
																				const hasStar = tools.includes("*");
																				let next: string[];
																				if (!hadStar && hasStar) {
																					next = ["*"];
																				} else if (hadStar && hasStar && tools.length > 1) {
																					next = tools.filter((t) => t !== "*");
																				} else {
																					next = tools;
																				}
																				updateVKConfigTools(vc.virtual_key_id, next);
																			}}
																			placeholder={
																				vc.tools_to_execute.includes("*")
																					? t("mcp.sheet.allToolsAllowed")
																					: vc.tools_to_execute.length === 0
																						? t("mcp.sheet.noToolsAllowed")
																						: t("mcp.sheet.selectTools")
																			}
																			maxCount={3}
																			className="bg-background dark:bg-input/30 border-input text-foreground hover:bg-accent hover:text-accent-foreground rounded-sm font-normal"
																		/>
																	</TableCell>
																	<TableCell>
																		<Button
																			type="button"
																			variant="ghost"
																			size="icon"
																			onClick={() => removeVKConfig(vc.virtual_key_id)}
																			className="text-muted-foreground hover:text-destructive"
																			data-testid={`mcpclient-virtualkey-remove-${vc.virtual_key_id}`}
																		>
																			<Trash2 className="h-4 w-4" />
																		</Button>
																	</TableCell>
																</TableRow>
															))}
														</TableBody>
													</Table>
												</div>
											) : form.watch("allow_by_default") ? (
												<div className="text-muted-foreground rounded-sm border p-6 text-center">
													<p className="text-sm">{t("mcp.sheet.allowedByDefaultEmpty")}</p>
												</div>
											) : (
												<div className="text-muted-foreground rounded-sm border p-6 text-center">
													<p className="text-sm">{t("mcp.sheet.noVkAccess")}</p>
												</div>
											)}
										</div>
									</TabsContent>
								</Tabs>
							</div>

							<div className="bg-card sticky bottom-0 z-10 flex justify-end gap-2 border-t px-4 py-4 md:px-8">
								<Button type="button" variant="outline" onClick={onClose}>
									{t("common.cancel")}
								</Button>
								<TooltipProvider>
									<Tooltip>
										<TooltipTrigger asChild>
											<span className="inline-block">
												<Button
													type="submit"
													disabled={isUpdating || !isDirty || !hasUpdateMCPClientAccess}
													isLoading={isUpdating}
													data-testid="mcpclient-save-btn"
												>
													{t("common.saveChanges")}
												</Button>
											</span>
										</TooltipTrigger>
										{(!hasUpdateMCPClientAccess || !isDirty) && (
											<TooltipContent>
												<p>{!hasUpdateMCPClientAccess ? t("mcp.noPermissionAction") : t("mcp.noChangesToSave")}</p>
											</TooltipContent>
										)}
									</Tooltip>
								</TooltipProvider>
							</div>
						</form>
					</Form>
				</SheetContent>
			</Sheet>
			<AlertDialog open={!!pendingNavDirection} onOpenChange={(open) => !open && setPendingNavDirection(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>{t("mcp.sheet.unsavedTitle")}</AlertDialogTitle>
						<AlertDialogDescription>{t("mcp.sheet.unsavedDesc")}</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel onClick={() => setPendingNavDirection(null)}>{t("common.cancel")}</AlertDialogCancel>
						<AlertDialogAction
							onClick={() => {
								const dir = pendingNavDirection;
								setPendingNavDirection(null);
								if (dir) onNavigate?.(dir);
							}}
						>
							{t("mcp.sheet.discardChanges")}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</>
	);
}