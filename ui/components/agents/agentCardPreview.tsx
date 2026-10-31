import { TruncatedLabel } from "@/components/ui/truncatedLabel";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import type { InspectAgentCardResponse, InspectedAgentSkill } from "@/lib/types/agents";
import { AlertTriangle, Bot, CheckCircle2, ChevronDown, ExternalLink } from "lucide-react";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { useRef, useState } from "react";

function capabilityLabel(name: string) {
	return name
		.replace(/([a-z0-9])([A-Z])/g, "$1 $2")
		.replace(/[_-]+/g, " ")
		.replace(/^./, (value) => value.toUpperCase());
}

function requirementLabel(requirement: string[], details?: Record<string, string[]>) {
	return requirement.length
		? requirement.map((name) => (details?.[name]?.length ? `${name} (${details[name].join(", ")})` : name)).join(" + ")
		: "None";
}

function DefinitionFields({ fields }: { fields: Record<string, unknown> }) {
	return (
		<dl className="space-y-2 text-xs">
			{Object.entries(fields).map(([key, value]) => (
				<div key={key} className="grid gap-1 sm:grid-cols-[7rem_minmax(0,1fr)] sm:gap-3">
					<dt className="text-muted-foreground">{capabilityLabel(key)}</dt>
					<dd className="min-w-0 break-words">
						{typeof value === "object" ? (
							<pre className="max-h-48 overflow-auto font-mono text-[11px] break-all whitespace-pre-wrap">
								{JSON.stringify(value, null, 2)}
							</pre>
						) : (
							<span className="font-mono break-all">{String(value)}</span>
						)}
					</dd>
				</div>
			))}
		</dl>
	);
}

function SecurityFields({ fields }: { fields: Record<string, unknown> }) {
	const labels: Record<string, string> = {
		oauth2MetadataUrl: "OAuth metadata URL",
		openIdConnectUrl: "OpenID Connect URL",
		authorizationUrl: "Authorization URL",
		tokenUrl: "Token URL",
		refreshUrl: "Refresh URL",
		bearerFormat: "Bearer format",
		authorizationCode: "Authorization code",
		clientCredentials: "Client credentials",
	};
	return (
		<dl className="divide-y text-xs">
			{Object.entries(fields).map(([key, value]) => {
				const record = value !== null && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : null;
				if (key === "flows" && record) {
					return (
						<div key={key} className="py-3 first:pt-0 last:pb-0">
							<dt className="sr-only">OAuth flows</dt>
							<dd className="space-y-4">
								{Object.entries(record).map(([flow, definition]) => {
									const flowFields =
										definition !== null && typeof definition === "object" && !Array.isArray(definition)
											? (definition as Record<string, unknown>)
											: null;
									return (
										<section key={flow} className="space-y-3">
											<dl className="border-b pb-2.5">
												<div className="grid gap-1 sm:grid-cols-[140px_minmax(0,1fr)] sm:gap-4">
													<dt className="text-muted-foreground">Flow</dt>
													<dd>{labels[flow] ?? capabilityLabel(flow)}</dd>
												</div>
											</dl>
											{flowFields ? (
												<SecurityFields fields={flowFields} />
											) : (
												<p className="text-muted-foreground">{definition === "REDACTED" ? "Redacted" : String(definition)}</p>
											)}
										</section>
									);
								})}
							</dd>
						</div>
					);
				}
				return (
					<div key={key} className="grid gap-1 py-2.5 first:pt-0 last:pb-0 sm:grid-cols-[140px_minmax(0,1fr)] sm:gap-4">
						<dt className="text-muted-foreground">{labels[key] ?? capabilityLabel(key)}</dt>
						<dd className="min-w-0 break-words">
							{record ? (
								key === "scopes" ? (
									<dl className="space-y-2">
										{Object.entries(record).map(([scope, description]) => (
											<div key={scope}>
												<dt className="font-mono">{scope}</dt>
												<dd className="text-muted-foreground">{String(description)}</dd>
											</div>
										))}
									</dl>
								) : (
									<SecurityFields fields={record} />
								)
							) : (
								<span className="font-mono break-all">
									{Array.isArray(value) ? value.join(", ") : value === "REDACTED" ? "Redacted" : String(value)}
								</span>
							)}
						</dd>
					</div>
				);
			})}
		</dl>
	);
}

function Stat({ label, value }: { label: string; value: React.ReactNode }) {
	return (
		<div className="min-w-0 px-3 py-2">
			<p className="text-muted-foreground text-xs">{label}</p>
			<p className="truncate text-sm font-medium">{value}</p>
		</div>
	);
}

