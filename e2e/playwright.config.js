import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  testMatch: "admin.spec.js",
  timeout: 30_000,
  fullyParallel: false,
  workers: 1,
  use: {
    baseURL: process.env.BASE_URL,
    browserName: "chromium",
    trace: "retain-on-failure",
  },
});
