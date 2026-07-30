# sessionmgr

Session Manager 是一个本地优先、跨机器、跨 Agent 平台的 AI 编程工作留档与迁移工具。

它保存的不是孤立的聊天记录，而是一次完整的 Agent Run：

```text
Run = Workspace checkpoints + Agent sessions + Runtime context + Lineage
```

当前 MVP 已实现可运行的 CLI 闭环：

- Capture：保存本地 commits、staged/unstaged 修改、untracked 文件和 Codex 原始 session；
- Verify：使用 SHA-256 对 manifest 和所有 payload object 做离线校验；
- Restore：在隔离的新 worktree 中依次恢复 commits、index、worktree 和 untracked 文件；
- Native Resume：实验性导入 Codex JSONL session，失败时保留 handoff 降级路径；
- Handoff：生成事实、推断和建议相分离的通用 Markdown；
- Sync：支持本地目录 Store，以及通过 SSH 传输的 `tar → zstd → age` 加密 Capsule；
- Safety：阻止路径逃逸、覆盖、危险 symlink 和携带高置信度秘密的远程 push；
- Catalog：使用可重建的 SQLite 索引浏览 Run、机器、Agent 和 lineage。

`v0.2.0-dev` 还包含一个 Wails + React 桌面 GUI 原型，用于可视化验收只读
Dashboard 和 Capture preflight。Preview 模式只读取内置 fixture，不会修改真实
Session Manager home、Codex session 或工作区。

## 环境要求

- Go 1.24 或更高版本；
- Git；
- macOS 或 Linux；
- 使用 SSH Store 时需要 `ssh`、`scp`，以及已配置的 SSH 登录能力。

SQLite 使用纯 Go 实现，构建不依赖 CGO。

## 构建与测试

```bash
make check
./bin/sessionmgr version
```

也可以直接运行：

```bash
CGO_ENABLED=0 go build -trimpath -o bin/sessionmgr ./cmd/sessionmgr
CGO_ENABLED=0 go test ./...
```

GUI 开发预览（需要 Wails v2.12 和 Node.js）：

```bash
cd gui/frontend && npm ci
cd ..
SESSIONMGR_GUI_PREVIEW=1 wails dev
```

前端也可以单独启动：`cd gui/frontend && npm run dev`。没有 Wails bridge 时会自动
使用同一套明确标注的 Preview 数据。

## 快速开始

初始化本机目录、machine ID 和 age identity：

```bash
sessionmgr init
sessionmgr doctor
```

在 Git worktree 中归档最近的 Codex session：

```bash
sessionmgr capture --latest --title "Implement parser"
```

显式加入 ignored 文件：

```bash
sessionmgr capture --latest --include-ignored 'fixtures/private/**'
```

浏览与验证：

```bash
sessionmgr list
sessionmgr show <run-id>
sessionmgr verify <run-id> --deep
```

默认恢复到仓库旁边的 `.sessionmgr-worktrees/`：

```bash
sessionmgr restore <run-id> --repo /path/to/repo
```

尝试实验性的 Codex 原生恢复：

```bash
sessionmgr restore <run-id> --repo /path/to/repo --native-session
```

单独生成 handoff：

```bash
sessionmgr handoff <run-id> --to generic
```

所有主要命令都支持 `--json`，进度和错误写入 stderr。

## Store 配置

默认配置位于 `~/.sessionmgr/config.toml`，也可以通过 `SESSIONMGR_HOME` 覆盖根目录。

目录 Store 示例：

```toml
[[stores]]
name = "portable-disk"
type = "file"
url = "/Volumes/Backup/sessionmgr-store"
```

SSH Store 示例：

```toml
[[stores]]
name = "personal-ssh"
type = "ssh"
url = "ssh://devbox.example.com/~/sessionmgr-store"
age_recipients = ["age1..."]
```

`sessionmgr init` 会输出本机 age recipient。把需要解密 Capsule 的设备 recipient 加入 Store 配置，然后执行：

```bash
sessionmgr push <run-id> --store personal-ssh
sessionmgr pull --store personal-ssh
```

SSH push 会先上传加密 Capsule，验证完成后才原子更新 Run ref。目标端的 `~/.sessionmgr/keys/identity.txt` 不会被上传。

## 数据目录

```text
~/.sessionmgr/
├── config.toml
├── catalog.sqlite
├── machine-id
├── objects/
├── runs/
├── refs/
├── keys/
├── tmp/
├── handoff/
└── operation-reports/
```

Run 和 payload object 不可变；catalog 只是索引，可以从 `refs/runs` 和 manifest 重建。

## 当前能力边界

- MVP 支持一个 Run 对应一个主 Git workspace；
- Codex 是首个无损 session adapter；
- Codex 没有稳定的跨版本导入协议，因此 native restore 明确标记为 experimental；
- 不迁移 Agent 登录、Git credentials、环境变量或设备身份数据库；
- 不支持两个设备同时编辑并自动合并同一个原生 session；
- submodule、Git LFS、shallow clone 和 sparse checkout 会产生 capability warning。

## 文档

- [产品需求文档](./docs/PRD.md)
- [技术规格](./docs/SPEC.md)
- [GUI 实现规划](./docs/GUI_IMPLEMENTATION_PLAN.md)
- [GUI 验收计划](./docs/GUI_ACCEPTANCE.md)
- [开发规则](./AGENTS.md)
- [版本 Devlogs](./docs/devlogs/README.md)
- [Manifest v1 Schema](./schemas/manifest-v1.schema.json)
- [Normalized Event v1 Schema](./schemas/normalized-event-v1.schema.json)
