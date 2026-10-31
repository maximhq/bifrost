import { describe, expect, it } from "vitest";
import i18n from "@/lib/i18n";
import { availableTabs } from "./providerConfigSheet";

describe("provider configuration tabs", () => {
	it("uses string labels for network, beta headers, and debugging", async () => {
		await i18n.changeLanguage("en");
		const tabs = availableTabs(false, false, undefined, true, false, i18n.getFixedT("en", "models"));

		expect(tabs.find((tab) => tab.id === "network")?.label).toBe("Network");
		expect(tabs.find((tab) => tab.id === "beta-headers")?.label).toBe("Beta Headers");
		expect(tabs.find((tab) => tab.id === "debugging")?.label).toBe("Debugging");
	});
});