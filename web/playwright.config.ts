import { defineConfig, devices } from "@playwright/test";

// Minimal Playwright config — we run a tiny set of mobile regression
// smoke tests against the static-exported SPA. The full E2E surface is
// validated against a live cluster (docs/LIVE_TEST_PLAN.md); this file
// is for the cheap-and-fast "did the dashboard layout break on phone"
// signal that's hard to catch by eye during normal dev.
//
// To run locally:
//   cd web
//   npm install            # picks up @playwright/test
//   npx playwright install chromium  # one-time browser download
//   npm run test:e2e:mobile
//
// CI would do the same. Browsers are not committed and are not pulled
// by `npm install` alone — the explicit `playwright install` step
// keeps repo clones light.

export default defineConfig({
  testDir: "./tests/e2e",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: process.env.KUSO_E2E_BASE_URL || "http://localhost:3000",
    trace: "on-first-retry",
    // Static-export apps are slow on first paint while the JS chunks
    // hydrate; bumping the default action timeout from 5s to 15s
    // keeps mobile-network simulations stable.
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
  },
  projects: [
    {
      name: "mobile-chromium",
      use: { ...devices["iPhone 13"] },
    },
  ],
  // Boot the dev server for local runs. CI sets KUSO_E2E_BASE_URL
  // against an already-running instance and skips this.
  webServer: process.env.KUSO_E2E_BASE_URL
    ? undefined
    : {
        command: "npm run dev",
        url: "http://localhost:3000",
        reuseExistingServer: !process.env.CI,
        timeout: 120_000,
      },
});
