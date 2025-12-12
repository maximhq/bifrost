export const keysRequired = (selectedProvider: string) => selectedProvider.toLowerCase() === "custom" || !["sgl"].includes(selectedProvider.toLowerCase());
