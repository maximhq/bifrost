export const keysRequired = (selectedProvider: string) => selectedProvider === "custom" || !["ollama", "sgl"].includes(selectedProvider);
