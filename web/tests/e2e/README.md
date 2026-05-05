# E2E tests

Playwright smoke tests for the kuso dashboard. The full E2E surface against a live cluster is in `docs/LIVE_TEST_PLAN.md`; these are the cheap pre-commit checks.

## Run locally

One-time setup:

```bash
cd web
npm install
npx playwright install chromium     # ~90 MB browser download, gitignored
```

Run the mobile smoke set against the bundled dev server:

```bash
npm run test:e2e:mobile
```

Run against an already-running instance (e.g. a real kuso install):

```bash
KUSO_E2E_BASE_URL=https://kuso.example.com npm run test:e2e:mobile
```

## What's covered

- **`mobile-smoke.spec.ts`** — login page renders cleanly at iPhone 13 viewport, no horizontal scroll, form fields are tappable, unauthenticated redirects don't leave a blank screen.

That's the floor. Anything authenticated needs a live cluster (or a fake API server) and lives in the live-test plan.

## CI

Not wired yet. To turn this on in GitHub Actions, the runner needs `npx playwright install --with-deps chromium` plus the same `npm run test:e2e:mobile`. Browsers are not committed and not part of `npm install`.
