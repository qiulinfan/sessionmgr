import { useEffect, useMemo, useState } from "react";
import { getDashboard, selectWorkspace } from "./bridge";
import {
  ActivityIcon,
  ArchiveIcon,
  ArrowIcon,
  CheckIcon,
  CloseIcon,
  DocumentIcon,
  FolderIcon,
  GridIcon,
  PlusIcon,
  PullIcon,
  SearchIcon,
  SettingsIcon,
  ShieldIcon,
  StoreIcon,
} from "./icons";
import { previewDashboard } from "./mock";
import type { Dashboard, RunCard } from "./types";

type Page = "Overview" | "Runs" | "Operations" | "Stores" | "Settings";

const navigation = [
  { label: "Overview" as const, icon: GridIcon },
  { label: "Runs" as const, icon: ArchiveIcon, count: 12 },
  { label: "Operations" as const, icon: ActivityIcon, live: true },
  { label: "Stores" as const, icon: StoreIcon },
  { label: "Settings" as const, icon: SettingsIcon },
];

function App() {
  const [dashboard, setDashboard] = useState<Dashboard>(previewDashboard);
  const [page, setPage] = useState<Page>("Overview");
  const [loading, setLoading] = useState(true);
  const [captureOpen, setCaptureOpen] = useState(false);
  const [workspace, setWorkspace] = useState("/Users/demo/Projects/sessionmgr");
  const [query, setQuery] = useState("");
  const [toast, setToast] = useState("");

  useEffect(() => {
    let current = true;
    getDashboard()
      .then((value) => current && setDashboard(value))
      .catch((error: unknown) => {
        const message = error instanceof Error ? error.message : String(error);
        current && setToast(`Could not load local data: ${message}`);
      })
      .finally(() => current && setLoading(false));
    return () => {
      current = false;
    };
  }, []);

  const filteredRuns = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return dashboard.recent_runs;
    return dashboard.recent_runs.filter((run) =>
      [run.title, run.repository, run.agent, run.sync_status]
        .join(" ")
        .toLowerCase()
        .includes(normalized),
    );
  }, [dashboard.recent_runs, query]);

  async function chooseWorkspace() {
    const selected = await selectWorkspace();
    if (selected) setWorkspace(selected);
  }

  function showPrototypeToast(action: string) {
    setToast(`${action} is read-only in this Phase 0 preview.`);
    window.setTimeout(() => setToast(""), 3500);
  }

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-mark">
            <span className="brand-mark-inner">S</span>
          </span>
          <span>
            <strong>Session</strong>
            <small>Manager</small>
          </span>
        </div>

        <nav aria-label="Primary navigation">
          <p className="nav-label">Workspace</p>
          {navigation.map((item) => {
            const Icon = item.icon;
            return (
              <button
                className={`nav-item ${page === item.label ? "active" : ""}`}
                key={item.label}
                onClick={() => setPage(item.label)}
              >
                <Icon />
                <span>{item.label}</span>
                {item.count ? <b>{dashboard.stats.runs}</b> : null}
                {item.live ? <i aria-label="operation active" /> : null}
              </button>
            );
          })}
        </nav>

        <div className="sidebar-bottom">
          <div className="local-card">
            <span className="local-icon"><ShieldIcon /></span>
            <div>
              <strong>Local only</strong>
              <small>Your data stays on this device</small>
            </div>
          </div>
          <div className="version">
            <span>v{dashboard.version}</span>
            <span className="status-dot">MVP Preview</span>
          </div>
        </div>
      </aside>

      <main className="main">
        <header className="topbar">
          <div>
            <p className="eyebrow">SESSION MANAGER</p>
            <h1>{page === "Overview" ? "Workspace continuity, verified." : page}</h1>
          </div>
          <div className="top-actions">
            <label className="search">
              <SearchIcon />
              <input
                aria-label="Search runs"
                placeholder="Search runs"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
              <kbd>⌘ K</kbd>
            </label>
            <button className="button secondary" onClick={() => showPrototypeToast("Pull")}>
              <PullIcon /> Pull
            </button>
            <button className="button primary" onClick={() => setCaptureOpen(true)}>
              <PlusIcon /> Capture run
            </button>
          </div>
        </header>

        <div className="content">
          {dashboard.preview ? (
            <div className="preview-banner">
              <span className="preview-spark">✦</span>
              <div>
                <strong>Acceptance Preview</strong>
                <span>Sample data · No real workspace will be changed</span>
              </div>
              <button onClick={() => showPrototypeToast("Live mode")}>Preview dataset</button>
            </div>
          ) : null}

          {page === "Overview" ? (
            <Overview
              dashboard={dashboard}
              runs={filteredRuns}
              loading={loading}
              onCapture={() => setCaptureOpen(true)}
              onNavigate={setPage}
              onPrototype={showPrototypeToast}
            />
          ) : (
            <PagePlaceholder
              page={page}
              dashboard={dashboard}
              onBack={() => setPage("Overview")}
            />
          )}
        </div>
      </main>

      {captureOpen ? (
        <CapturePanel
          workspace={workspace}
          onChoose={chooseWorkspace}
          onClose={() => setCaptureOpen(false)}
        />
      ) : null}

      {toast ? <div className="toast"><CheckIcon /> {toast}</div> : null}
    </div>
  );
}

