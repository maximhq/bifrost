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
	is_code_mode_client?: boolean;
	connection_type: MCPConnectionType;
	connection_string?: string;
	stdio_config?: MCPStdioConfig;
	tools_to_execute?: string[];
	tools_to_auto_execute?: string[];
	headers?: Record<string, string>;
}

export interface MCPClient {
	config: MCPClientConfig;
	tools: ToolFunction[];
	state: MCPConnectionState;
}

export interface CreateMCPClientRequest {
	name: string;
	is_code_mode_client?: boolean;
	connection_type: MCPConnectionType;
	connection_string?: string;
	stdio_config?: MCPStdioConfig;
	tools_to_execute?: string[];
	tools_to_auto_execute?: string[];
	headers?: Record<string, string>;
}

export interface UpdateMCPClientRequest {
	name?: string;
	is_code_mode_client?: boolean;
	headers?: Record<string, string>;
	tools_to_execute?: string[];
	tools_to_auto_execute?: string[];
}
