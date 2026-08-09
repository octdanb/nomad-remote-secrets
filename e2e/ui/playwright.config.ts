import { defineConfig, devices } from '@playwright/test';

// The Nomad web UI is served by the dev agent booted in run.sh.
const NOMAD_ADDR = process.env.NOMAD_ADDR || 'http://127.0.0.1:4646';

// Watch mode: `E2E_HEADED=1` (set by run.sh's --watch) opens a real browser so
// you can see the tests drive the Nomad UI. E2E_SLOWMO (ms, default 250 when
// headed) slows each action down enough to follow along. The `--headed`/`--ui`
// CLI flags still work and take precedence.
const HEADED = process.env.E2E_HEADED === '1';
const SLOWMO = Number(process.env.E2E_SLOWMO ?? (HEADED ? 500 : 0)) || 0;

export default defineConfig({
  testDir: './tests',
  // When watching, run one worker sequentially so a single browser window
  // does every test in order — easy to follow. Headless runs stay parallel.
  fullyParallel: !HEADED,
  workers: HEADED ? 1 : undefined,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['github'], ['list']] : 'list',
  use: {
    baseURL: NOMAD_ADDR,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    // Headed when watching, headless otherwise (CI and default local runs).
    headless: !HEADED,
    launchOptions: { slowMo: SLOWMO },
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
