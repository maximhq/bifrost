import { describe, expect, it } from "vitest";
import { parsePluginLogs } from "./pluginLogsView.utils";

const entry = (overrides: Record<string, unknown> = {}) => ({
	plugin_name: "governance",
	level: "info",
	message: "Rate limit window ok",
	timestamp: 1756881000002,
	...overrides,
});

describe("parsePluginLogs", () => {
	it("groups well-formed entries by plugin name", () => {
		const parsed = parsePluginLogs(
			JSON.stringify({ governance: [entry()], telemetry: [entry({ plugin_name: "telemetry", level: "error" })] }),
		);
		expect(parsed).not.toBeNull();
		expect(Object.keys(parsed!)).toEqual(["governance", "telemetry"]);
		expect(parsed!.governance).toHaveLength(1);
		expect(parsed!.telemetry[0].level).toBe("error");
	});

	it("drops null and non-object elements instead of letting them reach the view", () => {
		const parsed = parsePluginLogs(JSON.stringify({ governance: [null, "oops", 42, entry()] }));
		expect(parsed!.governance).toEqual([entry()]);
	});

	it("drops elements missing a field the view reads", () => {
		const parsed = parsePluginLogs(
			JSON.stringify({
				governance: [
					entry({ level: undefined }),
					entry({ message: 7 }),
					entry({ timestamp: "1756881000002" }),
					entry({ timestamp: Number.NaN }),
					entry({ message: "kept" }),
				],
			}),
		);
		expect(parsed!.governance.map((e) => e.message)).toEqual(["kept"]);
	});

	it("keeps an entry with an unrecognised level string, which the view badges as info", () => {
		const parsed = parsePluginLogs(JSON.stringify({ governance: [entry({ level: "trace" })] }));
		expect(parsed!.governance[0].level).toBe("trace");
	});

	it("drops a plugin whose value is not an array or has no valid entries", () => {
		const parsed = parsePluginLogs(
			JSON.stringify({ governance: "not-a-list", cache: [null], telemetry: [entry({ plugin_name: "telemetry" })] }),
		);
		expect(Object.keys(parsed!)).toEqual(["telemetry"]);
	});

	it("stores a plugin named __proto__ as an ordinary group", () => {
		// Built from a string: an object literal with a __proto__ key would set the prototype, not a property.
		const parsed = parsePluginLogs(`{"__proto__": [${JSON.stringify(entry({ plugin_name: "__proto__" }))}]}`);
		expect(parsed).not.toBeNull();
		expect(Object.keys(parsed!)).toEqual(["__proto__"]);
		expect(parsed!["__proto__"]).toHaveLength(1);
		expect(Object.getPrototypeOf(parsed)).toBeNull();
	});

	it("returns null when nothing renderable remains", () => {
		expect(parsePluginLogs(JSON.stringify({ governance: [null] }))).toBeNull();
		expect(parsePluginLogs(JSON.stringify({}))).toBeNull();
	});

	it("returns null for payloads that are not an object of plugins", () => {
		expect(parsePluginLogs("not json")).toBeNull();
		expect(parsePluginLogs("null")).toBeNull();
		expect(parsePluginLogs(JSON.stringify([entry()]))).toBeNull();
		expect(parsePluginLogs(JSON.stringify("governance"))).toBeNull();
	});
});