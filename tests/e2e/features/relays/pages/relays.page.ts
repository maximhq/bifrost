import { Locator, Page, expect } from "@playwright/test";
import { BasePage } from "../../../core/pages/base.page";
import { fillSelect } from "../../../core/utils/test-helpers";

/** The auth types the add/edit sheets offer, by the label shown in the select. */
export type RelayAuthLabel = "Bearer token" | "Custom header" | "Query parameter" | "Basic auth" | "AWS SigV4" | "No authentication";

export type RelayConfigTab = "general" | "authentication" | "advanced" | "network" | "proxy";

export interface RouteHeaderMatcher {
  name: string;
  value: string;
}

export interface RouteInput {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE" | "HEAD";
  path: string;
  headers?: RouteHeaderMatcher[];
}

/** Page object for /workspace/relays. */
export class RelaysPage extends BasePage {
  readonly createButton: Locator;
  readonly createFirstButton: Locator;
  readonly listItems: Locator;
  readonly editor: Locator;
  readonly addSheet: Locator;
  readonly configSheet: Locator;
  readonly routeSheet: Locator;
  readonly routesTable: Locator;
  readonly routesEmpty: Locator;
  readonly authTypeTrigger: Locator;

  constructor(page: Page) {
    super(page);
    this.createButton = page.getByTestId("relays-create-button");
    this.createFirstButton = page.getByTestId("relays-create-first");
    this.listItems = page.getByTestId("relay-list-item");
    this.editor = page.getByTestId("relay-view");
    this.addSheet = page.getByTestId("relay-add-sheet");
    this.configSheet = page.getByTestId("relay-config-sheet");
    this.routeSheet = page.getByTestId("relay-route-sheet");
    this.routesTable = page.getByTestId("relay-routes-table");
    this.routesEmpty = page.getByTestId("relay-routes-empty");
    this.authTypeTrigger = page.getByTestId("relay-auth-type");
  }

  async goto(): Promise<void> {
    await this.page.goto("/workspace/relays");
    await this.waitForPageLoad();
  }

  /** Opens the add sheet from either the empty state or the list. */
  async openCreate(): Promise<void> {
    if (await this.createFirstButton.isVisible().catch(() => false)) {
      await this.createFirstButton.click();
    } else {
      await this.createButton.click();
    }
    await expect(this.addSheet).toBeVisible();
    await this.waitForSheetAnimation();
  }

  /** Picks the auth type in whichever sheet (add or config) is open. */
  async selectAuthType(label: RelayAuthLabel): Promise<void> {
    await fillSelect(this.page, '[data-testid="relay-auth-type"]', label);
    await expect(this.authTypeTrigger).toContainText(label);
  }

  async createEndpoint(name: string, baseUrl: string, credential: string): Promise<void> {
    await this.openCreate();
    await this.page.getByTestId("relay-name").fill(name);
    await this.page.getByTestId("relay-new-base-url").fill(baseUrl);
    await this.page.getByTestId("relay-credential").fill(credential);
    await this.submitCreate(name);
  }

  /** Submits the add sheet and waits for the relay to appear in the list. */
  async submitCreate(name: string): Promise<void> {
    await this.page.getByTestId("relay-create").click();
    // Consume the creation toast so a later success-toast assertion cannot match this stale one.
    await this.waitForSuccessToast();
    await expect(this.addSheet).toBeHidden({ timeout: 10000 });
    await this.listItems.filter({ hasText: name }).first().waitFor();
  }

  async select(name: string): Promise<void> {
    await this.listItems.filter({ hasText: name }).first().click();
    await expect(this.editor).toBeVisible();
  }

  async openConfigTab(tab: RelayConfigTab): Promise<void> {
    if (!(await this.configSheet.isVisible().catch(() => false))) {
      await this.page.getByTestId("relay-edit-config").click();
      await expect(this.configSheet).toBeVisible();
      await this.waitForSheetAnimation();
    }
    await this.page.getByTestId(`relay-tab-${tab}`).click();
    await expect(this.page.getByTestId(`relay-tab-${tab}`)).toHaveAttribute("data-state", "active");
  }

