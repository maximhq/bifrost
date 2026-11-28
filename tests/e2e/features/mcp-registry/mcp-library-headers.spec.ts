import catalog from "../../../../community/mcp-library/servers.json";
import { expect, test } from "../../core/fixtures/base.fixture";

test("Tavily suggests Authorization only when installing with headers", async ({ page }) => {
	const tavily = catalog.servers.find((server) => server.name === "Tavily");
	expect(tavily).toBeDefined();
	await page.route(/\/api\/mcp\/library\?/, (route) =>
		route.fulfill({ json: { servers: [{ ...tavily, id: 1, slug: "tavily" }], total_count: 1, count: 1, limit: 24, offset: 0 } }),
	);
	await page.route("**/api/mcp/client", (route) => route.fulfill({ json: { status: "connected" } }));
	await page.goto("/workspace/mcp-registry/library");
	await page.getByTestId("mcp-library-table-view-toggle").click();
	const install = page.getByTestId("mcp-library-table-install-tavily");
	await install.click();
	const auth = page.getByTestId("auth-type-select");
	const table = page.getByTestId("mcp-headers-table");
	const firstName = table.getByPlaceholder("Header name").first();
	const submit = page.getByTestId("library-install-submit-btn");

	await expect(auth).toHaveText("OAuth 2.0");
	await auth.click();
	await page.getByTestId("auth-type-headers").click();
	await expect(firstName).toHaveValue("Authorization");
	await expect(table.getByText('Header "Authorization" must have a value')).toBeVisible();

	await auth.click();
	await page.getByTestId("auth-type-oauth").click();
	await expect(firstName).toHaveValue("");
	const oauthRequest = page.waitForRequest("**/api/mcp/client");
	await submit.click();
	const oauthPayload = (await oauthRequest).postDataJSON();
	expect(oauthPayload.auth_type).toBe("oauth");
	expect(oauthPayload.headers ?? {}).toEqual({});

	await install.click();
	await auth.click();
	await page.getByTestId("auth-type-headers").click();
	const value = table.getByPlaceholder("Header value").first();
	await value.fill("Bearer test-key");
	await auth.click();
	await page.getByTestId("auth-type-oauth").click();
	await auth.click();
	await page.getByTestId("auth-type-headers").click();
	await expect(value).toHaveValue("Bearer test-key");
	const headersRequest = page.waitForRequest("**/api/mcp/client");
	await submit.click();
	const headersPayload = (await headersRequest).postDataJSON();
	expect(headersPayload.auth_type).toBe("headers");
	expect(headersPayload.headers.Authorization.value).toBe("Bearer test-key");
});
