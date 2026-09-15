import { LogAssembler } from "./log-buffer.mjs";

const MAX_LOG_RECORDS = 10000;
const REFRESH_INTERVALS = new Set([0, 5, 10, 30, 60]);
const REFRESH_STORAGE_KEY = "docker-log-viewer-refresh-seconds";

const state = {
  agents: [],
  containers: [],
  images: [],
  selected: null,
  inventoryToken: 0,
  collapsedAgents: new Set(),
  failedAgents: new Set(),
  logs: [],
  assembler: new LogAssembler(),
  liveEnabled: true,
  following: false,
  controller: null,
  reconnectTimer: null,
  inventoryTimer: null,
  refreshSeconds: 5,
  streamInitialized: false,
  currentView: null,
  streamToken: 0,
  streamHadError: false,
  reconnectAttempt: 0,
  lastTimestamp: "",
  boundaryCounts: new Map(),
  replayGuard: null,
  unseenLogs: 0,
};

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];

async function fetchJSON(path) {
  const response = await fetch(path, {
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try {
      message = (await response.json()).message || message;
    } catch (_) {}
    throw new Error(message);
  }
  return response.json();
}

function showToast(message) {
  const toast = $("#toast");
  toast.textContent = message;
  toast.classList.add("show");
  clearTimeout(showToast.timer);
  showToast.timer = setTimeout(() => toast.classList.remove("show"), 4500);
}

function setEngineStatus(ok, message) {
  const root = $("#engine-status");
  const dot = root.querySelector(".status-dot");
  dot.className = `status-dot ${ok ? "running" : "error"}`;
  root.querySelector("small").textContent = message;
}

function storedRefreshSeconds() {
  try {
    const stored = localStorage.getItem(REFRESH_STORAGE_KEY);
    if (stored === null) return 5;
    const seconds = Number(stored);
    return REFRESH_INTERVALS.has(seconds) ? seconds : 5;
  } catch (_) {
    return 5;
  }
}

function setRefreshInterval(seconds) {
  state.refreshSeconds = REFRESH_INTERVALS.has(seconds) ? seconds : 5;
  clearInterval(state.inventoryTimer);
  state.inventoryTimer = null;
  $$(".refresh-interval").forEach((select) => {
    select.value = String(state.refreshSeconds);
  });
  try {
    localStorage.setItem(REFRESH_STORAGE_KEY, String(state.refreshSeconds));
  } catch (_) {}
  if (state.refreshSeconds > 0) {
    state.inventoryTimer = setInterval(
      () => loadInventory({ quiet: true }),
      state.refreshSeconds * 1000,
    );
  }
}

async function loadInventory({ quiet = false } = {}) {
  const inventoryToken = ++state.inventoryToken;
  try {
    const agents = await fetchJSON("/api/agents");
    if (inventoryToken !== state.inventoryToken) return;
    state.agents = agents;
    const connectedAgents = agents.filter((item) => item.connected);
    if (!connectedAgents.length) {
      stopLogStream({ flush: true });
      state.failedAgents = new Set();
      state.containers = [];
      state.images = [];
      state.selected = null;
      renderContainers();
      renderImages();
      renderTarget();
      setEngineStatus(
        false,
        agents.length ? "모든 Agent 오프라인" : "Agent 연결 대기",
      );
      return;
    }
    const inventories = await Promise.all(
      connectedAgents.map(async (agent) => {
        const query = new URLSearchParams({ agent: agent.id });
        try {
          const [containers, images] = await Promise.all([
            fetchJSON(`/api/containers?${query}`),
            fetchJSON(`/api/images?${query}`),
          ]);
          return { agent, containers, images };
        } catch (error) {
          return { agent, containers: [], images: [], error };
        }
      }),
    );
    if (inventoryToken !== state.inventoryToken) return;
    const containers = inventories.flatMap(({ agent, containers: values }) =>
      values.map((item) => ({
        ...item,
        agent_id: agent.id,
        agent_name: agent.name,
      })),
    );
    const images = inventories.flatMap(({ agent, images: values }) =>
      values.map((item) => ({
        ...item,
        agent_id: agent.id,
        agent_name: agent.name,
      })),
    );
    const previousTarget = selectedContainer();
    const previousSelection = state.selected;
    state.containers = containers;
    state.images = images;
    if (
      !state.selected ||
      !containers.some((item) => containerKey(item) === state.selected)
    ) {
      state.selected = containers[0] ? containerKey(containers[0]) : null;
    }
    const currentTarget = selectedContainer();
    const selectionChanged = previousSelection !== state.selected;
    const runtimeChanged =
      previousTarget &&
      currentTarget &&
      previousTarget.state !== currentTarget.state;
    if (
      state.currentView === "logs" &&
      state.streamInitialized &&
      selectionChanged
    )
      restartLogStream();
    else if (
      state.currentView === "logs" &&
      state.streamInitialized &&
      runtimeChanged
    )
      streamLogs({ resume: true });
    const failures = inventories.filter((item) => item.error);
    state.failedAgents = new Set(failures.map((item) => item.agent.id));
    renderContainers();
    renderImages();
    renderTarget();
    setEngineStatus(
      failures.length === 0,
      `${connectedAgents.length} agents · ${containers.length} containers`,
    );
    if (failures.length && !quiet)
      showToast(
        `${failures.length}개 Agent의 inventory를 불러오지 못했습니다.`,
      );
  } catch (error) {
    stopLogStream({ flush: true });
    state.agents = [];
    state.containers = [];
    state.images = [];
    state.selected = null;
    state.failedAgents = new Set();
    renderContainers();
    renderImages();
    renderTarget();
    setEngineStatus(false, "Agent에 연결할 수 없음");
    if (!quiet) showToast(`Docker Agent: ${error.message}`);
  }
}

function containerKey(item) {
  return `${item.agent_id}:${item.id}`;
}

function selectedContainer() {
  return state.containers.find((item) => containerKey(item) === state.selected);
}

function renderContainers() {
  const root = $("#container-list");
  const query = $("#container-search").value.trim().toLowerCase();
  root.replaceChildren();
  $("#container-count").textContent = state.containers.length;

  const agents = state.agents.filter((agent) => {
    if (!query) return true;
    if (agent.name.toLowerCase().includes(query)) return true;
    return state.containers.some(
      (item) => item.agent_id === agent.id && containerMatches(item, query),
    );
  });
  for (const agent of agents) {
    const hostMatches = Boolean(
      query && agent.name.toLowerCase().includes(query),
    );
    const items = state.containers.filter(
      (item) =>
        item.agent_id === agent.id &&
        (hostMatches || containerMatches(item, query)),
    );
    const group = document.createElement("details");
    const failed = state.failedAgents.has(agent.id);
    group.className = `agent-group${agent.connected ? "" : " offline"}${failed ? " unavailable" : ""}`;
    group.dataset.agentId = agent.id;
    group.open = query ? true : !state.collapsedAgents.has(agent.id);
    group.addEventListener("toggle", () => {
      if (group.open) state.collapsedAgents.delete(agent.id);
      else state.collapsedAgents.add(agent.id);
    });
    const summary = document.createElement("summary");
    const hostDot = document.createElement("span");
    hostDot.className = `status-dot ${agent.connected ? "running" : "stopped"}`;
    const hostName = document.createElement("strong");
    hostName.textContent = agent.name;
    const hostCount = document.createElement("span");
    hostCount.className = "host-count";
    hostCount.textContent = failed
      ? "error"
      : agent.connected
        ? String(items.length)
        : "offline";
    summary.append(hostDot, hostName, hostCount);
    const body = document.createElement("div");
    body.className = "agent-containers";
    for (const item of items) body.append(createContainerButton(item));
    if (!items.length) {
      const empty = document.createElement("p");
      empty.className = "agent-empty";
      empty.textContent = failed
        ? "Inventory 조회 실패"
        : agent.connected
          ? "컨테이너 없음"
          : "Agent 오프라인";
      body.append(empty);
    }
    group.append(summary, body);
    root.append(group);
  }
  if (!root.children.length) {
    const empty = document.createElement("p");
    empty.className = "empty-state";
    empty.textContent = state.agents.length
      ? "검색 결과가 없습니다."
      : "연결된 Agent가 없습니다.";
    root.append(empty);
  }
}

function containerMatches(item, query) {
  const text =
    `${item.agent_name} ${item.name} ${item.image} ${item.short_id} ${item.state}`.toLowerCase();
  return text.includes(query);
}

function createContainerButton(item) {
  const button = document.createElement("button");
  const key = containerKey(item);
  button.className = `container-item${state.selected === key ? " selected" : ""}`;
  button.dataset.key = key;
  button.setAttribute("aria-pressed", state.selected === key);
  const dot = document.createElement("span");
  dot.className = `status-dot ${item.state === "running" ? "running" : "stopped"}`;
  const text = document.createElement("span");
  const name = document.createElement("strong");
  name.textContent = item.name;
  const meta = document.createElement("small");
  meta.textContent = item.image;
  text.append(name, meta);
  const id = document.createElement("em");
  id.textContent = item.short_id;
  button.append(dot, text, id);
  return button;
}

function renderTarget() {
  const target = selectedContainer();
  const name = $("#target-name");
  const meta = $("#target-meta");
  const dot = $("#target-dot");
  if (!target) {
    name.textContent = "컨테이너 선택";
    meta.textContent = "조회할 컨테이너가 없습니다";
    dot.className = "status-dot stopped";
    renderLiveButton();
    return;
  }
  name.textContent = target.name;
  meta.textContent = `${target.agent_name} · ${target.image} · ${target.status}`;
  dot.className = `status-dot ${target.state === "running" ? "running" : "stopped"}`;
  renderLiveButton();
}

function formatBytes(bytes) {
  if (!Number.isFinite(bytes) || bytes < 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit += 1;
  }
  return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`;
}

function formatAge(timestamp) {
  const seconds = Math.max(0, Math.floor(Date.now() / 1000 - timestamp));
  if (seconds < 60) return "방금 전";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}분 전`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}시간 전`;
  if (seconds < 86400 * 30) return `${Math.floor(seconds / 86400)}일 전`;
  return new Intl.DateTimeFormat("ko-KR", {
    year: "numeric",
    month: "short",
    day: "numeric",
  }).format(new Date(timestamp * 1000));
}

function renderImages() {
  const root = $("#image-table-body");
  const query = $("#image-search").value.trim().toLowerCase();
  const images = state.images.filter((item) =>
    `${item.agent_name} ${item.tags.join(" ")} ${item.short_id}`
      .toLowerCase()
      .includes(query),
  );
  root.replaceChildren();
  $("#images-total").textContent = state.images.length;
  $("#images-used").textContent = state.images.filter(
    (item) => item.in_use,
  ).length;
  $("#images-running").textContent = state.images.filter(
    (item) => item.running_count > 0,
  ).length;
  for (const item of images) {
    const row = document.createElement("tr");
    const imageCell = document.createElement("td");
    const imageName = document.createElement("span");
    imageName.className = "image-name";
    imageName.textContent = item.tags[0];
    imageName.title = item.tags.join("\n");
    imageCell.append(imageName);
    if (item.tags.length > 1) {
      const more = document.createElement("span");
      more.className = "image-extra";
      more.textContent = `외 ${item.tags.length - 1}개 태그`;
      imageCell.append(more);
    }
    const hostCell = document.createElement("td");
    const host = document.createElement("span");
    host.className = "host-cell";
    const hostDot = document.createElement("span");
    hostDot.className = "status-dot running";
    const hostName = document.createElement("span");
    hostName.textContent = item.agent_name;
    host.title = `Agent ${item.agent_id}`;
    host.append(hostDot, hostName);
    hostCell.append(host);
    const sizeCell = document.createElement("td");
    sizeCell.className = "mono";
    sizeCell.textContent = formatBytes(item.size);
    const ageCell = document.createElement("td");
    ageCell.textContent = formatAge(item.created_at);
    const usageCell = document.createElement("td");
    const usage = document.createElement("div");
    usage.className = "usage";
    if (!item.containers?.length) {
      const empty = document.createElement("span");
      empty.className = "usage-empty";
      empty.textContent = "사용되지 않음";
      usage.append(empty);
    } else {
      for (const linked of item.containers) {
        const pill = document.createElement("span");
        pill.className = "usage-pill";
        pill.title = linked.status;
        const dot = document.createElement("span");
        dot.className = `status-dot ${linked.state === "running" ? "running" : "stopped"}`;
        const label = document.createElement("span");
        label.textContent = linked.name;
        pill.append(dot, label);
        usage.append(pill);
      }
    }
    usageCell.append(usage);
    row.append(imageCell, hostCell, sizeCell, ageCell, usageCell);
    root.append(row);
  }
  if (!images.length) {
    const row = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = 5;
    cell.className = "empty-state";
    cell.textContent = state.images.length
      ? "검색 결과가 없습니다."
      : "연결된 Agent에 이미지가 없습니다.";
    row.append(cell);
    root.append(row);
  }
}

function formatLogTimestamp(date) {
  const pad = (value, length = 2) => String(value).padStart(length, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}.${pad(date.getMilliseconds(), 3)}`;
}

