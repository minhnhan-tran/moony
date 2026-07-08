import { defineConfig, devices } from '@playwright/test';

// Where the SPA is served. Point this at your running local stack (a local relay
// + connector, or the deployed relay). Default matches the common local relay.
const BASE_URL = process.env.SQE_BASE_URL || 'https://localhost.apple.com:448';

// storageState replays a captured login for auth-gated targets (SQE_AUTH=1). For
// a loopback, unauthenticated instance leave it unset. See README ("Auth").
const STORAGE_STATE = process.env.SQE_STORAGE_STATE || undefined;

export default defineConfig({
  testDir: './tests',
  // An agent turn (real LLM + tools) can run for minutes; give tests room.
  timeout: 20 * 60 * 1000,
  expect: { timeout: 30_000 },
  fullyParallel: false, // one persona conversation at a time
  workers: 1,
  retries: 0,
  reporter: [['list'], ['html', { open: 'never' }]],
  use: {
    baseURL: BASE_URL,
    ignoreHTTPSErrors: true, // local relay commonly uses a loopback/self-signed cert
    headless: !process.env.HEADED,
    viewport: { width: 1280, height: 900 },
    trace: 'retain-on-failure',
    video: 'retain-on-failure',
    screenshot: 'only-on-failure',
    storageState: STORAGE_STATE,
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});
