export const keysRequired = (selectedProvider: string) => selectedProvider.toLowerCase() === "custom" || !["ollama", "sgl", "vllm"].includes(selectedProvider.toLowerCase());
