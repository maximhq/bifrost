import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import type { InspectAgentCardResponse, InspectedAgentSkill } from "@/lib/types/agents";
import { AlertTriangle, Bot, CheckCircle2, ChevronDown, ExternalLink } from "lucide-react";
import { useState } from "react";

function capabilityLabel(name: string) {
	return name
		.replace(/([a-z0-9])([A-Z])/g, "$1 $2")
		.replace(/[_-]+/g, " ")
		.replace(/^./, (value) => value.toUpperCase());
}

function requirementLabel(requirement: string[]) {
	return requirement.length ? requirement.join(" + ") : "None";
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
	const modes = [
		skill.input_modes?.length ? `In: ${skill.input_modes.join(", ")}` : null,
		skill.output_modes?.length ? `Out: ${skill.output_modes.join(", ")}` : null,
	].filter(Boolean);
	return (
		<div className="space-y-1.5 py-3 first:pt-0 last:pb-0">
			<p className="text-sm font-medium">{skill.name}</p>
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
			{(modes.length > 0 || (skill.security_requirements?.length ?? 0) > 0) && (
				<p className="text-muted-foreground text-xs">
					{[
						...modes,
						skill.security_requirements?.length ? `Auth: ${skill.security_requirements.map(requirementLabel).join(" or ")}` : null,
					]
						.filter(Boolean)
						.join(" · ")}
				</p>
			)}
			{skill.examples && skill.examples.length > 0 && (
				<ul className="text-muted-foreground list-disc space-y-0.5 pl-4 text-xs">
					{skill.examples.map((example) => (
						<li key={example}>{example}</li>
					))}
				</ul>
			)}
		</div>
	);
}

// Compact summary of a fetched A2A card; the long tail lives behind "Card details".
export function AgentCardPreview({ card }: { card: InspectAgentCardResponse }) {
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
				<div className="bg-muted flex size-10 shrink-0 items-center justify-center overflow-hidden rounded-sm border">
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
				<span className="inline-flex shrink-0 items-center gap-1 text-xs text-green-700 dark:text-green-400">
					<CheckCircle2 className="size-3.5" />
					Fetched
				</span>
			</div>

			<div className="bg-muted/30 grid grid-cols-3 divide-x border-t">
				<Stat label="Skills" value={skills.length} />
				<Stat label="Interfaces" value={interfaces.length} />
				<Stat label="Signatures" value={card.signature_count} />
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
						<DetailRow label="Interfaces">
							<div className="space-y-1">
								{interfaces.map((item) => (
									<div key={`${item.protocol_binding}-${item.url}`} className="text-xs">
										<span className="font-medium">{item.protocol_binding}</span>
										<span className="text-muted-foreground"> {item.protocol_version}</span>
										<p className="text-muted-foreground font-mono break-all">{item.url}</p>
										{item.tenant && <p className="text-muted-foreground">Tenant: {item.tenant}</p>}
									</div>
								))}
							</div>
						</DetailRow>
					)}
					<DetailRow label="Modes">
						<p className="text-xs break-all">
							<span className="text-muted-foreground">In </span>
							{inputModes || "not declared"}
							<span className="text-muted-foreground"> · Out </span>
							{outputModes || "not declared"}
						</p>
					</DetailRow>
					<DetailRow label="Authentication">
						<div className="flex flex-wrap gap-1">
							{card.security_requirements.length === 0 ? (
								<Badge variant="outline">None</Badge>
							) : (
								card.security_requirements.map((requirement, index) => (
									<Badge key={`${requirement.join("-")}-${index}`} variant="outline">
										{requirementLabel(requirement)}
									</Badge>
								))
							)}
						</div>
					</DetailRow>
					{card.capabilities.extendedAgentCard && (
						<DetailRow label="Extended card">
							<p className="text-muted-foreground text-xs">Fetched through the gateway with the agent request authentication.</p>
						</DetailRow>
					)}
					{extensions.length > 0 && (
						<DetailRow label="Extensions">
							<div className="flex flex-wrap gap-1">
								{extensions.map((extension) => (
									<TooltipProvider key={extension.uri}>
										<Tooltip>
											<TooltipTrigger asChild>
												<Badge variant={extension.required ? "warning" : "outline"} className="max-w-full truncate">
													{extension.uri}
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
						<DetailRow label="Skills">
							<div className="divide-y">
								{skills.map((skill) => (
									<SkillItem key={skill.id} skill={skill} />
								))}
							</div>
						</DetailRow>
					)}
				</CollapsibleContent>
			</Collapsible>
		</div>
	);
}