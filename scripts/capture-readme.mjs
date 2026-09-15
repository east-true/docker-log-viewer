import { mkdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";

const baseURL = process.env.DLV_TEST_BASE_URL || "http://127.0.0.1:18080";
const output = fileURLToPath(
  new URL("../docs/assets/overview.png", import.meta.url),
);
const id = (character) => character.repeat(64);
const agentID = "11111111-1111-4111-8111-111111111111";
const agents = [
  {
    id: agentID,
    name: "demo-host",
    connected: true,
    last_seen: "2026-09-15T05:12:30Z",
  },
];

const containers = [
  {
    id: id("a"),
    short_id: id("a").slice(0, 12),
    name: "api-gateway",
    image: "example/api:1.4.0",
    image_id: id("1"),
    state: "running",
    status: "Up 2 hours",
    created_at: 1789434000,
  },
  {
    id: id("b"),
    short_id: id("b").slice(0, 12),
    name: "background-worker",
    image: "example/worker:1.4.0",
    image_id: id("2"),
    state: "running",
    status: "Up 2 hours",
    created_at: 1789434000,
  },
  {
    id: id("c"),
    short_id: id("c").slice(0, 12),
    name: "database",
    image: "postgres:17-alpine",
    image_id: id("3"),
    state: "running",
    status: "Up 2 hours (healthy)",
    health: "healthy",
    created_at: 1789434000,
  },
  {
    id: id("d"),
    short_id: id("d").slice(0, 12),
    name: "cache",
    image: "redis:7-alpine",
    image_id: id("4"),
    state: "running",
    status: "Up 2 hours",
    created_at: 1789434000,
  },
  {
    id: id("e"),
    short_id: id("e").slice(0, 12),
    name: "old-worker",
    image: "example/worker:1.3.2",
    image_id: id("5"),
    state: "exited",
    status: "Exited (0) 3 days ago",
    created_at: 1789002000,
  },
];

const events = [
  ["05:12:14.042123456", "server listening on http://0.0.0.0:8080"],
  ["05:12:16.108452001", "database connection pool ready connections=12"],
  [
    "05:12:18.237091884",
    "request completed method=GET path=/health status=200 duration=2ms",
  ],
  [
    "05:12:19.503117442",
    "request completed method=GET path=/api/projects status=200 duration=18ms",
  ],
  [
    "05:12:21.025712990",
    "request completed method=POST path=/api/jobs status=202 duration=31ms",
  ],
  ["05:12:21.339001027", "job queued type=report.generate queue=default"],
  [
    "05:12:23.774219335",
    "request completed method=GET path=/api/jobs/42 status=200 duration=6ms",
  ],
  ["05:12:25.101774208", "cache hit key=project-summary ttl=300s"],
  [
    "05:12:27.682441705",
    "request completed method=GET path=/metrics status=200 duration=4ms",
  ],
].map(([time, message]) => ({
  type: "log",
  container_id: id("a"),
  container_name: "api-gateway",
  stream: "stdout",
  message: `2026-09-15T${time}Z ${message}\n`,
  observed_at: `2026-09-15T${time.slice(0, 12)}Z`,
}));

await mkdir(new URL("../docs/assets/", import.meta.url), { recursive: true });
const browser = await chromium.launch();
const page = await browser.newPage({
  viewport: { width: 1440, height: 900 },
  deviceScaleFactor: 1,
});
await page.route("**/api/agents", (route) => route.fulfill({ json: agents }));
await page.route("**/api/containers?*", (route) =>
  route.fulfill({ json: containers }),
);
await page.route("**/api/images?*", (route) => route.fulfill({ json: [] }));
await page.route("**/api/logs?*", (route) =>
  route.fulfill({
    status: 200,
    contentType: "application/x-ndjson",
    body: `${[{ type: "ready", observed_at: "2026-09-15T05:12:14Z" }, ...events]
      .map((event) => JSON.stringify(event))
      .join("\n")}\n`,
  }),
);

await page.goto(`${baseURL}/#logs`);
await page
  .locator(".log-row")
  .nth(events.length - 1)
  .waitFor();
const renderedNames = await page
  .locator(".container-item strong")
  .allTextContents();
const renderedImages = await page
  .locator(".container-item small")
  .allTextContents();
const renderedHosts = await page
  .locator(".agent-group summary strong")
  .allTextContents();
const expectedNames = containers.map((container) => container.name);
const expectedImages = containers.map((container) => container.image);
if (
  JSON.stringify(renderedNames) !== JSON.stringify(expectedNames) ||
  JSON.stringify(renderedImages) !== JSON.stringify(expectedImages) ||
  JSON.stringify(renderedHosts) !== JSON.stringify(["demo-host"])
) {
  throw new Error(
    "refusing to capture README screenshot: rendered inventory differs from synthetic fixtures",
  );
}
if ((await page.locator(".log-row").count()) !== events.length) {
  throw new Error(
    "refusing to capture README screenshot: rendered logs differ from synthetic fixtures",
  );
}
await page.evaluate(() => {
  const state = document.querySelector("#stream-state");
  state.className = "stream-state live";
  state.lastChild.textContent = " 실시간 ON";
});
await page.screenshot({ path: output, fullPage: false });
await browser.close();