function appendEvent(event) {
  if (event.type === "ready") {
    setStreamState(
      state.following ? "live" : "",
      state.following ? "실시간 ON" : "로그 불러오는 중",
    );
    return;
  }
  if (event.type === "error") {
    flushPendingLogs();
    state.streamHadError = true;
    state.following = false;
    appendLine({
      time: event.observed_at,
      container: event.container_name || "system",
      stream: "error",
      message: event.message,
      system: true,
    });
    setStreamState("error", "로그를 읽을 수 없음");
    return;
  }
  if (event.type === "end") {
    flushPendingLogs();
    if (state.streamHadError) return;
    const running = selectedContainer()?.state === "running";
    setStreamState(
      "",
      running && !state.liveEnabled ? "실시간 OFF" : "과거 로그",
    );
    return;
  }
  if (event.type !== "log") return;
  state.reconnectAttempt = 0;
  for (const record of state.assembler.push(event))
    appendAssembledRecord(record);
}

function appendAssembledRecord(record) {
  const value = {
    time: record.sourceTimestamp || record.observedAt,
    sourceTimestamp: record.sourceTimestamp,
    container: record.containerName || record.containerId.slice(0, 12),
    stream: record.stream,
    message: record.message,
  };
  if (isReplayDuplicate(value)) return;
  if (value.sourceTimestamp) {
    if (value.sourceTimestamp !== state.lastTimestamp) {
      state.lastTimestamp = value.sourceTimestamp;
      state.boundaryCounts = new Map();
    }
    const key = replayKey(value);
    state.boundaryCounts.set(key, (state.boundaryCounts.get(key) || 0) + 1);
  }
  appendLine(value);
}

