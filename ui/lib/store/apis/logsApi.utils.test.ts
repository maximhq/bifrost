import { describe, expect, it } from "vitest";

import { normalizeGetLogByIdArg } from "./logsApi.utils";

describe("normalizeGetLogByIdArg", () => {
	it("treats a bare string as an id without a timestamp", () => {
		expect(normalizeGetLogByIdArg("log-1")).toEqual({ id: "log-1" });
	});
	it("keeps the timestamp hint alongside the id", () => {
		expect(normalizeGetLogByIdArg({ id: "log-1", timestamp: "2026-09-30T17:34:19.870Z" })).toEqual({
			id: "log-1",
			timestamp: "2026-09-30T17:34:19.870Z",
		});
	});
});