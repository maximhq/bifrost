import { describe, expect, it } from "vitest";
import {
	secretVarText,
	telemetryDraftFromConfig,
	telemetryDraftKey,
	telemetryPayloadFromDraft,
	type TelemetryDraft,
} from "./gatewayTelemetrySection.utils";

const draft = (over: Partial<TelemetryDraft> = {}): TelemetryDraft => ({
	enabled: true,
	destinations: [{ id: "a", endpoint: "https://otel.example.com", headers: "authorization: env.OTEL_TOKEN" }],
	dropAttributes: "prompt, tool_input",
	...over,
});

describe("secretVarText", () => {
	it("shows the reference for env and vault values and the value otherwise", () => {
		expect(secretVarText({ value: "<REDACTED>", ref: "env.OTEL_TOKEN", type: "env" })).toBe("env.OTEL_TOKEN");
		expect(secretVarText({ value: "", ref: "vault.otel/token", type: "vault" })).toBe("vault.otel/token");
		expect(secretVarText({ value: "https://otel.example.com", type: "plain_text" })).toBe("https://otel.example.com");
		expect(secretVarText("env.X")).toBe("env.X");
		expect(secretVarText(undefined)).toBe("");
	});
});

describe("telemetryDraftFromConfig", () => {
	it("round-trips a stored block into the same payload", () => {
		const stored = {
			enabled: true,
			destinations: [
				{
					endpoint: { value: "https://otel.example.com", type: "plain_text" as const },
					headers: { authorization: { value: "<REDACTED>", ref: "env.OTEL_TOKEN", type: "env" as const } },
				},
			],
			drop_attributes: ["prompt"],
		};
		const d = telemetryDraftFromConfig(stored);
		expect(d.destinations[0].headers).toBe("authorization: env.OTEL_TOKEN");
		expect(telemetryPayloadFromDraft(d)).toEqual({
			telemetry: {
				enabled: true,
				destinations: [{ endpoint: "https://otel.example.com", headers: { authorization: "env.OTEL_TOKEN" } }],
				drop_attributes: ["prompt"],
			},
		});
	});

	it("starts empty and off with nothing stored", () => {
		expect(telemetryDraftFromConfig(undefined)).toEqual({ enabled: false, destinations: [], dropAttributes: "" });
	});
});

describe("telemetryDraftKey", () => {
	it("ignores row ids and whitespace so an untouched form is unchanged", () => {
		const a = draft();
		const b = draft({
			destinations: [{ id: "other", endpoint: " https://otel.example.com ", headers: "authorization: env.OTEL_TOKEN\n" }],
			dropAttributes: "prompt,tool_input",
		});
		expect(telemetryDraftKey(a)).toBe(telemetryDraftKey(b));
		expect(telemetryDraftKey(a)).not.toBe(telemetryDraftKey(draft({ enabled: false })));
	});
});

describe("telemetryPayloadFromDraft", () => {
	it("builds the payload, ignoring empty rows", () => {
		const d = draft({ destinations: [...draft().destinations, { id: "b", endpoint: "  ", headers: "" }] });
		expect(telemetryPayloadFromDraft(d)).toEqual({
			telemetry: {
				enabled: true,
				destinations: [{ endpoint: "https://otel.example.com", headers: { authorization: "env.OTEL_TOKEN" } }],
				drop_attributes: ["prompt", "tool_input"],
			},
		});
	});

	it("refuses what the server refuses", () => {
		expect(telemetryPayloadFromDraft(draft({ destinations: [] })).error).toEqual({ kind: "noDestination" });
		expect(telemetryPayloadFromDraft(draft({ destinations: [{ id: "a", endpoint: "ftp://x", headers: "" }] })).error).toEqual({
			kind: "endpointInvalid",
		});
		expect(
			telemetryPayloadFromDraft(draft({ destinations: [{ id: "a", endpoint: "https://x.example.com", headers: "authorization" }] })).error,
		).toEqual({ kind: "headerMalformed" });
		expect(
			telemetryPayloadFromDraft(
				draft({ destinations: [{ id: "a", endpoint: "https://x.example.com", headers: "authorization: Bearer abc" }] }),
			).error,
		).toEqual({
			kind: "headerNotReference",
			name: "authorization",
		});
	});

	it("allows an env endpoint and keeps collectors while forwarding is paused", () => {
		expect(
			telemetryPayloadFromDraft(
				draft({ enabled: false, destinations: [{ id: "a", endpoint: "env.OTEL_ENDPOINT", headers: "" }], dropAttributes: "" }),
			),
		).toEqual({
			telemetry: { enabled: false, destinations: [{ endpoint: "env.OTEL_ENDPOINT" }] },
		});
	});

	it("sends no block when telemetry was never set up", () => {
		expect(telemetryPayloadFromDraft({ enabled: false, destinations: [], dropAttributes: "" })).toEqual({});
	});
});