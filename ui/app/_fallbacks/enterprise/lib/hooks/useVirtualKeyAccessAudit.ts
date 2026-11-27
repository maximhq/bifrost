export type VirtualKeyAccessKind = "reveal" | "copy";

// OSS stub: there is no audit log to wait on, so every access is allowed.
export function useVirtualKeyAccessAudit(): (vkId: string, kind: VirtualKeyAccessKind) => Promise<boolean> {
	return () => Promise.resolve(true);
}