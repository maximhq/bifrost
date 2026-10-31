import { describe, expect, it } from "vitest";
import { rssFeedFormErrorField, rssNewsSwitchState } from "./rssSourcesSheet.utils";

describe("rssNewsSwitchState", () => {
	it("mirrors the loaded gateway setting", () => {
		expect(rssNewsSwitchState({ enabled: true }, false)).toEqual({ checked: true, ready: true, failed: false });
		expect(rssNewsSwitchState({ enabled: false }, false)).toEqual({ checked: false, ready: true, failed: false });
	});

	it("is not ready, and not shown as on, while the setting loads", () => {
		expect(rssNewsSwitchState(undefined, false)).toEqual({ checked: false, ready: false, failed: false });
	});

	it("reports a failed load instead of defaulting to on", () => {
		expect(rssNewsSwitchState(undefined, true)).toEqual({ checked: false, ready: false, failed: true });
	});
});
describe("rssFeedFormErrorField", () => {
	it("puts a duplicate-name rejection under Name", () => {
		expect(rssFeedFormErrorField("A feed with this name already exists")).toBe("name");
	});

	it("puts a refused logo under the logo field", () => {
		expect(rssFeedFormErrorField("icon_url must be an http or https URL or an image data URI")).toBe("icon_url");
	});

	it("puts the feed probe verdict under Feed URL", () => {
		expect(rssFeedFormErrorField("URL did not return a valid RSS or Atom feed")).toBe("url");
		expect(rssFeedFormErrorField("URL must be a public http or https address")).toBe("url");
	});
});