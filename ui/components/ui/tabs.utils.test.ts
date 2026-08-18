import { describe, expect, it } from "vitest";
import { partitionTabs } from "./tabs.utils";

// Six 100px tabs, matching the Install Bifrost MCP harness strip that overflows.
const SIX = [100, 100, 100, 100, 100, 100];
const MORE = 40;

describe("partitionTabs", () => {
	it("keeps every tab inline when they all fit", () => {
		expect(partitionTabs({ itemWidths: SIX, containerWidth: 600, moreButtonWidth: MORE, activeIndex: 0 })).toEqual({
			visible: [0, 1, 2, 3, 4, 5],
			overflow: [],
		});
	});

	it("keeps every tab inline before the container has been measured", () => {
		// First paint reports 0; collapsing there would flash a dropdown that
		// immediately disappears once layout settles.
		expect(partitionTabs({ itemWidths: SIX, containerWidth: 0, moreButtonWidth: MORE, activeIndex: 0 })).toEqual({
			visible: [0, 1, 2, 3, 4, 5],
			overflow: [],
		});
	});

	it("moves the tabs that do not fit into the overflow, reserving room for the more button", () => {
		// 450px total: the more button takes 40, leaving 410 => four 100px tabs.
		expect(partitionTabs({ itemWidths: SIX, containerWidth: 450, moreButtonWidth: MORE, activeIndex: 0 })).toEqual({
			visible: [0, 1, 2, 3],
			overflow: [4, 5],
		});
	});

	it("preserves DOM order in both groups", () => {
		const result = partitionTabs({ itemWidths: SIX, containerWidth: 250, moreButtonWidth: MORE, activeIndex: 0 });
		expect(result.visible).toEqual([...result.visible].sort((a, b) => a - b));
		expect(result.overflow).toEqual([...result.overflow].sort((a, b) => a - b));
		expect([...result.visible, ...result.overflow].sort((a, b) => a - b)).toEqual([0, 1, 2, 3, 4, 5]);
	});

	it("pulls the active tab inline when it would otherwise be hidden", () => {
		// Active tab 5 must stay visible: the selected harness is the one the
		// user is reading, so it cannot be the one buried in the dropdown.
		const result = partitionTabs({ itemWidths: SIX, containerWidth: 450, moreButtonWidth: MORE, activeIndex: 5 });
		expect(result.visible).toContain(5);
		expect(result.overflow).not.toContain(5);
	});

	it("keeps the active tab inline even when only one tab fits", () => {
		const result = partitionTabs({ itemWidths: SIX, containerWidth: 145, moreButtonWidth: MORE, activeIndex: 3 });
		expect(result.visible).toEqual([3]);
		expect(result.overflow).toEqual([0, 1, 2, 4, 5]);
	});

	it("keeps filling after a tab that is too wide to fit", () => {
		// A single wide tab must not strand the row: stopping at it left ~190px of
		// empty space on the dashboard while three tabs sat in the dropdown.
		const result = partitionTabs({ itemWidths: [50, 300, 50, 50], containerWidth: 240, moreButtonWidth: MORE, activeIndex: 0 });
		expect(result.visible).toEqual([0, 2, 3]);
		expect(result.overflow).toEqual([1]);
	});

	it("leaves no room for a tab it could still have fitted", () => {
		const itemWidths = [50, 300, 50, 50];
		const containerWidth = 240;
		const { visible, overflow } = partitionTabs({ itemWidths, containerWidth, moreButtonWidth: MORE, activeIndex: 0 });
		const used = visible.reduce((sum, index) => sum + itemWidths[index], 0);
		const available = containerWidth - MORE;
		for (const index of overflow) {
			expect(used + itemWidths[index]).toBeGreaterThan(available);
		}
	});

	it("handles an empty strip", () => {
		expect(partitionTabs({ itemWidths: [], containerWidth: 300, moreButtonWidth: MORE, activeIndex: -1 })).toEqual({
			visible: [],
			overflow: [],
		});
	});
});