import { defineConfig } from "cypress";
import { execSync } from "node:child_process";

function resolveApiToken(): string | undefined {
  if (process.env.CYPRESS_API_TOKEN) return process.env.CYPRESS_API_TOKEN;
  try {
    return execSync(
      "kubectl get secret sympozium-ui-token -n sympozium-system -o jsonpath='{.data.token}' | base64 -d",
      { encoding: "utf-8", timeout: 5000 },
    ).trim();
  } catch {
    return undefined;
  }
}

export default defineConfig({
  e2e: {
    baseUrl: process.env.CYPRESS_BASE_URL || "http://localhost:5173",
    env: {
      TEST_MODEL: process.env.CYPRESS_TEST_MODEL || "qwen/qwen3.5-9b",
      API_TOKEN: resolveApiToken(),
    },
    // Tests run against the live dev server + cluster — no mocking.
    supportFile: "cypress/support/e2e.ts",
    specPattern: "cypress/e2e/**/*.cy.ts",
    viewportWidth: 1280,
    viewportHeight: 800,
    defaultCommandTimeout: 15000,
    video: false,
  },
});
