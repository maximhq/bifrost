import { test, expect } from "../../core/fixtures/base.fixture";

test.use({ skipAutoLogin: true });

// A native input and overflowing box probe the loaded application's global CSS.
// No backend state is needed, and the test also runs against a production build.
for (const theme of ["light", "dark"] as const) {
  test(`${theme} native controls and scrollbar states`, async ({ page }) => {
    await page.addInitScript((value) => localStorage.setItem("theme", value), theme);
    await page.route("**/api/session/is-auth-enabled", (route) =>
      route.fulfill({
        json: { is_auth_enabled: true, has_valid_token: false },
      }),
    );
    await page.goto("/login");
    await expect(page.locator("html")).toHaveClass(new RegExp(`\\b${theme}\\b`));

    await page.evaluate(() => {
      // next-themes also writes this inline; remove that fallback so the test
      // specifically exercises the stylesheet's native-control declaration.
      document.documentElement.style.removeProperty("color-scheme");
      const probe = document.createElement("section");
      probe.style.cssText =
        "position:fixed;top:0;left:0;z-index:2147483647;background:white;padding:20px";
      probe.innerHTML = `
        <input type="text" data-testid="theme-native-input" aria-label="Native theme probe">
        <select data-testid="theme-native-select" aria-label="Native select probe"><option>Example</option></select>
        <div class="custom-scrollbar" data-testid="theme-scrollbar" style="width:120px;height:120px">
          <div style="width:240px;height:240px">Overflow in both directions</div>
        </div>`;
      document.body.appendChild(probe);
    });

    await expect(page.getByTestId("theme-native-input")).toHaveCSS("color-scheme", theme);
    await expect(page.getByTestId("theme-native-select")).toHaveCSS("color-scheme", theme);
    const scroll = page.getByTestId("theme-scrollbar");
    await expect(scroll).toHaveCSS(
      "scrollbar-color",
      `${theme === "dark" ? "rgb(63, 63, 70)" : "rgb(228, 228, 231)"} rgba(0, 0, 0, 0)`,
    );
    expect(
      await scroll.evaluate(
        (el) => el.scrollHeight > el.clientHeight && el.scrollWidth > el.clientWidth,
      ),
    ).toBe(true);

    // Exercise the WebKit fallback separately: scrollbar-color/width otherwise
    // override these pseudo-elements in current Chromium.
    await scroll.evaluate((el) => {
      el.style.scrollbarColor = "auto";
      el.style.scrollbarWidth = "auto";
    });
    const thumb = () =>
      scroll.evaluate((el) => {
        const style = getComputedStyle(el, "::-webkit-scrollbar-thumb");
        return { color: style.backgroundColor, visibility: style.visibility };
      });
    await page.mouse.move(500, 500);
    await expect.poll(thumb).toEqual({
      color: theme === "dark" ? "rgb(63, 63, 70)" : "rgb(228, 228, 231)",
      visibility: "hidden",
    });
    await scroll.hover();
    await expect.poll(() => scroll.evaluate((el) => el.matches(":hover"))).toBe(true);
    // Chromium can cache the scrollbar pseudo style across a pointer-only hover
    // transition. Invalidate it without changing any styling under assertion.
    await scroll.evaluate((el) => el.style.setProperty("--e2e-hover-recalc", "1"));
    await expect.poll(thumb).toEqual({
      color: theme === "dark" ? "rgb(113, 113, 122)" : "rgb(82, 82, 91)",
      visibility: "visible",
    });
  });
}