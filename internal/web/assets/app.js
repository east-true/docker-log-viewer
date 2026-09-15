import { LogAssembler } from "./log-buffer.mjs";

const MAX_LOG_RECORDS = 10000;

const state = {
  containers: [],
  images: [],
  selected: null,
  logs: [],
  assembler: new LogAssembler(),
  liveEnabled: true,
  following: false,
  controller: null,
  reconnectTimer: null,
  inventoryTimer: null,
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
  const response = await fetch(path, { headers: { Accept: "application/json" } });
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

async function loadInventory({ quiet = false } = {}) {
  try {
    const [containers, images] = await Promise.all([fetchJSON("/api/containers"), fetchJSON("/api/images")]);
    const previousTarget = state.containers.find((item) => item.id === state.selected);
    const previousSelection = state.selected;
    state.containers = containers;
    state.images = images;
    if (!state.selected || !containers.some((item) => item.id === state.selected)) {
      state.selected = containers[0]?.id || null;
    }
    const currentTarget = containers.find((item) => item.id === state.selected);
    const selectionChanged = previousSelection !== state.selected;
    const runtimeChanged = previousTarget && currentTarget && previousTarget.state !== currentTarget.state;
    if (state.currentView === "logs" && state.streamInitialized && selectionChanged) restartLogStream();
    else if (state.currentView === "logs" && state.streamInitialized && runtimeChanged) streamLogs({ resume: true });
    renderContainers();
    renderImages();
    renderTarget();
    setEngineStatus(true, `${containers.length} containers`);
  } catch (error) {
    setEngineStatus(false, "연결할 수 없음");
    if (!quiet) showToast(`Docker Engine: ${error.message}`);
  }
}

function renderContainers() {
  const root = $("#container-list");
  const query = $("#container-search").value.trim().toLowerCase();
  root.replaceChildren();
  $("#container-count").textContent = state.containers.length;

  const filtered = state.containers.filter((item) => {
    const text = `${item.name} ${item.image} ${item.short_id} ${item.state}`.toLowerCase();
    return text.includes(query);
  });
  for (const item of filtered) {
    const button = document.createElement("button");
    button.className = `container-item${state.selected === item.id ? " selected" : ""}`;
    button.dataset.id = item.id;
    button.setAttribute("role", "option");
    button.setAttribute("aria-selected", state.selected === item.id);
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
    root.append(button);
  }
  if (!root.children.length) {
    const empty = document.createElement("p");
    empty.className = "empty-state";
    empty.textContent = "검색 결과가 없습니다.";
    root.append(empty);
  }
}

function renderTarget() {
  const target = state.containers.find((item) => item.id === state.selected);
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
  meta.textContent = `${target.image} · ${target.status}`;
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
  return new Intl.DateTimeFormat("ko-KR", { year: "numeric", month: "short", day: "numeric" }).format(new Date(timestamp * 1000));
}

function renderImages() {
  const root = $("#image-table-body");
  const query = $("#image-search").value.trim().toLowerCase();
  const images = state.images.filter((item) => `${item.tags.join(" ")} ${item.short_id}`.toLowerCase().includes(query));
  root.replaceChildren();
  $("#images-total").textContent = state.images.length;
  $("#images-used").textContent = state.images.filter((item) => item.in_use).length;
  $("#images-running").textContent = state.images.filter((item) => item.running_count > 0).length;
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
    const idCell = document.createElement("td");
    idCell.className = "mono";
    idCell.textContent = item.short_id;
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
    row.append(imageCell, idCell, sizeCell, ageCell, usageCell);
    root.append(row);
  }
  if (!images.length) {
    const row = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = 5;
    cell.className = "empty-state";
    cell.textContent = state.images.length ? "검색 결과가 없습니다." : "로컬 이미지가 없습니다.";
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
    setStreamState(state.following ? "live" : "", state.following ? "실시간 ON" : "로그 불러오는 중");
    return;
  }
  if (event.type === "error") {
    flushPendingLogs();
    state.streamHadError = true;
    state.following = false;
    appendLine({ time: event.observed_at, container: event.container_name || "system", stream: "error", message: event.message, system: true });
    setStreamState("error", "로그를 읽을 수 없음");
    return;
  }
  if (event.type === "end") {
    flushPendingLogs();
    if (state.streamHadError) return;
    const running = state.containers.find((item) => item.id === state.selected)?.state === "running";
    setStreamState("", running && !state.liveEnabled ? "실시간 OFF" : "과거 로그");
    return;
  }
  if (event.type !== "log") return;
  state.reconnectAttempt = 0;
  for (const record of state.assembler.push(event)) appendAssembledRecord(record);
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
  state.replayGuard = { timestamp: state.lastTimestamp, counts: new Map(state.boundaryCounts) };
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
  if (!(typeof record.time === "string" && /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}$/.test(record.time))) {
    record.time = formatLogTimestamp(new Date(record.time));
  }
  state.logs.push(record);
  if (state.logs.length > MAX_LOG_RECORDS) state.logs.shift();
  const row = createLogRow(record);
  const output = $("#log-output");
  const pinned = output.scrollHeight - output.scrollTop - output.clientHeight < 70;
  output.append(row);
  while (output.children.length > MAX_LOG_RECORDS) output.firstElementChild.remove();
  applyLogFilterToRow(row);
  if (pinned) output.scrollTop = output.scrollHeight;
  else state.unseenLogs += 1;
  renderJumpToLatest();
  updateLineCount();
}

