import { Function as ToolFunction } from "./logs";
import { EnvVar } from "./schemas";

export type MCPConnectionType = "http" | "stdio" | "sse";

export type MCPConnectionState = "connected" | "disconnected" | "error";

export type { EnvVar };

export interface MCPStdioConfig {
	command: string;
	args: string[];
	envs: string[];
}

export interface MCPClientConfig {
	client_id: string; // Maps to ClientID in TableMCPClient
	name: string;
	is_code_mode_client?: boolean;
	connection_type: MCPConnectionType;
	connection_string?: EnvVar;
	stdio_config?: MCPStdioConfig;
	tools_to_execute?: string[];
	tools_to_auto_execute?: string[];
	headers?: Record<string, EnvVar>;
	is_ping_available?: boolean;
	tool_pricing?: Record<string, number>;
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
	connection_string?: EnvVar;
	stdio_config?: MCPStdioConfig;
	tools_to_execute?: string[];
	tools_to_auto_execute?: string[];
	headers?: Record<string, EnvVar>;
	is_ping_available?: boolean;
}

export interface UpdateMCPClientRequest {
	name?: string;
	is_code_mode_client?: boolean;
	headers?: Record<string, EnvVar>;
	tools_to_execute?: string[];
	tools_to_auto_execute?: string[];
	is_ping_available?: boolean;
	tool_pricing?: Record<string, number>;
}
