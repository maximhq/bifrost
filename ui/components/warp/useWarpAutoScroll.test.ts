import { describe, expect, it } from "vitest";
import { followStep, glideStart } from "./useWarpAutoScroll";

describe("followStep", () => {
	it("closes about 63% of the gap per time constant", () => {
		expect(followStep(0, 100, 50, 50)).toBeCloseTo(63.2, 1);
	});

	it("covers the same ground at 60Hz and 120Hz", () => {
		let at60 = 0;
		let at120 = 0;
		for (let i = 0; i < 6; i++) at60 = followStep(at60, 500, 1000 / 60);
		for (let i = 0; i < 12; i++) at120 = followStep(at120, 500, 1000 / 120);
		expect(at120).toBeCloseTo(at60, 6);
	});

	it("lands exactly on the target once within half a pixel, so the loop can stop", () => {
		expect(followStep(99.6, 100, 16)).toBe(100);
		expect(followStep(100.4, 100, 16)).toBe(100);
	});

	it("caps a long frame so a backgrounded tab does not jump the whole way", () => {
		expect(followStep(0, 1000, 5000)).toBe(followStep(0, 1000, 64));
		expect(followStep(0, 1000, 5000)).toBeLessThan(1000);
	});

	it("follows a target that moves down mid-glide", () => {
		const first = followStep(0, 400, 16);
		expect(followStep(first, 600, 16) - first).toBeGreaterThan(followStep(first, 400, 16) - first);
	});
});

describe("glideStart", () => {
	it("starts from where the viewport is when the bottom is near", () => {
		expect(glideStart(1000, 1500, 400)).toBe(1000);
	});

	it("skips to one viewport short when the bottom is far away", () => {
		expect(glideStart(0, 5000, 400)).toBe(4600);
	});

	it("skips in the right direction when the target is above", () => {
		expect(glideStart(5000, 0, 400)).toBe(400);
	});
});