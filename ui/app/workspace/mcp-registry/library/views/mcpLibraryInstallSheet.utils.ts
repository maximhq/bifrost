import { MCPAuthType } from "@/lib/types/mcp";

/**
 * Whether a catalog entry's declared header names should prefill the static
 * header map the installer edits.
 *
 * Only `headers` should. A `per_user_headers` entry declares names each caller
 * supplies at request time, and those already reach the form through
 * `perUserHeaderKeys`; seeding them here as well creates static rows with no
 * value, which `getHeadersValidationError` rejects before the per-user
 * verification dialog can open. STDIO servers are launched locally and send no
 * request auth at all.
 */
export function shouldSeedHeaders(authType: MCPAuthType, isStdio: boolean): boolean {
	return !isStdio && authType === "headers";
}