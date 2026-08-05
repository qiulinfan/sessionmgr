const tokenFromURL = window.location.hash.replace(/^#/, "");
if (tokenFromURL) sessionStorage.setItem("sessionmgr-token", tokenFromURL);
const token = tokenFromURL || sessionStorage.getItem("sessionmgr-token") || "";

const directory = document.querySelector("#directory");
const connection = document.querySelector("#connection");
const saveStatus = document.querySelector("#save-status");
const message = document.querySelector("#message");
const warnings = document.querySelector("#warnings");
const changes = document.querySelector("#changes");
const resultCount = document.querySelector("#result-count");
const exportButton = document.querySelector("#export");
const languageSelect = document.querySelector("#language");

const translations = {
  en: {
    language: "Language",
    connecting: "Connecting",
    connected: "Local connection",
    connectionFailed: "Connection failed",
    heroTitle: "Keep only what changed.",
    heroLede: "Choose a persistent directory and export local Codex sessions as Markdown, grouped by Git remote.",
    exportDirectory: "Export directory",
    directoryPlaceholder: "For example, /Users/me/Documents/session-archive",
    browse: "Browse",
    save: "Save",
    directoryHint: "This directory is saved in Session Manager's local configuration and restored the next time it starts.",
    exportSessions: "Export sessions",
    exportScope: "Export scope",
    allRepositories: "All Git repositories",
    currentRepository: "Current Git repository",
    exportChanges: "Export changes",
    exporting: "Exporting…",
    exportedChanges: "Changes from this export",
    notExported: "No export has been run yet.",
    readingSessions: "Reading Codex sessions…",
    noChanges: "No changes. The export directory is up to date.",
    busySessions: "No exported changes; {count} active session(s) will be retried next time.",
    filteredSessions: "No exported changes; {count} internal session(s) were excluded.",
    saving: "Saving…",
    saved: "Saved",
    waitingForDirectory: "Waiting for a directory…",
    saveDirectoryHint: "Select Save to remember this directory",
    directorySaved: "Directory saved",
    changeCount: "{count} change(s)",
    sessionCount: "{count} session(s)",
    badgeNew: "New",
    badgeUpdated: "Updated",
    badgeRenamed: "Renamed",
    attachmentSummary: "{attachments} attachment(s) · {archived} copied",
  },
  zh: {
    language: "语言",
    connecting: "正在连接",
    connected: "本地连接",
    connectionFailed: "连接失败",
    heroTitle: "只保存这次发生的变化。",
    heroLede: "选择一个持久目录，将本机 Codex sessions 按 Git 远程仓库导出为 Markdown。",
    exportDirectory: "导出目录",
    directoryPlaceholder: "例如 /Users/me/Documents/session-archive",
    browse: "浏览",
    save: "保存",
    directoryHint: "目录会保存在当前系统的 Session Manager 配置中，下次启动自动恢复。",
    exportSessions: "导出 sessions",
    exportScope: "导出范围",
    allRepositories: "全部 Git 仓库",
    currentRepository: "当前 Git 仓库",
    exportChanges: "导出变化",
    exporting: "正在导出…",
    exportedChanges: "本次导出变化",
    notExported: "尚未执行导出。",
    readingSessions: "正在读取 Codex sessions…",
    noChanges: "没有变化，导出目录已经是最新状态。",
    busySessions: "没有导出变化；{count} 个正在写入的 session 已留到下次。",
    filteredSessions: "没有导出变化；已排除 {count} 个内部 session。",
    saving: "保存中…",
    saved: "已保存",
    waitingForDirectory: "等待选择…",
    saveDirectoryHint: "点击保存以记住目录",
    directorySaved: "目录已保存",
    changeCount: "{count} 项变化",
    sessionCount: "{count} 个 session",
    badgeNew: "新增",
    badgeUpdated: "更新",
    badgeRenamed: "重命名",
    attachmentSummary: "附件 {attachments} · 已复制 {archived}",
  },
};

let language = loadLanguage();
let connectionState = "connecting";
let saveState = { kind: "none", value: "" };
let resultState = { kind: "idle", value: null };
let exportBusy = false;

function loadLanguage() {
  try {
    return localStorage.getItem("sessionmgr-language") === "zh" ? "zh" : "en";
  } catch (_) {
    return "en";
  }
}

function t(key, values = {}) {
  let value = translations[language][key] || translations.en[key] || key;
  for (const [name, replacement] of Object.entries(values)) {
    value = value.replaceAll(`{${name}}`, String(replacement));
  }
  return value;
}

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: {
      "Content-Type": "application/json",
      "X-Sessionmgr-Token": token,
      ...(options.headers || {}),
    },
  });
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
  return data;
}

