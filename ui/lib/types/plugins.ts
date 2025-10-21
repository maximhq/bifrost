// Plugins types that match the Go backend structures

export const SEMANTIC_CACHE_PLUGIN = "semantic_cache";
export const MAXIM_PLUGIN = "maxim";

export interface PluginStatus {
	name: string;
	status: string;
	logs: string[];
}

export interface Plugin {
	name: string;
	enabled: boolean;
	config: any;
	isCustom: boolean;
	path?: string;
	status?: PluginStatus;
}

export interface PluginsResponse {
	plugins: Plugin[];
	count: number;
}

export interface CreatePluginRequest {
	name: string;
	path: string;
	enabled: boolean;
	config: any;
}

export interface UpdatePluginRequest {
	enabled: boolean;
	path?: string;	
	config?: any;
}
