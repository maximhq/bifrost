import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { LogFilters } from "./filters";
import { MCPLogFilters } from "@/app/workspace/mcp-logs/views/filters";

vi.mock("@/components/ui/button", () => ({
	Button: ({ children, className, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement>) => (
		<button className={className} {...props}>
			{children}
		</button>
	),
}));

vi.mock("@/components/ui/input", () => ({
	Input: ({ className, ...props }: React.InputHTMLAttributes<HTMLInputElement>) => <input className={className} {...props} />,
}));

vi.mock("@/components/ui/popover", () => ({
	Popover: ({ children }: { children: ReactNode }) => <div>{children}</div>,
	PopoverTrigger: ({ children }: { children: ReactNode }) => <>{children}</>,
	PopoverContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));

vi.mock("@/components/ui/command", () => ({
	Command: ({ children }: { children: ReactNode }) => <div>{children}</div>,
	CommandList: ({ children }: { children: ReactNode }) => <div>{children}</div>,
	CommandItem: ({ children }: { children: ReactNode }) => <div>{children}</div>,
	CommandGroup: ({ children }: { children: ReactNode }) => <div>{children}</div>,
	CommandInput: (props: React.InputHTMLAttributes<HTMLInputElement>) => <input {...props} />,
	CommandEmpty: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));

vi.mock("@/components/filters/filterPopover", () => ({
	FilterPopover: () => <button type="button">Filters</button>,
}));

vi.mock("@/components/ui/datePickerWithRange", () => ({
	DateTimePickerWithRange: ({ triggerTestId }: { triggerTestId?: string }) => (
		<button type="button" data-testid={triggerTestId ?? "mock-date-range"}>
			Date range
		</button>
	),
}));

vi.mock("@/lib/store", () => ({
	getErrorMessage: () => "error",
	useRecalculateLogCostsMutation: () => [vi.fn(), { isLoading: false }],
	useGetMCPLogsFilterDataQuery: () => ({
		data: { tool_names: [], server_labels: [], virtual_keys: [] },
		isLoading: false,
	}),
}));

vi.mock("sonner", () => ({
	toast: {
		success: vi.fn(),
		error: vi.fn(),
	},
}));

describe("observability toolbar layout", () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	it("renders all key controls in the logs toolbar", () => {
		render(
			<LogFilters
				filters={{ content_search: "" }}
				onFiltersChange={vi.fn()}
				liveEnabled
				onLiveToggle={vi.fn()}
				fetchLogs={vi.fn(async () => undefined)}
				fetchStats={vi.fn(async () => undefined)}
			/>,
		);

		expect(screen.getByPlaceholderText("Search logs")).toBeInTheDocument();
		expect(screen.getByText("Live updates")).toBeInTheDocument();
		expect(screen.getByTestId("filter-date-range")).toBeInTheDocument();
		expect(screen.getByText("Filters")).toBeInTheDocument();
	});

	it("renders all key controls in the MCP logs toolbar", () => {
		render(
			<MCPLogFilters filters={{ content_search: "" }} onFiltersChange={vi.fn()} liveEnabled onLiveToggle={vi.fn()} />,
		);

		expect(screen.getByPlaceholderText("Search MCP logs")).toBeInTheDocument();
		expect(screen.getByText("Live updates")).toBeInTheDocument();
		expect(screen.getByTestId("mcp-filter-date-range")).toBeInTheDocument();
		expect(screen.getByText("Filters")).toBeInTheDocument();
	});
});