interface OverviewProps {
  dashboard: Dashboard;
  runs: RunCard[];
  loading: boolean;
  onCapture(): void;
  onNavigate(page: Page): void;
  onPrototype(action: string): void;
}

function Overview({
  dashboard,
  runs,
  loading,
  onCapture,
  onNavigate,
  onPrototype,
}: OverviewProps) {
  const stats = [
    { label: "Total runs", value: dashboard.stats.runs, delta: "+3 this week", tone: "blue" },
    { label: "Verified", value: dashboard.stats.verified, delta: "83% healthy", tone: "green" },
    { label: "Needs attention", value: dashboard.stats.needs_attention, delta: "Review integrity", tone: "amber" },
    { label: "Connected stores", value: dashboard.stats.stores, delta: "All reachable", tone: "violet" },
  ];

  return (
    <>
      <section className="health-strip" aria-label="System health">
        <div className="section-heading compact">
          <div>
            <span className="section-kicker">SYSTEM HEALTH</span>
            <h2>Ready to preserve context</h2>
          </div>
          <span className="last-check">Checked just now</span>
        </div>
        <div className="health-grid">
          {dashboard.health.map((item) => (
            <div className="health-item" key={item.id} title={item.detail}>
              <span className={`health-check ${item.status}`}><CheckIcon /></span>
              <div>
                <strong>{item.label}</strong>
                <small>{item.detail}</small>
              </div>
            </div>
          ))}
        </div>
      </section>

      <section className="stats-grid" aria-label="Run summary">
        {stats.map((stat) => (
          <article className={`stat-card ${stat.tone}`} key={stat.label}>
            <div className="stat-top">
              <span>{stat.label}</span>
              <span className="stat-glyph">
                {stat.label === "Verified" ? <ShieldIcon /> : stat.label === "Connected stores" ? <StoreIcon /> : <ArchiveIcon />}
              </span>
            </div>
            <strong>{loading ? "—" : stat.value}</strong>
            <small>{stat.delta}</small>
          </article>
        ))}
      </section>

      <div className="dashboard-grid">
        <section className="panel runs-panel">
          <div className="section-heading">
            <div>
              <span className="section-kicker">RECENT ACTIVITY</span>
              <h2>Runs</h2>
            </div>
            <button className="text-button" onClick={() => onNavigate("Runs")}>
              View all <ArrowIcon />
            </button>
          </div>
          <div className="runs-table">
            <div className="run-row run-head">
              <span>RUN</span><span>AGENT</span><span>INTEGRITY</span><span>SYNC</span><span />
            </div>
            {runs.map((run) => <RunRow run={run} key={run.id} />)}
            {runs.length === 0 ? (
              <div className="empty-filter">No Runs match this search.</div>
            ) : null}
          </div>
        </section>

        <aside className="right-column">
          <section className="panel operation-card">
            <div className="operation-title">
              <span className="pulse" />
              <div>
                <span className="section-kicker">ACTIVE OPERATION</span>
                <h3>Restore workspace</h3>
              </div>
              <span className="operation-count">7 / 9</span>
            </div>
            <div className="operation-progress"><span /></div>
            <p>Rebuilding <code>sessionmgr</code> in an isolated worktree</p>
            <ul className="step-list">
              <li className="done"><CheckIcon /><span>Commits</span><small>Restored</small></li>
              <li className="done"><CheckIcon /><span>Staged changes</span><small>Applied</small></li>
              <li className="active"><span className="step-spinner" /><span>Untracked files</span><small>18 / 24</small></li>
              <li><span className="step-dot" /><span>Verify digest</span><small>Waiting</small></li>
            </ul>
            <button className="operation-link" onClick={() => onNavigate("Operations")}>
              Open operation details <ArrowIcon />
            </button>
          </section>

          <section className="panel quick-card">
            <div className="section-heading">
              <div>
                <span className="section-kicker">SHORTCUTS</span>
                <h3>Quick actions</h3>
              </div>
            </div>
            <button onClick={() => onPrototype("Verify latest")}>
              <span className="quick-icon green"><ShieldIcon /></span>
              <span><strong>Verify latest</strong><small>Deep integrity check</small></span>
              <ArrowIcon />
            </button>
            <button onClick={() => onPrototype("Generate handoff")}>
              <span className="quick-icon blue"><DocumentIcon /></span>
              <span><strong>Generate handoff</strong><small>Facts, inference, next steps</small></span>
              <ArrowIcon />
            </button>
            <button onClick={() => onPrototype("Test stores")}>
              <span className="quick-icon violet"><StoreIcon /></span>
              <span><strong>Test stores</strong><small>2 configured endpoints</small></span>
              <ArrowIcon />
            </button>
          </section>
        </aside>
      </div>

      <section className="panel store-strip">
        <div className="section-heading">
          <div>
            <span className="section-kicker">DURABLE COPIES</span>
            <h2>Stores</h2>
          </div>
          <button className="text-button" onClick={() => onNavigate("Stores")}>Manage stores <ArrowIcon /></button>
        </div>
        <div className="store-list">
          {dashboard.stores.map((store) => (
            <article key={store.name}>
              <span className={`store-icon ${store.type}`}><StoreIcon /></span>
              <div>
                <strong>{store.name}</strong>
                <small>{store.type === "ssh" ? "SSH · age encrypted" : "Local filesystem"}</small>
              </div>
              <span className="store-detail">{store.detail}</span>
              <span className="ready-pill"><i /> Ready</span>
            </article>
          ))}
          <button className="add-store" onClick={() => onPrototype("Add store")}><PlusIcon /> Add store</button>
        </div>
      </section>

      <section className="cta-card">
        <span className="cta-glow" />
        <div className="cta-icon"><ArchiveIcon /></div>
        <div>
          <span className="section-kicker">PRESERVE THE MOMENT</span>
          <h2>Capture before you context-switch.</h2>
          <p>Bundle workspace state, Agent session, and runtime evidence into one verified Run.</p>
        </div>
        <button className="button primary" onClick={onCapture}><PlusIcon /> Capture this workspace</button>
      </section>
    </>
  );
}

