import { describe, expect, it, vi } from "vitest";
import { disabledMenuItemProps } from "./disabledReason";

const selectEvent = () => ({ preventDefault: vi.fn() }) as unknown as Event;

describe("disabledMenuItemProps", () => {
	it("passes an allowed item's handler through untouched", () => {
		const onSelect = vi.fn();
		const props = disabledMenuItemProps(undefined, onSelect);
		expect(props["aria-disabled"]).toBeUndefined();
		expect(props.onSelect).toBe(onSelect);
	});

	// A Radix-disabled item is skipped by arrow-key navigation, so a denied item
	// stays enabled for Radix and is only marked disabled for assistive tech.
	it("keeps a denied item focusable but marks it aria-disabled", () => {
		const props = disabledMenuItemProps("You don't have permission to delete teams.", vi.fn());
		expect(props["aria-disabled"]).toBe(true);
		expect(props).not.toHaveProperty("disabled");
	});

	it("never runs the action of a denied item and keeps the menu open", () => {
		const onSelect = vi.fn();
		const props = disabledMenuItemProps("You don't have permission to delete teams.", onSelect);
		const event = selectEvent();
		props.onSelect?.(event);
		expect(onSelect).not.toHaveBeenCalled();
		expect(event.preventDefault).toHaveBeenCalledOnce();
	});
});