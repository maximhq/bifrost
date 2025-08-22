// Plugins types that match the Go backend structures

export const SEMANTIC_CACHE_PLUGIN = "semantic_cache";

export interface Plugin {
	name: string;
	enabled: boolean;
	config: any;
}

export interface PluginsResponse {
	plugins: Plugin[];
	count: number;
}

export interface CreatePluginRequest {
	name: string;
	enabled: boolean;
	config: any;
}

export interface UpdatePluginRequest {
	enabled: boolean;
	config: any;
}
