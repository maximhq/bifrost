import { describe, expect, it, vi } from "vitest";
import type { Relay } from "@/lib/types/relays";
import { withRoutes } from "./routes.utils";
import { toRelayForm } from "@/lib/utils/relayForm";

// A browser without JSON.rawJSON: big numbers are read rounded.
vi.mock("@/lib/types/schemas", async (importOriginal) => ({
	...(await importOriginal<typeof import("@/lib/types/schemas")>()),
	exactJSONNumbers: false,
	parseJSONExact: (text: string) => JSON.parse(text),
}));

describe("withRoutes in a browser that rounds big numbers", () => {
	const relay = {
		name: "r",
		base_url: "https://api.example.com",
		auth: { type: "none" },
		routes: [
			{ method: "POST", path: "/seeded", body_overrides: { seed: 9007199254740992 } },
			{ method: "GET", path: "/plain" },
		],
	} as unknown as Relay;
	const formRoutes = toRelayForm(relay).routes;

	it("refuses a save that would write the rounded number back", () => {
		expect(() => withRoutes(relay, formRoutes)).toThrow();
	});
	it("lets the route holding it be deleted, since nothing is written back", () => {
		expect(withRoutes(relay, [formRoutes[1]]).routes).toHaveLength(1);
	});
});