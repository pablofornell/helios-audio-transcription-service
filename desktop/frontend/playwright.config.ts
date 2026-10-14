import { defineConfig } from '@playwright/test';

const webPort = process.env.HELIOS_WEB_PORT ?? '5188';
const baseURL = `http://127.0.0.1:${webPort}`;

export default defineConfig({
  testDir: './tests/web',
  workers: 1,
  use: { baseURL, browserName: 'chromium', headless: true, viewport: { width: 1440, height: 1000 }, trace: 'retain-on-failure' },
  webServer: { command: `npm run dev:web -- --port ${webPort} --strictPort`, url: baseURL, reuseExistingServer: !process.env.CI },
});
