import type { TraceDetail, TraceSpan } from "@/lib/types/traces";

/** Extra tabs of the span detail pane. None yet: review and activity add theirs here. */
export function renderTraceSpanExtras(
	_detail: TraceDetail,
	_span: TraceSpan,
): { tabs: { id: string; label: string; content: React.ReactNode }[] } {
	return { tabs: [] };
}