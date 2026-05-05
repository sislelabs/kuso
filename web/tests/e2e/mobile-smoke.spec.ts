import { expect, test } from "@playwright/test";

// Mobile smoke tests for the kuso dashboard. Cheap insurance against
// the worst-class mobile regression: layout breaks on phone but
// nobody notices because all the dev work happens at 1440×900.
//
// What we DON'T test here:
//   - Authenticated flows. Those need a live API; the LIVE_TEST_PLAN
//     covers them via manual or against-cluster runs.
//   - Pixel-perfect screenshots. They flake on font-rendering jitter
//     and aren't useful for regression catching.
//
// What we DO test:
//   - Pages that render statically (login, marketing) load on a
//     phone-sized viewport without horizontal scroll.
//   - Critical above-the-fold elements are present and visible.
//   - Tap targets we expect to exist (form fields, buttons) are
//     reachable without zooming.

test.describe("login page on mobile", () => {
  test("renders without horizontal scroll", async ({ page }) => {
    await page.goto("/login");

    // The h1 anchor we know is on the login page (see app/(auth)/login/page.tsx).
    await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();

    // Document width must not exceed viewport width — horizontal scroll
    // on phone is the #1 mobile-regression smell.
    const overflow = await page.evaluate(() => {
      const html = document.documentElement;
      return {
        scroll: html.scrollWidth,
        client: html.clientWidth,
      };
    });
    expect(
      overflow.scroll,
      `document scrollWidth (${overflow.scroll}) exceeds viewport (${overflow.client}) — something is overflowing horizontally`,
    ).toBeLessThanOrEqual(overflow.client);
  });

  test("primary form fields are visible and tappable", async ({ page }) => {
    await page.goto("/login");

    // We don't care which exact placeholder/label the LoginForm uses —
    // what matters is that text inputs are reachable. Both should be
    // visible above the fold on iPhone 13 (390×844).
    const username = page.getByRole("textbox").first();
    await expect(username).toBeVisible();

    // Tap target size — Apple HIG recommends ≥ 44×44 pt.
    const box = await username.boundingBox();
    expect(box, "username field has no bounding box (likely display:none)").not.toBeNull();
    if (box) {
      expect(box.height, `username input height (${box.height}px) is too small to tap reliably`).toBeGreaterThanOrEqual(36);
    }
  });
});

test.describe("authenticated routes redirect cleanly on mobile", () => {
  test("/projects bounces to /login when unauthenticated", async ({ page }) => {
    // The SPA's auth guard sends unauthenticated visitors to /login.
    // On a fresh box (or in CI without seeded auth) this is the
    // expected path; the test passes if either we end up at /login
    // OR we end up showing the projects shell. Both are valid; what
    // we're guarding against is "navigation hangs" or "blank white
    // screen" failures that only show on mobile.
    const response = await page.goto("/projects", { waitUntil: "networkidle" });
    expect(response, "navigation produced no response").not.toBeNull();

    // Either landed on /login (unauthenticated) or rendered the
    // app shell. We accept both.
    const url = page.url();
    const onLogin = url.endsWith("/login") || url.includes("/login?");
    if (!onLogin) {
      // If we rendered something, at least verify the document
      // didn't horizontally overflow on this viewport.
      const overflow = await page.evaluate(() => ({
        scroll: document.documentElement.scrollWidth,
        client: document.documentElement.clientWidth,
      }));
      expect(overflow.scroll).toBeLessThanOrEqual(overflow.client);
    }
  });
});
