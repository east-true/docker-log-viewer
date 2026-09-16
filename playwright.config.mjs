import { defineConfig } from "@playwright/test";

const retainFailureArtifacts =
  Boolean(process.env.CI) && !process.env.DLV_TEST_WEB_TOKEN;

export default defineConfig({
  testDir: "./tests/ui",
  testMatch: "**/*.spec.mjs",
  fullyParallel: false,
  workers: 1,
  timeout: 30000,
  expect: { timeout: 5000 },
  outputDir: "test-results",
  reporter: process.env.CI
    ? [["line"], ["html", { outputFolder: "playwright-report", open: "never" }]]
    : "list",
  use: {
    headless: true,
    ignoreHTTPSErrors: process.env.DLV_TEST_IGNORE_HTTPS_ERRORS === "1",
    httpCredentials: process.env.DLV_TEST_WEB_TOKEN
      ? {
          username: process.env.DLV_TEST_WEB_USER || "admin",
          password: process.env.DLV_TEST_WEB_TOKEN,
        }
      : undefined,
    screenshot: retainFailureArtifacts ? "only-on-failure" : "off",
    trace: retainFailureArtifacts ? "retain-on-failure" : "off",
  },
});
