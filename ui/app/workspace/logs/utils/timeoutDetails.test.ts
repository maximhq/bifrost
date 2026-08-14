import { describe, expect, it } from "vitest";

import type { BifrostError } from "@/lib/types/logs";
import { getTimeoutDetails } from "./timeoutDetails";

describe("getTimeoutDetails", () => {
	it("shows upstream timeout evidence without claiming the configured timeout fired", () => {
		const error: BifrostError = {
			is_bifrost_error: false,
			error: { message: "upstream connection or proxy timed out" },
			extra_fields: {
				timeout_source: "upstream_connection_timeout",
				configured_timeout_seconds: 600,
				elapsed_ms: 27_000,
				upstream_response_received: false,
			},
		};

		const rendered = getTimeoutDetails(error).map(({ label, value }) => `${label}: ${value}`).join("\n");
		expect(rendered).toContain("Upstream connection or proxy timed out");
		expect(rendered).toContain("27.00 s (27000 ms)");
		expect(rendered).toContain("Configured timeout: 600 s");
		expect(rendered).not.toContain("default is 300 seconds");
	});

	it("labels an unattributed deadline as unknown rather than a Bifrost client timeout", () => {
		const error: BifrostError = {
			is_bifrost_error: false,
			error: { message: "request timed out" },
			extra_fields: { timeout_source: "unknown_timeout", configured_timeout_seconds: 600, elapsed_ms: 56_000 },
		};
		const rendered = getTimeoutDetails(error).map(({ value }) => value).join(" ");
		expect(rendered).toContain("not attributed to the configured provider timeout");
	});

	it("shows an upstream disconnect separately from a configured timeout", () => {
		const error: BifrostError = {
			is_bifrost_error: false,
			error: { message: "upstream disconnected" },
			extra_fields: { timeout_source: "upstream_connection_error", configured_timeout_seconds: 600, elapsed_ms: 27_000, upstream_response_received: false },
		};
		const rendered = getTimeoutDetails(error).map(({ value }) => value).join(" ");
		expect(rendered).toContain("disconnected before returning a response");
	});
});