  /** The save button of a config tab (General, Authentication, Advanced, Network, Proxy). */
  saveButton(tab: RelayConfigTab): Locator {
    return this.page.getByTestId(`relay-save-${tab}`);
  }

  /** Saves the open config tab and consumes the success toast. */
  async saveConfigTab(tab: RelayConfigTab): Promise<void> {
    await expect(this.saveButton(tab)).toBeEnabled();
    await this.saveButton(tab).click();
    await this.waitForSuccessToast("Relay configuration updated");
  }

  /** Closes whichever sheet is open (config, route or preview) with Escape. */
  async closeSheets(): Promise<void> {
    for (let i = 0; i < 3; i++) {
      const open = this.page.locator('[role="dialog"]');
      if ((await open.count()) === 0) return;
      await this.page.keyboard.press("Escape");
      await expect(open.first()).toBeHidden({ timeout: 5000 }).catch(() => undefined);
    }
  }

  async deleteSelected(): Promise<void> {
    await this.page.getByTestId("relay-delete").click();
    await this.page.getByTestId("relay-delete-confirm").click();
  }

  // ---- Routes --------------------------------------------------------------------------------

  routeRow(index: number): Locator {
    return this.page.getByTestId(`relay-route-row-${index}`);
  }

  routeEnabledSwitch(index: number): Locator {
    return this.page.getByTestId(`relay-route-enabled-switch-${index}`);
  }

  /** Opens the add-route sheet. */
  async openAddRoute(): Promise<void> {
    await this.page.getByTestId("relay-routes-add").click();
    await expect(this.routeSheet).toBeVisible();
    await this.waitForSheetAnimation();
  }

  /** Opens the edit sheet of the route at `index` through its actions menu. */
  async openEditRoute(index: number): Promise<void> {
    await this.page.getByTestId(`relay-route-actions-${index}`).click();
    await this.page.getByTestId(`relay-route-edit-${index}`).click();
    await expect(this.routeSheet).toBeVisible();
    await this.waitForSheetAnimation();
  }

  /** Fills the open route sheet. Header matchers go into the table's trailing empty row, after the existing rows. */
  async fillRoute(route: RouteInput): Promise<void> {
    if (route.method) {
      await fillSelect(this.page, '[data-testid="relay-route-method"]', route.method);
      await expect(this.page.getByTestId("relay-route-method")).toContainText(route.method);
    }
    await this.page.getByTestId("relay-route-path").fill(route.path);
    for (const header of route.headers ?? []) {
      const trailing = (await this.page.locator('[data-testid^="relay-route-header-name-"]').count()) - 1;
      await this.page.getByTestId(`relay-route-header-name-${trailing}`).fill(header.name);
      await this.page.getByTestId(`relay-route-header-value-${trailing}`).fill(header.value);
    }
  }

  /** Saves the open route sheet and waits for it to close. */
  async saveRoute(): Promise<void> {
    await this.page.getByTestId("relay-route-save").click();
    await this.waitForSuccessToast();
    await expect(this.routeSheet).toBeHidden({ timeout: 10000 });
  }

  /** Adds a route end to end. */
  async addRoute(route: RouteInput): Promise<void> {
    await this.openAddRoute();
    await this.fillRoute(route);
    await this.saveRoute();
  }

  /** Removes the route at `index` through its actions menu and the confirm dialog. */
  async removeRoute(index: number): Promise<void> {
    await this.page.getByTestId(`relay-route-actions-${index}`).click();
    await this.page.getByTestId(`relay-route-remove-${index}`).click();
    const confirm = this.page.getByTestId("relay-route-delete-confirm");
    await expect(confirm).toBeVisible();
    await confirm.click();
    await this.waitForSuccessToast("Route deleted");
  }
}
