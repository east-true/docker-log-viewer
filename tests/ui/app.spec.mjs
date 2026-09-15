import { expect, test } from "@playwright/test";

const containerID = "a".repeat(64);
const agentID = "11111111-1111-4111-8111-111111111111";
const baseURL = process.env.DLV_TEST_BASE_URL || "http://127.0.0.1:18080";
const agent = {
  id: agentID,
  name: "lab-host",
  connected: true,
  last_seen: "2026-09-15T01:00:00Z",
};
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

test("keeps logs, flushes the final line, and deduplicates a resumed stream", async ({
  page,
}) => {
  const requests = [];
  await page.route("**/api/agents", (route) =>
    route.fulfill({ json: [agent] }),
  );
  await page.route("**/api/containers?*", (route) =>
    route.fulfill({ json: [container] }),
  );
  await page.route("**/api/images?*", (route) => route.fulfill({ json: [] }));
  await page.route("**/api/logs?*", (route) => {
    const url = new URL(route.request().url());
    requests.push(Object.fromEntries(url.searchParams));
    const resumed = url.searchParams.has("since");
    const events = resumed
      ? [
          { type: "ready", observed_at: "2026-09-15T01:00:01Z" },
          {
            type: "log",
            container_id: containerID,
            container_name: "web",
            stream: "stdout",
            message: "2026-09-15T01:00:00.223456789Z final without newline\n",
            observed_at: "2026-09-15T01:00:01Z",
          },
          {
            type: "log",
            container_id: containerID,
            container_name: "web",
            stream: "stdout",
            message: "2026-09-15T01:00:01.323456789Z new line\n",
            observed_at: "2026-09-15T01:00:02Z",
          },
          { type: "end", observed_at: "2026-09-15T01:00:02Z" },
        ]
      : [
          { type: "ready", observed_at: "2026-09-15T01:00:00Z" },
          {
            type: "log",
            container_id: containerID,
            container_name: "web",
            stream: "stdout",
            message:
              "2026-09-15T01:00:00.123456789Z first\n2026-09-15T01:00:00.223456789Z final without newline",
            observed_at: "2026-09-15T01:00:01Z",
          },
          { type: "end", observed_at: "2026-09-15T01:00:01Z" },
        ];
    return route.fulfill({
      status: 200,
      contentType: "application/x-ndjson",
      body: ndjson(events),
    });
  });

  await page.goto(`${baseURL}/`);
  await page.getByRole("button", { name: "Logs" }).click();
  await expect(page.locator(".log-row")).toHaveCount(2);
  await expect(page.locator(".log-message").last()).toHaveText(
    "final without newline",
  );

  await page.locator("#live-button").click();
  await expect(page.locator("#live-button em")).toHaveText("실시간 OFF");
  await expect(page.locator(".log-row")).toHaveCount(2);
  await page.locator("#live-button").click();

  await expect(page.locator(".log-row")).toHaveCount(3);
  await expect(page.locator(".log-message").allTextContents()).resolves.toEqual(
    ["first", "final without newline", "new line"],
  );
  expect(requests[1]).toMatchObject({
    agent: agentID,
    container: containerID,
    tail: "all",
    since: "2026-09-15T01:00:00.223456789Z",
    follow: "true",
  });
});

test("loads the real local Docker inventory", async ({ page }) => {
  await page.goto(`${baseURL}/`);
  await expect(page.locator("#engine-status small")).toContainText(
    "containers",
  );
  await expect(page.locator("#images-total")).not.toHaveText("—");
  await page.getByRole("button", { name: "Logs" }).click();
  await expect(page.locator(".agent-group summary").first()).toBeVisible();
  await expect(
    page.locator("#container-list .container-item").first(),
  ).toBeVisible();
});

