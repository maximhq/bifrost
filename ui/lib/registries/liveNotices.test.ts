import { describe, expect, it } from "vitest";
import { countAttentionNotices, type LiveNotice } from "./liveNotices";

const notice = (severity: LiveNotice["severity"]): LiveNotice => ({ id: severity, severity, title: severity, message: "" });

describe("countAttentionNotices", () => {
	it("counts errors and warnings but not info or success", () => {
		expect(countAttentionNotices([notice("error"), notice("warning"), notice("info"), notice("success")])).toBe(2);
	});
	it("is zero for no notices", () => {
		expect(countAttentionNotices([])).toBe(0);
	});
});