function createLogRow(record) {
  const row = document.createElement("div");
  row.className = `log-row ${record.stream}${record.system ? " system" : ""}`;
  row.dataset.search = `${record.container} ${record.stream} ${record.message}`.toLowerCase();
  for (const [className, value] of [
    ["log-time", record.time], ["log-container", record.container], ["log-stream", record.stream], ["log-message", record.message],
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
  const hideStderr = !$("#stderr-toggle").checked && row.classList.contains("stderr");
  row.hidden = hideStderr || (query && !row.dataset.search.includes(query));
}

function filterLogs() {
  $$("#log-output .log-row").forEach(applyLogFilterToRow);
  updateLineCount();
}

function updateLineCount() {
  const count = $$("#log-output .log-row:not([hidden])").length;
  $("#visible-lines").textContent = `${count.toLocaleString()} / ${state.logs.length.toLocaleString()} lines`;
}

function renderJumpToLatest() {
  const button = $("#jump-latest");
  button.hidden = state.unseenLogs === 0;
  button.textContent = state.unseenLogs ? `새 로그 ${state.unseenLogs.toLocaleString()}개 · 최신으로` : "새 로그 · 최신으로";
}

function setStreamState(kind, text) {
  const value = $("#stream-state");
  value.className = `stream-state ${kind}`;
  value.lastChild.textContent = ` ${text}`;
}

function renderLiveButton() {
  const button = $("#live-button");
  const target = state.containers.find((item) => item.id === state.selected);
  const available = target?.state === "running";
  const active = available && state.liveEnabled;
  button.disabled = !available;
  button.classList.toggle("active", active);
  button.setAttribute("aria-pressed", String(active));
  button.title = !available ? "중지된 컨테이너는 실시간 로그를 지원하지 않습니다" : active ? "실시간 로그 끄기" : "실시간 로그 켜기";
  button.querySelector("span").textContent = active ? "●" : "○";
  button.querySelector("em").textContent = !available ? "실시간 불가" : active ? "실시간 ON" : "실시간 OFF";
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
  const targetID = state.selected;
  const controller = new AbortController();
  state.controller = controller;
  setStreamState("", "연결 중");
  const params = new URLSearchParams({
    container: targetID,
    tail: resume && state.lastTimestamp ? "all" : $("#tail-select").value,
    follow: "false",
  });
  const running = state.containers.find((item) => item.id === targetID)?.state === "running";
  state.following = Boolean(running && state.liveEnabled);
  params.set("follow", String(state.following));
  if (resume && state.lastTimestamp) params.set("since", state.lastTimestamp);
  else if ($("#since-select").value) params.set("since", $("#since-select").value);
  try {
    const response = await fetch(`/api/logs?${params}`, { signal: controller.signal, headers: { Accept: "application/x-ndjson" } });
    if (!response.ok) {
      let message = `${response.status} ${response.statusText}`;
      try { message = (await response.json()).message || message; } catch (_) {}
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
      for (const line of lines) if (line.trim() && token === state.streamToken) appendEvent(JSON.parse(line));
    }
    if (buffer.trim() && token === state.streamToken) appendEvent(JSON.parse(buffer));
    if (!controller.signal.aborted && token === state.streamToken && !state.streamHadError) {
      flushPendingLogs();
      setStreamState("", running && !state.liveEnabled ? "실시간 OFF" : "과거 로그");
    }
  } catch (error) {
    if (error.name === "AbortError" || token !== state.streamToken) return;
    state.assembler.reset();
    const canRetry = error.retryable !== false && state.liveEnabled && state.currentView === "logs" && state.selected === targetID && state.containers.find((item) => item.id === targetID)?.state === "running";
    if (canRetry) {
      state.reconnectAttempt += 1;
      const delay = Math.min(30000, 2000 * 2 ** (state.reconnectAttempt - 1));
      setStreamState("error", `${Math.round(delay / 1000)}초 후 재연결`);
      if (state.reconnectAttempt === 1) showToast(`로그 연결이 끊겼습니다: ${error.message}`);
      state.reconnectTimer = setTimeout(() => streamLogs({ resume: true }), delay);
    } else {
      setStreamState("error", "연결 오류");
      showToast(`로그 스트림: ${error.message}`);
    }
  } finally {
    if (token === state.streamToken && state.controller === controller) state.controller = null;
  }
}

function downloadLogs() {
  if (!state.logs.length) {
    showToast("저장할 로그가 없습니다.");
    return;
  }
  const content = state.logs.map((item) => `${item.time} [${item.container}] [${item.stream}] ${item.message}`).join("\n");
  const blob = new Blob([content], { type: "text/plain;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  const target = state.containers.find((item) => item.id === state.selected);
  const safeName = (target?.name || "container").replaceAll(/[^a-zA-Z0-9_.-]/g, "_");
  anchor.download = `${safeName}-logs-${new Date().toISOString().replaceAll(":", "-")}.log`;
  anchor.click();
  URL.revokeObjectURL(url);
}

function showView(name) {
  const previous = state.currentView;
  state.currentView = name;
  $$(".nav-item").forEach((item) => item.classList.toggle("active", item.dataset.view === name));
  $$(".view").forEach((item) => item.classList.toggle("active", item.id === `${name}-view`));
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
    if (!button || !button.dataset.id) return;
    if (state.selected === button.dataset.id) {
      $(".container-panel").classList.remove("open");
      return;
    }
    state.selected = button.dataset.id;
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
  $("#wrap-toggle").addEventListener("change", (event) => $("#log-output").classList.toggle("wrap", event.target.checked));
  $("#tail-select").addEventListener("change", restartLogStream);
  $("#since-select").addEventListener("change", restartLogStream);
  $("#clear-button").addEventListener("click", clearLogs);
  $("#download-button").addEventListener("click", downloadLogs);
  $("#refresh-button").addEventListener("click", () => loadInventory());
  $("#image-refresh-button").addEventListener("click", () => loadInventory());
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
  $("#log-output").addEventListener("scroll", () => {
    const output = $("#log-output");
    if (output.scrollHeight - output.scrollTop - output.clientHeight < 70) {
      state.unseenLogs = 0;
      renderJumpToLatest();
    }
  }, { passive: true });
  $("#collapse-list").addEventListener("click", () => {
    if (matchMedia("(max-width: 720px)").matches) $(".container-panel").classList.remove("open");
    else $(".workspace").classList.toggle("collapsed");
  });
  $("#open-list").addEventListener("click", () => {
    if (matchMedia("(max-width: 720px)").matches) $(".container-panel").classList.add("open");
    else $(".workspace").classList.remove("collapsed");
  });
  $$(".nav-item").forEach((item) => item.addEventListener("click", () => showView(item.dataset.view)));
  document.addEventListener("keydown", (event) => {
    if (event.target.matches("input, select, textarea") || event.ctrlKey || event.metaKey || event.altKey) return;
    if (event.key.toLowerCase() === "l") showView("logs");
    if (event.key.toLowerCase() === "i") showView("images");
  });
}

async function init() {
  bindEvents();
  showView(location.hash === "#logs" ? "logs" : "images");
  await loadInventory();
  if (state.currentView === "logs" && !state.streamInitialized) streamLogs();
  state.inventoryTimer = setInterval(() => loadInventory({ quiet: true }), 5000);
}

init();