test("groups containers by host and shows image hosts", async ({ page }) => {
  const secondAgentID = "22222222-2222-4222-8222-222222222222";
  const secondContainer = {
    ...container,
    id: "c".repeat(64),
    short_id: "c".repeat(12),
    name: "worker",
  };
  const image = {
    id: "sha256:" + "d".repeat(64),
    short_id: "d".repeat(12),
    tags: ["example/app:latest"],
    size: 42,
    created_at: 1,
    in_use: false,
    running_count: 0,
    container_count: 0,
    containers: [],
  };
  const logRequests = [];
  await page.route("**/api/agents", (route) =>
    route.fulfill({
      json: [
        agent,
        {
          id: secondAgentID,
          name: "edge-host",
          connected: true,
          last_seen: "2026-09-15T01:00:00Z",
        },
      ],
    }),
  );
  await page.route("**/api/containers?*", (route) => {
    const selected = new URL(route.request().url()).searchParams.get("agent");
    return route.fulfill({
      json: selected === secondAgentID ? [secondContainer] : [container],
    });
  });
  await page.route("**/api/images?*", (route) => {
    const selected = new URL(route.request().url()).searchParams.get("agent");
    return route.fulfill({
      json: [
        {
          ...image,
          tags: [
            `example/${selected === secondAgentID ? "worker" : "web"}:latest`,
          ],
        },
      ],
    });
  });
  await page.route("**/api/logs?*", (route) => {
    logRequests.push(
      Object.fromEntries(new URL(route.request().url()).searchParams),
    );
    return route.fulfill({
      status: 200,
      contentType: "application/x-ndjson",
      body: ndjson([{ type: "end", observed_at: "2026-09-15T01:00:00Z" }]),
    });
  });

  await page.goto(`${baseURL}/`);
  await expect(page.locator("#image-table-body tr")).toHaveCount(2);
  await expect(page.locator(".host-cell").allTextContents()).resolves.toEqual([
    "lab-host",
    "edge-host",
  ]);
  await page.getByRole("button", { name: "Logs" }).click();
  await expect(
    page.locator(".agent-group summary strong").allTextContents(),
  ).resolves.toEqual(["lab-host", "edge-host"]);
  await page.locator(".container-item", { hasText: "worker" }).click();
  await expect(page.locator("#target-meta")).toContainText("edge-host");
  await expect
    .poll(() => logRequests.some((request) => request.agent === secondAgentID))
    .toBe(true);
});

test("refreshes every agent inventory at the selected interval", async ({
  page,
}) => {
  let agentRequests = 0;
  await page.clock.install();
  await page.route("**/api/agents", (route) => {
    agentRequests += 1;
    return route.fulfill({ json: [agent] });
  });
  await page.route("**/api/containers?*", (route) =>
    route.fulfill({ json: [container] }),
  );
  await page.route("**/api/images?*", (route) => route.fulfill({ json: [] }));

  await page.goto(`${baseURL}/`);
  await expect.poll(() => agentRequests).toBe(1);
  await expect(page.locator(".refresh-interval:visible")).toHaveValue("5");
  await page.locator(".refresh-interval:visible").selectOption("10");
  await expect(page.locator(".refresh-interval").first()).toHaveValue("10");

  await page.clock.fastForward(9999);
  expect(agentRequests).toBe(1);
  await page.clock.fastForward(1);
  await expect.poll(() => agentRequests).toBe(2);

  await page.locator(".refresh-interval:visible").selectOption("0");
  await expect(page.locator(".refresh-interval").first()).toHaveValue("0");
  await page.clock.fastForward(60_000);
  expect(agentRequests).toBe(2);
});

