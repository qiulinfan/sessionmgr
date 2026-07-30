import type { Dashboard } from "./types";

export const previewDashboard: Dashboard = {
  schema_version: 1,
  preview: true,
  version: "0.2.0-dev",
  home: "/tmp/sessionmgr-acceptance",
  health: [
    { id: "home", label: "Session home", status: "ready", detail: "Acceptance sandbox" },
    { id: "git", label: "Git", status: "ready", detail: "2.52.0" },
    { id: "codex", label: "Codex", status: "ready", detail: "3 fixture sessions" },
    { id: "encryption", label: "Encryption", status: "ready", detail: "age1…8x4k" },
  ],
  stats: { runs: 12, verified: 10, needs_attention: 2, stores: 2 },
  recent_runs: [
    {
      id: "019fb197-fa7d-7aa1-ae70-43e8e9434c0d",
      title: "Implement parser recovery",
      repository: "sessionmgr",
      agent: "codex",
      created_at: "Today · 14:32",
      integrity: "verified",
      sync_status: "personal-ssh",
      relation: "capture",
    },
    {
      id: "019fb0c1-5d42-70e8-8a2f-19e5c7cc4d31",
      title: "Fix binary patch restore",
      repository: "sessionmgr",
      agent: "codex",
      created_at: "Today · 11:08",
      integrity: "verified",
      sync_status: "local",
      relation: "revision",
    },
    {
      id: "019fae91-940c-74f3-925c-10c88748f429",
      title: "Investigate SSH retry",
      repository: "capsule-lab",
      agent: "codex",
      created_at: "Yesterday · 18:47",
      integrity: "warning",
      sync_status: "not pushed",
      relation: "fork",
    },
  ],
  stores: [
    { name: "local", type: "file", status: "ready", detail: "12 Runs" },
    { name: "personal-ssh", type: "ssh", status: "ready", detail: "Encrypted · 8 Runs" },
  ],
};
