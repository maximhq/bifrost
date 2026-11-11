import { expect, test } from "../../core/fixtures/base.fixture";

test.describe("Model Catalog - Price Precision", () => {
	test("displays sub-cent token prices accurately in table and attribute drawer", async ({ page }) => {
		const mockModel = {
			name: "gpt-4o-mini-precision-test",
			provider: "openai",
			context_length: 128000,
			max_input_tokens: 128000,
			max_output_tokens: 16384,
			// $0.075 / 1M (was previously rounded to $0.08)
			input_cost_per_token: 0.000000075,
			// $1.875 / 1M (was previously rounded to $1.88)
			output_cost_per_token: 0.000001875,
			// $0.035 / 1M (was previously rounded to $0.04)
			cache_creation_input_token_cost: 0.000000035,
			// $0.00875 / 1M (was previously rounded to $0.01)
			cache_read_input_token_cost: 0.00000000875,
			additional_attributes: {
				description: "Model for testing price precision display in E2E",
			},
		};

		// Intercept the model details API to supply the precision test model
		await page.route("**/api/models/details*", async (route) => {
			if (route.request().method() === "GET") {
				await route.fulfill({
					status: 200,
					contentType: "application/json",
					body: JSON.stringify({
						models: [mockModel],
						total: 1,
						pricing_overrides: {},
					}),
				});
			} else {
				await route.continue();
			}
		});

		// Navigate directly to the Models attribute tab
		await page.goto("/workspace/model-catalog?tab=attributes");

		// Locate the row in the catalog table
		const row = page.getByTestId("model-catalog-row-gpt-4o-mini-precision-test-openai");
		await expect(row).toBeVisible();

		// Verify per-1M sub-cent prices in the table retain accurate decimal precision
		await expect(row).toContainText("$0.075");
		await expect(row).toContainText("$1.875");
		await expect(row).toContainText("$0.035");
		await expect(row).toContainText("$0.00875");

		// Open the Attribute Drawer for this model
		await page.getByTestId("model-catalog-edit-gpt-4o-mini-precision-test-openai").click();

		// Verify the Attribute Drawer opens
		const sheet = page.getByTestId("model-catalog-attribute-sheet");
		await expect(sheet).toBeVisible();

		// Verify full price labels in the attribute drawer preserve sub-cent rates with / 1M tokens
		await expect(page.getByTestId("model-catalog-input-cost")).toHaveText("$0.075 / 1M tokens");
		await expect(page.getByTestId("model-catalog-output-cost")).toHaveText("$1.875 / 1M tokens");
		await expect(page.getByTestId("model-catalog-cache-write-cost")).toHaveText("$0.035 / 1M tokens");
		await expect(page.getByTestId("model-catalog-cache-read-cost")).toHaveText("$0.00875 / 1M tokens");
	});
});