function applyLanguage() {
  document.documentElement.lang = language === "zh" ? "zh-CN" : "en";
  languageSelect.value = language;
  for (const element of document.querySelectorAll("[data-i18n]")) {
    element.textContent = t(element.dataset.i18n);
  }
  for (const element of document.querySelectorAll("[data-i18n-placeholder]")) {
    element.placeholder = t(element.dataset.i18nPlaceholder);
  }
  for (const element of document.querySelectorAll("[data-i18n-aria-label]")) {
    element.setAttribute("aria-label", t(element.dataset.i18nAriaLabel));
  }
  renderConnection();
  renderSaveStatus();
  renderBusy();
  renderResult();
}

function renderConnection() {
  connection.textContent = t({ connecting: "connecting", connected: "connected", failed: "connectionFailed" }[connectionState]);
  connection.classList.toggle("ready", connectionState === "connected");
}

function setSaveState(kind, value = "") {
  saveState = { kind, value };
  renderSaveStatus();
}

function renderSaveStatus() {
  saveStatus.textContent = saveState.kind === "key" ? t(saveState.value) : saveState.kind === "error" ? saveState.value : "";
}

function setBusy(busy) {
  exportBusy = busy;
  renderBusy();
}

function renderBusy() {
  exportButton.disabled = exportBusy;
  exportButton.textContent = t(exportBusy ? "exporting" : "exportChanges");
}

function showError(error) {
  resultState = { kind: "error", value: error.message || String(error) };
  renderResult();
}

function renderResult() {
  changes.replaceChildren();
  warnings.replaceChildren();
  warnings.classList.add("hidden");
  if (resultState.kind === "idle") {
    resultCount.textContent = "—";
    message.className = "empty";
    message.textContent = t("notExported");
    return;
  }
  if (resultState.kind === "loading") {
    resultCount.textContent = "—";
    message.className = "empty";
    message.textContent = t("readingSessions");
    return;
  }
  if (resultState.kind === "error") {
    resultCount.textContent = "—";
    message.className = "empty error";
    message.textContent = resultState.value;
    return;
  }
  renderChanges(resultState.value);
}

function relativePathParts(path, output) {
  const normalizedPath = String(path || "").replaceAll("\\", "/").replace(/\/+$/, "");
  const normalizedOutput = String(output || "").replaceAll("\\", "/").replace(/\/+$/, "");
  const comparablePath = normalizedPath.toLowerCase();
  const comparableOutput = normalizedOutput.toLowerCase();
  const prefix = `${comparableOutput}/`;
  const relative = comparablePath.startsWith(prefix) ? normalizedPath.slice(normalizedOutput.length + 1) : normalizedPath;
  return relative.split("/").filter(Boolean);
}

function groupChanges(items, output) {
  const repositories = new Map();
  for (const item of items) {
    const parts = relativePathParts(item.path, output);
    const repositoryLabel = parts.length >= 2 ? `${parts[0]}/${parts[1]}` : item.repository_name;
    const repositoryKey = item.repository_key || repositoryLabel;
    if (!repositories.has(repositoryKey)) {
      repositories.set(repositoryKey, { label: repositoryLabel, devices: new Map(), count: 0 });
    }
    const repository = repositories.get(repositoryKey);
    const deviceLabel = parts[2] || item.device_name || "device";
    if (!repository.devices.has(deviceLabel)) {
      repository.devices.set(deviceLabel, { label: deviceLabel, items: [] });
    }
    repository.devices.get(deviceLabel).items.push({
      ...item,
      relativeParts: parts,
      sessionFolder: parts.length >= 2 ? parts.at(-2) : item.title,
    });
    repository.count += 1;
  }
  return [...repositories.values()];
}

function treeSummary(label, count, countKey, className) {
  const summary = document.createElement("summary");
  summary.className = `tree-summary ${className}`;
  const folder = document.createElement("span");
  folder.className = "folder-icon";
  folder.setAttribute("aria-hidden", "true");
  const name = document.createElement("span");
  name.className = "tree-name";
  name.textContent = label;
  const tally = document.createElement("span");
  tally.className = "tree-count";
  tally.textContent = t(countKey, { count });
  summary.append(folder, name, tally);
  return summary;
}