function flushPendingLogs() {
  for (const record of state.assembler.flush()) appendAssembledRecord(record);
}

function prepareReplayGuard() {
  if (!state.lastTimestamp) {
    state.replayGuard = null;
    return;
  }
  state.replayGuard = {
    timestamp: state.lastTimestamp,
    counts: new Map(state.boundaryCounts),
  };
}

function isReplayDuplicate(record) {
  const guard = state.replayGuard;
  if (!guard || !record.sourceTimestamp) return false;
  if (record.sourceTimestamp !== guard.timestamp) {
    state.replayGuard = null;
    return false;
  }
  const key = replayKey(record);
  const remaining = guard.counts.get(key) || 0;
  if (!remaining) return false;
  if (remaining === 1) guard.counts.delete(key);
  else guard.counts.set(key, remaining - 1);
  return true;
}

function replayKey(record) {
  return `${record.container}\u0000${record.stream}\u0000${record.message}`;
}

function appendLine(record) {
  if (
    !(
      typeof record.time === "string" &&
      /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}$/.test(record.time)
    )
  ) {
    record.time = formatLogTimestamp(new Date(record.time));
  }
  state.logs.push(record);
  if (state.logs.length > MAX_LOG_RECORDS) state.logs.shift();
  const row = createLogRow(record);
  const output = $("#log-output");
  const pinned =
    output.scrollHeight - output.scrollTop - output.clientHeight < 70;
  output.append(row);
  while (output.children.length > MAX_LOG_RECORDS)
    output.firstElementChild.remove();
  applyLogFilterToRow(row);
  if (pinned) output.scrollTop = output.scrollHeight;
  else state.unseenLogs += 1;
  renderJumpToLatest();
  updateLineCount();
}

