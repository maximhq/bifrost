import { Function as ToolFunction } from "./logs";

export type MCPConnectionType = "http" | "stdio" | "sse";

export type MCPConnectionState = "connected" | "disconnected" | "error";

export interface MCPStdioConfig {
	command: string;
	args: string[];
	envs: string[];
}

export interface MCPClientConfig {
	id: string;
	name: string;
	connection_type: MCPConnectionType;
	connection_string?: string;
	stdio_config?: MCPStdioConfig;
	tools_to_execute?: string[];
	headers?: Record<string, string>;
}

export interface MCPClient {
	config: MCPClientConfig;
	tools: ToolFunction[];
	state: MCPConnectionState;
}

export interface CreateMCPClientRequest {
	name: string;
	connection_type: MCPConnectionType;
	connection_string?: string;
	stdio_config?: MCPStdioConfig;
	tools_to_execute?: string[];
	headers?: Record<string, string>;
}

export interface UpdateMCPClientRequest {
	name?: string;
	headers?: Record<string, string>;
	tools_to_execute?: string[];
}