function RunRow({ run }: { run: RunCard }) {
  const initials = run.repository.slice(0, 2).toUpperCase();
  const warning = run.integrity !== "verified";
  return (
    <button className="run-row" title={`Open ${run.title}`}>
      <span className="run-main">
        <span className={`repo-avatar ${run.repository === "capsule-lab" ? "purple" : ""}`}>{initials}</span>
        <span>
          <strong>{run.title}</strong>
          <small>{run.repository} · {run.created_at}</small>
        </span>
      </span>
      <span><span className="agent-pill"><i>⌘</i>{run.agent}</span></span>
      <span><span className={`integrity-pill ${warning ? "warning" : ""}`}>
        {warning ? "!" : <CheckIcon />} {warning ? "Review" : "Verified"}
      </span></span>
      <span className={`sync-label ${run.sync_status === "not pushed" ? "muted" : ""}`}>
        <i /> {run.sync_status}
      </span>
      <span><ArrowIcon className="row-arrow" /></span>
    </button>
  );
}

function CapturePanel({
  workspace,
  onChoose,
  onClose,
}: {
  workspace: string;
  onChoose(): void;
  onClose(): void;
}) {
  const workspaceParts = workspace.split("/");
  const workspaceName = workspaceParts[workspaceParts.length - 1] || workspace;
  return (
    <div className="modal-layer" role="dialog" aria-modal="true" aria-label="Capture a Run">
      <button className="modal-backdrop" aria-label="Close" onClick={onClose} />
      <section className="capture-panel">
        <header>
          <div>
            <span className="section-kicker">NEW RUN</span>
            <h2>Capture this workspace</h2>
            <p>Review exactly what will be preserved before anything is written.</p>
          </div>
          <button className="close-button" onClick={onClose}><CloseIcon /></button>
        </header>
        <div className="capture-steps">
          <span className="active"><i>1</i>Source</span><b />
          <span><i>2</i>Contents</span><b />
          <span><i>3</i>Review</span><b />
          <span><i>4</i>Capture</span>
        </div>
        <div className="capture-body">
          <label>Workspace</label>
          <button className="workspace-picker" onClick={onChoose}>
            <span className="folder-box"><FolderIcon /></span>
            <span><strong>{workspaceName}</strong><small>{workspace}</small></span>
            <span className="change-link">Change</span>
          </button>
          <label>Agent session</label>
          <div className="session-choice">
            <span className="radio selected"><i /></span>
            <span className="session-agent">⌘</span>
            <div><strong>Latest Codex session</strong><small>Parser recovery · active 6 minutes ago</small></div>
            <span className="recommended">Recommended</span>
          </div>
          <div className="preflight-note">
            <ShieldIcon />
            <span><strong>Preflight only</strong><small>This prototype stops before writing a Run.</small></span>
          </div>
        </div>
        <footer>
          <button className="button secondary" onClick={onClose}>Cancel</button>
          <button className="button primary" onClick={onClose}>Review contents <ArrowIcon /></button>
        </footer>
      </section>
    </div>
  );
}

function PagePlaceholder({
  page,
  dashboard,
  onBack,
}: {
  page: Page;
  dashboard: Dashboard;
  onBack(): void;
}) {
  const copy: Record<Page, [string, string]> = {
    Overview: ["Overview", ""],
    Runs: ["Run catalog", `${dashboard.stats.runs} immutable workspace snapshots are indexed locally.`],
    Operations: ["Operation evidence", "Progress, safety decisions, and digest evidence will live here."],
    Stores: ["Durable stores", `${dashboard.stats.stores} encrypted or local endpoints are configured.`],
    Settings: ["Local configuration", "Control paths, adapters, and safety defaults without exposing secrets."],
  };
  return (
    <section className="placeholder panel">
      <span className="placeholder-icon">
        {page === "Stores" ? <StoreIcon /> : page === "Settings" ? <SettingsIcon /> : <ArchiveIcon />}
      </span>
      <span className="section-kicker">PHASE 1</span>
      <h2>{copy[page][0]}</h2>
      <p>{copy[page][1]}</p>
      <div className="preview-rule"><span /><i>Read-only foundation is connected</i><span /></div>
      <button className="button secondary" onClick={onBack}>Return to overview</button>
    </section>
  );
}

export default App;