function createLogRow(record) {
  const row = document.createElement("div");
  row.className = `log-row ${record.stream}${record.system ? " system" : ""}`;
  row.dataset.search =
    `${record.container} ${record.stream} ${record.message}`.toLowerCase();
  for (const [className, value] of [
    ["log-time", record.time],
    ["log-container", record.container],
    ["log-stream", record.stream],
    ["log-message", record.message],
  ]) {
    const span = document.createElement("span");
    span.className = className;
    span.textContent = value;
    row.append(span);
  }
  return row;
}

function applyLogFilterToRow(row) {
  const query = $("#log-search").value.trim().toLowerCase();
  const hideStderr =
    !$("#stderr-toggle").checked && row.classList.contains("stderr");
  row.hidden = hideStderr || (query && !row.dataset.search.includes(query));
}

function filterLogs() {
  $$("#log-output .log-row").forEach(applyLogFilterToRow);
  updateLineCount();
}

function updateLineCount() {
  const count = $$("#log-output .log-row:not([hidden])").length;
  $("#visible-lines").textContent =
    `${count.toLocaleString()} / ${state.logs.length.toLocaleString()} lines`;
}

function renderJumpToLatest() {
  const button = $("#jump-latest");
  button.hidden = state.unseenLogs === 0;
  button.textContent = state.unseenLogs
    ? `새 로그 ${state.unseenLogs.toLocaleString()}개 · 최신으로`
    : "새 로그 · 최신으로";
}

