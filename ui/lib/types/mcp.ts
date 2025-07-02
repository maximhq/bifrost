export type MCPConnectionType = "http" | "stdio" | "sse";

export type MCPConnectionState = "connected" | "disconnected" | "error";

export interface MCPStdioConfig {
	command: string;
	args: string[];
	envs: string[];
}

export interface MCPClientConfig {
	name: string;
	connection_type: MCPConnectionType;
	connection_string?: string;
	stdio_config?: MCPStdioConfig;
	tools_to_skip?: string[];
	tools_to_execute?: string[];
}

export interface MCPClient {
	name: string;
	config: MCPClientConfig;
	tools: string[];
	state: MCPConnectionState;
}

export interface CreateMCPClientRequest {
	name: string;
	connection_type: MCPConnectionType;
	connection_string?: string;
	stdio_config?: MCPStdioConfig;
	tools_to_skip?: string[];
	tools_to_execute?: string[];
}

export interface UpdateMCPClientRequest {
	tools_to_execute?: string[];
	tools_to_skip?: string[];
}
