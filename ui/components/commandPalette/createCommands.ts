import type shellEn from "@/locales/en/shell.json";
import { RbacResource } from "@enterprise/lib";

/**
 * Create flows the palette offers. Each page opens its flow from ?action=<action> (useActionParam),
 * so the palette only has to link there. `action` defaults to "create"; a page with two flows names the second.
 */
export const CREATE_COMMANDS: readonly {
	id: string;
	href: string;
	action?: string;
	title: keyof typeof shellEn.commandPalette.create; // key under commandPalette.create
	resource: RbacResource;
}[] = [
	{ id: "create:routing-rule", href: "/workspace/routing-rules", title: "routingRule", resource: RbacResource.RoutingRules },
	{ id: "create:model-limit", href: "/workspace/model-limits", title: "modelLimit", resource: RbacResource.Governance },
	{ id: "create:mcp-server", href: "/workspace/mcp-registry", title: "mcpServer", resource: RbacResource.MCPGateway },
	{
		id: "create:mcp-library-server",
		href: "/workspace/mcp-registry/library",
		title: "mcpLibraryServer",
		resource: RbacResource.MCPGateway,
	},
	{ id: "create:virtual-mcp", href: "/workspace/virtual-mcps", title: "virtualMcp", resource: RbacResource.VirtualMCPs },
	{ id: "create:agent", href: "/workspace/agent-gateway", title: "agent", resource: RbacResource.AgentGateway },
	{ id: "create:relay", href: "/workspace/relays", title: "relay", resource: RbacResource.Relays },
	{ id: "create:plugin", href: "/workspace/plugins", title: "plugin", resource: RbacResource.Plugins },
	{ id: "create:alert-channel", href: "/workspace/alerting/channels", title: "alertChannel", resource: RbacResource.Governance },
	{ id: "create:alert-rule", href: "/workspace/alerting/rules", title: "alertRule", resource: RbacResource.Governance },
	{ id: "create:virtual-key", href: "/workspace/governance/virtual-keys", title: "virtualKey", resource: RbacResource.VirtualKeys },
	{ id: "create:user", href: "/workspace/governance/users", title: "user", resource: RbacResource.Users },
	{ id: "create:team", href: "/workspace/governance/teams", title: "team", resource: RbacResource.Teams },
	{ id: "create:business-unit", href: "/workspace/governance/business-units", title: "businessUnit", resource: RbacResource.BusinessUnits },
	{ id: "create:customer", href: "/workspace/governance/customers", title: "customer", resource: RbacResource.Customers },
	{ id: "create:role", href: "/workspace/governance/rbac", title: "role", resource: RbacResource.RBAC },
	{
		id: "create:access-profile",
		href: "/workspace/governance/access-profiles",
		title: "accessProfile",
		resource: RbacResource.AccessProfiles,
	},
	{ id: "create:project", href: "/workspace/governance/projects", title: "project", resource: RbacResource.Projects },
	{ id: "create:webhook", href: "/workspace/webhooks", title: "webhook", resource: RbacResource.Governance },
	{ id: "create:prompt", href: "/workspace/prompt-repo", title: "prompt", resource: RbacResource.PromptRepository },
	{
		id: "create:prompt-folder",
		href: "/workspace/prompt-repo",
		action: "create-folder",
		title: "promptFolder",
		resource: RbacResource.PromptRepository,
	},
	{ id: "create:skill", href: "/workspace/skills-repo", title: "skill", resource: RbacResource.SkillsRepository },
	{ id: "create:api-key", href: "/workspace/config/api-keys", title: "apiKey", resource: RbacResource.APIKeys },
];