function setStreamState(kind, text) {
  const value = $("#stream-state");
  value.className = `stream-state ${kind}`;
  value.lastChild.textContent = ` ${text}`;
}

function renderLiveButton() {
  const button = $("#live-button");
  const target = selectedContainer();
  const available = target?.state === "running";
  const active = available && state.liveEnabled;
  button.disabled = !available;
  button.classList.toggle("active", active);
  button.setAttribute("aria-pressed", String(active));
  button.title = !available
    ? "중지된 컨테이너는 실시간 로그를 지원하지 않습니다"
    : active
      ? "실시간 로그 끄기"
      : "실시간 로그 켜기";
  button.querySelector("span").textContent = active ? "●" : "○";
  button.querySelector("em").textContent = !available
    ? "실시간 불가"
    : active
      ? "실시간 ON"
      : "실시간 OFF";
}

function clearLogs({ resetCursor = false } = {}) {
  state.logs = [];
  state.assembler.reset();
  state.replayGuard = null;
  state.unseenLogs = 0;
  if (resetCursor) {
    state.lastTimestamp = "";
    state.boundaryCounts = new Map();
  }
  $("#log-output").replaceChildren();
  renderJumpToLatest();
  updateLineCount();
}

function restartLogStream() {
  renderTarget();
  streamLogs({ reset: true });
}

function stopLogStream({ flush = false } = {}) {
  clearTimeout(state.reconnectTimer);
  state.reconnectTimer = null;
  if (flush) flushPendingLogs();
  else state.assembler.reset();
  if (state.controller) state.controller.abort();
  state.controller = null;
  state.following = false;
  state.streamToken += 1;
}

