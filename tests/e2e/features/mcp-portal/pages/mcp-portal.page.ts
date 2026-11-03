import type { Page } from "@playwright/test";
import { BasePage } from "../../../core/pages/base.page";

export class MCPPortalPage extends BasePage {
  constructor(page: Page) {
    super(page);
  }

  async goto(virtualKeyId?: string) {
    await this.page.goto(
      `/workspace/mcp-portal${virtualKeyId ? `?virtual_key_id=${encodeURIComponent(virtualKeyId)}` : ""}`,
    );
    await this.page.getByTestId("mcp-portal-page").waitFor();
  }

  server(id: string) {
    return this.page.getByTestId(`mcp-portal-server-${id}`);
  }

  status(id: string) {
    return this.page.getByTestId(`mcp-portal-credential-status-${id}`);
  }

  async configure(id: string) {
    const popupPromise = this.page.waitForEvent("popup");
    await this.page.getByTestId(`mcp-portal-configure-${id}`).click();
    const popup = await popupPromise;
    await popup.waitForURL(/\/workspace\/mcp-sessions\/auth\?/);
    return popup;
  }

  async refresh() {
    const response = this.page.waitForResponse(
      (res) => new URL(res.url()).pathname === "/api/mcp/portal",
    );
    await this.page.getByTestId("mcp-portal-refresh").click();
    await response;
  }
}