test("keeps grouped container and image lists scrollable", async ({ page }) => {
  const agents = [
    agent,
    {
      id: "22222222-2222-4222-8222-222222222222",
      name: "edge-host",
      connected: true,
      last_seen: "2026-09-15T01:00:00Z",
    },
  ];
  await page.route("**/api/agents", (route) => route.fulfill({ json: agents }));
  await page.route("**/api/containers?*", (route) => {
    const selectedAgent = new URL(route.request().url()).searchParams.get(
      "agent",
    );
    const prefix = selectedAgent === agentID ? "a" : "b";
    return route.fulfill({
      json: Array.from({ length: 36 }, (_, index) => ({
        ...container,
        id: `${prefix}${String(index).padStart(63, "0")}`,
        short_id: `${prefix}${String(index).padStart(11, "0")}`,
        name: `${prefix}-container-${index}`,
      })),
    });
  });
  await page.route("**/api/images?*", (route) => {
    const selectedAgent = new URL(route.request().url()).searchParams.get(
      "agent",
    );
    const prefix = selectedAgent === agentID ? "a" : "b";
    return route.fulfill({
      json: Array.from({ length: 36 }, (_, index) => ({
        id: `sha256:${prefix}${String(index).padStart(63, "0")}`,
        short_id: `${prefix}${String(index).padStart(11, "0")}`,
        tags: [`example/${prefix}-image-${index}:latest`],
        size: index + 1,
        created_at: 1,
        in_use: false,
        running_count: 0,
        container_count: 0,
        containers: [],
      })),
    });
  });
  await page.route("**/api/logs?*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/x-ndjson",
      body: ndjson([{ type: "end", observed_at: "2026-09-15T01:00:00Z" }]),
    }),
  );

  await page.setViewportSize({ width: 1024, height: 700 });
  await page.goto(`${baseURL}/#logs`);
  const containerList = page.locator("#container-list");
  await expect
    .poll(() =>
      containerList.evaluate(
        (element) => element.scrollHeight > element.clientHeight,
      ),
    )
    .toBe(true);
  await containerList.evaluate((element) => {
    element.scrollTop = element.scrollHeight;
  });
  await expect
    .poll(() => containerList.evaluate((element) => element.scrollTop))
    .toBeGreaterThan(0);
  await page.locator(".agent-group summary").first().click();
  await expect(page.locator(".agent-group").first()).not.toHaveAttribute(
    "open",
    "",
  );
  await page.locator(".agent-group summary").first().click();
  await expect(page.locator(".agent-group").first()).toHaveAttribute(
    "open",
    "",
  );

  await page.getByRole("button", { name: "Images" }).click();
  const imageList = page.locator(".table-scroll");
  await expect
    .poll(() =>
      imageList.evaluate(
        (element) => element.scrollHeight > element.clientHeight,
      ),
    )
    .toBe(true);
  await imageList.evaluate((element) => {
    element.scrollTop = element.scrollHeight;
  });
  await expect
    .poll(() => imageList.evaluate((element) => element.scrollTop))
    .toBeGreaterThan(0);
});

test("pauses and resumes a real Docker log stream without clearing it", async ({
  page,
}) => {
  const containerName = process.env.DLV_LIVE_TEST_CONTAINER;
  test.skip(
    !containerName,
    "set DLV_LIVE_TEST_CONTAINER to run the live Docker smoke test",
  );

  await page.goto(`${baseURL}/#logs`);
  const item = page.locator("#container-list .container-item", {
    hasText: containerName,
  });
  await expect(item).toBeVisible({ timeout: 10000 });
  await item.click();
  await expect
    .poll(() => page.locator(".log-row.stdout").count())
    .toBeGreaterThan(1);

  await page.locator("#live-button").click();
  const pausedCount = await page.locator(".log-row.stdout").count();
  await page.waitForTimeout(700);
  await expect(page.locator(".log-row.stdout")).toHaveCount(pausedCount);

  await page.locator("#live-button").click();
  await expect
    .poll(() => page.locator(".log-row.stdout").count())
    .toBeGreaterThan(pausedCount);
  const messages = await page
    .locator(".log-row.stdout .log-message")
    .allTextContents();
  expect(new Set(messages).size).toBe(messages.length);
});