async function streamLogs({ reset = false, resume = false } = {}) {
  state.streamInitialized = true;
  clearTimeout(state.reconnectTimer);
  if (state.controller) state.controller.abort();
  const token = ++state.streamToken;
  state.controller = null;
  state.assembler.reset();
  state.streamHadError = false;
  if (reset) {
    clearLogs({ resetCursor: true });
    state.reconnectAttempt = 0;
  }
  if (resume) prepareReplayGuard();
  if (!state.selected) {
    setStreamState("", "대상 없음");
    return;
  }
  const target = selectedContainer();
  if (!target) {
    setStreamState("", "대상 없음");
    return;
  }
  const targetKey = state.selected;
  const targetID = target.id;
  const controller = new AbortController();
  state.controller = controller;
  setStreamState("", "연결 중");
  const params = new URLSearchParams({
    agent: target.agent_id,
    container: targetID,
    tail: resume && state.lastTimestamp ? "all" : $("#tail-select").value,
    follow: "false",
  });
  const running = target.state === "running";
  state.following = Boolean(running && state.liveEnabled);
  params.set("follow", String(state.following));
  if (resume && state.lastTimestamp) params.set("since", state.lastTimestamp);
  else if ($("#since-select").value)
    params.set("since", $("#since-select").value);
  try {
    const response = await fetch(`/api/logs?${params}`, {
      signal: controller.signal,
      headers: { Accept: "application/x-ndjson" },
    });
    if (!response.ok) {
      let message = `${response.status} ${response.statusText}`;
      try {
        message = (await response.json()).message || message;
      } catch (_) {}
      const error = new Error(message);
      error.retryable = response.status >= 500;
      throw error;
    }
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split("\n");
      buffer = lines.pop() || "";
      for (const line of lines)
        if (line.trim() && token === state.streamToken)
          appendEvent(JSON.parse(line));
    }
    if (buffer.trim() && token === state.streamToken)
      appendEvent(JSON.parse(buffer));
    if (
      !controller.signal.aborted &&
      token === state.streamToken &&
      !state.streamHadError
    ) {
      flushPendingLogs();
      setStreamState(
        "",
        running && !state.liveEnabled ? "실시간 OFF" : "과거 로그",
      );
    }
  } catch (error) {
    if (error.name === "AbortError" || token !== state.streamToken) return;
    state.assembler.reset();
    const canRetry =
      error.retryable !== false &&
      state.liveEnabled &&
      state.currentView === "logs" &&
      state.selected === targetKey &&
      selectedContainer()?.state === "running";
    if (canRetry) {
      state.reconnectAttempt += 1;
      const delay = Math.min(30000, 2000 * 2 ** (state.reconnectAttempt - 1));
      setStreamState("error", `${Math.round(delay / 1000)}초 후 재연결`);
      if (state.reconnectAttempt === 1)
        showToast(`로그 연결이 끊겼습니다: ${error.message}`);
      state.reconnectTimer = setTimeout(
        () => streamLogs({ resume: true }),
        delay,
      );
    } else {
      setStreamState("error", "연결 오류");
      showToast(`로그 스트림: ${error.message}`);
    }
  } finally {
    if (token === state.streamToken && state.controller === controller)
      state.controller = null;
  }
}

