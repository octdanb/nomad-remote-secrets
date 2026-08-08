import { defineConfig, devices } from '@playwright/test';

// The Nomad web UI is served by the dev agent booted in run.sh.
const NOMAD_ADDR = process.env.NOMAD_ADDR || 'http://127.0.0.1:4646';

export default defineConfig({
  testDir: './tests',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['github'], ['list']] : 'list',
  use: {
    baseURL: NOMAD_ADDR,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
