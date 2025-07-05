import { Database, Settings, Server, Hash, Key, Globe, Loader2 } from "lucide-react";

export const ProviderIcons = {
	openai: <Database />,
	anthropic: <Settings />,
	bedrock: <Server />,
	cohere: <Hash />,
	vertex: <Key />,
	ollama: <Globe />,
	mistral: <Loader2 />,
	azure: <Loader2 />,
} as const;