function sessionChange(item) {
  const card = document.createElement("article");
  card.className = "session-change";

  const heading = document.createElement("div");
  heading.className = "session-heading";
  const title = document.createElement("h3");
  title.textContent = item.title;
  const badge = document.createElement("span");
  badge.className = `badge ${item.kind}`;
  badge.textContent = t({ new: "badgeNew", updated: "badgeUpdated", renamed: "badgeRenamed" }[item.kind] || item.kind);
  heading.append(title, badge);

  const path = document.createElement("p");
  path.className = "path";
  path.textContent = `${item.sessionFolder}/conversation.md`;
  card.append(heading, path);

  if ((item.attachments || 0) > 0) {
    const attachmentSummary = document.createElement("p");
    attachmentSummary.className = "attachment-summary";
    attachmentSummary.textContent = t("attachmentSummary", {
      attachments: item.attachments,
      archived: item.archived_attachments || 0,
    });
    card.append(attachmentSummary);
  }
  return card;
}

function renderChanges(payload) {
  const items = payload.result.changes || [];
  const busy = payload.result.busy || 0;
  const filtered = payload.result.filtered_internal || 0;
  resultCount.textContent = String(items.length);
  if (items.length === 0) {
    message.className = busy > 0 || filtered > 0 ? "empty busy" : "empty success";
    message.textContent = busy > 0
      ? t("busySessions", { count: busy })
      : filtered > 0
        ? t("filteredSessions", { count: filtered })
        : t("noChanges");
  } else {
    message.className = "hidden";
    for (const repository of groupChanges(items, payload.result.output)) {
      const repositoryTree = document.createElement("details");
      repositoryTree.className = "tree-group repository-tree";
      repositoryTree.open = true;
      repositoryTree.append(treeSummary(repository.label, repository.count, "changeCount", "repository-summary"));

      const repositoryChildren = document.createElement("div");
      repositoryChildren.className = "tree-children repository-children";
      for (const device of repository.devices.values()) {
        const deviceTree = document.createElement("details");
        deviceTree.className = "tree-group device-tree";
        deviceTree.open = true;
        deviceTree.append(treeSummary(device.label, device.items.length, "sessionCount", "device-summary"));

        const sessionList = document.createElement("div");
        sessionList.className = "tree-children session-list";
        for (const item of device.items) sessionList.append(sessionChange(item));
        deviceTree.append(sessionList);
        repositoryChildren.append(deviceTree);
      }
      repositoryTree.append(repositoryChildren);
      changes.append(repositoryTree);
    }
  }

  const warningItems = [...(payload.result.warnings || [])];
  if (payload.error) warningItems.push(payload.error);
  if (warningItems.length) {
    warnings.classList.remove("hidden");
    for (const text of warningItems) {
      const item = document.createElement("p");
      item.textContent = text;
      warnings.append(item);
    }
  }
}

languageSelect.addEventListener("change", () => {
  language = languageSelect.value === "zh" ? "zh" : "en";
  try {
    localStorage.setItem("sessionmgr-language", language);
  } catch (_) {
    // Language persistence is optional when browser storage is unavailable.
  }
  applyLanguage();
});

document.querySelector("#save").addEventListener("click", async () => {
  setSaveState("key", "saving");
  try {
    const state = await api("/api/config", {
      method: "PUT",
      body: JSON.stringify({ directory: directory.value }),
    });
    directory.value = state.directory;
    setSaveState("key", "saved");
  } catch (error) {
    setSaveState("error", error.message || String(error));
  }
});

document.querySelector("#browse").addEventListener("click", async () => {
  setSaveState("key", "waitingForDirectory");
  try {
    const state = await api("/api/pick-directory", { method: "POST", body: "{}" });
    directory.value = state.directory;
    setSaveState("key", "saveDirectoryHint");
  } catch (error) {
    setSaveState("error", error.message || String(error));
  }
});

exportButton.addEventListener("click", async () => {
  setBusy(true);
  resultState = { kind: "loading", value: null };
  renderResult();
  try {
    const payload = await api("/api/export", {
      method: "POST",
      body: JSON.stringify({ directory: directory.value, scope: document.querySelector("#scope").value }),
    });
    resultState = { kind: "payload", value: payload };
    renderResult();
    setSaveState("key", "directorySaved");
  } catch (error) {
    showError(error);
  } finally {
    setBusy(false);
  }
});

applyLanguage();
api("/api/state")
  .then((state) => {
    directory.value = state.directory || "";
    connectionState = "connected";
    renderConnection();
  })
  .catch((error) => {
    connectionState = "failed";
    renderConnection();
    showError(error);
  });
