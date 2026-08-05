# Session Manager v0.3 产品需求

## 1. 产品定义

Session Manager 是一个跨 macOS、Linux、Windows 的 Codex session Markdown 导出器，
同时提供 CLI 和本地 GUI。

Git 已经负责代码历史和跨机器同步。本产品只负责把分散在本机 Codex 状态目录中的
sessions 转成可读、可由 Git 跟踪历史的文件。

## 2. 核心用户流程

### 首次使用

1. 用户启动 GUI 或运行 CLI 配置命令；
2. 选择一个导出目录；
3. Session Manager 创建目录并持久保存绝对路径；
4. 用户执行第一次导出；
5. UI 只显示这次创建或更新的 session 文档。

### 后续使用

1. Session Manager 自动恢复已配置目录；
2. 用户执行导出；
3. 系统扫描本机 active/archived Codex sessions；
4. source 与已记录内容没有变化时不显示；
5. 只显示本次新增、内容更新或名称变化。

## 3. 核心模型

```text
hosted Git remote key -> set(device ID + native session ID -> current Markdown)
```

- repository key 必须在同一 hosted repository 的 SSH/HTTPS clone 间稳定；
- 不允许用本机路径猜测跨机器仓库身份；
- session key 是持久 device ID 与 Codex 原生 session ID 的组合 hash；
- session 名称是可变的人类语义，只用于可见目录名而不承担身份职责；
- source hash 用于源变化检测，document hash 用于保护生成文件，二者均只进入隐藏 sidecar；
- 每个设备/session 只有一个 `conversation.md`，历史版本由 Git 保存；
- 多台机器按可读设备目录导出，结果通过普通 Git 合并。

## 4. 功能需求

### FR-1 持久目录

- GUI 和 CLI 必须共享一个 schema-versioned 配置；
- 配置文件必须使用操作系统标准配置目录；
- 用户指定不存在的导出目录时应安全创建；
- 下次启动必须自动恢复目录；
- 明确指定 `--directory` 时同时更新持久配置；
- 配置文件符号链接不得被跟随写入。
- 每台机器必须在本地配置中持久保存一个随机 device ID 与可读 device name；
- device identity 不得放在 Git 管理的导出根目录中，避免其他机器继承同一身份。

### FR-2 Session discovery

- 扫描 Codex `sessions/` 与 `archived_sessions/` JSONL；
- 支持全部仓库、当前/指定仓库、指定 session 三种范围；
- 全部候选文件必须共享一个短暂稳定窗口，而不是逐文件等待；
- 活跃文件在观察与读取前后必须保持 identity、size 和 mtime 一致；
- locked、变化中、被替换/移动或尾部 JSONL 不完整的文件必须标记 `busy`，本次静默忽略；
- `busy` 必须在 JSON 计数中可观察，但不得产生 warning 或非零退出码；
- 不得修改、移动或删除 Codex 源文件。

### FR-3 Repository identity

- hosted remote 去掉 scheme、Git username、HTTP credentials、query、fragment 和结尾
  `.git` 后作为身份输入；
- host 转小写，path 在 v1 中保留大小写；
- 空 remote、local path remote、`file://` remote 必须跳过；
- repository identity 冲突不得覆盖已有文件。
- 可见 repository 路径使用规范化 remote 的 host/owner/repo，不包含 hash；
- 完整 key 与 canonical remote 必须保存在 `.sessionmgr-repository.json`。

### FR-4 Markdown export

- 可见文档固定命名为 `conversation.md`，父目录使用创建时间与最新 session 名称；
- 设备、原生 session ID、session key、源 hash 与文档 hash 只保存在
  `.sessionmgr-session.json`；