function downloadLogs() {
  if (!state.logs.length) {
    showToast("저장할 로그가 없습니다.");
    return;
  }
  const content = state.logs
    .map(
      (item) =>
        `${item.time} [${item.container}] [${item.stream}] ${item.message}`,
    )
    .join("\n");
  const blob = new Blob([content], { type: "text/plain;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  const target = selectedContainer();
  const safeName = (target?.name || "container").replaceAll(
    /[^a-zA-Z0-9_.-]/g,
    "_",
  );
  anchor.download = `${safeName}-logs-${new Date().toISOString().replaceAll(":", "-")}.log`;
  anchor.click();
  URL.revokeObjectURL(url);
}

function showView(name) {
  const previous = state.currentView;
  state.currentView = name;
  $$(".nav-item").forEach((item) =>
    item.classList.toggle("active", item.dataset.view === name),
  );
  $$(".view").forEach((item) =>
    item.classList.toggle("active", item.id === `${name}-view`),
  );
  location.hash = name;
  if (name === "images" && previous === "logs") {
    stopLogStream({ flush: true });
  }
  if (name === "logs" && previous !== "logs" && state.selected) {
    streamLogs({ reset: !state.logs.length, resume: state.logs.length > 0 });
  }
}

function bindEvents() {
  $("#container-list").addEventListener("click", (event) => {
    const button = event.target.closest(".container-item");
    if (!button || !button.dataset.key) return;
    if (state.selected === button.dataset.key) {
      $(".container-panel").classList.remove("open");
      return;
    }
    state.selected = button.dataset.key;
    renderContainers();
    $(".container-panel").classList.remove("open");
    restartLogStream();
  });
  $("#container-search").addEventListener("input", renderContainers);
  $("#image-search").addEventListener("input", renderImages);
  $("#log-search").addEventListener("input", () => {
    cancelAnimationFrame(filterLogs.frame);
    filterLogs.frame = requestAnimationFrame(filterLogs);
  });
  $("#stderr-toggle").addEventListener("change", filterLogs);
  $("#wrap-toggle").addEventListener("change", (event) =>
    $("#log-output").classList.toggle("wrap", event.target.checked),
  );
  $("#tail-select").addEventListener("change", restartLogStream);
  $("#since-select").addEventListener("change", restartLogStream);
  $("#clear-button").addEventListener("click", clearLogs);
  $("#download-button").addEventListener("click", downloadLogs);
  $("#refresh-button").addEventListener("click", () => loadInventory());
  $("#image-refresh-button").addEventListener("click", () => loadInventory());
  $$(".refresh-interval").forEach((select) =>
    select.addEventListener("change", (event) =>
      setRefreshInterval(Number(event.target.value)),
    ),
  );
  $("#live-button").addEventListener("click", () => {
    state.liveEnabled = !state.liveEnabled;
    renderLiveButton();
    if (!state.liveEnabled) {
      stopLogStream({ flush: true });
      setStreamState("", "실시간 OFF");
      return;
    }
    streamLogs({ resume: true });
  });
  $("#jump-latest").addEventListener("click", () => {
    const output = $("#log-output");
    output.scrollTop = output.scrollHeight;
    state.unseenLogs = 0;
    renderJumpToLatest();
  });
  $("#log-output").addEventListener(
    "scroll",
    () => {
      const output = $("#log-output");
      if (output.scrollHeight - output.scrollTop - output.clientHeight < 70) {
        state.unseenLogs = 0;
        renderJumpToLatest();
      }
    },
    { passive: true },
  );
  $("#collapse-list").addEventListener("click", () => {
    if (matchMedia("(max-width: 720px)").matches)
      $(".container-panel").classList.remove("open");
    else $(".workspace").classList.toggle("collapsed");
  });
  $("#open-list").addEventListener("click", () => {
    if (matchMedia("(max-width: 720px)").matches)
      $(".container-panel").classList.add("open");
    else $(".workspace").classList.remove("collapsed");
  });
  $$(".nav-item").forEach((item) =>
    item.addEventListener("click", () => showView(item.dataset.view)),
  );
  document.addEventListener("keydown", (event) => {
    if (
      event.target.matches("input, select, textarea") ||
      event.ctrlKey ||
      event.metaKey ||
      event.altKey
    )
      return;
    if (event.key.toLowerCase() === "l") showView("logs");
    if (event.key.toLowerCase() === "i") showView("images");
  });
}

async function init() {
  bindEvents();
  showView(location.hash === "#logs" ? "logs" : "images");
  await loadInventory();
  if (state.currentView === "logs" && !state.streamInitialized) streamLogs();
  setRefreshInterval(storedRefreshSeconds());
}

init();
