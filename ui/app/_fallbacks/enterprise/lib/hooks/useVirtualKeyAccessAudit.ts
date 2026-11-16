export type VirtualKeyAccessKind = "reveal" | "copy";

// OSS stub: virtual key access auditing is enterprise-only.
export function useVirtualKeyAccessAudit(): (vkId: string, kind: VirtualKeyAccessKind) => void {
	return () => {};
}