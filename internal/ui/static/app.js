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

function setBusy(busy) {
  exportButton.disabled = busy;
  exportButton.textContent = busy ? "正在导出…" : "导出变化";
}

function showError(error) {
  message.className = "empty error";
  message.textContent = error.message || String(error);
}

function renderChanges(payload) {
  changes.replaceChildren();
  warnings.replaceChildren();
  const items = payload.result.changes || [];
  resultCount.textContent = String(items.length);
  if (items.length === 0) {
    message.className = "empty success";
    message.textContent = "没有变化，导出目录已经是最新状态。";
  } else {
    message.className = "hidden";
    for (const item of items) {
      const card = document.createElement("article");
      card.className = "change-card";

      const badge = document.createElement("span");
      badge.className = `badge ${item.kind}`;
      badge.textContent = { new: "新增", updated: "更新", renamed: "重命名" }[item.kind] || item.kind;

      const title = document.createElement("h3");
      title.textContent = item.title;

      const repo = document.createElement("p");
      repo.className = "repo";
      repo.textContent = item.repository_name;

      const path = document.createElement("p");
      path.className = "path";
      path.textContent = item.path;

      card.append(badge, title, repo, path);
      changes.append(card);
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
  } else {
    warnings.classList.add("hidden");
  }
}

document.querySelector("#save").addEventListener("click", async () => {
  saveStatus.textContent = "保存中…";
  try {
    const state = await api("/api/config", {
      method: "PUT",
      body: JSON.stringify({ directory: directory.value }),
    });
    directory.value = state.directory;
    saveStatus.textContent = "已保存";
  } catch (error) {
    saveStatus.textContent = error.message;
  }
});

document.querySelector("#browse").addEventListener("click", async () => {
  saveStatus.textContent = "等待选择…";
  try {
    const state = await api("/api/pick-directory", { method: "POST", body: "{}" });
    directory.value = state.directory;
    saveStatus.textContent = "点击保存以记住目录";
  } catch (error) {
    saveStatus.textContent = error.message;
  }
});

exportButton.addEventListener("click", async () => {
  setBusy(true);
  message.className = "empty";
  message.textContent = "正在读取 Codex sessions…";
  try {
    const payload = await api("/api/export", {
      method: "POST",
      body: JSON.stringify({ directory: directory.value, scope: document.querySelector("#scope").value }),
    });
    renderChanges(payload);
    saveStatus.textContent = "目录已保存";
  } catch (error) {
    showError(error);
  } finally {
    setBusy(false);
  }
});

api("/api/state")
  .then((state) => {
    directory.value = state.directory || "";
    connection.textContent = "本地连接";
    connection.classList.add("ready");
  })
  .catch((error) => {
    connection.textContent = "连接失败";
    showError(error);
  });