- Markdown 保存 session 名称、设备显示名、Codex 版本和 Git 提示，不暴露 identity hash；
- 分别保存创建、首条消息、末条消息、末事件、标题更新和总体更新时间；
- 保存总消息、用户消息和助手消息数量；
- 保存用户与助手的可读对话；
- 每条可读消息必须保留文件顺序与原始 timestamp；缺失时间不得用文件 mtime 猜测；
- tool 调用只保存数量，不保存参数或输出；
- developer/system 指令、内部 reasoning、认证数据和环境变量值不得进入导出；
- 常见私钥、token、credential URL 与 secret assignment 必须脱敏；
- malformed/omitted records 必须通过计数保持可见。

### FR-5 Incremental changeset

- 第一次看到某 session 时标记 `new`；
- source hash 变化时标记 `updated`；
- source hash 不变但显示名称变化时标记 `renamed`；
- source、标题、renderer 与生成内容均相同时必须是 no-op；
- 更新前必须验证当前 `conversation.md` 匹配 sidecar 的 document hash；人工编辑或
  identity/path collision 必须保留原文件并把该 source 作为 skipped 报告；
- 标题变化时，在所有权验证后重命名语义目录并更新同一个文档；
- CLI 和 GUI 默认只渲染当前操作创建的 changeset；
- changeset 为空时显示单一的 no-change 状态，不回显历史 catalog。

### FR-6 GUI

- 无参数启动程序时打开 GUI；
- GUI 必须由同一二进制提供，不依赖 Node 或平台 WebView SDK；
- 服务只能监听 loopback；
- 每次启动必须生成随机 API token；
- GUI 必须支持保存目录、系统目录选择、导出范围和 changeset 展示；
- 桌面与窄屏布局必须可用；
- UI 不得执行 `git add`、commit 或 push。

### FR-7 CLI

- 必须提供 `config set-directory`、`config show`、`export`、`list`、`gui`、`version`；
- human output 与 JSON output 必须分离；
- `archive` 作为 `export` 兼容别名；
- partial export 必须保留成功 changeset，同时以非零退出码和 warning 报告跳过项。

### FR-8 三系统分发

- 核心与 GUI 必须保持纯 Go、`CGO_ENABLED=0` 可构建；
- 必须能交叉构建 macOS、Linux、Windows；
- 目录打开/浏览使用各平台最接近的可用方式，并提供手工路径 fallback。

## 5. 非目标

- workspace capture/restore；
- Codex native resume/import；
- SQLite catalog；
- Capsule、加密、SSH Store 或自定义同步协议；
- 自动 Git commit/push；
- lossless replay 或完整 tool trace；
- 公网 GUI 服务。

## 6. 验收标准

1. GUI 保存目录后重载仍显示该目录。
2. CLI 配置目录后，后续 `export` 无需再次传路径。
3. 同一 GitHub 仓库的 SSH/HTTPS sessions 进入同一 repository set。
4. 没有 hosted remote 的 session 被明确跳过。
5. 首次导出显示 `new`，重复导出只显示 no-change。
6. JSONL 追加后显示 `updated`。
7. 只重命名 session 后显示 `renamed`。
8. 同一 device/session 重复导出只保留一个 `conversation.md`；内容更新原地更新该文件。
9. GUI API 没有随机 token 时返回 unauthorized。
10. GUI 拒绝非 loopback listen address。
11. macOS、Linux、Windows no-CGO 构建全部通过。
12. 原始 Codex JSONL 在导出前后字节一致。
13. 稳定窗口内发生变化或尾部不完整的 session 只增加 `busy`，不生成文档且导出成功。
14. Markdown 明确区分创建、首次/最后对话与最后源事件时间，并为每条消息显示时间点。
15. 可见路径和 Markdown 文件名不包含 repository/session/content hash。
16. 隐藏 sidecar 中的 session key 可由 device ID 与 Codex native session ID 重新计算验证。
17. 手工改过的 `conversation.md` 与语义路径 identity collision 均不会被覆盖。
18. v1/v2 hash-named archive 仍可由 `list --history` 检查，但不会自动删除或改写。
