import path from "node:path";
import { defineConfig } from "vitest/config";

// Minimal unit-test setup: node environment, no jsdom, no component
// rendering — these tests target pure transform modules only. The
// Playwright e2e suite (playwright.config.ts) is separate; exclude its
// tests/ dir so `vitest run` doesn't try to execute Playwright specs.
export default defineConfig({
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  test: {
    environment: "node",
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
