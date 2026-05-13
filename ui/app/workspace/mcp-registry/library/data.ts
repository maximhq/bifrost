import { MCPAuthType } from "@/lib/types/mcp";

export interface MCPLibraryServer {
	name: string;
	description: string;
	link: string;
	logo: string;
	url: string;
	slug: string;
	defaultAuthType: MCPAuthType;
}

export const MCP_LIBRARY_SERVERS: MCPLibraryServer[] = [
	{
		name: "Notion",
		description: "This project implements an MCP server for the Notion API.",
		link: "https://github.com/makenotion/notion-mcp-server#readme",
		logo: "https://avatars.githubusercontent.com/u/4792552?s=200&v=4",
		url: "https://mcp.notion.com/mcp",
		slug: "notion",
		defaultAuthType: "oauth",
	},
	{
		name: "Linear",
		description: "Search, create, and update Linear issues, projects, and comments.",
		link: "https://linear.app/docs/mcp",
		logo: "https://linear.app/favicon.ico",
		url: "https://mcp.linear.app/mcp",
		slug: "linear",
		defaultAuthType: "oauth",
	},
	{
		name: "Exa",
		description: "Search Engine made for AIs by Exa.",
		link: "https://github.com/exa-labs/exa-mcp-server",
		logo: "https://exa.ai/images/favicon-32x32.png",
		url: "https://mcp.exa.ai/mcp",
		slug: "exa",
		defaultAuthType: "none",
	},
];