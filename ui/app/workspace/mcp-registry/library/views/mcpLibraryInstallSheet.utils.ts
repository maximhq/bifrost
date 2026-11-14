import { MCPAuthType } from "@/lib/types/mcp";

export function shouldSeedHeaders(authType: MCPAuthType, isStdio: boolean): boolean {
	return !isStdio && (authType === "headers" || authType === "per_user_headers");
}