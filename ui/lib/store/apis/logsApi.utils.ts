// A log id, or a log id with the row's timestamp from a list response.
export type GetLogByIdArg = string | { id: string; timestamp?: string };

export function normalizeGetLogByIdArg(arg: GetLogByIdArg): { id: string; timestamp?: string } {
	return typeof arg === "string" ? { id: arg } : arg;
}