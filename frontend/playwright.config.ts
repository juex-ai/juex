import { existsSync } from "node:fs";
import { platform } from "node:os";

import { defineConfig } from "@playwright/test";

function chromeExecutable(): string {
  const configured = process.env.CHROME_PATH?.trim();
  if (configured) return configured;

  const candidates =
    platform() === "darwin"
      ? ["/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"]
      : platform() === "win32"
        ? [
            "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
            "C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe",
          ]
        : [
            "/usr/bin/google-chrome",
            "/usr/bin/google-chrome-stable",
            "/usr/bin/chromium",
            "/usr/bin/chromium-browser",
          ];
  const executable = candidates.find(existsSync);
  if (!executable) {
    throw new Error("Chrome not found; set CHROME_PATH to run browser tests.");
  }
  return executable;
}

export default defineConfig({
  testDir: "../tests/e2e/browser",
  fullyParallel: false,
  workers: 1,
  reporter: "line",
  use: {
    baseURL: "http://127.0.0.1:4173",
    launchOptions: { executablePath: chromeExecutable() },
    trace: "retain-on-failure",
  },
  webServer: {
    command: "pnpm exec vite preview --host 127.0.0.1 --port 4173 --strictPort",
    url: "http://127.0.0.1:4173",
    reuseExistingServer: false,
  },
});
