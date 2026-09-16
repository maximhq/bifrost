import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { LogEntry } from "@/lib/types/logs";
import { LogDetailSheet } from "./logDetailsSheet";

const state = vi.hoisted(() => ({ query: {} as Record<string, unknown> }));
vi.mock("@/lib/store/apis/logsApi", () => ({ useGetLogByIdQuery: () => state.query }));
vi.mock("@/lib/store/apis/promptsApi", () => ({ useGetPromptQuery: () => ({}) }));
vi.mock("@/hooks/useSheetNavigation", () => ({ useSheetNavigation: () => ({}) }));
vi.mock("@/components/sheetNavigationButtons", () => ({ SheetNavigationButtons: () => null }));
vi.mock("@/components/ui/sheet", () => ({
	Sheet: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
	SheetContent: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
	SheetTitle: ({ children }: { children: React.ReactNode }) => <h1>{children}</h1>,
}));
vi.mock("./logDetailView", () => ({
	LogDetailView: ({ log }: { log: LogEntry }) => (
		<button data-testid="logdetails-export-log-button">
			{log.id}:{log.raw_request}
		</button>
	),
}));

const row = { id: "current", status: "success", raw_request: "LIST PREVIEW" } as LogEntry;
const detail = { ...row, raw_request: "COMPLETE PAYLOAD" };
const render = () => renderToStaticMarkup(<LogDetailSheet log={row} open onOpenChange={() => {}} />);

describe("log detail export safety", () => {
	beforeEach(() => {
		state.query = { isLoading: false, isError: false, isFetching: false, refetch: vi.fn() };
	});
	it("never exposes list previews for export when detail hydration fails", () => {
		state.query.isError = true;
		const html = render();
		expect(html).not.toContain("logdetails-export-log-button");
		expect(html).toContain("Unable to load complete log details");
		expect(html).toContain("logdetails-retry-button");
	});
	it("never exports stale data for another log after navigation fails", () => {
		Object.assign(state.query, { isError: true, data: { ...detail, id: "previous" } });
		expect(render()).not.toContain("logdetails-export-log-button");
	});
	it("blocks cached detail after a failed refresh", () => {
		Object.assign(state.query, { isError: true, data: detail, currentData: detail });
		expect(render()).not.toContain("logdetails-export-log-button");
	});
	it("waits for the current detail while navigating", () => {
		state.query.data = { ...detail, id: "previous" };
		expect(render()).not.toContain("logdetails-export-log-button");
	});
	it("exports the hydrated current detail", () => {
		Object.assign(state.query, { data: detail, currentData: detail });
		expect(render()).toContain("COMPLETE PAYLOAD");
		expect(render()).not.toContain("LIST PREVIEW");
	});
	it("keeps successfully hydrated detail visible during polling", () => {
		Object.assign(state.query, { data: detail, currentData: detail, isFetching: true });
		expect(render()).toContain("COMPLETE PAYLOAD");
	});
});