function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
	return (
		<div className="grid grid-cols-[7rem_1fr] gap-3 py-2 text-sm">
			<span className="text-muted-foreground text-xs leading-5">{label}</span>
			<div className="min-w-0">{children}</div>
		</div>
	);
}

function SkillItem({ skill }: { skill: InspectedAgentSkill }) {
	const idRef = useRef<HTMLButtonElement>(null);
	const [idTruncated, setIdTruncated] = useState(false);
	const { copy } = useCopyToClipboard({ successMessage: "Copied" });

	return (
		<details className="group/skill">
			<summary
				data-testid="agent-card-skill-toggle"
				className="bg-muted/30 hover:bg-muted/50 flex cursor-pointer list-none items-center justify-between gap-2 px-3 py-2.5 [&::-webkit-details-marker]:hidden"
			>
				<div className="flex min-w-0 flex-1 items-center justify-between gap-3">
					<span className="min-w-0 text-sm font-medium">{skill.name}</span>
					{skill.id && (
						<TooltipProvider>
							<Tooltip
								onOpenChange={(open) => {
									if (open && idRef.current) setIdTruncated(idRef.current.scrollWidth > idRef.current.clientWidth);
								}}
							>
								<TooltipTrigger asChild>
									<button
										type="button"
										ref={idRef}
										className="text-muted-foreground max-w-[45%] truncate font-mono text-[11px] hover:underline"
										aria-label={`Copy skill ID ${skill.id}`}
										onClick={(event) => {
											event.preventDefault();
											event.stopPropagation();
											copy(skill.id);
										}}
									>
										{skill.id}
									</button>
								</TooltipTrigger>
								<TooltipContent>
									{idTruncated && <p>{skill.id}</p>}
									<p>Click to copy skill ID</p>
								</TooltipContent>
							</Tooltip>
						</TooltipProvider>
					)}
				</div>
				<ChevronDown className="text-muted-foreground size-3.5 shrink-0 transition-transform group-open/skill:rotate-180" />
			</summary>
			<div className="space-y-3 border-t px-5 py-3">
				{skill.description && <p className="text-muted-foreground text-xs">{skill.description}</p>}
				{skill.tags && skill.tags.length > 0 && (
					<div className="flex flex-wrap gap-1">
						{skill.tags.map((tag) => (
							<Badge key={tag} variant="secondary" className="font-normal">
								{tag}
							</Badge>
						))}
					</div>
				)}
				{Boolean(skill.input_modes?.length || skill.output_modes?.length || skill.security_requirements?.length) && (
					<SecurityFields
						fields={{
							...(skill.input_modes?.length ? { inputModes: skill.input_modes } : {}),
							...(skill.output_modes?.length ? { outputModes: skill.output_modes } : {}),
							...(skill.security_requirements?.length
								? {
										authentication: skill.security_requirements
											.map((requirement, index) => requirementLabel(requirement, skill.security_requirement_details?.[index]))
											.join(" or "),
									}
								: {}),
						}}
					/>
				)}

				{skill.examples && skill.examples.length > 0 && (
					<ul className="text-muted-foreground list-disc space-y-0.5 pl-4 text-xs">
						{skill.examples.map((example) => (
							<li key={example}>{example}</li>
						))}
					</ul>
				)}
			</div>
		</details>
	);
}

