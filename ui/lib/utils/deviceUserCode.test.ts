import { afterEach, describe, expect, it, vi } from "vitest";
import { formatUserCode, isCompleteUserCode, recallDeviceUserCode, rememberDeviceUserCode } from "./deviceUserCode";

describe("deviceUserCode", () => {
	afterEach(() => vi.unstubAllGlobals());

	it("formats however the code is typed", () => {
		expect(formatUserCode("abcd efgh")).toBe("ABCD-EFGH");
		expect(formatUserCode("ab")).toBe("AB");
		expect(isCompleteUserCode("abcd-efg")).toBe(false);
		expect(isCompleteUserCode("abcdefgh")).toBe(true);
	});

	it("recalls the code only for the flow it was typed for", () => {
		const store = new Map<string, string>();
		vi.stubGlobal("sessionStorage", {
			getItem: (k: string) => store.get(k) ?? null,
			setItem: (k: string, v: string) => store.set(k, v),
		});
		rememberDeviceUserCode("flow-1", "abcdefgh");
		expect(recallDeviceUserCode("flow-1")).toBe("ABCD-EFGH");
		expect(recallDeviceUserCode("flow-2")).toBe("");
	});

	it("degrades to asking for the code when storage is blocked", () => {
		vi.stubGlobal("sessionStorage", {
			getItem: () => {
				throw new Error("blocked");
			},
			setItem: () => {
				throw new Error("blocked");
			},
		});
		expect(() => rememberDeviceUserCode("flow-1", "abcdefgh")).not.toThrow();
		expect(recallDeviceUserCode("flow-1")).toBe("");
	});
});