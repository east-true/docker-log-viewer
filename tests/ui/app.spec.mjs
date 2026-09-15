import { expect, test } from "@playwright/test";

const containerID = "a".repeat(64);
const baseURL = process.env.DLV_TEST_BASE_URL || "http://127.0.0.1:18080";
const container = {
  id: containerID,
  short_id: containerID.slice(0, 12),
  name: "web",
  image: "example/web:latest",
  image_id: "b".repeat(64),
  state: "running",
  status: "Up 1 minute",
  created_at: 1,
};

function ndjson(events) {
  return `${events.map((event) => JSON.stringify(event)).join("\n")}\n`;
}

test("keeps logs, flushes the final line, and deduplicates a resumed stream", async ({ page }) => {
  const requests = [];
  await page.route("**/api/containers", (route) => route.fulfill({ json: [container] }));
  await page.route("**/api/images", (route) => route.fulfill({ json: [] }));
  await page.route("**/api/logs?*", (route) => {
    const url = new URL(route.request().url());
    requests.push(Object.fromEntries(url.searchParams));
    const resumed = url.searchParams.has("since");
    const events = resumed
      ? [
          { type: "ready", observed_at: "2026-09-15T01:00:01Z" },
          { type: "log", container_id: containerID, container_name: "web", stream: "stdout", message: "2026-09-15T01:00:00.223456789Z final without newline\n", observed_at: "2026-09-15T01:00:01Z" },
          { type: "log", container_id: containerID, container_name: "web", stream: "stdout", message: "2026-09-15T01:00:01.323456789Z new line\n", observed_at: "2026-09-15T01:00:02Z" },
          { type: "end", observed_at: "2026-09-15T01:00:02Z" },
        ]
      : [
          { type: "ready", observed_at: "2026-09-15T01:00:00Z" },
          { type: "log", container_id: containerID, container_name: "web", stream: "stdout", message: "2026-09-15T01:00:00.123456789Z first\n2026-09-15T01:00:00.223456789Z final without newline", observed_at: "2026-09-15T01:00:01Z" },
          { type: "end", observed_at: "2026-09-15T01:00:01Z" },
        ];
    return route.fulfill({ status: 200, contentType: "application/x-ndjson", body: ndjson(events) });
  });

  await page.goto(`${baseURL}/`);
  await page.getByRole("button", { name: "Logs" }).click();
  await expect(page.locator(".log-row")).toHaveCount(2);
  await expect(page.locator(".log-message").last()).toHaveText("final without newline");

  await page.locator("#live-button").click();
  await expect(page.locator("#live-button em")).toHaveText("실시간 OFF");
  await expect(page.locator(".log-row")).toHaveCount(2);
  await page.locator("#live-button").click();

  await expect(page.locator(".log-row")).toHaveCount(3);
  await expect(page.locator(".log-message").allTextContents()).resolves.toEqual(["first", "final without newline", "new line"]);
  expect(requests[1]).toMatchObject({ container: containerID, tail: "all", since: "2026-09-15T01:00:00.223456789Z", follow: "true" });
});

test("loads the real local Docker inventory", async ({ page }) => {
  await page.goto(`${baseURL}/`);
  await expect(page.locator("#engine-status small")).toContainText("containers");
  await expect(page.locator("#images-total")).not.toHaveText("—");
  await page.getByRole("button", { name: "Logs" }).click();
  await expect(page.locator("#container-list .container-item").first()).toBeVisible();
});

test("pauses and resumes a real Docker log stream without clearing it", async ({ page }) => {
  const containerName = process.env.DLV_LIVE_TEST_CONTAINER;
  test.skip(!containerName, "set DLV_LIVE_TEST_CONTAINER to run the live Docker smoke test");

  await page.goto(`${baseURL}/#logs`);
  const item = page.locator("#container-list .container-item", { hasText: containerName });
  await expect(item).toBeVisible({ timeout: 10000 });
  await item.click();
  await expect.poll(() => page.locator(".log-row.stdout").count()).toBeGreaterThan(1);

  await page.locator("#live-button").click();
  const pausedCount = await page.locator(".log-row.stdout").count();
  await page.waitForTimeout(700);
  await expect(page.locator(".log-row.stdout")).toHaveCount(pausedCount);

  await page.locator("#live-button").click();
  await expect.poll(() => page.locator(".log-row.stdout").count()).toBeGreaterThan(pausedCount);
  const messages = await page.locator(".log-row.stdout .log-message").allTextContents();
  expect(new Set(messages).size).toBe(messages.length);
});