// Compact summary of a fetched A2A card; the long tail lives behind "Card details".
export function AgentCardPreview({
	card,
	showFetched = true,
	gatewayCard = false,
}: {
	card: InspectAgentCardResponse;
	showFetched?: boolean;
	gatewayCard?: boolean;
}) {
	const [detailsOpen, setDetailsOpen] = useState(false);
	const [iconFailed, setIconFailed] = useState(false);
	const capabilities = Object.entries(card.capabilities)
		.filter(([, supported]) => supported)
		.map(([name]) => name)
		.sort();
	const skills = card.skills ?? [];
	const interfaces = card.interfaces ?? [];
	const extensions = card.extensions ?? [];
	const inputModes = card.default_input_modes?.join(", ");
	const outputModes = card.default_output_modes?.join(", ");

	return (
		<div className="overflow-hidden rounded-sm border" data-testid="agent-card-preview">
			<div className="flex items-start gap-3 p-4">
				<div
					className={`flex size-10 shrink-0 items-center justify-center overflow-hidden rounded-sm ${card.icon_url && !iconFailed ? "" : "bg-muted border"}`}
				>
					{card.icon_url && !iconFailed ? (
						<img src={card.icon_url} alt="" className="size-full object-cover" onError={() => setIconFailed(true)} />
					) : (
						<Bot className="text-muted-foreground size-5" />
					)}
				</div>
				<div className="min-w-0 flex-1 space-y-0.5">
					<div className="flex flex-wrap items-center gap-2">
						<p className="truncate font-medium">{card.name || "Unnamed agent"}</p>
						{card.version && <Badge variant="outline">v{card.version}</Badge>}
					</div>
					<div className="text-muted-foreground flex flex-wrap items-center gap-x-3 text-xs">
						{card.provider &&
							(card.provider_url ? (
								<a href={card.provider_url} target="_blank" rel="noreferrer" className="hover:text-foreground hover:underline">
									by {card.provider}
								</a>
							) : (
								<span>by {card.provider}</span>
							))}
						{card.documentation_url && (
							<a
								href={card.documentation_url}
								target="_blank"
								rel="noreferrer"
								className="hover:text-foreground inline-flex items-center gap-1 hover:underline"
							>
								Docs <ExternalLink className="size-3" />
							</a>
						)}
					</div>
				</div>
				{showFetched && (
					<span className="inline-flex shrink-0 items-center gap-1 text-xs text-green-700 dark:text-green-400">
						<CheckCircle2 className="size-3.5" />
						Fetched
					</span>
				)}
			</div>

			{card.description && <p className="text-muted-foreground px-4 pb-4 text-xs leading-relaxed break-words">{card.description}</p>}

			<div className="bg-muted/30 grid grid-cols-3 divide-x border-t">
				<Stat label="Skills" value={skills.length} />
				<Stat label="Interfaces" value={interfaces.length} />
				<Stat
					label="Signatures"
					value={
						gatewayCard ? (
							<TooltipProvider>
								<Tooltip>
									<TooltipTrigger asChild>
										<span className="cursor-help border-b border-dotted">{card.signature_count}</span>
									</TooltipTrigger>
									<TooltipContent className="max-w-80">
										Upstream signatures are removed because the gateway rewrites the card’s endpoints and authentication requirements.
										Original signatures remain in raw capture when recorded.
									</TooltipContent>
								</Tooltip>
							</TooltipProvider>
						) : (
							card.signature_count
						)
					}
				/>
			</div>

			{capabilities.length > 0 && (
				<div className="flex flex-wrap gap-1.5 border-t px-4 py-3">
					{capabilities.map((name) => (
						<Badge key={name} variant="default">
							{capabilityLabel(name)}
						</Badge>
					))}
				</div>
			)}

			{card.warnings && card.warnings.length > 0 && (
				<div className="border-t p-3">
					<Alert variant="warning">
						<AlertTriangle className="h-4 w-4" />
						<AlertDescription>
							{card.warnings.map((warning) => (
								<p key={warning}>{warning}</p>
							))}
						</AlertDescription>
					</Alert>
				</div>
			)}

			<Collapsible open={detailsOpen} onOpenChange={setDetailsOpen} className="border-t">
				<CollapsibleTrigger
					className="text-muted-foreground hover:text-foreground flex w-full items-center justify-between px-4 py-2.5 text-xs font-medium"
					data-testid="agent-card-details-toggle"
				>
					{detailsOpen ? "Hide card details" : "Show card details"}
					<ChevronDown className={`size-4 transition-transform ${detailsOpen ? "rotate-180" : ""}`} />
				</CollapsibleTrigger>
				<CollapsibleContent className="divide-y border-t px-4 pb-2">
					{interfaces.length > 0 && (
						<section className="py-3">
							<p className="text-muted-foreground mb-2 text-xs">Interfaces</p>
							<div className="space-y-2">
								{interfaces.map((item) => (
									<div key={`${item.protocol_binding}-${item.url}`} className="flex min-w-0 items-start gap-3">
										<div className="flex w-28 shrink-0 flex-wrap items-center gap-1.5">
											<span className="text-xs">{item.protocol_binding}</span>
											{item.protocol_version && <span className="text-muted-foreground text-[11px]">v{item.protocol_version}</span>}
										</div>
										<div className="min-w-0 flex-1">
											<TruncatedLabel className="block font-mono text-xs">{item.url}</TruncatedLabel>
											{item.tenant && <p className="text-muted-foreground mt-1 text-xs">Tenant: {item.tenant}</p>}
										</div>
									</div>
								))}
							</div>
						</section>
					)}
					<DetailRow label="Input modes">
						<p className="font-mono text-xs break-words">{inputModes || "Not declared"}</p>
					</DetailRow>
					<DetailRow label="Output modes">
						<p className="font-mono text-xs break-words">{outputModes || "Not declared"}</p>
					</DetailRow>
					<DetailRow label="Authentication">
						<div className="flex flex-wrap gap-1">
							{card.security_requirements.length === 0 ? (
								<Badge variant="outline">None</Badge>
							) : (
								card.security_requirements.map((requirement, index) => (
									<Badge key={`${requirement.join("-")}-${index}`} variant="outline">
										{requirementLabel(requirement, card.security_requirement_details?.[index])}
									</Badge>
								))
							)}
						</div>
					</DetailRow>
					{card.security_schemes.length > 0 && (
						<section className="py-3">
							<p className="text-muted-foreground mb-2 text-xs">Security schemes</p>
							<div className="divide-y overflow-hidden rounded-sm border">
								{card.security_schemes.map((scheme) => (
									<details key={scheme.name} className="group/scheme">
										<summary
											data-testid="agent-card-security-toggle"
											className="bg-muted/30 hover:bg-muted/50 flex cursor-pointer list-none items-center gap-2 px-3 py-2.5 [&::-webkit-details-marker]:hidden"
										>
											<span className="min-w-0 flex-1 truncate text-xs font-medium">{scheme.name}</span>
											<span className="text-muted-foreground shrink-0 font-mono text-[11px]">{scheme.type}</span>
											<ChevronDown className="text-muted-foreground size-3.5 shrink-0 transition-transform group-open/scheme:rotate-180" />
										</summary>
										<div className="space-y-3 border-t px-5 py-3">
											{scheme.description && <p className="text-muted-foreground text-xs">{scheme.description}</p>}
											<SecurityFields
												fields={
													scheme.definition
														? Object.fromEntries(Object.entries(scheme.definition).filter(([key]) => key !== "description"))
														: Object.fromEntries(Object.entries(scheme).filter(([key]) => !["name", "type", "description"].includes(key)))
												}
											/>
										</div>
									</details>
								))}
							</div>
						</section>
					)}
					{(card.signatures?.length ?? 0) > 0 && (
						<DetailRow label="Signatures">
							<div className="divide-y">
								{card.signatures?.map((signature, index) => (
									<details key={index} className="group/signature py-2 first:pt-0 last:pb-0">
										<summary
											data-testid="agent-card-signature-toggle"
											className="flex cursor-pointer list-none items-center justify-between text-xs [&::-webkit-details-marker]:hidden"
										>
											Signature {index + 1}
											<ChevronDown className="text-muted-foreground size-3.5 transition-transform group-open/signature:rotate-180" />
										</summary>
										<div className="pt-2">
											<DefinitionFields fields={signature} />
										</div>
									</details>
								))}
							</div>
						</DetailRow>
					)}
					{card.capabilities.extendedAgentCard && (
						<DetailRow label="Extended card">
							<p className="text-muted-foreground text-xs">Supports fetching an extended agent card with authentication.</p>
						</DetailRow>
					)}
					{extensions.length > 0 && (
						<DetailRow label="Extensions">
							<div className="flex flex-wrap gap-1">
								{extensions.map((extension) => (
									<TooltipProvider key={extension.uri}>
										<Tooltip>
											<TooltipTrigger asChild>
												<Badge variant="outline" className="max-w-full">
													<span className="min-w-0 truncate">{extension.uri}</span>
													{extension.required && (
														<span className="text-destructive shrink-0" aria-label="Required">
															*
														</span>
													)}
												</Badge>
											</TooltipTrigger>
											{(extension.description || extension.params || extension.required) && (
												<TooltipContent className="max-w-80">
													{extension.required && <p className="font-medium">Required</p>}
													{extension.description && <p>{extension.description}</p>}
													{extension.params && (
														<pre className="mt-1 text-xs whitespace-pre-wrap">{JSON.stringify(extension.params, null, 2)}</pre>
													)}
												</TooltipContent>
											)}
										</Tooltip>
									</TooltipProvider>
								))}
							</div>
						</DetailRow>
					)}
					{skills.length > 0 && (
						<section className="py-3">
							<p className="text-muted-foreground mb-2 text-xs">Skills</p>
							<div className="divide-y overflow-hidden rounded-sm border">
								{skills.map((skill) => (
									<SkillItem key={skill.id} skill={skill} />
								))}
							</div>
						</section>
					)}
				</CollapsibleContent>
			</Collapsible>
		</div>
	);
}