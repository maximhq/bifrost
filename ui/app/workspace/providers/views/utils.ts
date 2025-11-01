export const keysRequired = (selectedProvider: string) => selectedProvider.toLowerCase() === "custom" || !["ollama", "sgl"].includes(selectedProvider.toLowerCase());
