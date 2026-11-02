import { configuredAgentEndpointOrigin, mapAgentCard, suggestedAgentEndpoints } from "@/lib/utils/agentCard";
import { Badge } from "@/components/ui/badge";
import { getExternalBaseUrl } from "@/app/workspace/mcp-registry/views/mcpUsageGuide/utils";
import { SectionHeader } from "@/app/workspace/mcp-registry/views/sectionHeader";
import { VirtualKeySelector } from "@/components/entitySelectors/virtualKeySelector";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { VirtualKeyListItem } from "@/components/virtualKeyListItem";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useGetCoreConfigQuery, useGetVirtualKeysQuery } from "@/lib/store";
import { useCreateAgentMutation, useInspectAgentCardMutation, useUpdateAgentMutation } from "@/lib/store/apis/agentsApi";
import type { AgentRegistrationView, AgentSecurityScheme, AgentUpstreamAuth, InspectAgentCardResponse } from "@/lib/types/agents";
import AgentAccessProfiles from "@enterprise/components/agent-gateway/agentAccessProfiles";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertTriangle, ChevronDown, FileSearch, Info, Plus, RefreshCw, Trash2 } from "lucide-react";
import type { TFunction } from "i18next";
import { useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { AuthFields, TLSFields, authSummary, emptyAuth, emptySecret, hasTLS, normalizeAuth } from "./agentAuthFields";
import { AgentCardPreview } from "@/components/agents/agentCardPreview";
import { AgentEndpoints } from "./agentEndpoints";
import { withBasePath } from "@/lib/utils/basePath";

const AGENT_NAME_PATTERN = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

function validateAgentName(name: string, t: TFunction<"observability">): string | null {
	if (name.length < 1 || name.length > 255) return t("agentGateway.sheet.nameLength");
	if (!AGENT_NAME_PATTERN.test(name)) return t("agentGateway.sheet.namePattern");
	return null;
}

function isHttpUrl(value: string) {
	return /^https?:\/\//i.test(value.trim());
}

function headerName(scheme: AgentSecurityScheme): string | null {
	if (scheme.type === "apiKey" && scheme.in === "header") return scheme.header || null;
	if (scheme.type === "http" && ["bearer", "basic"].includes(scheme.scheme?.toLowerCase() ?? "")) return "Authorization";
	return null;
}

function authForRequirement(result: InspectAgentCardResponse, index: number): AgentUpstreamAuth | null {
	const names = result.security_requirements[index] ?? [];
	if (names.length === 0) return emptyAuth();
	const schemes = names.map((name) => result.security_schemes.find((scheme) => scheme.name === name));
	if (schemes.some((scheme) => !scheme)) return null;

	const headers = schemes.flatMap((scheme) => {
		const header = scheme ? headerName(scheme) : null;
		return header ? [header] : [];
	});
	const oauthSchemes = schemes.filter((scheme) => scheme?.type === "oauth2" || scheme?.type === "openIdConnect");
	const hasMutualTLS = schemes.some((scheme) => scheme?.type === "mutualTLS");
	if (
		headers.length + oauthSchemes.length + Number(hasMutualTLS) !== schemes.length ||
		oauthSchemes.length > 1 ||
		new Set(headers).size !== headers.length
	) {
		return null;
	}
	if (oauthSchemes.length && headers.length) return null;

	const oauthScheme = oauthSchemes[0];
	return {
		...emptyAuth(),
		type: oauthScheme ? "oauth" : headers.length ? "headers" : "none",
		headers: Object.fromEntries(headers.map((header) => [header, emptySecret()])),
		oauth: oauthScheme
			? {
					token_url: oauthScheme.token_url,
					client_id: emptySecret(),
					client_secret: emptySecret(),
					scopes: oauthScheme.scopes ?? [],
				}
			: undefined,
		security_schemes: names,
		tls: hasMutualTLS ? {} : undefined,
	};
}

function requirementGap(result: InspectAgentCardResponse, index: number, t: TFunction<"observability">): string | null {
	const names = result.security_requirements[index] ?? [];
	const schemes = names.map((name) => result.security_schemes.find((scheme) => scheme.name === name));
	if (schemes.some((scheme) => !scheme)) return t("agentGateway.sheet.gapMissingScheme");
	const oauthCount = schemes.filter((scheme) => scheme?.type === "oauth2" || scheme?.type === "openIdConnect").length;
	const headerCount = schemes.filter((scheme) => scheme && headerName(scheme)).length;
	if (oauthCount > 1) return t("agentGateway.sheet.gapOneOauth");
	if (oauthCount && headerCount) return t("agentGateway.sheet.gapOauthHeaders");
	const headers = schemes.flatMap((scheme) => {
		const header = scheme ? headerName(scheme) : null;
		return header ? [header] : [];
	});
	if (new Set(headers).size !== headers.length) return t("agentGateway.sheet.gapDuplicateHeader");
	return t("agentGateway.sheet.gapUnsupported");
}

function SettingRow({
	label,
	description,
	checked,
	onCheckedChange,
	testId,
	children,
}: {
	label: string;
	description?: string;
	checked: boolean;
	onCheckedChange: (checked: boolean) => void;
	testId?: string;
	children?: React.ReactNode;
}) {
	return (
		<div className="rounded-sm border">
			<div className="flex items-center justify-between gap-4 p-3">
				<div className="space-y-0.5">
					<Label>{label}</Label>
					{description && <p className="text-muted-foreground text-xs">{description}</p>}
				</div>
				<Switch checked={checked} onCheckedChange={onCheckedChange} data-testid={testId} />
			</div>
			{children && <div className="bg-muted/30 flex flex-col gap-2 border-t p-3">{children}</div>}
		</div>
	);
}

// Collapsed by default; the trigger carries a one-line summary so the closed state still says what is configured.
function OptionGroup({
	title,
	indicator,
	tooltip,
	summary,
	open,
	onOpenChange,
	testId,
	children,
}: {
	title: string;
	indicator?: React.ReactNode;
	tooltip?: string;
	summary: string;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	testId?: string;
	children: React.ReactNode;
}) {
	return (
		<Collapsible open={open} onOpenChange={onOpenChange} className="rounded-sm border">
			<CollapsibleTrigger className="flex w-full items-center justify-between gap-3 px-3 py-2.5 text-left" data-testid={testId}>
				<span className="flex min-h-6 items-center gap-2 text-sm font-medium">
					{title}
					{indicator}
					{tooltip && (
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<Info className="text-muted-foreground h-4 w-4 cursor-help" onClick={(event) => event.stopPropagation()} />
								</TooltipTrigger>
								<TooltipContent className="max-w-xs">{tooltip}</TooltipContent>
							</Tooltip>
						</TooltipProvider>
					)}
				</span>
				<span className="text-muted-foreground flex min-w-0 items-center gap-2 text-xs">
					<span className="truncate">{summary}</span>
					<ChevronDown className={`size-4 shrink-0 transition-transform ${open ? "rotate-180" : ""}`} />
				</span>
			</CollapsibleTrigger>
			<CollapsibleContent className="space-y-4 border-t p-3">{children}</CollapsibleContent>
		</Collapsible>
	);
}

interface AgentSheetProps {
	agent?: AgentRegistrationView;
	onClose: () => void;
	onSaved: () => void;
}

/** Sheet for registering a new agent, or editing `agent` when one is given. */
export default function AgentSheet({ agent, onClose, onSaved }: AgentSheetProps) {
	const { t } = useTranslation("observability");
	const { t: tCommon } = useTranslation("common");
	const isEditing = !!agent;
	const canSave = useRbac(RbacResource.AgentGateway, isEditing ? RbacOperation.Update : RbacOperation.Create);
	const [createAgent, { isLoading: isCreating }] = useCreateAgentMutation();
	const [updateAgent, { isLoading: isUpdating }] = useUpdateAgentMutation();
	const [inspectCard, { isLoading: isInspecting }] = useInspectAgentCardMutation();
	const { toast } = useToast();
	const connectionRef = useRef<HTMLDivElement>(null);
	const authRef = useRef<HTMLDivElement>(null);
	const scrollTo = (ref: React.RefObject<HTMLDivElement | null>) => ref.current?.scrollIntoView({ behavior: "smooth", block: "start" });
	const [name, setName] = useState(agent?.name ?? "");
	const [nameError, setNameError] = useState<string | null>(null);
	const [agentCardUrl, setAgentCardUrl] = useState(agent?.agent_card_url ?? "");
	const [agentCardUrlError, setAgentCardUrlError] = useState<string | null>(null);
	const [cardAuth, setCardAuth] = useState<AgentUpstreamAuth>(agent?.discovery_auth ?? emptyAuth());
	const [runtimeAuth, setRuntimeAuth] = useState<AgentUpstreamAuth>(agent?.runtime_auth ?? emptyAuth());
	const [inspection, setInspection] = useState<InspectAgentCardResponse | null>(null);
	const [inspectionError, setInspectionError] = useState<string | null>(null);
	const [selectedRequirement, setSelectedRequirement] = useState(0);
	const [manualMode, setManualMode] = useState(agent?.runtime_auth?.advanced ?? false);
	const [cardOptionsOpen, setCardOptionsOpen] = useState(
		(agent?.discovery_auth?.type ?? "none") !== "none" || hasTLS(agent?.discovery_auth?.tls),
	);
	const [runtimeTLSOpen, setRuntimeTLSOpen] = useState(hasTLS(agent?.runtime_auth?.tls));
	const [allowByDefault, setAllowByDefault] = useState(agent?.allow_by_default ?? false);
	const [forwardAcceptedCredential, setForwardAcceptedCredential] = useState(agent?.forward_accepted_credential ?? false);
	const [forwardAcceptedCredentialOverridesAuth, setForwardAcceptedCredentialOverridesAuth] = useState(
		agent?.forward_accepted_credential_overrides_auth ?? false,
	);
	const [virtualKeyIds, setVirtualKeyIds] = useState(agent?.virtual_key_ids ?? []);
	const [credentialOrigins, setCredentialOrigins] = useState(() => (agent?.credential_origins ?? []).map((value, id) => ({ id, value })));
	let cardOrigin = "";
	try {
		const parsedCardURL = new URL(agentCardUrl);
		if (parsedCardURL.protocol === "http:" || parsedCardURL.protocol === "https:") cardOrigin = parsedCardURL.origin;
	} catch {
		// An incomplete card URL has no allowed origin to display yet.
	}
	const nextCredentialOriginId = useRef(credentialOrigins.length);
	const additionalEndpointCount = credentialOrigins.filter((origin) => origin.value.trim()).length;
	const allowedEndpointOrigins = new Set(credentialOrigins.map((origin) => configuredAgentEndpointOrigin(origin.value)));
	const advertisedEndpoints = suggestedAgentEndpoints(agentCardUrl, inspection?.interfaces, []).sort(
		(left, right) => Number(allowedEndpointOrigins.has(left)) - Number(allowedEndpointOrigins.has(right)),
	);
	const suggestedEndpoints = suggestedAgentEndpoints(
		agentCardUrl,
		inspection?.interfaces,
		credentialOrigins.map((origin) => origin.value.trim()),
	);
	const [credentialOriginsOpen, setCredentialOriginsOpen] = useState((agent?.credential_origins?.length ?? 0) > 0);
	const { data: vksData } = useGetVirtualKeysQuery({ limit: 1000 });
	const vkById = new Map((vksData?.virtual_keys ?? []).map((virtualKey) => [virtualKey.id, virtualKey]));
	const { data: coreConfig } = useGetCoreConfigQuery({ fromDB: true });
	const baseUrl = getExternalBaseUrl(coreConfig?.client_config);
	const grpc = coreConfig?.agent_gateway
		? {
				baseDomain: coreConfig.agent_gateway.grpc_base_domain,
				port: coreConfig.agent_gateway.grpc_port,
			}
		: undefined;

	const applyRequirement = (result: InspectAgentCardResponse, index: number) => {
		setSelectedRequirement(index);
		const next = authForRequirement(result, index);
		if (!next) return;
		setRuntimeAuth((current) => ({
			...next,
			headers: Object.fromEntries(
				Object.entries(next.headers ?? {}).map(([header, empty]) => [header, current.headers?.[header] ?? empty]),
			),
			oauth:
				next.oauth && current.type === "oauth"
					? {
							...next.oauth,
							client_id: current.oauth?.client_id ?? next.oauth.client_id,
							client_secret: current.oauth?.client_secret ?? next.oauth.client_secret,
							token_url: next.oauth.token_url ?? current.oauth?.token_url,
						}
					: next.oauth,
			tls: current.tls ?? next.tls,
		}));
	};

	const handleInspect = async () => {
		if (!isHttpUrl(agentCardUrl)) {
			setAgentCardUrlError(t("agentGateway.sheet.invalidUrl"));
			scrollTo(connectionRef);
			return;
		}
		setInspectionError(null);
		try {
			const result = await inspectCard({
				agent_card_url: agentCardUrl.trim(),
				discovery_auth: normalizeAuth(cardAuth),
			}).unwrap();
			setInspection(result);
			applyRequirement(result, 0);
		} catch (error) {
			setInspectionError(getErrorMessage(error));
		}
	};

	const selectedAuth = inspection ? authForRequirement(inspection, selectedRequirement) : null;
	const selectedRequirementNames = inspection?.security_requirements[selectedRequirement] ?? [];
	const requiredHeaderKeys = selectedRequirementNames
		.map((name) => inspection?.security_schemes.find((scheme) => scheme.name === name))
		.map((scheme) => (scheme ? headerName(scheme) : null))
		.filter((name): name is string => !!name);
	const unsupportedRequirement = !!inspection && !selectedAuth;
	const unsupportedReason = inspection && unsupportedRequirement ? requirementGap(inspection, selectedRequirement, t) : null;
	const expectedSchemes = selectedAuth?.security_schemes ?? [];
	const authMismatch =
		!!inspection &&
		expectedSchemes.length > 0 &&
		(expectedSchemes.length !== runtimeAuth.security_schemes?.length ||
			expectedSchemes.some((name) => !runtimeAuth.security_schemes?.includes(name)));
	const isBlocked = !manualMode && ((!isEditing && !inspection) || unsupportedRequirement);
	const authLocked = isEditing && !inspection && !manualMode;

	const handleSave = async () => {
		const validation = validateAgentName(name, t);
		if (validation) {
			setNameError(validation);
			scrollTo(connectionRef);
			return;
		}
		if (!isHttpUrl(agentCardUrl)) {
			setAgentCardUrlError(t("agentGateway.sheet.invalidUrl"));
			scrollTo(connectionRef);
			return;
		}
		if (isBlocked) {
			if (unsupportedRequirement) {
				scrollTo(authRef);
			} else {
				setInspectionError(t("agentGateway.sheet.fetchFirst"));
				scrollTo(connectionRef);
			}
			return;
		}
		const payload = {
			agent_card_url: agentCardUrl.trim(),
			allow_by_default: allowByDefault,
			forward_accepted_credential: forwardAcceptedCredential,
			forward_accepted_credential_overrides_auth: forwardAcceptedCredentialOverridesAuth,
			discovery_auth: normalizeAuth(cardAuth),
			runtime_auth: normalizeAuth({ ...runtimeAuth, advanced: manualMode }),
			virtual_key_ids: virtualKeyIds,
			credential_origins: credentialOrigins.map((origin) => origin.value.trim()).filter(Boolean),
		};
		try {
			if (agent)
				await updateAgent({
					name: agent.name,
					data: { ...payload, enabled: agent.enabled },
				}).unwrap();
			else await createAgent({ name, enabled: true, ...payload }).unwrap();
			toast({
				title: t("labels.success"),
				description: agent ? t("agentGateway.sheet.updated") : t("agentGateway.sheet.registered"),
			});
			onSaved();
		} catch (error) {
			toast({
				title: t("agentGateway.sheet.saveFailed"),
				description: getErrorMessage(error),
				variant: "destructive",
			});
		}
	};

	const fetchButton = (label: string) => (
		<Button
			type="button"
			variant="outline"
			className="h-9 shrink-0"
			onClick={() => void handleInspect()}
			isLoading={isInspecting}
			disabled={!agentCardUrl.trim()}
			data-testid="agent-card-fetch-button"
		>
			<RefreshCw className="h-4 w-4" />
			{label}
		</Button>
	);

	return (
		<Sheet open onOpenChange={(open) => !open && onClose()}>
			<SheetContent
				className="flex w-full flex-col gap-0 overflow-x-hidden p-0 pt-4 sm:max-w-[40rem]"
				onInteractOutside={(event) => event.preventDefault()}
				onEscapeKeyDown={onClose}
			>
				<SheetHeader className="w-full flex-col items-start gap-1 p-0 py-4" headerClassName="mb-0 bg-card sticky -top-4 z-10 px-4 md:px-8">
					<SheetTitle className="font-medium">{agent ? name.trim() : t("agentGateway.sheet.newAgent")}</SheetTitle>
					<SheetDescription className="line-clamp-2">{inspection?.description || t("agentGateway.sheet.subtitle")}</SheetDescription>
				</SheetHeader>
				<div className="space-y-8 px-4 pb-8 md:px-8">
					<section ref={connectionRef} className="scroll-mt-4 space-y-4">
						<div className="flex flex-col gap-2">
							<Label>{t("agentGateway.sheet.name")}</Label>
							<Input
								value={name}
								placeholder="accounting-agent"
								disabled={isEditing}
								data-testid="agent-name-input"
								onChange={(event) => {
									setName(event.target.value);
									setNameError(event.target.value ? validateAgentName(event.target.value, t) : null);
								}}
							/>
							{nameError && <p className="text-destructive text-xs">{nameError}</p>}
							{grpc && name.length > 63 && (
								<Alert variant="warning" data-testid="agent-grpc-name-length-warning">
									<AlertTriangle className="h-4 w-4" />
									<AlertTitle>{t("agentGateway.sheet.grpcUnavailableTitle")}</AlertTitle>
									<AlertDescription>{t("agentGateway.sheet.grpcUnavailableBody")}</AlertDescription>
								</Alert>
							)}
						</div>
						<div className="flex flex-col gap-2">
							<Label>{t("agentGateway.sheet.cardUrl")}</Label>
							<div className="flex gap-2">
								<Input
									value={agentCardUrl}
									placeholder="https://agent.example.com/.well-known/agent-card.json"
									data-testid="agent-card-url-input"
									onChange={(event) => {
										setAgentCardUrl(event.target.value);
										setAgentCardUrlError(null);
										setInspection(null);
									}}
								/>
								{fetchButton(inspection ? t("agentGateway.sheet.refetch") : t("agentGateway.sheet.fetch"))}
							</div>
							{agentCardUrlError && <p className="text-destructive text-xs">{agentCardUrlError}</p>}
						</div>
						<OptionGroup
							title={t("agentGateway.sheet.cardFetchOptions")}
							tooltip={t("agentGateway.sheet.cardFetchOptionsHelp")}
							summary={authSummary(cardAuth, t)}
							open={cardOptionsOpen}
							onOpenChange={setCardOptionsOpen}
							testId="agent-card-options-toggle"
						>
							<AuthFields
								value={cardAuth}
								onChange={(next) => {
									setCardAuth(next);
									setInspection(null);
								}}
							/>
							<div className="flex flex-col gap-2 border-t pt-4">
								<Label>{t("agentGateway.sheet.tls")}</Label>
								<TLSFields
									value={cardAuth.tls ?? {}}
									onChange={(tls) => {
										setCardAuth({ ...cardAuth, tls });
										setInspection(null);
									}}
								/>
							</div>
						</OptionGroup>
						{inspectionError && (
							<Alert variant="destructive" data-testid="agent-card-fetch-error">
								<AlertTriangle className="h-4 w-4" />
								<AlertTitle>{t("agentGateway.sheet.fetchErrorTitle")}</AlertTitle>
								<AlertDescription>{inspectionError}</AlertDescription>
							</Alert>
						)}
						{inspection ? (
							<AgentCardPreview card={inspection.card ? { ...mapAgentCard(inspection.card), warnings: inspection.warnings } : inspection} />
						) : (
							!inspectionError && (
								<div className="text-muted-foreground flex flex-col items-center gap-2 rounded-sm border border-dashed p-6 text-center text-sm">
									<FileSearch className="size-5" />
									{isInspecting
										? t("agentGateway.sheet.fetching")
										: isEditing
											? t("agentGateway.sheet.refetchToPreview")
											: t("agentGateway.sheet.fetchToPreview")}
								</div>
							)
						)}
					</section>

					<DottedSeparator />

					<section ref={authRef} className="scroll-mt-4 space-y-4">
						<SectionHeader title={t("agentGateway.sheet.requestAuth")} description={t("agentGateway.sheet.requestAuthDescription")} />
						{unsupportedReason && !manualMode && (
							<Alert variant="destructive">
								<AlertTriangle className="h-4 w-4" />
								<AlertTitle>{t("agentGateway.sheet.requirementUnusable")}</AlertTitle>
								<AlertDescription>{t("agentGateway.sheet.requirementUnusableBody", { reason: unsupportedReason })}</AlertDescription>
							</Alert>
						)}
						{authMismatch && !manualMode && (
							<Alert variant="warning">
								<AlertTriangle className="h-4 w-4" />
								<AlertDescription>{t("agentGateway.sheet.authMismatch")}</AlertDescription>
							</Alert>
						)}
						{authLocked && (
							<div className="flex items-center justify-between gap-3 rounded-sm border border-dashed p-3">
								<p className="text-muted-foreground text-xs">{t("agentGateway.sheet.refetchToEdit")}</p>
								{fetchButton(t("agentGateway.sheet.refetchCard"))}
							</div>
						)}
						{!inspection && !manualMode && !isEditing && (
							<div className="text-muted-foreground rounded-sm border border-dashed p-5 text-center text-sm">
								{t("agentGateway.sheet.fetchAbove")}
							</div>
						)}
						{inspection && inspection.security_requirements.length > 1 && !manualMode && (
							<div className="flex flex-col gap-2">
								<Label>{t("agentGateway.sheet.cardRequirement")}</Label>
								<Select value={String(selectedRequirement)} onValueChange={(value) => applyRequirement(inspection, Number(value))}>
									<SelectTrigger className="w-full">
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										{inspection.security_requirements.map((requirement, index) => (
											<SelectItem key={index} value={String(index)}>
												{requirement.length ? requirement.join(" + ") : t("agentGateway.sheet.none")}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
								<p className="text-muted-foreground text-xs">{t("agentGateway.sheet.requirementHelp")}</p>
							</div>
						)}
						{(selectedAuth || manualMode || isEditing) && (
							<AuthFields
								value={runtimeAuth}
								onChange={setRuntimeAuth}
								readOnlyAuth={!manualMode}
								disabled={authLocked}
								requiredHeaderKeys={manualMode ? [] : requiredHeaderKeys}
							/>
						)}
						<OptionGroup
							title={t("agentGateway.sheet.tlsAndCerts")}
							summary={
								runtimeAuth.tls?.insecure_skip_verify
									? t("agentGateway.sheet.tlsVerificationOff")
									: hasTLS(runtimeAuth.tls)
										? t("agentGateway.sheet.tlsCustom")
										: t("agentGateway.sheet.tlsDefault")
							}
							open={runtimeTLSOpen}
							onOpenChange={setRuntimeTLSOpen}
							testId="agent-runtime-tls-toggle"
						>
							<TLSFields value={runtimeAuth.tls ?? {}} onChange={(tls) => setRuntimeAuth({ ...runtimeAuth, tls })} />
						</OptionGroup>
						<OptionGroup
							title={t("agentGateway.sheet.additionalEndpoints")}
							indicator={
								suggestedEndpoints.length > 0 && (
									<Badge variant="destructive" data-testid="agent-endpoints-approval-needed">
										{t("agentGateway.sheet.needsApproval", { count: suggestedEndpoints.length })}
									</Badge>
								)
							}
							tooltip={t("agentGateway.sheet.additionalEndpointsHelp")}
							summary={
								additionalEndpointCount
									? t("agentGateway.sheet.endpointsAdded", { count: additionalEndpointCount })
									: t("agentGateway.sheet.noneAdded")
							}
							open={credentialOriginsOpen}
							onOpenChange={setCredentialOriginsOpen}
							testId="agent-credential-origins-toggle"
						>
							<p className="text-muted-foreground text-xs">
								<Trans
									t={t}
									i18nKey="agentGateway.sheet.additionalEndpointsIntro"
									components={{
										settings: (
											<a href={withBasePath("/workspace/config/agent-gateway")} className="text-primary underline underline-offset-2" />
										),
									}}
								/>
							</p>
							{cardOrigin && (
								<div className="bg-muted/30 space-y-1 rounded-sm px-3 py-2">
									<p className="text-muted-foreground text-xs">{t("agentGateway.sheet.allowedFromCard")}</p>
									<p className="font-mono text-xs break-all" data-testid="agent-card-allowed-origin">
										{cardOrigin}
									</p>
								</div>
							)}
							<div className="space-y-2">
								{advertisedEndpoints.length > 0 && (
									<div className="space-y-2" data-testid="agent-suggested-endpoints">
										<p className="text-muted-foreground text-xs">{t("agentGateway.sheet.advertisedEndpoints")}</p>
										{advertisedEndpoints.map((endpoint) => {
											const allowed = allowedEndpointOrigins.has(endpoint);
											return (
												<div key={endpoint} className="flex items-center gap-2">
													{allowed ? (
														<span
															className="bg-muted flex h-9 min-w-0 flex-1 items-center rounded-md border px-3 text-sm"
															data-testid="agent-approved-endpoint"
														>
															<span className="truncate">{endpoint}</span>
														</span>
													) : (
														<Input
															value={endpoint}
															readOnly
															aria-label={t("agentGateway.sheet.suggestedEndpointAria")}
															className="bg-muted h-9"
														/>
													)}
													<Button
														type="button"
														variant="outline"
														size="sm"
														className={allowed ? "text-destructive hover:text-destructive h-9 w-16 shrink-0" : "h-9 w-16 shrink-0"}
														disabled={!allowed && credentialOrigins.length >= 32}
														aria-label={
															allowed
																? t("agentGateway.sheet.removeEndpointAria", { endpoint })
																: t("agentGateway.sheet.allowEndpointAria", { endpoint })
														}
														onClick={() => {
															if (allowed) {
																setCredentialOrigins((current) =>
																	current.filter((origin) => configuredAgentEndpointOrigin(origin.value) !== endpoint),
																);
															} else {
																const id = nextCredentialOriginId.current++;
																setCredentialOrigins((current) => [...current, { id, value: endpoint }]);
															}
														}}
													>
														{allowed ? <Trash2 className="h-4 w-4" /> : t("agentGateway.sheet.allow")}
													</Button>
												</div>
											);
										})}
									</div>
								)}
								{credentialOrigins.map((origin, index) =>
									advertisedEndpoints.includes(configuredAgentEndpointOrigin(origin.value) ?? "") ? null : (
										<div key={origin.id} className="flex items-center gap-2">
											<Input
												value={origin.value}
												className="h-9"
												placeholder="https://research.example.com:9000"
												aria-label={t("agentGateway.sheet.additionalEndpointAria")}
												data-testid={`agent-credential-origin-${index}`}
												onChange={(event) =>
													setCredentialOrigins((current) =>
														current.map((item) => (item.id === origin.id ? { ...item, value: event.target.value } : item)),
													)
												}
											/>
											<Button
												type="button"
												variant="outline"
												size="icon"
												className="text-destructive hover:text-destructive h-9 w-16 shrink-0"
												aria-label={t("agentGateway.sheet.removeAgentEndpointAria")}
												onClick={() => setCredentialOrigins((current) => current.filter((item) => item.id !== origin.id))}
											>
												<Trash2 className="h-4 w-4" />
											</Button>
										</div>
									),
								)}
							</div>
							<Button
								type="button"
								variant="outline"
								size="sm"
								data-testid="agent-add-credential-origin"
								disabled={credentialOrigins.length >= 32}
								onClick={() => {
									const id = nextCredentialOriginId.current++;
									setCredentialOrigins((current) => [...current, { id, value: "" }]);
								}}
							>
								<Plus className="h-4 w-4" />
								{t("agentGateway.sheet.addEndpoint")}
							</Button>
						</OptionGroup>

						<SettingRow
							label={t("agentGateway.sheet.forwardAuth")}
							description={t("agentGateway.sheet.forwardAuthHelp")}
							checked={forwardAcceptedCredential}
							onCheckedChange={setForwardAcceptedCredential}
							testId="agent-forward-credential-switch"
						>
							{forwardAcceptedCredential && (
								<>
									<Label>{t("agentGateway.sheet.sameHeader")}</Label>
									<Select
										value={forwardAcceptedCredentialOverridesAuth ? "incoming" : "configured"}
										onValueChange={(value) => setForwardAcceptedCredentialOverridesAuth(value === "incoming")}
									>
										<SelectTrigger className="w-full">
											<SelectValue>
												{forwardAcceptedCredentialOverridesAuth
													? t("agentGateway.sheet.useIncoming")
													: t("agentGateway.sheet.keepConfigured")}
											</SelectValue>
										</SelectTrigger>
										<SelectContent>
											<SelectItem value="configured" className="py-2" textValue={t("agentGateway.sheet.keepConfigured")}>
												<div className="space-y-1 whitespace-normal">
													<p>{t("agentGateway.sheet.keepConfigured")}</p>
													<p className="text-muted-foreground text-xs">{t("agentGateway.sheet.keepConfiguredHelp")}</p>
												</div>
											</SelectItem>
											<SelectItem value="incoming" className="py-2" textValue={t("agentGateway.sheet.useIncoming")}>
												<div className="space-y-1 whitespace-normal">
													<p>{t("agentGateway.sheet.useIncoming")}</p>
													<p className="text-muted-foreground text-xs">{t("agentGateway.sheet.useIncomingHelp")}</p>
												</div>
											</SelectItem>
										</SelectContent>
									</Select>
								</>
							)}
						</SettingRow>
						<SettingRow
							label={t("agentGateway.sheet.manual")}
							description={t("agentGateway.sheet.manualHelp")}
							checked={manualMode}
							onCheckedChange={(checked) => {
								setManualMode(checked);
								setRuntimeAuth((current) => ({ ...current, advanced: checked }));
							}}
							testId="agent-manual-auth-switch"
						/>
					</section>

					<DottedSeparator />

					<section className="space-y-4">
						<SectionHeader
							title={t("agentGateway.sheet.access")}
							description={t("agentGateway.sheet.accessDescription")}
							action={
								<VirtualKeySelector
									mode="add"
									excludeIds={virtualKeyIds}
									onSelect={(option) => setVirtualKeyIds((current) => [...current, option.value])}
									trigger={
										<Button type="button" variant="outline" size="sm" disabled={allowByDefault} data-testid="agent-add-virtual-key-button">
											<Plus className="h-4 w-4" />
											{t("agentGateway.sheet.addKey")}
										</Button>
									}
								/>
							}
						/>
						<SettingRow
							label={t("agentGateway.sheet.allowAny")}
							description={t("agentGateway.sheet.allowAnyHelp")}
							checked={allowByDefault}
							onCheckedChange={setAllowByDefault}
							testId="agent-allow-by-default-switch"
						/>
						{allowByDefault || virtualKeyIds.length === 0 ? (
							<div className="text-muted-foreground rounded-sm border border-dashed p-5 text-center text-sm">
								{allowByDefault ? t("agentGateway.sheet.anyKeyCanReach") : t("agentGateway.sheet.noKeysYet")}
							</div>
						) : (
							<div className="divide-y rounded-sm border">
								{virtualKeyIds.map((id) => (
									<VirtualKeyListItem
										key={id}
										name={vkById.get(id)?.name}
										secret={vkById.get(id)?.value}
										fallbackLabel={id}
										onRemove={() => setVirtualKeyIds((current) => current.filter((value) => value !== id))}
										removeAriaLabel={t("agentGateway.sheet.revokeGrantAria")}
									/>
								))}
							</div>
						)}
						{agent && <AgentAccessProfiles agentName={agent.name} active />}
					</section>

					{agent && (
						<>
							<DottedSeparator />
							<AgentEndpoints agentName={agent.name} baseUrl={baseUrl} grpc={grpc} />
						</>
					)}
				</div>
				<div className="bg-card sticky bottom-0 flex items-center justify-between gap-3 border-t px-4 py-4 md:px-8">
					<p className="text-muted-foreground text-xs">
						{isBlocked && !unsupportedRequirement ? t("agentGateway.sheet.fetchToContinue") : ""}
					</p>
					<div className="flex gap-2">
						<Button type="button" variant="outline" onClick={onClose}>
							{tCommon("cancel")}
						</Button>
						<Button
							type="button"
							onClick={() => void handleSave()}
							disabled={isCreating || isUpdating || !canSave || !!nameError || !name || !agentCardUrl}
							isLoading={isCreating || isUpdating}
							data-testid="agent-save-button"
						>
							{agent ? t("agentGateway.sheet.saveChanges") : tCommon("create")}
						</Button>
					</div>
				</div>
			</SheetContent>
		</Sheet>
	);
}